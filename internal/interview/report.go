package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"interview-ace/internal/llm"
)

// 评分维度，跟数据库 scores.dimension 的 check 约束一一对应。
const (
	DimAccuracy = "accuracy" // 准确性：答对没有、有没有事实错误
	DimClarity  = "clarity"  // 清晰度：表达是否结构化、能不能听懂
	DimDepth    = "depth"    // 深度：有没有讲到原理、边界、权衡
)

// Dimensions 是固定顺序的三个维度。
// 顺序固定是为了让测试和前端渲染都稳定——map 遍历顺序不可控。
var Dimensions = []string{DimAccuracy, DimClarity, DimDepth}

// 分数取值范围。
const (
	MinScore = 1
	MaxScore = 5
)

// 评分的错误。
var (
	ErrBadReport = errors.New("模型返回的评分不合法")
)

// maxScoreAttempts 是评分重试次数。评分比出题更怕跑偏，多给一次机会。
const maxScoreAttempts = 3

// Score 是一个维度上的得分。
type Score struct {
	Dimension string `json:"dimension"`
	Score     int    `json:"score"`
	Comment   string `json:"comment"`
}

// TurnScore 是单轮的三个维度。
type TurnScore struct {
	Seq    int     `json:"seq"`
	Scores []Score `json:"scores"`
}

// FollowupStat 是按追问类型聚合的失分情况。
//
// 为什么值得单独统计：总分只回答「差多少」，不回答「差在哪」。
// 同样是 62 分，可能是边界类追问扛不住（没想过量级变化），
// 也可能是盲区类追问答不上来（没复盘过自己的方案）——这两类的改法完全不同，
// 混在一个总分里就给不出针对性建议。
type FollowupStat struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Count 是这类追问出现了几轮。
	Count int `json:"count"`
	// AvgScore 是这些轮次三个维度的平均分，保留 1 位小数。
	AvgScore float64 `json:"avg_score"`
}

// DefaultSamples 是默认采样次数。
//
// 为什么要采样多次：模型对同一份回答的评分本身有抖动，
// 单次评分算出来的「一致性」是假的。采样 3 次取中位数，
// 才能看出哪些维度是稳定的、哪些是模型在飘。
const DefaultSamples = 3

// MaxSamples 是采样上限，防止有人调太高把成本打爆。
const MaxSamples = 5

// Report 是一份完整的面试报告。
//
// Total 和 Level 不由模型给——模型只给三个维度的分和文字评价，
// 总分由程序按固定公式算。否则同一场面试跑两次会出两个总分，
// 「评分一致性」这个卖点就没了。
type Report struct {
	Total       int         `json:"total"`
	Level       string      `json:"level"`
	Dimensions  []Score     `json:"dimensions"`
	TurnScores  []TurnScore `json:"turn_scores"`
	Strengths   []string    `json:"strengths"`
	Weaknesses  []string    `json:"weaknesses"`
	Suggestions []string    `json:"suggestions"`

	// SampleCount 是实际成功的采样次数。
	SampleCount int `json:"sample_count"`
	// SampleFailed 是失败的采样次数。不报出来用户会以为「只采了一次」，
	// 实际上是几次失败被悄悄丢掉了。
	SampleFailed int `json:"sample_failed,omitempty"`
	// Consistent 表示各次采样在三个维度上的分差都不超过 1 分。
	// 不稳定的维度会列在 UnstableDims 里，让用户知道这个分数不太可信。
	Consistent   bool     `json:"consistent"`
	UnstableDims []string `json:"unstable_dims,omitempty"`

	// FollowupStats 按追问类型聚合，最弱的排前面。
	// 由程序算，不经过模型——归因是确定性逻辑，让模型算等于又引入一次抖动。
	FollowupStats []FollowupStat `json:"followup_stats,omitempty"`
}

// ScoreRequest 是评分的输入。
type ScoreRequest struct {
	RoleType string
	JDText   string
	Turns    []QAPair
	// Samples 是采样次数。0 用默认值 3；上限 5。
	Samples int
}

// QAPair 是一问一答。
type QAPair struct {
	Seq      int
	Question string
	Answer   string
	// Kind 是追问类型，非追问的轮次为空。
	// 评分本身不看这个字段，只在报告里做「哪类追问失分最多」的归因。
	Kind string
}

