// Package parse 把简历纯文本变成结构化数据。
//
// 单独成包而不是塞进 resume：resume 是纯函数（不依赖网络），
// 这里要调大模型。混在一起的话，想测文本清洗就得先造一个假模型客户端。
package parse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"interview-ace/internal/llm"
)

// 解析阶段的错误。handler 据此决定是降级还是报错。
var (
	// ErrInvalidJSON 模型没按格式输出。可以重试，但重试也可能还是错。
	ErrInvalidJSON = errors.New("模型返回的不是合法 JSON")
	// ErrEmptyResult 结构合法但内容为空，说明这份简历模型读不出东西。
	ErrEmptyResult = errors.New("解析结果为空")
)

// maxAttempts 是解析的最大尝试次数。
// 模型偶尔输出不合法 JSON，追加一句更明确的指令重试一次通常就好。
// 空结果不重试——同样的输入大概率还是空，白花钱。
const maxAttempts = 3

// retryNudge 是重试时追加的纠正指令。
const retryNudge = "上一次输出不是合法 JSON。请只输出 JSON 对象本身：不要 markdown 代码块，不要任何解释文字。"

// maxInputRunes 是喂给模型的简历正文上限。
// 一份简历正常 1-2 页，3000 字足够；超长的多半是格式乱了，截断比塞爆上下文好。
const maxInputRunes = 3000

// 教育、项目、工作各条的字段。
type Education struct {
	School string `json:"school"`
	Major  string `json:"major"`
	Degree string `json:"degree"`
	Period string `json:"period"`
}

type Project struct {
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Details []string `json:"details"`
}

type Work struct {
	Company string   `json:"company"`
	Title   string   `json:"title"`
	Period  string   `json:"period"`
	Details []string `json:"details"`
}

// Parsed 是解析出来的结构化简历。
// 字段都用 omitempty：没解析到的段落不要塞一堆空数组，读起来更清楚。
type Parsed struct {
	Name      string      `json:"name,omitempty"`
	Education []Education `json:"education,omitempty"`
	Projects  []Project   `json:"projects,omitempty"`
	Work      []Work      `json:"work,omitempty"`
	Skills    []string    `json:"skills,omitempty"`
}

// IsEmpty 表示什么都没解析出来。
func (p Parsed) IsEmpty() bool {
	return len(p.Education) == 0 && len(p.Projects) == 0 &&
		len(p.Work) == 0 && len(p.Skills) == 0 && strings.TrimSpace(p.Name) == ""
}

// Completer 是解析需要的最小模型能力。用接口是为了单测能塞固定响应，
// 不用真的花钱调 API。
type Completer interface {
	Chat(ctx context.Context, msgs []llm.Message) (llm.Reply, error)
}

// CacheKey 用「模型名 + 简历正文」算缓存键。
//
// 必须带上模型名：换模型后旧结果不能复用，否则会拿到别的模型的结构化结果，
// 而且这种错误很难发现——结果看着正常，只是来自另一个模型。
func CacheKey(model, text string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0}) // 分隔符，避免 "ab"+"c" 和 "a"+"bc" 撞键
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// Result 是一次解析的结果，带上用量方便算成本。
type Result struct {
	Data  Parsed
	Usage llm.Usage
}

const systemPrompt = `你是简历解析器。把用户给的简历正文抽成 JSON，只输出 JSON，不要任何解释和 markdown 代码块。

JSON 结构：
{
  "name": "姓名，找不到就空字符串",
  "education": [{"school":"学校","major":"专业","degree":"学历","period":"起止时间"}],
  "projects": [{"name":"项目名","role":"担任角色","details":["要点1","要点2"]}],
  "work": [{"company":"公司","title":"职位","period":"起止时间","details":["要点1"]}],
  "skills": ["技能1","技能2"]
}

规则：
1. 只抽原文里有的信息，不要编造。原文没有的段落返回空数组。
2. details 保留原文的要点描述，每条一句话，不要改写润色。
3. 时间用原文写法，不要换算格式。
4. 输出必须是能被 JSON.parse 解析的纯 JSON。`

// Resume 解析一份简历正文。
//
// 失败时返回错误，调用方应当降级（保留原文继续用），而不是把整个上传判失败——
// 结构化只是「更好用」，不是「没有就不行」。
func Resume(ctx context.Context, c Completer, text string) (Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{}, ErrEmptyResult
	}

	runes := []rune(text)
	if len(runes) > maxInputRunes {
		text = string(runes[:maxInputRunes])
	}

	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: text},
	}

	var total llm.Usage
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reply, err := c.Chat(ctx, msgs)
		if err != nil {
			// llm 客户端内部已经对 429/5xx 重试过，这里不再叠加重试
			return Result{Usage: total}, fmt.Errorf("调用模型失败: %w", err)
		}
		total = addUsage(total, reply.Usage)

		parsed, err := decodeJSON(reply.Content)
		if err != nil {
			lastErr = err
			// 把模型的错误输出也带上，让它看到自己上一条写了什么
			msgs = append(msgs,
				llm.Message{Role: "assistant", Content: reply.Content},
				llm.Message{Role: "user", Content: retryNudge},
			)
			continue
		}

		parsed = normalize(parsed)
		if parsed.IsEmpty() {
			return Result{Usage: total}, ErrEmptyResult
		}

		return Result{Data: parsed, Usage: total}, nil
	}

	return Result{Usage: total}, fmt.Errorf("重试 %d 次后仍未拿到合法 JSON: %w", maxAttempts, lastErr)
}

// addUsage 累加多次调用的用量，方便算总成本。
func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
	}
}

// decodeJSON 从模型输出里取出 JSON 并解析。
//
// 不直接用 json.Unmarshal 是因为模型经常包一层 markdown 代码块，
// 或者在前后加一句「好的，这是解析结果：」。截取第一个 { 到最后一个 } 最稳。
func decodeJSON(raw string) (Parsed, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Parsed{}, fmt.Errorf("%w: 输出为空", ErrInvalidJSON)
	}

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return Parsed{}, fmt.Errorf("%w: 找不到 JSON 对象", ErrInvalidJSON)
	}

	var out Parsed
	// 不设 DisallowUnknownFields：模型偶尔会多输出一个字段（比如自己加的 summary），
	// 为这个整体失败不值得，忽略掉就好。
	if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
		return Parsed{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}

	return out, nil
}
