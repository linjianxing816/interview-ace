// Package resume 负责简历文本的清洗与结构化切分。
//
// 这里只有纯函数：输入字符串，输出字符串或结构体，不碰文件、网络、数据库。
// PDF 抽取在 extractor.go 里单独实现，方便用假数据测这些规则。
package resume

import (
	"strings"
	"unicode"
)

// 扫描件判定阈值：平均每页可提取字符数低于这个值，就认为不是文本型 PDF。
//
// 为什么是 20：一页正常的文字简历至少几百字；扫描件抽取出来通常只有
// 页码、页眉几个字符，甚至完全是空。20 这个数量级能稳稳分开两者，
// 又不至于把「只有一页、内容很少的实习简历」误判成扫描件。
const minRunesPerPage = 20

// 降级切片长度：按这个粒度把原文切给 LLM，避免超长 prompt。
const chunkRunes = 1500

// CleanText 把 PDF 抽出的原始文本整理成可用于分析的形态。
//
// 处理四类噪音：
//  1. 控制字符和零宽字符（PDF 里常见 \x00、\u200b）
//  2. Windows / 老式 Mac 换行统一成 \n
//  3. 行尾多余空格、连续空行压缩
//  4. 英文单词被换行截断的情况："engi-\nneering" → "engineering"
func CleanText(raw string) string {
	if raw == "" {
		return ""
	}

	// 换行统一
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	// 去掉控制字符和零宽字符，保留 \n 和 \t
	raw = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		// 零宽空格 / 零宽连字 / BOM
		if r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\ufeff' {
			return -1
		}
		return r
	}, raw)

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimRight(line, " \t")

		// 连字符换行：上一行以 "-" 结尾，下一行以小写字母开头
		if n := len(out); n > 0 && strings.HasSuffix(out[n-1], "-") && startsWithLowerASCII(line) {
			out[n-1] = strings.TrimSuffix(out[n-1], "-") + line
			continue
		}

		out = append(out, line)
	}

	// 连续空行压成一个
	var b strings.Builder
	blank := 0
	for _, line := range out {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	return strings.TrimSpace(b.String())
}

func startsWithLowerASCII(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= 'a' && c <= 'z'
}

// Scanned 判断一份 PDF 是不是「扫描件」（没有可提取的文字层）。
//
// 只看「总字符数 / 页数」是否低于阈值。判断不出来时宁可真（保守）：
// 把文本型误判成扫描件，用户会看到「不支持」；反过来则会拿垃圾喂给 LLM，
// 生成一堆莫名其妙的面试题——后者更难排查。
func Scanned(text string, pages int) bool {
	if pages <= 0 {
		pages = 1
	}
	return len([]rune(strings.TrimSpace(text)))/pages < minRunesPerPage
}

// Sections 是按标题切出来的段落。没匹配到的内容进 Other。
type Sections struct {
	Education []string
	Projects  []string
	Work      []string
	Skills    []string
	Other     []string
}

// Empty 表示一个段落都没切出来（通常是版式特别怪，或者纯图片）。
func (s Sections) Empty() bool {
	return len(s.Education) == 0 && len(s.Projects) == 0 &&
		len(s.Work) == 0 && len(s.Skills) == 0 && len(s.Other) == 0
}

// sectionHeadings 把常见标题映射到段落。键是去掉空格和冒号后的标题。
var sectionHeadings = map[string]string{
	"教育背景": "education", "教育经历": "education", "学历": "education", "education": "education",
	"项目经历": "projects", "项目经验": "projects", "项目": "projects", "projects": "projects",
	"工作经历": "work", "实习经历": "work", "工作经验": "work", "工作": "work", "experience": "work",
	"专业技能": "skills", "技能": "skills", "技能清单": "skills", "skills": "skills",
}

// SplitSections 按常见标题把简历切成四段。
//
// 规则很朴素：一行去掉空白和冒号后，如果整行就是一个已知标题（长度 ≤ 10），
// 就认为它是段落分隔。不做模糊匹配——宁可少切，也不要切错。
// 标题之前的内容进 Other（通常是姓名、联系方式）。
func SplitSections(text string) Sections {
	var s Sections
	cur := &s.Other

	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		if target, ok := lookupHeading(line); ok {
			switch target {
			case "education":
				cur = &s.Education
			case "projects":
				cur = &s.Projects
			case "work":
				cur = &s.Work
			case "skills":
				cur = &s.Skills
			}
			continue
		}

		// 不是标题就归到当前段落。这里不能因为「不是标题」而丢弃内容。
		*cur = append(*cur, line)
	}

	return s
}

// lookupHeading 判断这一行是不是段落标题，是的话返回目标段落名。
//
// 单独拆出来是因为「不是标题」有两种情况：空行（调用方跳过）
// 和正文（调用方保留）。早先把两者混在一起返回空串，
// 导致超过 10 个字符的正文行被整行丢掉。
func lookupHeading(line string) (string, bool) {
	key := normalizeHeading(line)
	if key == "" {
		return "", false
	}
	target, ok := sectionHeadings[key]
	return target, ok
}

// normalizeHeading 去掉空白、冒号和常见装饰符，转小写。
// 超过 10 个字符的肯定不是标题，返回空串。
func normalizeHeading(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	line = strings.Trim(line, "：:【】[]■●◆◇-—= ")
	line = strings.Join(strings.Fields(line), "")
	line = strings.ToLower(line)

	if len([]rune(line)) > 10 {
		return ""
	}
	return line
}

