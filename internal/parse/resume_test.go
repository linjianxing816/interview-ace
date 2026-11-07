package parse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"interview-ace/internal/llm"
)

// fakeCompleter 返回预设内容，不花钱。
// contents 有多条时按调用顺序依次返回，用来测重试。
type fakeCompleter struct {
	content  string
	contents []string
	err      error
	// 记录收到的用户消息，用来断言截断行为
	gotUser string
	usage   llm.Usage
	calls   int
	// 最后一次请求的完整消息列表，用来断言重试时追加了纠正指令
	lastMsgs []llm.Message
}

func (f *fakeCompleter) Chat(_ context.Context, msgs []llm.Message) (llm.Reply, error) {
	f.calls++
	f.lastMsgs = msgs
	for _, m := range msgs {
		if m.Role == "user" {
			f.gotUser = m.Content
		}
	}
	if f.err != nil {
		return llm.Reply{}, f.err
	}

	content := f.content
	if len(f.contents) > 0 {
		idx := f.calls - 1
		if idx >= len(f.contents) {
			idx = len(f.contents) - 1
		}
		content = f.contents[idx]
	}
	return llm.Reply{Content: content, Usage: f.usage}, nil
}

const goodJSON = `{
  "name": "张三",
  "education": [{"school":"某大学","major":"计算机","degree":"本科","period":"2018-2022"}],
  "projects": [{"name":"评测平台","role":"独立开发","details":["三层判定模型","90 次执行"]}],
  "work": [{"company":"某公司","title":"测试实习生","period":"2022.6-2022.9","details":["回归测试"]}],
  "skills": ["Go","Python","PostgreSQL"]
}`

func TestResume_正常解析(t *testing.T) {
	fc := &fakeCompleter{content: goodJSON, usage: llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}}

	res, err := Resume(context.Background(), fc, "张三的简历正文，内容足够长")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if res.Data.Name != "张三" {
		t.Errorf("Name = %q", res.Data.Name)
	}
	if len(res.Data.Education) != 1 || res.Data.Education[0].School != "某大学" {
		t.Errorf("Education = %+v", res.Data.Education)
	}
	if len(res.Data.Projects) != 1 || len(res.Data.Projects[0].Details) != 2 {
		t.Errorf("Projects = %+v", res.Data.Projects)
	}
	if len(res.Data.Work) != 1 || res.Data.Work[0].Company != "某公司" {
		t.Errorf("Work = %+v", res.Data.Work)
	}
	if len(res.Data.Skills) != 3 {
		t.Errorf("Skills = %v", res.Data.Skills)
	}
	if res.Usage.TotalTokens != 150 {
		t.Errorf("Usage 没透传: %+v", res.Usage)
	}
}

// TestResume_容忍markdown代码块 覆盖模型最常见的「不听话」：
// 明确要求只输出 JSON，它还是包一层 ```json。
func TestResume_容忍markdown代码块(t *testing.T) {
	fc := &fakeCompleter{content: "```json\n" + goodJSON + "\n```"}

	res, err := Resume(context.Background(), fc, "简历正文")
	if err != nil {
		t.Fatalf("带代码块的输出应能解析，实际报错: %v", err)
	}
	if res.Data.Name != "张三" {
		t.Errorf("Name = %q", res.Data.Name)
	}
}

func TestResume_容忍前后废话(t *testing.T) {
	fc := &fakeCompleter{content: "好的，这是解析结果：\n" + goodJSON + "\n希望有帮助！"}

	if _, err := Resume(context.Background(), fc, "简历正文"); err != nil {
		t.Fatalf("前后带解释文字应能解析，实际报错: %v", err)
	}
}

func TestResume_容忍多余字段(t *testing.T) {
	body := `{"name":"李四","summary":"模型自己加的字段","skills":["Go"]}`
	fc := &fakeCompleter{content: body}

	res, err := Resume(context.Background(), fc, "简历正文")
	if err != nil {
		t.Fatalf("多余字段不该导致失败: %v", err)
	}
	if res.Data.Name != "李四" {
		t.Errorf("Name = %q", res.Data.Name)
	}
}

