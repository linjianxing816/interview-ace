package resume

import (
	"strings"
	"testing"
)

func TestMergeLines_按空行归并碎片(t *testing.T) {
	// 模拟 PDF 抽取：同一视觉行被拆成多块，块之间没有空行
	in := "邮箱：\nljx@qq.com\n\n手机：（\n+86\n）\n13800000000"
	want := "邮箱：ljx@qq.com\n手机：（+86）13800000000"

	if got := MergeLines(in); got != want {
		t.Errorf("MergeLines 结果不符\n实际: %q\n期望: %q", got, want)
	}
}

func TestMergeLines_标题单独成行(t *testing.T) {
	in := "教育背景\n某大学\n软件工程\n\n项目经历\n某项目"
	got := MergeLines(in)

	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("行数 = %d，期望 4，实际: %q", len(lines), got)
	}
	if lines[0] != "教育背景" {
		t.Errorf("第 1 行 = %q，期望标题独占一行", lines[0])
	}
	if lines[2] != "项目经历" {
		t.Errorf("第 3 行 = %q，期望标题独占一行", lines[2])
	}
}

func TestMergeLines_竖线装饰被丢弃(t *testing.T) {
	in := "手机：\n|\n13800000000"
	if got := MergeLines(in); strings.Contains(got, "|") {
		t.Errorf("竖线应被丢弃，实际: %q", got)
	}
}

// TestMergeLines_括号冒号连字符不丢 是防数据丢失的关键断言。
// 早期版本把所有纯符号行都当噪音删掉，结果 "（+86）" 少了右括号、
// "CET-4" 变成 "CET 4"、"2024.9—2028.6" 少了连接符。
func TestMergeLines_括号冒号连字符不丢(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"右括号", "手机：（\n+86\n）", "手机：（+86）"},
		{"冒号", "项目描述\n:", "项目描述:"},
		{"连字符", "英语能力:CET\n-\n4", "英语能力:CET-4"},
		{"破折号", "2024.9\n—\n2028.6", "2024.9—2028.6"},
		{"斜杠", "某大学\n/\n软件工程", "某大学/软件工程"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MergeLines(c.in); got != c.want {
				t.Errorf("MergeLines(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

func TestMergeLines_英文碎片之间补空格(t *testing.T) {
	in := "Saleor\ntesting\npractice"
	if got := MergeLines(in); got != "Saleor testing practice" {
		t.Errorf("英文碎片应补空格，实际: %q", got)
	}
}

func TestMergeLines_中文碎片之间不补空格(t *testing.T) {
	in := "广东第二师范学院\n软件工程\n本科"
	if got := MergeLines(in); got != "广东第二师范学院软件工程本科" {
		t.Errorf("中文碎片之间不该有空格，实际: %q", got)
	}
}

func TestMergeLines_圆点单独成行时与下一行合并(t *testing.T) {
	in := "项目工作:\n•\n使用 Postman 设计用例"
	got := MergeLines(in)
	if !strings.Contains(got, "•使用 Postman") && !strings.Contains(got, "• 使用 Postman") {
		t.Errorf("圆点应与后续内容合并，实际: %q", got)
	}
}

// TestMergeLines_CET4不被拆开 覆盖「连字符不能当列表符号」这条：
// 中文简历里 "CET-4"、"2024-2026" 很常见，把 "-" 当列表符号会把词劈开。
func TestMergeLines_CET4不被拆开(t *testing.T) {
	in := "英语:CET-4\n2024-2026"
	got := MergeLines(in)
	if !strings.Contains(got, "CET-4") {
		t.Errorf("CET-4 被拆开了，实际: %q", got)
	}
	if !strings.Contains(got, "2024-2026") {
		t.Errorf("日期区间被拆开了，实际: %q", got)
	}
}

func TestMergeLines_空输入(t *testing.T) {
	if got := MergeLines(""); got != "" {
		t.Errorf("空输入应返回空串，实际 %q", got)
	}
}
