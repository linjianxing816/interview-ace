package resume

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"interview-ace/internal/testutil"
)

func TestExtractPDF_能抽出文本(t *testing.T) {
	raw := testutil.BuildPDF("Hello InterviewAce Resume")

	ex, err := ExtractPDF(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}

	if ex.Pages != 1 {
		t.Errorf("页数 = %d，期望 1", ex.Pages)
	}
	if !strings.Contains(ex.Text, "Hello InterviewAce Resume") {
		t.Errorf("抽出的文本不含预期内容，实际: %q", ex.Text)
	}
}

func TestExtractPDF_文本型不会被误判为扫描件(t *testing.T) {
	raw := testutil.BuildPDF("Zhang San is a backend engineer with three years of Go experience")

	ex, err := ExtractPDF(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if Scanned(ex.Text, ex.Pages) {
		t.Errorf("文本型 PDF 被判成扫描件，文本: %q", ex.Text)
	}
}

func TestExtractPDF_非PDF返回ErrNotPDF(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"纯文本", []byte("这不是 PDF，只是一段普通文字")},
		{"空字节", []byte{}},
		{"截断的PDF头", []byte("%PDF-1.4\n")},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ExtractPDF(bytes.NewReader(c.data), int64(len(c.data)))
			if err == nil {
				t.Fatal("期望报错，实际成功")
			}
			if !errors.Is(err, ErrNotPDF) {
				t.Errorf("期望 ErrNotPDF，实际: %v", err)
			}
		})
	}
}

func TestExtractPDF_大小为0返回ErrNotPDF(t *testing.T) {
	_, err := ExtractPDF(bytes.NewReader(nil), 0)
	if !errors.Is(err, ErrNotPDF) {
		t.Errorf("期望 ErrNotPDF，实际: %v", err)
	}
}

// TestExtractPDF_中文简历 是这条链路的硬需求：
// 中文 PDF 用 CID 字体（STSong-Light + UniGB-UCS2-H），
// 字体不嵌进文件，抽出来是 UTF-16 编码的字形索引，必须能正确还原成汉字。
func TestExtractPDF_中文简历(t *testing.T) {
	const body = "张三 后端工程师 教育背景 某某大学 计算机科学与技术 项目经历 评测平台 专业技能 Go Python"
	raw := testutil.BuildPDFChinese(body)

	ex, err := ExtractPDF(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("中文 PDF 抽取失败: %v", err)
	}

	for _, want := range []string{"张三", "教育背景", "某某大学", "专业技能"} {
		if !strings.Contains(ex.Text, want) {
			t.Errorf("抽出的文本缺少 %q，实际: %q", want, ex.Text)
		}
	}

	if Scanned(ex.Text, ex.Pages) {
		t.Error("中文简历不该被判成扫描件")
	}

	// 汉字不能被抽成乱码或问号
	if strings.Contains(ex.Text, "\uFFFD") {
		t.Errorf("文本里有替换字符，说明编码没还原对: %q", ex.Text)
	}
}

func TestExtractPDF_中文与英文混排(t *testing.T) {
	const body = "李四 Go 后端 三年经验 PostgreSQL Docker 熟练掌握"
	raw := testutil.BuildPDFChinese(body)

	ex, err := ExtractPDF(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if !strings.Contains(ex.Text, "PostgreSQL") || !strings.Contains(ex.Text, "李四") {
		t.Errorf("中英混排内容丢失: %q", ex.Text)
	}
}