func TestResume_非法JSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"完全不是 JSON", "抱歉，我无法解析这份简历"},
		{"括号不闭合", `{"name":"张三"`},
		{"空输出", ""},
		{"类型不对", `{"skills":"Go"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCompleter{content: c.content}
			_, err := Resume(context.Background(), fc, "简历正文")
			if err == nil {
				t.Fatal("期望报错")
			}
			if !errors.Is(err, ErrInvalidJSON) {
				t.Errorf("期望 ErrInvalidJSON，实际: %v", err)
			}
		})
	}
}

func TestResume_空结果(t *testing.T) {
	fc := &fakeCompleter{content: `{"name":"","education":[],"projects":[],"work":[],"skills":[]}`}

	_, err := Resume(context.Background(), fc, "简历正文")
	if !errors.Is(err, ErrEmptyResult) {
		t.Errorf("期望 ErrEmptyResult，实际: %v", err)
	}
}

func TestResume_空输入不调模型(t *testing.T) {
	fc := &fakeCompleter{content: goodJSON}

	_, err := Resume(context.Background(), fc, "   ")
	if !errors.Is(err, ErrEmptyResult) {
		t.Errorf("期望 ErrEmptyResult，实际: %v", err)
	}
	if fc.gotUser != "" {
		t.Error("空输入不该调用模型")
	}
}

func TestResume_超长文本被截断(t *testing.T) {
	fc := &fakeCompleter{content: goodJSON}
	long := strings.Repeat("字", maxInputRunes+500)

	if _, err := Resume(context.Background(), fc, long); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	got := len([]rune(fc.gotUser))
	if got != maxInputRunes {
		t.Errorf("传给模型的长度 = %d，期望截断到 %d", got, maxInputRunes)
	}
}

func TestResume_模型调用失败向上传递(t *testing.T) {
	sentinel := errors.New("限流")
	fc := &fakeCompleter{err: sentinel}

	_, err := Resume(context.Background(), fc, "简历正文")
	if err == nil {
		t.Fatal("期望报错")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("期望包装原始错误，实际: %v", err)
	}
	// 不能把模型错误误判成 JSON 错误——两者处理方式不同
	if errors.Is(err, ErrInvalidJSON) {
		t.Error("模型调用失败不该被当成 JSON 错误")
	}
}

func TestDecodeJSON_直接从对象开始(t *testing.T) {
	p, err := decodeJSON(`{"skills":["Go"]}`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(p.Skills) != 1 {
		t.Errorf("Skills = %v", p.Skills)
	}
}

// TestResume_非法JSON后重试成功 覆盖模型第一次抽风、第二次改好的情况。
func TestResume_非法JSON后重试成功(t *testing.T) {
	fc := &fakeCompleter{contents: []string{
		"抱歉，我无法解析", // 第 1 次：废话
		goodJSON,   // 第 2 次：正常
	}}

	res, err := Resume(context.Background(), fc, "简历正文")
	if err != nil {
		t.Fatalf("重试后应成功，实际: %v", err)
	}
	if fc.calls != 2 {
		t.Errorf("调用次数 = %d，期望 2", fc.calls)
	}
	if res.Data.Name != "张三" {
		t.Errorf("Name = %q", res.Data.Name)
	}
}

// TestResume_重试时追加纠正指令 验证第二次请求带上了「上次输出不是 JSON」的提示，
// 而不是原样再问一遍——原样重问模型大概率还是错。
func TestResume_重试时追加纠正指令(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"不是JSON", goodJSON}}

	if _, err := Resume(context.Background(), fc, "简历正文"); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}

	found := false
	for _, m := range fc.lastMsgs {
		if m.Role == "user" && strings.Contains(m.Content, "不是合法 JSON") {
			found = true
		}
	}
	if !found {
		t.Errorf("重试请求里没有纠正指令，消息列表: %+v", fc.lastMsgs)
	}
}

func TestResume_重试到上限仍失败(t *testing.T) {
	fc := &fakeCompleter{content: "始终不是 JSON"}

	_, err := Resume(context.Background(), fc, "简历正文")
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("期望 ErrInvalidJSON，实际: %v", err)
	}
	if fc.calls != maxAttempts {
		t.Errorf("调用次数 = %d，期望 %d", fc.calls, maxAttempts)
	}
}

// TestResume_空结果不重试 验证省钱：空结果重试大概率还是空。
func TestResume_空结果不重试(t *testing.T) {
	fc := &fakeCompleter{content: `{"name":""}`}

	_, err := Resume(context.Background(), fc, "简历正文")
	if !errors.Is(err, ErrEmptyResult) {
		t.Fatalf("期望 ErrEmptyResult，实际: %v", err)
	}
	if fc.calls != 1 {
		t.Errorf("调用次数 = %d，期望 1（空结果不该重试）", fc.calls)
	}
}

// TestResume_重试累加用量 验证成本统计不会漏掉失败那次的消耗。
func TestResume_重试累加用量(t *testing.T) {
	fc := &fakeCompleter{
		contents: []string{"不是JSON", goodJSON},
		usage:    llm.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
	}

	res, err := Resume(context.Background(), fc, "简历正文")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Usage.TotalTokens != 240 {
		t.Errorf("TotalTokens = %d，期望 240（两次调用各 120）", res.Usage.TotalTokens)
	}
}

func TestNormalize_清洗模型输出(t *testing.T) {
	in := Parsed{
		Name: "  张三  ",
		Education: []Education{
			{School: " 某大学 ", Major: " 计算机 "},
			{}, // 全空，应被丢掉
		},
		Projects: []Project{
			{Name: " 项目A ", Details: []string{"  要点一  ", "", "要点一", "要点二"}},
		},
		Skills: []string{" Go ", "Go", "", "Python"},
	}

	out := normalize(in)

	if out.Name != "张三" {
		t.Errorf("Name 未 trim: %q", out.Name)
	}
	if len(out.Education) != 1 {
		t.Errorf("空教育条目未丢弃，剩 %d 条", len(out.Education))
	}
	if out.Education[0].School != "某大学" {
		t.Errorf("School 未 trim: %q", out.Education[0].School)
	}
	if got := out.Projects[0].Details; len(got) != 2 {
		t.Errorf("要点去重失败，得到 %v", got)
	}
	if got := out.Skills; len(got) != 2 {
		t.Errorf("技能去重/去空失败，得到 %v", got)
	}
}

func TestNormalize_超长字段被截断(t *testing.T) {
	long := strings.Repeat("字", maxFieldRunes+100)
	out := normalize(Parsed{Name: long})

	if n := len([]rune(out.Name)); n != maxFieldRunes {
		t.Errorf("Name 长度 = %d，期望截断到 %d", n, maxFieldRunes)
	}
}

func TestNormalize_要点数量上限(t *testing.T) {
	details := make([]string, 30)
	for i := range details {
		details[i] = strings.Repeat("x", i+1)
	}
	out := normalize(Parsed{Projects: []Project{{Name: "p", Details: details}}})

	if n := len(out.Projects[0].Details); n != maxDetails {
		t.Errorf("要点数 = %d，期望上限 %d", n, maxDetails)
	}
}

func TestNormalize_全是空条目时判为空(t *testing.T) {
	out := normalize(Parsed{
		Education: []Education{{}, {}},
		Projects:  []Project{{Details: []string{"", "  "}}},
		Skills:    []string{"", "  "},
	})
	if !out.IsEmpty() {
		t.Errorf("全是空条目应判为空，实际: %+v", out)
	}
}
