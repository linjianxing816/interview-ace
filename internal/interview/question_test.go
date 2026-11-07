package interview

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"interview-ace/internal/llm"
	"interview-ace/internal/parse"
)

// fakeCompleter 是假模型。按调用序号依次返回 contents 里的内容。
//
// 带锁是因为评分采样已经改成并发了：多个 goroutine 同时调 Chat，
// 不加锁的话 calls++ 会丢计数（测试里还要断言调用次数）。
// 并发下「第几次调用拿到哪条内容」不固定，所以依赖具体顺序的用例
// 必须用 Samples: 1，或者只断言与顺序无关的结果（如中位数）。
type fakeCompleter struct {
	mu       sync.Mutex
	contents []string
	err      error
	// failFirst 是「前 N 次调用直接失败」。
	// 并发评分时没法指定「第几次采样失败」，只能指定「总共失败几次调用」，
	// 所以用到它的测试断言要写成与调度无关的形式。
	failFirst int
	calls     int
	lastUser  string
}

func (f *fakeCompleter) Chat(_ context.Context, msgs []llm.Message) (llm.Reply, error) {
	var user string
	for _, m := range msgs {
		if m.Role == "user" {
			user = m.Content
		}
	}

	f.mu.Lock()
	f.calls++
	n := f.calls
	idx := n - 1
	f.lastUser = user
	f.mu.Unlock()

	if f.failFirst > 0 && n <= f.failFirst {
		return llm.Reply{}, errors.New("注入的失败")
	}
	if f.err != nil {
		return llm.Reply{}, f.err
	}
	if idx >= len(f.contents) {
		idx = len(f.contents) - 1
	}
	return llm.Reply{Content: f.contents[idx]}, nil
}

const goodQuestion = `{"question":"你说用 Postgres 做过并发写入，当时怎么避免超卖的？","type":"tech"}`

func TestGenerate_正常出题(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodQuestion}}

	q, err := Generate(context.Background(), fc, QuestionRequest{
		RoleType: "backend", JDText: "负责后端开发", ResumeSummary: "项目：X", Seq: 1,
	})
	if err != nil {
		t.Fatalf("出题失败: %v", err)
	}
	if !strings.Contains(q.Text, "Postgres") {
		t.Errorf("题目内容不对: %q", q.Text)
	}
	if q.Type != TypeTech {
		t.Errorf("Type = %q，期望 tech", q.Type)
	}
}

func TestGenerate_容忍markdown代码块(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"```json\n" + goodQuestion + "\n```"}}

	if _, err := Generate(context.Background(), fc, QuestionRequest{Seq: 1}); err != nil {
		t.Fatalf("带代码块应能解析，实际: %v", err)
	}
}

// TestGenerate_非法题目重试 覆盖模型输出「太短」「类型不对」「不是 JSON」三种情况。
func TestGenerate_非法题目重试(t *testing.T) {
	cases := []struct{ name, bad string }{
		{"不是JSON", "好的，我来出一道题。"},
		{"题目太短", `{"question":"你好","type":"tech"}`},
		{"类型不对", `{"question":"请介绍一下你最近做的一个项目","type":"随便"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCompleter{contents: []string{c.bad, goodQuestion}}

			q, err := Generate(context.Background(), fc, QuestionRequest{Seq: 1})
			if err != nil {
				t.Fatalf("重试后应成功，实际: %v", err)
			}
			if q.Type != TypeTech {
				t.Errorf("Type = %q", q.Type)
			}
			if fc.calls != 2 {
				t.Errorf("调用次数 = %d，期望 2", fc.calls)
			}
		})
	}
}

func TestGenerate_重试到上限仍失败(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"始终不是 JSON"}}

	_, err := Generate(context.Background(), fc, QuestionRequest{Seq: 1})
	if !errors.Is(err, ErrBadQuestion) {
		t.Fatalf("期望 ErrBadQuestion，实际: %v", err)
	}
	if fc.calls != maxQuestionAttempts {
		t.Errorf("调用次数 = %d，期望 %d", fc.calls, maxQuestionAttempts)
	}
}

func TestGenerate_模型调用失败不重试(t *testing.T) {
	sentinel := errors.New("限流")
	fc := &fakeCompleter{err: sentinel}

	_, err := Generate(context.Background(), fc, QuestionRequest{Seq: 1})
	if !errors.Is(err, sentinel) {
		t.Fatalf("期望包装原始错误，实际: %v", err)
	}
	if fc.calls != 1 {
		t.Errorf("模型层错误不该重试，实际调用 %d 次", fc.calls)
	}
}

func TestGenerate_空客户端报错(t *testing.T) {
	if _, err := Generate(context.Background(), nil, QuestionRequest{Seq: 1}); err == nil {
		t.Fatal("nil 客户端应报错")
	}
}

// TestGenerate_已问过的题进prompt 验证去重靠 prompt 实现。
func TestGenerate_已问过的题进prompt(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodQuestion}}

	_, err := Generate(context.Background(), fc, QuestionRequest{
		Seq:   2,
		Asked: []string{"第一个问题：介绍一下你自己"},
	})
	if err != nil {
		t.Fatalf("出题失败: %v", err)
	}
	if !strings.Contains(fc.lastUser, "已经问过") || !strings.Contains(fc.lastUser, "介绍一下你自己") {
		t.Errorf("prompt 里没带上已问过的题: %s", fc.lastUser)
	}
}

// TestGenerate_简历里不出现隐私字段 是隐私断言：
// 出题的 prompt 不该带姓名、邮箱、手机号。
func TestGenerate_简历里不出现隐私字段(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodQuestion}}

	summary := SummarizeResume(parse.Parsed{
		Name:     "陆进兴",
		Projects: []parse.Project{{Name: "评测平台", Details: []string{"三层判定"}}},
		Skills:   []string{"Go"},
	})

	if _, err := Generate(context.Background(), fc, QuestionRequest{ResumeSummary: summary, Seq: 1}); err != nil {
		t.Fatalf("出题失败: %v", err)
	}
	if strings.Contains(fc.lastUser, "陆进兴") {
		t.Error("prompt 里出现了候选人姓名，应该只给项目和技术栈")
	}
	if !strings.Contains(fc.lastUser, "评测平台") {
		t.Error("prompt 里应该包含项目信息")
	}
}

func TestSummarizeResume_包含各段落(t *testing.T) {
	p := parse.Parsed{
		Education: []parse.Education{{School: "某大学", Major: "软件工程", Degree: "本科"}},
		Projects:  []parse.Project{{Name: "项目A", Role: "独立开发", Details: []string{"要点1"}}},
		Work:      []parse.Work{{Company: "某公司", Title: "实习生", Period: "2022"}},
		Skills:    []string{"Go", "Python"},
	}
	s := SummarizeResume(p)

	for _, want := range []string{"某大学", "项目A", "独立开发", "要点1", "某公司", "实习生", "Go", "Python"} {
		if !strings.Contains(s, want) {
			t.Errorf("摘要缺少 %q\n完整: %s", want, s)
		}
	}
}

func TestSummarizeResume_空简历返回空串(t *testing.T) {
	if got := SummarizeResume(parse.Parsed{}); got != "" {
		t.Errorf("空简历应返回空串，实际: %q", got)
	}
}

func TestBuildQuestionPrompt_超长内容被截断(t *testing.T) {
	long := strings.Repeat("字", 5000)
	prompt := buildQuestionPrompt(QuestionRequest{JDText: long, ResumeSummary: long, Seq: 1})

	if len([]rune(prompt)) > 3200 {
		t.Errorf("prompt 太长（%d 字），应该截断", len([]rune(prompt)))
	}
}
