package resume

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// 抽取阶段的错误。handler 根据它们决定给用户什么提示。
var (
	ErrNotPDF    = errors.New("不是有效的 PDF 文件")
	ErrEncrypted = errors.New("PDF 已加密，无法读取")
)

// Extraction 是一次抽取的结果。
// Pages 和 Text 一起用：单看 Text 判断不了「这页是空的」还是「整份是扫描件」。
type Extraction struct {
	Text  string
	Pages int
}

// ExtractPDF 从 PDF 里抽出纯文本。
//
// 只支持文本型 PDF。扫描件（图片）抽不出文字，返回空 Text——
// 调用方用 Scanned() 判断，再决定是「降级」还是「明确不支持」。
//
// r 用 io.ReaderAt 而不是 io.Reader：PDF 的交叉引用表需要随机访问，
// 用 io.Reader 就得先整个读进内存，白白多一份拷贝。
func ExtractPDF(r io.ReaderAt, size int64) (ex Extraction, err error) {
	if size <= 0 {
		return ex, ErrNotPDF
	}

	// 这个库遇到畸形 PDF 会 panic。上传的是用户文件，不能让它拖垮整个进程，
	// 所以这里兜一层，把 panic 转成普通错误。
	defer func() {
		if rec := recover(); rec != nil {
			ex = Extraction{}
			err = fmt.Errorf("%w: 解析时发生异常 (%v)", ErrNotPDF, rec)
		}
	}()

	reader, err := pdf.NewReader(r, size)
	if err != nil {
		return ex, fmt.Errorf("%w: %v", ErrNotPDF, err)
	}

	ex.Pages = reader.NumPage()

	var b strings.Builder
	for i := 1; i <= ex.Pages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}

		text, pageErr := page.GetPlainText(nil)
		if pageErr != nil {
			// 单页抽不出来不影响其他页，继续往下走
			continue
		}

		b.WriteString(text)
		b.WriteByte('\n')
	}

	// 先清洗（去控制字符、统一换行），再归并被拆碎的短行。
	// 顺序不能反：归并依赖 CleanText 已经压掉多余空行。
	ex.Text = MergeLines(CleanText(b.String()))
	return ex, nil
}