const scoreSystemPrompt = `你是一名严格但公正的技术面试评分官。根据问答记录给候选人打分。

只输出 JSON，不要 markdown 代码块，不要解释：
{
  "dimensions": [
    {"dimension":"accuracy","score":3,"comment":"一句话依据"},
    {"dimension":"clarity","score":3,"comment":"一句话依据"},
    {"dimension":"depth","score":3,"comment":"一句话依据"}
  ],
  "turn_scores": [
    {"seq":1,"scores":[
      {"dimension":"accuracy","score":3,"comment":"..."},
      {"dimension":"clarity","score":3,"comment":"..."},
      {"dimension":"depth","score":3,"comment":"..."}
    ]}
  ],
  "strengths": ["具体优点，要引用他的原话或做法"],
  "weaknesses": ["具体不足，指出缺了什么"],
  "suggestions": ["可执行的改进建议"]
}

评分标准（1-5 分）：
- accuracy 准确性：答案事实是否正确、有没有明显技术错误。5=完全正确且有细节，3=大体对但含糊，1=明显错误或答非所问。
- clarity 清晰度：表达是否有条理。5=先结论后展开、分点清楚，3=能听懂但绕，1=听不懂。
- depth 深度：是否讲到原理、边界条件、权衡取舍。5=讲到底层和取舍，3=停留在「用了什么」，1=只会背结论。

规则：
1. 三个维度必须都出现，分数必须是 1-5 的整数。
2. turn_scores 必须覆盖每一轮，seq 和输入的轮次一一对应。
3. 评价必须引用候选人的具体表述，不要写「表现良好」这种空话。
4. 答得不好就给低分，不要为了鼓励而抬高。
5. 不要自己算总分，也不要输出 total 字段。`

