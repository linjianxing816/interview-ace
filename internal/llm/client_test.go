package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"interview-ace/internal/config"
)

// newTestClient 起一个假的服务端，返回客户端和「收到的请求体」的抓取口。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return New(config.LLMConfig{
		APIKey:    "test-key",
		BaseURL:   srv.URL,
		Model:     "test-model",
		Timeout:   5 * time.Second,
		MaxTokens: 64,
	})
}

// fastRetry 把重试间隔调到 1ms，避免测试真等 2 秒。
func fastRetry(t *testing.T) {
	t.Helper()
	old := retryBackoff
	retryBackoff = time.Millisecond
	t.Cleanup(func() { retryBackoff = old })
}

func okBody(content string, usage string) string {
	return `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"` + content +
		`"},"finish_reason":"stop"}],"usage":` + usage + `}`
}

func TestChat_正常返回内容与用量(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody("hello", `{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}`))
	})

	reply, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}

	if reply.Content != "hello" {
		t.Errorf("Content = %q，期望 hello", reply.Content)
	}
	if reply.Usage.PromptTokens != 12 || reply.Usage.CompletionTokens != 3 || reply.Usage.TotalTokens != 15 {
		t.Errorf("用量解析错误: %+v", reply.Usage)
	}
	if reply.Model != "test-model" {
		t.Errorf("Model = %q，期望 test-model", reply.Model)
	}
}

// TestChat_thinking默认关闭 验证默认请求体里带的是 disabled。
// 这个参数很关键：开着会让 temperature 失效、CoT 计入输出 token。
func TestChat_thinking默认关闭(t *testing.T) {
	var gotThinking string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotThinking = body.Thinking.Type
		_, _ = io.WriteString(w, okBody("ok", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`))
	})

	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if gotThinking != "disabled" {
		t.Errorf("thinking.type = %q，期望 disabled", gotThinking)
	}
}

func TestChat_可开启thinking(t *testing.T) {
	var gotThinking string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotThinking = body.Thinking.Type
		_, _ = io.WriteString(w, okBody("ok", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`))
	}))
	defer srv.Close()

	c := New(config.LLMConfig{
		APIKey: "k", BaseURL: srv.URL, Model: "m",
		Timeout: 5 * time.Second, MaxTokens: 64, Thinking: true,
	})

	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if gotThinking != "enabled" {
		t.Errorf("thinking.type = %q，期望 enabled", gotThinking)
	}
}

func TestChat_请求带Bearer头且temperature为0(t *testing.T) {
	var gotAuth string
	var gotTemp float64

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body chatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTemp = body.Temperature
		_, _ = io.WriteString(w, okBody("ok", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`))
	})

	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotTemp != 0 {
		t.Errorf("temperature = %v，期望 0", gotTemp)
	}
}

// TestChat_401不重试 验证鉴权失败不会被无意义地重试三次。
func TestChat_401不重试(t *testing.T) {
	fastRetry(t)
	var calls int32

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid api key sk-leaked"}`)
	})

	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("期望报错")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("请求次数 = %d，期望 1（401 不该重试）", got)
	}

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("期望 401 错误，实际: %v", err)
	}
	// 关键：错误信息里不能出现响应体（可能回显 key）
	if strings.Contains(err.Error(), "sk-leaked") {
		t.Errorf("错误信息泄漏了响应体: %s", err.Error())
	}
}

func TestChat_429重试到上限(t *testing.T) {
	fastRetry(t)
	var calls int32

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	})

	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("期望报错")
	}
	if got := atomic.LoadInt32(&calls); got != maxAttempts {
		t.Errorf("请求次数 = %d，期望 %d", got, maxAttempts)
	}
}

func TestChat_5xx两次后成功(t *testing.T) {
	fastRetry(t)
	var calls int32

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, okBody("recovered", `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`))
	})

	reply, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("第三次应成功，实际报错: %v", err)
	}
	if reply.Content != "recovered" {
		t.Errorf("Content = %q", reply.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("请求次数 = %d，期望 3", got)
	}
}

func TestChat_响应不是JSON不重试(t *testing.T) {
	fastRetry(t)
	var calls int32

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = io.WriteString(w, "<html>gateway error</html>")
	})

	_, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("期望报错")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("请求次数 = %d，期望 1（结构异常重试没意义）", got)
	}
}

func TestChat_响应没有choices(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"m","choices":[],"usage":{}}`)
	})

	if _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("choices 为空时应报错")
	}
}

func TestChat_上下文取消时停止(t *testing.T) {
	fastRetry(t)

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立刻取消

	if _, err := c.Chat(ctx, []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("上下文已取消时应报错")
	}
}

func TestHintFor_状态码翻译(t *testing.T) {
	cases := map[int]string{
		401: "key 无效或无权限",
		402: "余额不足",
		429: "触发限流",
		404: "base_url 或 model 名称不对",
		500: "服务端错误",
	}
	for status, want := range cases {
		if got := hintFor(status); got != want {
			t.Errorf("hintFor(%d) = %q，期望 %q", status, got, want)
		}
	}
}

// TestChat_真实接口冒烟 打一次真实 API，验证「请求体形状」和真实服务端一致。
//
// 默认跳过：它花钱、依赖网络、依赖余额。只有手动设 LLM_SMOKE=1 才跑。
// 假服务端测不出「thinking 字段位置对不对」这种问题——只有真接口能。
//
//	LLM_SMOKE=1 go test ./internal/llm -run TestChat_真实接口冒烟 -v
func TestChat_真实接口冒烟(t *testing.T) {
	if os.Getenv("LLM_SMOKE") != "1" {
		t.Skip("未设置 LLM_SMOKE=1，跳过真实接口测试")
	}

	cfg, err := config.Load("../../.env")
	if err != nil {
		t.Fatalf("读取配置失败: %v", err)
	}

	// 把输出压到最小，控制成本
	cfg.LLM.MaxTokens = 8
	c := New(cfg.LLM)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reply, err := c.Chat(ctx, []Message{{Role: "user", Content: "Reply with exactly: OK"}})
	if err != nil {
		t.Fatalf("真实调用失败: %v", err)
	}

	t.Logf("model=%s content=%q latency=%dms prompt_tokens=%d completion_tokens=%d",
		reply.Model, reply.Content, reply.LatencyMS,
		reply.Usage.PromptTokens, reply.Usage.CompletionTokens)

	if reply.Content == "" {
		t.Error("回复内容为空")
	}
	if reply.Usage.TotalTokens <= 0 {
		t.Error("用量为 0，响应结构可能和预期不符")
	}
}
