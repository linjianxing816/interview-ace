package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"interview-ace/internal/llm"
)

// 追问类型。这些不是拍脑袋编的，是从牛客/CSDN 的真实面经里归出来的七类。
//
// 真实面试官不会随机深挖，他们有固定的追问套路：
//   - 归因：不问「你做了什么」，问「提升来自哪一类资源」「你觉得本质是什么」
//   - 递进：做了什么 → 为什么这么设计 → 出过什么问题 → 怎么定位 → 怎么改
//   - 价值：覆盖率测到之后能拿来干什么
//   - 盲区：当初有没有没考虑过的问题
//   - 验证：你怎么知道兜底真的生效了
//   - 边界：如果并发翻倍 / 数据量翻十倍
//   - 权衡：为什么选 A 不选 B
const (
	FollowupCause     = "cause"     // 归因
	FollowupProbe     = "probe"     // 递进（继续挖细节）
	FollowupValue     = "value"     // 价值
	FollowupBlindspot = "blindspot" // 盲区
	FollowupVerify    = "verify"    // 验证手段
	FollowupBoundary  = "boundary"  // 边界
	FollowupTradeoff  = "tradeoff"  // 权衡
)

// followupKinds 是允许的追问类型，也用来做校验。
var followupKinds = map[string]string{
	FollowupCause:     "归因（为什么有效、本质是什么）",
	FollowupProbe:     "递进（继续挖细节、具体怎么做）",
	FollowupValue:     "价值（这个结果拿来干什么）",
	FollowupBlindspot: "盲区（当初有没有没考虑到的）",
	FollowupVerify:    "验证（怎么确认它真的生效）",
	FollowupBoundary:  "边界（如果量级变大/条件变化）",
	FollowupTradeoff:  "权衡（为什么选这个不选那个）",
}

// followupShortLabels 是报告和前端用的短标签。
//
// 为什么不直接复用 followupKinds 的值：那是写给模型看的说明（带括号解释），
// 塞进报告的表格里会撑爆一列。两处共用同一组 key，加类型时两边都要改。
var followupShortLabels = map[string]string{
	FollowupCause:     "归因",
	FollowupProbe:     "递进",
	FollowupValue:     "价值",
	FollowupBlindspot: "盲区",
	FollowupVerify:    "验证",
	FollowupBoundary:  "边界",
	FollowupTradeoff:  "权衡",
}

// FollowupShortLabel 返回追问类型的短标签，未知类型原样返回。
func FollowupShortLabel(kind string) string {
	if v, ok := followupShortLabels[kind]; ok {
		return v
	}
	return kind
}

// MaxFollowupDepth 是同一话题连续追问的最大层数。
//
// 为什么是 2：面经里普遍是「项目各问 2~4 层」，但那是含第一问的总层数。
// 我们这边第一问算一层，再追两层就到了「怎么定位、怎么改」的深度，
// 再追下去会变成钻牛角尖，真实面试官不会这样。
const MaxFollowupDepth = 2

// ErrBadStep 表示模型给的「下一步」不合法。
var ErrBadStep = errors.New("模型返回的下一步不合法")

// StepRequest 是「答完一题之后下一步干什么」的输入。
type StepRequest struct {
	RoleType      string
	JDText        string
	ResumeSummary string
	Asked         []string // 已经问过的题目
	History       []QAPair // 之前的问答（用于让模型知道上下文）
	LastQuestion  string   // 刚问的题
	LastAnswer    string   // 刚答的内容
	FollowupDepth int      // 当前话题已经追问了几层
	Seq           int      // 这一题会是第几题
}

const stepSystemPrompt = `你是一名技术面试官，正在决定「继续追问」还是「换个话题」。

只输出 JSON，不要 markdown 代码块，不要解释：
{"action":"followup","followup_kind":"cause","question":"追问内容"}
或
{"action":"next","type":"project","question":"新题目"}

【什么时候追问】
候选人的回答里出现了下面任一情况，就追问：
- 只说了「用了什么」没说「为什么用」
- 有模糊表述（「优化了一下」「处理了异常」「做了兜底」）没讲清具体怎么做
- 给了结论没给数据（「性能提升了」但没说从多少到多少）
- 提到问题但没说怎么定位、怎么改

【什么时候换话题】
- 回答已经具体到实现细节和数据，再追就是钻牛角尖
- 已经追问了 2 层（同一话题最多追 2 层）
- 回答明显是背的/空的，再追也问不出东西

【追问的七种类型，按回答的薄弱点选一个】
- cause 归因：问为什么有效、本质是什么。例：「这个提升本质上是解决了什么瓶颈？」
- probe 递进：继续挖具体实现。例：「具体怎么实现的？能说下关键那几行逻辑吗？」
- value 价值：问结果拿来干什么。例：「覆盖率测到 85% 之后，你拿这个数字做了什么决策？」
- blindspot 盲区：问当初漏掉的。例：「现在回头看，当初有没有完全没考虑到的情况？」
- verify 验证：问怎么确认生效。例：「你怎么知道这个兜底真的生效了，而不是碰巧没触发？」
- boundary 边界：问量级/条件变化。例：「如果并发从 50 涨到 500，你这个方案还成立吗？」
- tradeoff 权衡：问为什么选这个。例：「为什么用唯一索引而不是分布式锁？两者在这个场景下的差别是什么？」

【硬规则】
1. 追问必须针对他刚才那句话里的**具体模糊点**，不要泛泛问「能详细说说吗」。
2. 追问一次只问一件事，不要在一句里塞三个问题。
3. 换话题时 type 只能是 tech / project / behavior。
4. 不要重复已经问过的题目。
5. 不要问八股题，要围绕他的项目和回答。`

