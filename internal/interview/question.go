package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"interview-ace/internal/llm"
	"interview-ace/internal/parse"
)

// 出题阶段的错误。
var (
	// ErrBadQuestion 模型给的题不合法（类型不对、太短、跑题）。
	ErrBadQuestion = errors.New("模型返回的题目不合法")
)

// 题目类型，跟数据库 turns.question_type 的 check 约束对应。
const (
	TypeTech     = "tech"
	TypeProject  = "project"
	TypeBehavior = "behavior"
	TypeFollowup = "followup"
)

// maxQuestionAttempts 是出题的最大尝试次数。
// 模型偶尔给出空题或类型不对，加一句纠正再试一次通常就好。
const maxQuestionAttempts = 3

// minQuestionRunes 是题目最短长度。少于这个基本是「好的」这种废话。
const minQuestionRunes = 8

// Question 是一道题。
//
// Kind 只有追问才有值，取值见 followup.go 的七种追问类型；
// 普通题（tech/project/behavior）的 Kind 为空。
type Question struct {
	Text string
	Type string
	Kind string
}

// QuestionRequest 是出题的输入。
type QuestionRequest struct {
	RoleType      string   // backend / test / ai
	JDText        string   // 岗位描述
	ResumeSummary string   // 简历摘要（从结构化结果拼的）
	Asked         []string // 已经问过的题目，用来避免重复
	Seq           int      // 这是第几题（从 1 开始）
}

// Completer 是出题需要的最小模型能力。
type Completer interface {
	Chat(ctx context.Context, msgs []llm.Message) (llm.Reply, error)
}

const questionSystemPrompt = `你是一名技术面试官。根据候选人的简历和岗位描述，出一道面试题。

只输出 JSON，不要 markdown 代码块，不要解释：
{"question": "题目正文", "type": "tech|project|behavior"}

规则：
1. type 只能是 tech（技术知识）、project（深挖简历里的项目）、behavior（行为/协作）三者之一。
2. 优先围绕候选人简历里的**具体项目和技术栈**提问，不要出泛泛的八股题。
3. 一次只问一个问题，不要在一道题里塞三问。
4. 题目要具体到能被追问，比如「你说用 Postgres 做过…，当时怎么处理…」。
5. 不要重复已经问过的问题。`

// Generate 生成一道题。
//
// 失败时返回错误，调用方决定是降级用预置模板还是报错——
// 出题是主流程，不能静默失败。
func Generate(ctx context.Context, c Completer, req QuestionRequest) (Question, error) {
	if c == nil {
		return Question{}, errors.New("模型客户端未初始化")
	}

	userMsg := buildQuestionPrompt(req)

	msgs := []llm.Message{
		{Role: "system", Content: questionSystemPrompt},
		{Role: "user", Content: userMsg},
	}

	var lastErr error
	for attempt := 1; attempt <= maxQuestionAttempts; attempt++ {
		reply, err := c.Chat(ctx, msgs)
		if err != nil {
			return Question{}, fmt.Errorf("调用模型失败: %w", err)
		}

		q, err := decodeQuestion(reply.Content)
		if err != nil {
			lastErr = err
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: reply.Content},
				llm.Message{Role: "user", Content: "上一个输出不合法：" + err.Error() + "。请只输出 JSON 对象。"},
			)
			continue
		}

		return q, nil
	}

	return Question{}, fmt.Errorf("重试 %d 次后仍未拿到合法题目: %w", maxQuestionAttempts, lastErr)
}

// buildQuestionPrompt 拼出题的用户消息。
//
// 简历摘要和 JD 都截断：这两样加起来可能几千字，
// 出题只需要知道「这人做过什么、岗位要什么」，不需要全文。
func buildQuestionPrompt(req QuestionRequest) string {
	var b strings.Builder

	b.WriteString("岗位方向：")
	b.WriteString(fallback(req.RoleType, "未指定"))
	b.WriteString("\n\n岗位描述：\n")
	b.WriteString(truncateRunes(fallback(req.JDText, "（未提供）"), 1200))
	b.WriteString("\n\n候选人简历摘要：\n")
	b.WriteString(truncateRunes(fallback(req.ResumeSummary, "（未提供）"), 1500))

	if len(req.Asked) > 0 {
		b.WriteString("\n\n已经问过（不要重复）：\n")
		for _, q := range req.Asked {
			b.WriteString("- ")
			b.WriteString(truncateRunes(q, 120))
			b.WriteString("\n")
		}
	}

	b.WriteString(fmt.Sprintf("\n这是第 %d 题。", req.Seq))
	return b.String()
}

// decodeQuestion 解析模型输出。
func decodeQuestion(raw string) (Question, error) {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return Question{}, fmt.Errorf("%w: 找不到 JSON 对象", ErrBadQuestion)
	}

	var body struct {
		Question string `json:"question"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &body); err != nil {
		return Question{}, fmt.Errorf("%w: %v", ErrBadQuestion, err)
	}

	text := strings.TrimSpace(body.Question)
	if len([]rune(text)) < minQuestionRunes {
		return Question{}, fmt.Errorf("%w: 题目太短（%d 字）", ErrBadQuestion, len([]rune(text)))
	}

	typ := strings.TrimSpace(strings.ToLower(body.Type))
	switch typ {
	case TypeTech, TypeProject, TypeBehavior:
	default:
		return Question{}, fmt.Errorf("%w: type 只能是 tech/project/behavior，实际 %q", ErrBadQuestion, body.Type)
	}

	return Question{Text: text, Type: typ}, nil
}

// SummarizeResume 把结构化简历压成一段摘要，供出题用。
//
// 只取和面试相关的：项目名 + 要点、技能、学历。
// 姓名邮箱手机号一律不进 prompt——没必要，也少一个泄露面。
func SummarizeResume(p parse.Parsed) string {
	var b strings.Builder

	if len(p.Education) > 0 {
		b.WriteString("教育：")
		for i, e := range p.Education {
			if i > 0 {
				b.WriteString("；")
			}
			b.WriteString(strings.TrimSpace(e.School + " " + e.Major + " " + e.Degree))
		}
		b.WriteString("\n")
	}

	if len(p.Projects) > 0 {
		b.WriteString("项目：\n")
		for _, pr := range p.Projects {
			b.WriteString("- ")
			b.WriteString(pr.Name)
			if pr.Role != "" {
				b.WriteString("（" + pr.Role + "）")
			}
			for _, d := range pr.Details {
				b.WriteString("\n  · ")
				b.WriteString(d)
			}
			b.WriteString("\n")
		}
	}

	if len(p.Work) > 0 {
		b.WriteString("工作：\n")
		for _, w := range p.Work {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(w.Company + " " + w.Title + " " + w.Period))
			for _, d := range w.Details {
				b.WriteString("\n  · ")
				b.WriteString(d)
			}
			b.WriteString("\n")
		}
	}

	if len(p.Skills) > 0 {
		b.WriteString("技能：")
		b.WriteString(strings.Join(p.Skills, "、"))
	}

	return strings.TrimSpace(b.String())
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