// Score 给整场面试打分。
//
// 采样多次取中位数：单次评分的分数本身有抖动，
// 用它算出来的「一致性」是假的。多次采样才能看出哪个维度在飘。
//
// 采样是**并发**跑的：三次评分互相不依赖，串行跑等于把等待时间乘三。
// 实测串行 15-30 秒，并发后约 10 秒（单次调用的耗时决定）。
//
// 结果按「第几次采样」排序后再合并，不是按完成顺序——
// 否则哪次先返回，报告里的文字评价就跟着变，同样的输入会出不同的报告。
//
// 部分采样失败时用成功的那些继续算，全失败才报错——
// 让用户为一次超时重跑整场面试不值得。
func ScoreInterview(ctx context.Context, c Completer, req ScoreRequest) (Report, error) {
	if c == nil {
		return Report{}, errors.New("模型客户端未初始化")
	}
	if len(req.Turns) == 0 {
		return Report{}, errors.New("没有可评分的问答")
	}

	samples := req.Samples
	if samples <= 0 {
		samples = DefaultSamples
	}
	if samples > MaxSamples {
		samples = MaxSamples
	}

	// 最多 5 个 goroutine，不值得上信号量。
	// 通道带缓冲，保证所有 goroutine 都能把结果塞进去再退出，不会泄漏。
	type outcome struct {
		idx    int
		report Report
		err    error
	}
	results := make(chan outcome, samples)

	for i := 0; i < samples; i++ {
		go func(idx int) {
			r, err := scoreOnce(ctx, c, req)
			results <- outcome{idx: idx, report: r, err: err}
		}(i)
	}

	// 先全部收完再处理：这样「部分失败」的判断和串行版完全一致
	collected := make([]outcome, samples)
	for i := 0; i < samples; i++ {
		o := <-results
		collected[o.idx] = o
	}

	var reports []Report
	var firstErr error
	failures := 0

	for _, o := range collected {
		if o.err != nil {
			failures++
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		reports = append(reports, o.report)
	}

	if len(reports) == 0 {
		return Report{}, fmt.Errorf("评分全部失败: %w", firstErr)
	}

	out := mergeReports(reports)
	out.SampleFailed = failures
	out.FollowupStats = followupStats(req.Turns, out.TurnScores)
	return out, nil
}

// followupStats 按追问类型聚合，找出「哪类追问下分最低」。
//
// 分母是「该类型的轮次 × 3 个维度」，不是「该类型的轮次数」——
// 否则某类只有一轮、恰好那轮深度给了 1 分，会被放大成整类崩盘。
//
// 只统计有 Kind 的轮次：普通题目没有类型，混进来会把平均值拉平。
func followupStats(pairs []QAPair, turnScores []TurnScore) []FollowupStat {
	kindOf := make(map[int]string, len(pairs))
	for _, p := range pairs {
		if p.Kind != "" {
			kindOf[p.Seq] = p.Kind
		}
	}
	if len(kindOf) == 0 {
		return nil
	}

	type acc struct {
		sum   int
		dims  int
		turns int
	}
	byKind := make(map[string]*acc, len(kindOf))

	for _, ts := range turnScores {
		kind, ok := kindOf[ts.Seq]
		if !ok {
			continue
		}
		a := byKind[kind]
		if a == nil {
			a = &acc{}
			byKind[kind] = a
		}
		for _, s := range ts.Scores {
			a.sum += s.Score
			a.dims++
		}
		a.turns++
	}

	out := make([]FollowupStat, 0, len(byKind))
	for kind, a := range byKind {
		if a.dims == 0 {
			continue
		}
		out = append(out, FollowupStat{
			Kind:     kind,
			Label:    FollowupShortLabel(kind),
			Count:    a.turns,
			AvgScore: math.Round(float64(a.sum)/float64(a.dims)*10) / 10,
		})
	}

	// 最弱的排前面；同分时轮次多的在前（样本多的结论更可信）
	sort.Slice(out, func(i, j int) bool {
		if out[i].AvgScore != out[j].AvgScore {
			return out[i].AvgScore < out[j].AvgScore
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// scoreOnce 跑一次评分。
func scoreOnce(ctx context.Context, c Completer, req ScoreRequest) (Report, error) {
	msgs := []llm.Message{
		{Role: "system", Content: scoreSystemPrompt},
		{Role: "user", Content: buildScorePrompt(req)},
	}

	var lastErr error
	for attempt := 1; attempt <= maxScoreAttempts; attempt++ {
		reply, err := c.Chat(ctx, msgs)
		if err != nil {
			return Report{}, fmt.Errorf("调用模型失败: %w", err)
		}

		report, err := decodeReport(reply.Content, len(req.Turns))
		if err != nil {
			lastErr = err
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: reply.Content},
				llm.Message{Role: "user", Content: "上一个输出不合法：" + err.Error() + "。请只输出 JSON 对象，且覆盖全部轮次。"},
			)
			continue
		}

		return report, nil
	}

	return Report{}, fmt.Errorf("重试 %d 次后仍未拿到合法评分: %w", maxScoreAttempts, lastErr)
}

// mergeReports 把多次采样合成一份报告：每个维度取中位数分。
//
// 取中位数而不是平均值：平均会被一次异常采样拉偏，
// 中位数代表「模型最常给的分」，更接近真实水平。
func mergeReports(reports []Report) Report {
	if len(reports) == 1 {
		r := reports[0]
		r.SampleCount = 1
		r.Consistent = true
		return r
	}

	out := reports[0]
	out.SampleCount = len(reports)

	// 总评三维度取中位数
	out.Dimensions = mergeDimensions(reports)
	out.Total = computeTotal(out.Dimensions)
	out.Level = levelOf(out.Total)

	// 单轮分也取中位数
	out.TurnScores = mergeTurnScores(reports)

	// 文字部分取第一次的（中位数对应的那次），其余补充并集
	out.Strengths = mergeLines(reports, func(r Report) []string { return r.Strengths })
	out.Weaknesses = mergeLines(reports, func(r Report) []string { return r.Weaknesses })
	out.Suggestions = mergeLines(reports, func(r Report) []string { return r.Suggestions })

	out.UnstableDims = unstableDimensions(reports)
	out.Consistent = len(out.UnstableDims) == 0

	return out
}

// mergeDimensions 对每个维度取中位数分，comment 用中位数那次采样里的。
func mergeDimensions(reports []Report) []Score {
	out := make([]Score, 0, len(Dimensions))

	for _, dim := range Dimensions {
		var picked []Score
		for _, r := range reports {
			for _, d := range r.Dimensions {
				if d.Dimension == dim {
					picked = append(picked, d)
				}
			}
		}
		if len(picked) == 0 {
			continue
		}
		median := medianScore(picked)
		// comment 取分数等于中位数的那条，避免「5 分的评价配 3 分」
		comment := picked[0].Comment
		for _, p := range picked {
			if p.Score == median {
				comment = p.Comment
				break
			}
		}
		out = append(out, Score{Dimension: dim, Score: median, Comment: comment})
	}

	return out
}

func mergeTurnScores(reports []Report) []TurnScore {
	if len(reports) == 0 {
		return nil
	}

	// 用第一次的轮次列表做骨架
	out := make([]TurnScore, 0, len(reports[0].TurnScores))
	for _, base := range reports[0].TurnScores {
		merged := TurnScore{Seq: base.Seq}

		for _, dim := range Dimensions {
			var picked []Score
			for _, r := range reports {
				for _, ts := range r.TurnScores {
					if ts.Seq != base.Seq {
						continue
					}
					for _, d := range ts.Scores {
						if d.Dimension == dim {
							picked = append(picked, d)
						}
					}
				}
			}
			if len(picked) == 0 {
				continue
			}
			median := medianScore(picked)
			comment := picked[0].Comment
			for _, p := range picked {
				if p.Score == median {
					comment = p.Comment
					break
				}
			}
			merged.Scores = append(merged.Scores, Score{Dimension: dim, Score: median, Comment: comment})
		}

		out = append(out, merged)
	}

	return out
}

// medianScore 取中位数（偶数个时取下中位数，保证结果仍是 1-5 的整数）。
func medianScore(scores []Score) int {
	vals := make([]int, len(scores))
	for i, s := range scores {
		vals[i] = s.Score
	}
	sort.Ints(vals)
	return vals[(len(vals)-1)/2]
}

// unstableDimensions 找出各次采样分差超过 1 的维度。
//
// 分差 0 或 1 算稳定（模型给 4 和 5 只是表达习惯差异），
// 差 2 以上说明模型对这个维度的判断本身不稳定，分数不可信。
func unstableDimensions(reports []Report) []string {
	var out []string

	for _, dim := range Dimensions {
		minS, maxS := 99, -1
		for _, r := range reports {
			for _, d := range r.Dimensions {
				if d.Dimension != dim {
					continue
				}
				if d.Score < minS {
					minS = d.Score
				}
				if d.Score > maxS {
					maxS = d.Score
				}
			}
		}
		if maxS >= 0 && maxS-minS > 1 {
			out = append(out, dim)
		}
	}

	return out
}

// mergeLines 合并多次采样的文字列表：以第一次为主，其余补充，去重限量。
func mergeLines(reports []Report, pick func(Report) []string) []string {
	var all []string
	for _, r := range reports {
		all = append(all, pick(r)...)
	}
	return cleanLines(all)
}

// buildScorePrompt 拼评分输入。每条回答截断到 800 字：
// 超长回答多半是复制粘贴，全塞进去只会让模型抓不住重点。
func buildScorePrompt(req ScoreRequest) string {
	var b strings.Builder

	b.WriteString("岗位方向：")
	b.WriteString(fallback(req.RoleType, "未指定"))
	b.WriteString("\n岗位描述：\n")
	b.WriteString(truncateRunes(fallback(req.JDText, "（未提供）"), 800))

	b.WriteString("\n\n问答记录：\n")
	for _, t := range req.Turns {
		b.WriteString(fmt.Sprintf("\n[第 %d 轮]\n问：%s\n答：%s\n",
			t.Seq,
			truncateRunes(t.Question, 300),
			truncateRunes(fallback(t.Answer, "（未作答）"), 800)))
	}

	b.WriteString(fmt.Sprintf("\n共 %d 轮，请覆盖全部轮次。", len(req.Turns)))
	return b.String()
}

// decodeReport 解析并校验模型输出。
func decodeReport(raw string, wantTurns int) (Report, error) {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return Report{}, fmt.Errorf("%w: 找不到 JSON 对象", ErrBadReport)
	}

	var body struct {
		Dimensions  []Score     `json:"dimensions"`
		TurnScores  []TurnScore `json:"turn_scores"`
		Strengths   []string    `json:"strengths"`
		Weaknesses  []string    `json:"weaknesses"`
		Suggestions []string    `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &body); err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrBadReport, err)
	}

	dims, err := normalizeScores(body.Dimensions, "总评")
	if err != nil {
		return Report{}, err
	}

	// 轮次必须全覆盖：少一轮就意味着有回答没被评，报告会失真
	if len(body.TurnScores) != wantTurns {
		return Report{}, fmt.Errorf("%w: 应覆盖 %d 轮，实际 %d 轮", ErrBadReport, wantTurns, len(body.TurnScores))
	}

	turnScores := make([]TurnScore, 0, len(body.TurnScores))
	for _, ts := range body.TurnScores {
		if ts.Seq <= 0 {
			return Report{}, fmt.Errorf("%w: 轮次 seq 非法（%d）", ErrBadReport, ts.Seq)
		}
		scores, err := normalizeScores(ts.Scores, fmt.Sprintf("第 %d 轮", ts.Seq))
		if err != nil {
			return Report{}, err
		}
		turnScores = append(turnScores, TurnScore{Seq: ts.Seq, Scores: scores})
	}

	total := computeTotal(dims)

	return Report{
		Total:       total,
		Level:       levelOf(total),
		Dimensions:  dims,
		TurnScores:  turnScores,
		Strengths:   cleanLines(body.Strengths),
		Weaknesses:  cleanLines(body.Weaknesses),
		Suggestions: cleanLines(body.Suggestions),
	}, nil
}

// normalizeScores 校验并规整一组维度分：三个维度必须齐、分数在 1-5、comment 非空。
func normalizeScores(in []Score, where string) ([]Score, error) {
	byDim := make(map[string]Score, len(in))
	for _, s := range in {
		dim := strings.ToLower(strings.TrimSpace(s.Dimension))
		if !isKnownDimension(dim) {
			return nil, fmt.Errorf("%w: %s 出现未知维度 %q", ErrBadReport, where, s.Dimension)
		}
		if s.Score < MinScore || s.Score > MaxScore {
			return nil, fmt.Errorf("%w: %s 的 %s 分数 %d 超出 %d-%d",
				ErrBadReport, where, dim, s.Score, MinScore, MaxScore)
		}
		byDim[dim] = Score{
			Dimension: dim,
			Score:     s.Score,
			Comment:   truncateRunes(strings.TrimSpace(s.Comment), 300),
		}
	}

	out := make([]Score, 0, len(Dimensions))
	for _, dim := range Dimensions {
		s, ok := byDim[dim]
		if !ok {
			return nil, fmt.Errorf("%w: %s 缺少维度 %s", ErrBadReport, where, dim)
		}
		if s.Comment == "" {
			return nil, fmt.Errorf("%w: %s 的 %s 缺少评价", ErrBadReport, where, dim)
		}
		out = append(out, s)
	}
	return out, nil
}

func isKnownDimension(d string) bool {
	for _, k := range Dimensions {
		if d == k {
			return true
		}
	}
	return false
}

// computeTotal 把三个维度的平均分换算成百分制。
//
// 公式：avg(三个维度) / 5 * 100，四舍五入。
// 不让模型给总分的原因：模型每次给的数字不一样，同一份回答跑两次会出两个总分，
// 那样「评分一致性」就没法验证了。程序算的分只受维度分影响，可复现。
func computeTotal(dims []Score) int {
	if len(dims) == 0 {
		return 0
	}
	sum := 0
	for _, d := range dims {
		sum += d.Score
	}
	avg := float64(sum) / float64(len(dims))
	return int(math.Round(avg / float64(MaxScore) * 100))
}

// levelOf 按总分给等级。阈值是产品决策，写在代码里方便对齐前端。
func levelOf(total int) string {
	switch {
	case total >= 85:
		return "优秀"
	case total >= 70:
		return "良好"
	case total >= 55:
		return "一般"
	default:
		return "待提升"
	}
}

// cleanLines 清洗文字列表：去空、去重、限量。
func cleanLines(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = truncateRunes(strings.TrimSpace(s), 300)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= 6 {
			break
		}
	}
	return out
}