// Chunk 把长文本切成不超过 maxRunes 的片段，用于解析失败时降级喂给 LLM。
//
// 优先在换行处断开，实在没有换行才硬切——硬切会把一个词劈成两半，
// 但比整段超长被模型截断好。
func Chunk(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = chunkRunes
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var chunks []string
	var cur strings.Builder
	curLen := 0

	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, strings.TrimSpace(cur.String()))
			cur.Reset()
			curLen = 0
		}
	}

	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)

		// 单行就超长：先冲掉已有的，再硬切这一行
		if len(runes) > maxRunes {
			flush()
			for len(runes) > maxRunes {
				chunks = append(chunks, string(runes[:maxRunes]))
				runes = runes[maxRunes:]
			}
			if len(runes) > 0 {
				cur.WriteString(string(runes))
				curLen = len(runes)
			}
			continue
		}

		// 加上这一行会超：先冲掉
		if curLen+len(runes)+1 > maxRunes {
			flush()
		}

		if curLen > 0 {
			cur.WriteByte('\n')
			curLen++
		}
		cur.WriteString(line)
		curLen += len(runes)
	}

	flush()
	return chunks
}

// bulletSymbols 是圆点类列表符号，出现在行首就说明是独立条目。
// 刻意不含 "-" 和 "*"：中文简历里 "CET-4"、"2024-2026" 到处都是，
// 把它们当列表符号会把一个词劈成两行。
var bulletSymbols = []string{"•", "·", "●", "○", "▪", "◦", "▪︎"}

// dashBullets 只在后面跟空格时才算列表符号（英文简历的 "- " 写法）。
var dashBullets = []string{"- ", "* ", "－ "}

// MergeLines 把被 PDF 拆碎的短行按「空行」重新归并成段落。
//
// 为什么需要：PDF 里每个文本块是独立定位的，抽取后一块一行。
// 一份排版型中文简历（多列、标签值分离）能被拆成 200 多行、平均每行 6 个字。
// 直接喂给模型既浪费 token，也让模型难以判断哪些内容属于同一条目。
//
// 依据的信号：PDF 抽取结果里，**空行通常对应视觉上的段落间距**，
// 而同一视觉行被拆开的文本块之间没有空行。
//
// 三种情况不参与归并：
//  1. 已知段落标题（教育背景 / 项目经历…）单独成行
//  2. 以列表符号开头的行单独成行
//  3. 空行本身就是块的分隔
func MergeLines(text string) string {
	var out []string
	var cur []string

	flush := func() {
		if len(cur) > 0 {
			out = append(out, joinFragments(cur))
			cur = nil
		}
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)

		if line == "" {
			flush()
			continue
		}

		// 竖线类装饰符丢掉；其他纯符号（括号、冒号、连字符）并入当前块，
		// 它们往往是内容的一部分，丢了就是丢数据
		if isDecorative(line) {
			continue
		}
		if isSymbolOnly(line) {
			if len(cur) > 0 {
				cur = append(cur, line)
			}
			continue
		}

		if _, ok := lookupHeading(line); ok {
			flush()
			out = append(out, line)
			continue
		}

		if isBulletOnly(line) {
			// "•" 自己占一行时，让它作为新块的开头，等下一行拼进来
			flush()
			cur = append(cur, line)
			continue
		}

		if hasBulletPrefix(line) {
			flush()
			cur = append(cur, line)
			continue
		}

		cur = append(cur, line)
	}
	flush()

	return strings.Join(out, "\n")
}

func hasBulletPrefix(s string) bool {
	for _, p := range bulletSymbols {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	for _, p := range dashBullets {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// decorativeSymbols 是纯粹的排版装饰，单独出现时直接丢掉。
//
// 只列竖线类：它们在 PDF 里几乎只用来分隔两列，永远不是内容。
// 括号、冒号、连字符、破折号都不在此列——它们经常是内容的一部分
// （"（+86）"、"CET-4"、"2024.9—2028.6"），丢掉就是丢数据。
var decorativeSymbols = []string{"|", "｜", "丨", "¦"}

func isDecorative(s string) bool {
	for _, d := range decorativeSymbols {
		if s == d {
			return true
		}
	}
	return false
}

// isSymbolOnly 判断这一行是不是纯符号（没有字母和数字，且不超过 3 个字符）。
func isSymbolOnly(s string) bool {
	if s == "" {
		return false
	}
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		n++
		if n > 3 {
			return false
		}
	}
	return true
}

// isBulletOnly 判断这一行是不是只有一个列表符号（"•" 单独占一行）。
func isBulletOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range bulletSymbols {
		if s == p {
			return true
		}
	}
	return false
}

// joinFragments 拼接同一块的碎片。
// 两侧都是 ASCII 字母数字时补一个空格（"Saleor" + "testing" → "Saleor testing"），
// 其余情况直接拼（中文之间不该有空格）。
func joinFragments(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 && needSpaceBetween(b.String(), p) {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return b.String()
}

func needSpaceBetween(prev, next string) bool {
	if prev == "" || next == "" {
		return false
	}
	return isASCIIAlnum(prev[len(prev)-1]) && isASCIIAlnum(next[0])
}

func isASCIIAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