// NextStep 决定答完这一题之后是追问还是换话题。
func NextStep(ctx context.Context, c Completer, req StepRequest) (Question, error) {
	if c == nil {
		return Question{}, errors.New("模型客户端未初始化")
	}

	msgs := []llm.Message{
		{Role: "system", Content: stepSystemPrompt},
		{Role: "user", Content: buildStepPrompt(req)},
	}

	var lastErr error
	for attempt := 1; attempt <= maxQuestionAttempts; attempt++ {
		reply, err := c.Chat(ctx, msgs)
		if err != nil {
			return Question{}, fmt.Errorf("调用模型失败: %w", err)
		}

		q, err := decodeStep(reply.Content, req.FollowupDepth)
		if err != nil {
			lastErr = err
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: reply.Content},
				llm.Message{Role: "user", Content: "上一个输出不合法：" + err.Error() + "。请只输出 JSON 对象。"},
			)
			continue
		}

		// 换话题的新题要查重，追问不查。
		//
		// 追问本来就在同一话题上深挖，和原题的二元组重叠率很高——
		// 对追问查重会把正常的「具体怎么实现的」全部打回，追问功能直接废掉。
		// prompt 里已经写了「不要重复」，这里是模型不听话时的程序兜底。
		if q.Type != TypeFollowup {
			if dup, score := FindDuplicate(q.Text, req.Asked); dup != "" {
				lastErr = fmt.Errorf("%w: 和已问过的「%s」太像（%.0f%%）",
					ErrBadStep, truncateRunes(dup, 40), score*100)
				msgs = append(msgs,
					llm.Message{Role: "assistant", Content: reply.Content},
					llm.Message{Role: "user", Content: duplicateHint(dup, score)},
				)
				continue
			}
		}

		return q, nil
	}

	return Question{}, fmt.Errorf("重试 %d 次后仍未拿到合法的下一步: %w", maxQuestionAttempts, lastErr)
}

func buildStepPrompt(req StepRequest) string {
	var b strings.Builder

	b.WriteString("岗位方向：")
	b.WriteString(fallback(req.RoleType, "未指定"))
	b.WriteString("\n岗位描述：\n")
	b.WriteString(truncateRunes(fallback(req.JDText, "（未提供）"), 800))
	b.WriteString("\n\n候选人简历摘要：\n")
	b.WriteString(truncateRunes(fallback(req.ResumeSummary, "（未提供）"), 1000))

	// 只带最近三轮问答，更早的对判断「这句话该怎么追」没帮助，还费 token
	if len(req.History) > 3 {
		req.History = req.History[len(req.History)-3:]
	}
	if len(req.History) > 0 {
		b.WriteString("\n\n最近的问答：\n")
		for _, h := range req.History {
			b.WriteString(fmt.Sprintf("问：%s\n答：%s\n",
				truncateRunes(h.Question, 200), truncateRunes(fallback(h.Answer, "（未作答）"), 400)))
		}
	}

	b.WriteString("\n\n【刚问的题】\n")
	b.WriteString(truncateRunes(req.LastQuestion, 300))
	b.WriteString("\n\n【刚答的内容】\n")
	b.WriteString(truncateRunes(fallback(req.LastAnswer, "（未作答）"), 900))

	b.WriteString(fmt.Sprintf("\n\n当前话题已追问 %d 层，上限 %d 层。", req.FollowupDepth, MaxFollowupDepth))
	if req.FollowupDepth >= MaxFollowupDepth {
		b.WriteString("已到上限，必须换话题（action=next）。")
	}

	if len(req.Asked) > 0 {
		b.WriteString("\n\n已经问过的题（不要重复）：\n")
		for _, q := range req.Asked {
			b.WriteString("- ")
			b.WriteString(truncateRunes(q, 100))
			b.WriteString("\n")
		}
	}

	b.WriteString(fmt.Sprintf("\n这一题会是第 %d 题。", req.Seq))
	return b.String()
}

// decodeStep 解析并校验「下一步」。
func decodeStep(raw string, currentDepth int) (Question, error) {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return Question{}, fmt.Errorf("%w: 找不到 JSON 对象", ErrBadStep)
	}

	var body struct {
		Action       string `json:"action"`
		Type         string `json:"type"`
		FollowupKind string `json:"followup_kind"`
		Question     string `json:"question"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &body); err != nil {
		return Question{}, fmt.Errorf("%w: %v", ErrBadStep, err)
	}

	text := strings.TrimSpace(body.Question)
	if len([]rune(text)) < minQuestionRunes {
		return Question{}, fmt.Errorf("%w: 题目太短（%d 字）", ErrBadStep, len([]rune(text)))
	}

	switch body.Action {
	case "followup":
		// 深度已到上限还硬要追问 → 当成非法，让模型重来
		if currentDepth >= MaxFollowupDepth {
			return Question{}, fmt.Errorf("%w: 已追问 %d 层，不能再追", ErrBadStep, currentDepth)
		}
		kind := strings.TrimSpace(strings.ToLower(body.FollowupKind))
		if _, ok := followupKinds[kind]; !ok {
			return Question{}, fmt.Errorf("%w: 未知追问类型 %q", ErrBadStep, body.FollowupKind)
		}
		return Question{Text: text, Type: TypeFollowup, Kind: kind}, nil

	case "next":
		typ := strings.TrimSpace(strings.ToLower(body.Type))
		switch typ {
		case TypeTech, TypeProject, TypeBehavior:
		default:
			return Question{}, fmt.Errorf("%w: 换话题时 type 只能是 tech/project/behavior，实际 %q", ErrBadStep, body.Type)
		}
		return Question{Text: text, Type: typ}, nil

	default:
		return Question{}, fmt.Errorf("%w: action 只能是 followup / next，实际 %q", ErrBadStep, body.Action)
	}
}
