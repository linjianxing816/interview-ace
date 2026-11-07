package resume

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// 抽取阶段的错误。
var (
	ErrNotDOCX     = errors.New("不是有效的 Word(.docx) 文件")
	ErrUnsupported = errors.New("不支持的文件格式：只接受 PDF 和 Word(.docx)。老式 .doc 请另存为 .docx 或 PDF")
)

// docxMainPart 是 docx 里存放正文的部件路径。
// docx 本质是个 zip 包，正文在 word/document.xml。
const docxMainPart = "word/document.xml"

// maxDocxPartBytes 是单个 XML 部件的读取上限，防止畸形文件把内存吃光。
const maxDocxPartBytes = 16 << 20 // 16MB

// ExtractDOCX 从 Word(.docx) 里抽出纯文本。
//
// 用标准库就够了（archive/zip + encoding/xml），不需要引第三方 docx 库：
// 我们只要文字，不要样式、图片、页眉页脚。
//
// 不支持老式 .doc（二进制格式），它会落到 ErrUnsupported。
func ExtractDOCX(r io.ReaderAt, size int64) (ex Extraction, err error) {
	if size <= 0 {
		return ex, ErrNotDOCX
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ex, fmt.Errorf("%w: %v", ErrNotDOCX, err)
	}

	var part *zip.File
	for _, f := range zr.File {
		if f.Name == docxMainPart {
			part = f
			break
		}
	}
	if part == nil {
		return ex, fmt.Errorf("%w: 包里没有 %s", ErrNotDOCX, docxMainPart)
	}

	rc, err := part.Open()
	if err != nil {
		return ex, fmt.Errorf("%w: 打开正文部件失败: %v", ErrNotDOCX, err)
	}
	defer func() { _ = rc.Close() }()

	raw, err := io.ReadAll(io.LimitReader(rc, maxDocxPartBytes))
	if err != nil {
		return ex, fmt.Errorf("%w: 读取正文失败: %v", ErrNotDOCX, err)
	}

	text, err := docxText(raw)
	if err != nil {
		return ex, fmt.Errorf("%w: %v", ErrNotDOCX, err)
	}

	// docx 没有可靠的页数概念（分页由渲染时决定），统一按 1 页算。
	// Scanned 的阈值是按「每页字数」算的，1 页足够区分「有字」和「没字」。
	return Extraction{
		Text:  MergeLines(CleanText(text)),
		Pages: 1,
	}, nil
}

// docxText 从 document.xml 里取出文字。
//
// 只关心三种标签：
//   - <w:t>   文本片段
//   - <w:tab> 制表符
//   - <w:br> / <w:cr> 换行
//
// 段落结束（</w:p>）补一个换行，这样段落边界能保留下来。
// 表格、页眉、脚注都不处理——简历正文基本不会只有表格。
func docxText(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	var b strings.Builder
	inText := false

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}

	return b.String(), nil
}

// Extract 按文件头自动判断格式并抽取文字。
//
// 用魔数而不是扩展名：用户把 .docx 改名成 .pdf 上传的情况真实存在，
// 按扩展名判断会在那种时候给出莫名其妙的错误。
func Extract(r io.ReaderAt, size int64) (Extraction, error) {
	if size <= 0 {
		return Extraction{}, ErrUnsupported
	}

	var head [4]byte
	n, err := r.ReadAt(head[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Extraction{}, fmt.Errorf("读取文件头失败: %w", err)
	}
	if n < 4 {
		return Extraction{}, ErrUnsupported
	}

	switch {
	case bytes.Equal(head[:], []byte("%PDF")):
		return ExtractPDF(r, size)
	case bytes.Equal(head[:], []byte("PK\x03\x04")):
		return ExtractDOCX(r, size)
	default:
		return Extraction{}, ErrUnsupported
	}
}
