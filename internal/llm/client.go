// Package llm 是 OpenAI 兼容接口的最小客户端。
//
// 只做这个项目需要的事：发消息、拿回复、记用量、按状态码决定要不要重试。
// 不引官方 SDK——它带一堆用不上的类型，而且出问题时不好判断到底发了什么。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"interview-ace/internal/config"
)

// 重试策略。只对「等一会儿可能就好」的错误重试：
// 429（限流）和 5xx（服务端抖动）。4xx 的其他错误重试多少次都是一样的结果。
const (
	maxAttempts  = 3
	maxRespBytes = 1 << 20 // 1MB，防止异常响应把内存吃光
)

// retryBackoff 是第一次重试前的等待时间，之后每次翻倍。
// 用 var 而不是 const：测试里要把它调成 1ms，否则每个重试用例都要真等 2 秒多。
var retryBackoff = 800 * time.Millisecond

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage 是这次调用的 token 用量，用于成本核算。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Reply 是一次成功调用的结果。
type Reply struct {
	Content   string
	Usage     Usage
	Model     string
	LatencyMS int64
}

// Error 是调用失败。Status 为 0 表示网络层错误（连不上、超时）。
type Error struct {
	Status    int
	Retryable bool
	Reason    string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("调用大模型失败: %s", e.Reason)
	}
	return fmt.Sprintf("调用大模型失败 (HTTP %d): %s", e.Status, e.Reason)
}

// Client 是聊天补全客户端。并发安全，可以全局复用一个。
type Client struct {
	cfg  config.LLMConfig
	http *http.Client
}

// New 建客户端。cfg 里的 Timeout 会被用作单次请求的整体超时。
func New(cfg config.LLMConfig) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.Timeout,
			// 不跟随重定向：base_url 配错时应该报错，而不是被跳到别的地址
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// chatRequest 是发给服务端的请求体。
// thinking 放顶层——这是该接口的约定，不是 OpenAI 官方字段。
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Thinking    struct {
		Type string `json:"type"`
	} `json:"thinking"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// Chat 发一轮对话。temperature 固定 0——这个项目的场景都要可复现。
func (c *Client) Chat(ctx context.Context, msgs []Message) (Reply, error) {
	body := chatRequest{
		Model:       c.cfg.Model,
		Messages:    msgs,
		Temperature: 0,
		MaxTokens:   c.cfg.MaxTokens,
	}
	body.Thinking.Type = "disabled"
	if c.cfg.Thinking {
		body.Thinking.Type = "enabled"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Reply{}, fmt.Errorf("序列化请求失败: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reply, err := c.once(ctx, payload)
		if err == nil {
			return reply, nil
		}
		lastErr = err

		var apiErr *Error
		if !errors.As(err, &apiErr) || !apiErr.Retryable || attempt == maxAttempts {
			return Reply{}, err
		}

		// 指数退避：0.8s / 1.6s。用乘法而不是 2 的幂，方便调。
		wait := time.Duration(float64(retryBackoff) * math.Pow(2, float64(attempt-1)))
		select {
		case <-ctx.Done():
			return Reply{}, fmt.Errorf("等待重试时上下文取消: %w", ctx.Err())
		case <-time.After(wait):
		}
	}

	return Reply{}, lastErr
}

func (c *Client) once(ctx context.Context, payload []byte) (Reply, error) {
	url := c.cfg.BaseURL + "/chat/completions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Reply{}, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	started := time.Now()
	resp, err := c.http.Do(req)
	latency := time.Since(started).Milliseconds()

	if err != nil {
		// 网络层错误：连不上、DNS、超时。这类通常值得重试。
		return Reply{}, &Error{Status: 0, Retryable: true, Reason: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if readErr != nil {
		return Reply{}, &Error{Status: resp.StatusCode, Retryable: true, Reason: "读取响应失败: " + readErr.Error()}
	}

	if resp.StatusCode != http.StatusOK {
		// 不把响应体放进错误信息：401/403 的 body 有时会回显 key 片段。
		return Reply{}, &Error{
			Status:    resp.StatusCode,
			Retryable: resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500,
			Reason:    hintFor(resp.StatusCode),
		}
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		// 结构异常不重试：要么厂商改协议，要么网关塞了 HTML，重试还是同一个结果
		return Reply{}, &Error{Status: resp.StatusCode, Retryable: false, Reason: "响应不是合法 JSON"}
	}
	if len(out.Choices) == 0 {
		return Reply{}, &Error{Status: resp.StatusCode, Retryable: false, Reason: "响应里没有 choices"}
	}

	return Reply{
		Content:   out.Choices[0].Message.Content,
		Usage:     out.Usage,
		Model:     out.Model,
		LatencyMS: latency,
	}, nil
}

// hintFor 把状态码翻译成排查方向。不带响应体，避免泄漏。
func hintFor(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "key 无效或无权限"
	case http.StatusPaymentRequired:
		return "余额不足"
	case http.StatusTooManyRequests:
		return "触发限流"
	case http.StatusNotFound:
		return "base_url 或 model 名称不对"
	default:
		if status >= 500 {
			return "服务端错误"
		}
		return "请求被拒绝"
	}
}
