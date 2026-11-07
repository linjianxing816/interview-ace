package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeFilename_合法UTF8原样返回(t *testing.T) {
	cases := []string{"cv.pdf", "个人简历.pdf", "my-resume-2024.docx"}
	for _, c := range cases {
		if got := sanitizeFilename(c); got != c {
			t.Errorf("sanitizeFilename(%q) = %q，期望原样返回", c, got)
		}
	}
}

// TestSanitizeFilename_GBK文件名能还原 复现真实故障：
// Windows 上的 curl 按 GBK 发 "个人简历.pdf"，Go 收到的是 B8 F6 C8 CB BC F2 C0 FA .pdf。
// 不处理的话写库会报 invalid byte sequence for encoding "UTF8": 0xb8。
func TestSanitizeFilename_GBK文件名能还原(t *testing.T) {
	gbk := string([]byte{0xB8, 0xF6, 0xC8, 0xCB, 0xBC, 0xF2, 0xC0, 0xFA}) + ".pdf"

	if utf8.ValidString(gbk) {
		t.Fatal("测试数据本身应该是非法 UTF-8")
	}

	got := sanitizeFilename(gbk)
	if !utf8.ValidString(got) {
		t.Fatalf("结果不是合法 UTF-8: %q", got)
	}
	if got != "个人简历.pdf" {
		t.Errorf("GBK 文件名应还原为 个人简历.pdf，实际: %q", got)
	}
}

func TestSanitizeFilename_空名与彻底不可用(t *testing.T) {
	if got := sanitizeFilename("   "); got != defaultFilename {
		t.Errorf("空白文件名应返回默认名，实际: %q", got)
	}
	// 0xFF 在任何常见单字节编码里都不是合法起始字节
	if got := sanitizeFilename(string([]byte{0xFF, 0xFE, 0xFD})); !utf8.ValidString(got) {
		t.Errorf("无法还原时应返回合法字符串，实际: %q", got)
	}
}

func TestToValidUTF8_非法字节被清掉(t *testing.T) {
	bad := "正常内容" + string([]byte{0xB8, 0xF6}) + "结尾"
	if utf8.ValidString(bad) {
		t.Fatal("测试数据本身应该是非法 UTF-8")
	}

	got := toValidUTF8(bad)
	if !utf8.ValidString(got) {
		t.Fatalf("结果不是合法 UTF-8: %q", got)
	}
	if !strings.Contains(got, "正常内容") || !strings.Contains(got, "结尾") {
		t.Errorf("合法部分不该被丢掉，实际: %q", got)
	}
}

func TestToValidUTF8_合法输入原样返回(t *testing.T) {
	s := "正常的中文内容 with English"
	if got := toValidUTF8(s); got != s {
		t.Errorf("合法输入应原样返回，实际: %q", got)
	}
}
