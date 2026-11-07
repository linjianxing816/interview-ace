package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/config"
)

// newTestRouter 返回一个丢弃日志的路由，避免测试输出被刷屏。
// Pool 传 nil：这里只测不依赖数据库的行为。
func newTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	cfg := &config.Config{Server: config.ServerConfig{Port: 8080, JWTSecret: "test"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Deps{Config: cfg, Log: log, Pool: nil})
}

// TestReadyz_无连接池时返回503 验证就绪探针在数据库不可用时确实拦住流量。
// 用 nil 池代替「真把库停了」——后者没法在单测里稳定复现，而这条分支是一样的。
func TestReadyz_无连接池时返回503(t *testing.T) {
	r := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n原文: %s", err, w.Body.String())
	}
	if body["status"] != "unavailable" {
		t.Errorf("status = %v，期望 unavailable", body["status"])
	}
	// 不能把内部错误细节泄漏出去
	if _, leaked := body["error"]; leaked {
		t.Errorf("响应体不该包含 error 字段: %s", w.Body.String())
	}
}

func TestHealthz_返回200与ok(t *testing.T) {
	r := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n原文: %s", err, w.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v，期望 ok", body["status"])
	}
	if _, ok := body["time"]; !ok {
		t.Error("响应缺少 time 字段")
	}
}

func TestRequestID_自动生成(t *testing.T) {
	r := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	got := w.Header().Get("X-Request-ID")
	if got == "" {
		t.Fatal("未生成 X-Request-ID 响应头")
	}
	// 8 字节 hex = 16 个字符
	if len(got) != 16 {
		t.Errorf("X-Request-ID 长度 = %d (%q)，期望 16", len(got), got)
	}
}

func TestRequestID_沿用客户端传入(t *testing.T) {
	r := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "client-supplied-id")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("X-Request-ID"); got != "client-supplied-id" {
		t.Errorf("X-Request-ID = %q，期望沿用客户端传入的值", got)
	}
}

func TestRecoverPanic_返回500不中断进程(t *testing.T) {
	r := newTestRouter(t)
	r.GET("/boom", func(c *gin.Context) {
		panic("故意炸一下")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d，期望 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Errorf("响应体应含 internal server error，实际: %s", w.Body.String())
	}
	// 堆栈不能泄漏给客户端
	if strings.Contains(w.Body.String(), "goroutine") {
		t.Error("响应体泄漏了堆栈信息")
	}

	// panic 之后服务应仍可正常响应
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w2.Code != http.StatusOK {
		t.Errorf("panic 后健康检查状态码 = %d，期望 200", w2.Code)
	}
}

func TestVersion_返回服务名(t *testing.T) {
	r := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/version", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["service"] != "interview-ace" {
		t.Errorf("service = %v，期望 interview-ace", body["service"])
	}
}
