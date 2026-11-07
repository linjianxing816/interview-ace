package resume

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText_换行统一与控制字符(t *testing.T) {
	raw := "第一行\r\n第二行\r第三行\x00\x07"
	got := CleanText(raw)
	want := "第一行\n第二行\n第三行"
	if got != want {
		t.Errorf("CleanText = %q，期望 %q", got, want)
	}
}

func TestCleanText_零宽字符被去掉(t *testing.T) {
	raw := "王\u200b小明\ufeff"
	if got := CleanText(raw); got != "王小明" {
		t.Errorf("CleanText = %q，期望 %q", got, "王小明")
	}
}

func TestCleanText_连续空行压缩(t *testing.T) {
	raw := "A\n\n\n\nB"
	if got := CleanText(raw); got != "A\n\nB" {
		t.Errorf("CleanText = %q，期望保留一个空行", got)
	}
}

func TestCleanText_行尾空格被去掉(t *testing.T) {
	raw := "有空格   \n下一行\t\t"
	want := "有空格\n下一行"
	if got := CleanText(raw); got != want {
		t.Errorf("CleanText = %q，期望 %q", got, want)
	}
}

func TestCleanText_英文连字符换行合并(t *testing.T) {
	raw := "负责 engi-\nneering 模块"
	want := "负责 engineering 模块"
	if got := CleanText(raw); got != want {
		t.Errorf("CleanText = %q，期望 %q", got, want)
	}
}

func TestCleanText_中文后连字符不合并(t *testing.T) {
	// 下一行以中文开头时不能合并，否则会把中文句子粘在一起
	raw := "负责模块-\n需要熟悉 Go"
	if got := CleanText(raw); !strings.Contains(got, "-\n") {
		t.Errorf("中文开头的下一行不该触发连字符合并，实际 = %q", got)
	}
}

func TestCleanText_空输入(t *testing.T) {
	if got := CleanText(""); got != "" {
		t.Errorf("空输入应返回空串，实际 %q", got)
	}
}

func TestScanned_文本型与扫描件(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		pages int
		want  bool
	}{
		{"正常两页简历", strings.Repeat("这是一段简历内容。", 60), 2, false},
		{"扫描件只有页码", "1\n2\n", 2, true},
		{"扫描件完全空白", "", 3, true},
		{"单页内容很少但够阈值", strings.Repeat("字", 25), 1, false},
		{"单页刚好不够阈值", strings.Repeat("字", 19), 1, true},
		{"页数为 0 按一页算", strings.Repeat("字", 25), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Scanned(c.text, c.pages); got != c.want {
				t.Errorf("Scanned = %v，期望 %v（文本长度 %d，页数 %d）",
					got, c.want, len([]rune(c.text)), c.pages)
			}
		})
	}
}

const sampleResume = `王小明
13800000000 | wang@example.com

教育背景
某某大学 计算机科学与技术 本科 2018-2022

项目经历
Agent 测试用例生成平台
- 设计三层判定模型，覆盖 90 次执行
- 通过率 0.878 vs 0.465

工作经历
某公司 测试实习生
- 负责回归测试用例维护

专业技能
Go / Python / PostgreSQL
`

func TestSplitSections_四段都切出来(t *testing.T) {
	s := SplitSections(sampleResume)

	cases := []struct {
		name string
		got  []string
		want string
	}{
		{"教育", s.Education, "某某大学"},
		{"项目", s.Projects, "Agent 测试用例生成平台"},
		{"工作", s.Work, "某公司 测试实习生"},
		{"技能", s.Skills, "Go / Python / PostgreSQL"},
		{"其他", s.Other, "王小明"},
	}

	for _, c := range cases {
		if len(c.got) == 0 {
			t.Errorf("%s 段为空，期望包含 %q", c.name, c.want)
			continue
		}
		joined := strings.Join(c.got, "\n")
		if !strings.Contains(joined, c.want) {
			t.Errorf("%s 段内容 = %q，期望包含 %q", c.name, joined, c.want)
		}
	}
}

func TestSplitSections_标题带冒号和装饰(t *testing.T) {
	text := "【教育背景】：\n某大学\n■ 专业技能\nGo"
	s := SplitSections(text)
	if len(s.Education) == 0 || !strings.Contains(strings.Join(s.Education, "\n"), "某大学") {
		t.Errorf("带装饰的标题未识别，教育段 = %v", s.Education)
	}
	if len(s.Skills) == 0 || !strings.Contains(strings.Join(s.Skills, "\n"), "Go") {
		t.Errorf("■ 开头的标题未识别，技能段 = %v", s.Skills)
	}
}

func TestSplitSections_没有标题时全进Other(t *testing.T) {
	text := "一段没有任何标题的简历正文\n第二行"
	s := SplitSections(text)
	if len(s.Other) != 2 {
		t.Errorf("Other 应有 2 行，实际 %d 行: %v", len(s.Other), s.Other)
	}
	if s.Empty() {
		t.Error("Empty() 应为 false")
	}
}

func TestSplitSections_超长行不当标题(t *testing.T) {
	// 一行超过 10 个字符，即使以「技能」开头也不是标题
	text := "技能：熟练掌握 Go 和 PostgreSQL 以及 Docker"
	s := SplitSections(text)
	if len(s.Skills) != 0 {
		t.Errorf("超长行不该被当成标题，技能段 = %v", s.Skills)
	}
}

func TestChunk_按长度切分且不破坏UTF8(t *testing.T) {
	text := strings.Repeat("测试内容", 200) // 800 个字符
	chunks := Chunk(text, 100)

	if len(chunks) < 8 {
		t.Fatalf("应切成至少 8 段，实际 %d 段", len(chunks))
	}
	var total int
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("第 %d 段不是合法 UTF-8", i)
		}
		n := len([]rune(c))
		if n > 100 {
			t.Errorf("第 %d 段长度 %d，超过上限 100", i, n)
		}
		total += n
	}
	if total != len([]rune(text)) {
		t.Errorf("切分后总字符数 %d，原文 %d，内容丢失", total, len([]rune(text)))
	}
}

func TestChunk_优先在换行处断开(t *testing.T) {
	text := "第一行内容\n第二行内容\n第三行内容"
	chunks := Chunk(text, 12)
	for _, c := range chunks {
		if strings.HasPrefix(c, "行") || strings.HasSuffix(c, "第一") {
			t.Errorf("不该在词中间断开，实际切出: %q", c)
		}
	}
}

func TestChunk_空文本返回nil(t *testing.T) {
	if got := Chunk("   ", 100); got != nil {
		t.Errorf("空白文本应返回 nil，实际 %v", got)
	}
}
