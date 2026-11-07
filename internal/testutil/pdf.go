// Package testutil 放测试共用的辅助函数。
// 它不是生产代码，只是为了让多个测试包不必各自复制一份造数据逻辑。
package testutil

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strings"
)

// BuildPDF 手工拼一个最小可用的单页 PDF，正文是 pageText。
//
// 为什么不引 PDF 生成库：为了造一条测试数据引一个依赖不划算。
// 手写要注意 xref 表里的字节偏移必须准确，否则解析会失败。
func BuildPDF(pageText string) []byte {
	var b bytes.Buffer
	var offsets []int

	obj := func(s string) {
		offsets = append(offsets, b.Len())
		b.WriteString(s)
	}

	b.WriteString("%PDF-1.4\n")
	obj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	obj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	obj("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")

	stream := fmt.Sprintf("BT /F1 12 Tf 72 700 Td (%s) Tj ET", escapePDFString(pageText))
	obj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	obj("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	xrefPos := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, off := range offsets {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefPos))

	return b.Bytes()
}

// escapePDFString 转义 PDF 字符串里的特殊字符（括号和反斜杠）。
func escapePDFString(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', ')', '\\':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// BuildPDFChinese 拼一个含中文的单页 PDF。
//
// 中文不能用 Helvetica——它只有 Latin 字形。这里用 Adobe 预定义的 CJK 字体
// STSong-Light + UniGB-UCS2-H 编码：字体不嵌进文件，由阅读器提供。
// 正文按 UTF-16BE 写成十六进制字符串 <4E2D6587> 这种形式。
//
// 这跟真实简历的字体子集不一样，但足以验证「抽取器能处理 CID 字体」这件事。
func BuildPDFChinese(pageText string) []byte {
	var b bytes.Buffer
	var offsets []int

	obj := func(s string) {
		offsets = append(offsets, b.Len())
		b.WriteString(s)
	}

	hex := utf16BEHex(pageText)
	stream := fmt.Sprintf("BT /F1 12 Tf 72 700 Td <%s> Tj ET", hex)

	b.WriteString("%PDF-1.4\n")
	obj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	obj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	obj("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
		"/Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	obj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	obj("5 0 obj\n<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light " +
		"/Encoding /UniGB-UCS2-H /DescendantFonts [6 0 R] >>\nendobj\n")
	obj("6 0 obj\n<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light " +
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /DW 1000 >>\nendobj\n")

	xrefPos := b.Len()
	b.WriteString("xref\n0 7\n0000000000 65535 f \n")
	for _, off := range offsets {
		b.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	b.WriteString(fmt.Sprintf("trailer\n<< /Size 7 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefPos))

	return b.Bytes()
}

// utf16BEHex 把字符串转成 UTF-16BE 的十六进制表示（PDF 的 CID 字符串格式）。
func utf16BEHex(s string) string {
	var sb bytes.Buffer
	for _, r := range s {
		sb.WriteByte(byte(r >> 8))
		sb.WriteByte(byte(r))
	}
	return strings.ToUpper(hex.EncodeToString(sb.Bytes()))
}

// BuildDOCX 造一个最小的 .docx：本质是个 zip，正文在 word/document.xml。
// 只写正文部件，不写 [Content_Types].xml / 样式 / 关系——抽取器只认正文。
func BuildDOCX(paragraphs ...string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("word/document.xml")
	if err != nil {
		panic("构造测试 docx 失败: " + err.Error())
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		sb.WriteString("<w:p><w:r><w:t>")
		_ = xml.EscapeText(&sb, []byte(p))
		sb.WriteString("</w:t></w:r></w:p>")
	}
	sb.WriteString(`</w:body></w:document>`)

	if _, err := w.Write([]byte(sb.String())); err != nil {
		panic("写入测试 docx 失败: " + err.Error())
	}
	if err := zw.Close(); err != nil {
		panic("关闭测试 docx 失败: " + err.Error())
	}

	return buf.Bytes()
}

// BuildRawDOCX 用给定的 document.xml 内容造 docx，用于测特殊标签。
func BuildRawDOCX(documentXML string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		panic("构造测试 docx 失败: " + err.Error())
	}
	if _, err := w.Write([]byte(documentXML)); err != nil {
		panic("写入测试 docx 失败: " + err.Error())
	}
	if err := zw.Close(); err != nil {
		panic("关闭测试 docx 失败: " + err.Error())
	}
	return buf.Bytes()
}

// BuildEmptyDOCX 造一个只有其他部件、没有 word/document.xml 的包。
func BuildEmptyDOCX() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("[Content_Types].xml")
	if err != nil {
		panic("构造测试 docx 失败: " + err.Error())
	}
	if _, err := w.Write([]byte(`<?xml version="1.0"?><Types/>`)); err != nil {
		panic("写入测试 docx 失败: " + err.Error())
	}
	if err := zw.Close(); err != nil {
		panic("关闭测试 docx 失败: " + err.Error())
	}
	return buf.Bytes()
}
