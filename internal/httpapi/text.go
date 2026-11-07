package httpapi

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// defaultFilename 是文件名彻底不可用时的兜底。
const defaultFilename = "resume"

// sanitizeFilename 把客户端传来的文件名转成合法 UTF-8。
//
// 为什么需要：Windows 上的 curl、老版本浏览器、部分移动端会按**本地代码页**
// （中文系统是 GBK）发送 multipart 的 filename 字段。Go 原样收下，
// 这个字符串就是非法 UTF-8，写进 PostgreSQL 直接报
// `invalid byte sequence for encoding "UTF8": 0xb8`。
//
// 处理顺序：
//  1. 本来就是合法 UTF-8（现代浏览器、Go/Python 客户端）→ 原样返回
//  2. 不合法 → 试 GBK 解码（覆盖中文 Windows 的绝大多数情况）
//  3. 还是不行 → 用默认名，保证能存进去
//
// 不抛错：文件名存不下来不该让整次上传失败，用户关心的是简历内容。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return defaultFilename
	}
	if utf8.ValidString(name) {
		return name
	}

	if decoded, err := simplifiedchinese.GBK.NewDecoder().String(name); err == nil && utf8.ValidString(decoded) {
		decoded = strings.TrimSpace(decoded)
		if decoded != "" {
			return decoded
		}
	}

	return defaultFilename
}

// toValidUTF8 把可能非法的字符串清洗成合法 UTF-8（非法字节直接丢掉）。
//
// 数据库那一层不接受非法 UTF-8，而我们的文本来自用户上传的文件，
// 抽取库对某些畸形 PDF 可能吐出非法字节。这是最后一道保险。
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "")
}
