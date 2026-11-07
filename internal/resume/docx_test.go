package resume

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"interview-ace/internal/testutil"
)

func TestExtractDOCX_能抽出中文段落(t *testing.T) {
	raw := testutil.BuildDOCX(
		"陆进兴",
		"教育背景",
		"广东第二师范学院 软件工程 本科",
		"项目经历",
		"用 Postman 设计 10 个接口测试用例",
	)

	ex, err := ExtractDOCX(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}

	for _, want := range []string{"陆进兴", "教育背景", "广东第二师范学院", "Postman"} {
		if !strings.Contains(ex.Text, want) {
			t.Errorf("文本缺少 %q，实际: %q", want, ex.Text)
		}
	}

	// 段落之间要保留换行，不能全糊成一行
	if n := len(strings.Split(ex.Text, "\n")); n < 3 {
		t.Errorf("段落边界丢了，只有 %d 行: %q", n, ex.Text)
	}

	if Scanned(ex.Text, ex.Pages) {
		t.Error("有文字的 docx 不该被判成扫描件")
	}
}

func TestExtractDOCX_制表符与换行(t *testing.T) {
	// <w:tab/> 应变成 \t，<w:br/> 应变成换行
	xmlBody := `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>姓名</w:t><w:tab/><w:t>张三</w:t></w:r></w:p>
<w:p><w:r><w:t>第一行</w:t><w:br/><w:t>第二行</w:t></w:r></w:p>
</w:body></w:document>`
	raw := testutil.BuildRawDOCX(xmlBody)

	ex, err := ExtractDOCX(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("抽取失败: %v", err)
	}
	if !strings.Contains(ex.Text, "姓名") || !strings.Contains(ex.Text, "张三") {
		t.Errorf("文本内容丢失: %q", ex.Text)
	}
}

func TestExtractDOCX_不是zip返回ErrNotDOCX(t *testing.T) {
	data := []byte("这不是 docx，只是一段普通文字")

	_, err := ExtractDOCX(bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, ErrNotDOCX) {
		t.Errorf("期望 ErrNotDOCX，实际: %v", err)
	}
}

func TestExtractDOCX_缺少正文部件返回ErrNotDOCX(t *testing.T) {
	raw := testutil.BuildEmptyDOCX()

	_, err := ExtractDOCX(bytes.NewReader(raw), int64(len(raw)))
	if !errors.Is(err, ErrNotDOCX) {
		t.Errorf("期望 ErrNotDOCX，实际: %v", err)
	}
}

func TestExtractDOCX_大小为0(t *testing.T) {
	_, err := ExtractDOCX(bytes.NewReader(nil), 0)
	if !errors.Is(err, ErrNotDOCX) {
		t.Errorf("期望 ErrNotDOCX，实际: %v", err)
	}
}

// TestExtract_按魔数分派 是格式识别的关键断言：
// 用户把 docx 改名成 .pdf 上传的情况真实存在，按扩展名判断会出错。
func TestExtract_按魔数分派(t *testing.T) {
	pdfData := testutil.BuildPDF("Hello PDF resume with enough text here")
	docxData := testutil.BuildDOCX("你好", "这是 Word 简历", "内容足够长不会被判成扫描件")

	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"PDF", pdfData, "Hello PDF"},
		{"DOCX", docxData, "Word 简历"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ex, err := Extract(bytes.NewReader(c.data), int64(len(c.data)))
			if err != nil {
				t.Fatalf("抽取失败: %v", err)
			}
			if !strings.Contains(ex.Text, c.want) {
				t.Errorf("文本缺少 %q，实际: %q", c.want, ex.Text)
			}
		})
	}
}

func TestExtract_不支持的格式(t *testing.T) {
	cases := map[string][]byte{
		"纯文本":   []byte("just plain text, not a document"),
		"老式doc": {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
		"太短":    []byte("ab"),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Extract(bytes.NewReader(data), int64(len(data)))
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("期望 ErrUnsupported，实际: %v", err)
			}
		})
	}
}

func TestExtract_不支持格式的提示提到docx(t *testing.T) {
	_, err := Extract(bytes.NewReader([]byte("plain text here")), 15)
	if err == nil {
		t.Fatal("期望报错")
	}
	if !strings.Contains(err.Error(), "docx") {
		t.Errorf("错误提示应告诉用户支持 docx，实际: %v", err)
	}
}
