package interview

import (
	"fmt"
	"strings"
)

// 题目去重。
//
// 为什么需要：prompt 里写了「不要重复已经问过的题」，但模型不保证遵守——
// 它会把同一道题换个说法再问一遍（「怎么防超卖」→「如何避免超卖」）。
// prompt 是约束，这里是兜底：模型不听话时程序拦住它。
//
// 为什么只查「换话题」的新题，不查追问：
// 追问天然和原题高度相似——原题问「怎么防超卖」，追问问「具体怎么实现的」，
// 二元组重叠率很高。查重会把正常的追问全部打回，追问功能直接废掉。
// 只有 action=next（换话题）的题目才应该和已问题目完全不同。

// dupThreshold 是判定「两道题是同一题」的相似度阈值。
//
// 用 Dice 系数（0-1）+ 字符级 bigram：
//   - 中文不需要分词，按字切二元组就够
//   - 改几个字的题目相似度通常在 0.8 以上
//   - 同一项目的两个不同问题（「怎么防超卖」vs「怎么压测」）共享「怎么」这类
//     高频二元组，相似度能到 0.3-0.4
//
// 0.75 是两者之间：改字能拦住，不同话题不会误杀。
const dupThreshold = 0.75

// dupMinRunes 是查重的最短长度。
// 太短的题目（「介绍一下」）二元组太少，相似度没有统计意义。
const dupMinRunes = 10

// similarity 用字符级 bigram 的 Dice 系数算相似度。
//
// Dice = 2 × |A ∩ B| / (|A| + |B|)，比 Jaccard 对短文本更宽容：
// 两个 20 字的题目改 3 个字，Dice 约 0.85，Jaccard 约 0.74。
func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}

	setA := make(map[string]struct{}, len(ra)-1)
	for i := 0; i+1 < len(ra); i++ {
		setA[string(ra[i:i+2])] = struct{}{}
	}
	setB := make(map[string]struct{}, len(rb)-1)
	for i := 0; i+1 < len(rb); i++ {
		setB[string(rb[i:i+2])] = struct{}{}
	}

	inter := 0
	for g := range setA {
		if _, ok := setB[g]; ok {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(setA)+len(setB))
}

// FindDuplicate 在已问题目里找和新题最像的一条。
//
// 返回值：最像的那道题（原始文本，给提示用）+ 相似度。
// 没有超过阈值时返回空串，但相似度仍然返回（调用方可打日志看「差一点就重复」的题）。
//
// 比较前两边都做归一化（去标点空白），否则「怎么防超卖？」和「怎么防超卖?」
// 会因为一个标点被判成不同题。
func FindDuplicate(question string, asked []string) (string, float64) {
	nq := normalizeQuestion(question)
	if len([]rune(nq)) < dupMinRunes {
		return "", 0
	}

	var (
		best      string
		bestScore float64
	)
	for _, prev := range asked {
		np := normalizeQuestion(prev)
		if len([]rune(np)) < dupMinRunes {
			continue
		}
		if s := similarity(nq, np); s > bestScore {
			best, bestScore = prev, s
		}
	}

	if bestScore < dupThreshold {
		return "", bestScore
	}
	return best, bestScore
}

// duplicateHint 是打回重复题时给模型的提示。
//
// 带上相似度和原题：只说「重复了」，模型下一轮多半还是同一道题；
// 指出「和哪道题像」，它才知道该往哪个方向换。
func duplicateHint(dup string, score float64) string {
	return fmt.Sprintf(
		"这道题和已经问过的「%s」重复了（相似度 %.0f%%）。"+
			"请换一个完全不同的话题重新出题，不要只改措辞。",
		truncateRunes(dup, 40), score*100)
}

// normalizeQuestion 去掉空白和标点后再比，避免「怎么防超卖？」和
// 「怎么防超卖?」因为一个标点判成不同题。
//
// 只保留汉字、字母、数字：这些才是题目语义的载体。
func normalizeQuestion(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
			b.WriteRune(r)
		}
	}
	return b.String()
}
