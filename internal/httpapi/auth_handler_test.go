package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/auth"
	"interview-ace/internal/config"
	"interview-ace/internal/store"
)

// fakeUsers 是内存版 UserStore。
// 用它而不是真数据库：这些用例测的是状态码和响应体，不该被数据库可用性影响。
type fakeUsers struct {
	byEmail   map[string]store.User
	nextID    int64
	existsErr error // 非 nil 时模拟「查库失败」
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{byEmail: make(map[string]store.User)}
}

func (f *fakeUsers) Create(_ context.Context, email, hash string) (store.User, error) {
	if _, ok := f.byEmail[email]; ok {
		return store.User{}, store.ErrEmailTaken
	}
	f.nextID++
	u := store.User{ID: f.nextID, Email: email, PasswordHash: hash, CreatedAt: time.Now()}
	f.byEmail[email] = u
	return u, nil
}

// Exists 判断 id 是否还有对应账号。
// 删用户用 Delete 模拟「令牌还在但账号没了」——真实故障就是这么发生的。
func (f *fakeUsers) Exists(_ context.Context, id int64) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	for _, u := range f.byEmail {
		if u.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// Delete 从假库里移除用户，模拟账号被删。
func (f *fakeUsers) Delete(email string) {
	delete(f.byEmail, email)
}

func (f *fakeUsers) ByEmail(_ context.Context, email string) (store.User, error) {
	u, ok := f.byEmail[email]
	if !ok {
		return store.User{}, store.ErrUserNotFound
	}
	return u, nil
}

const testSecret = "test-secret"

func newAuthRouter(t *testing.T) *gin.Engine {
	t.Helper()
	cfg := &config.Config{Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Deps{Config: cfg, Log: log, Pool: nil, Users: newFakeUsers()})
}

func postJSON(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是合法 JSON: %v\n原文: %s", err, w.Body.String())
	}
	return m
}

func TestRegister_成功返回201与可用令牌(t *testing.T) {
	r := newAuthRouter(t)

	w := postJSON(t, r, "/api/auth/register", `{"email":"A@Example.com","password":"secret123"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201，body=%s", w.Code, w.Body.String())
	}

	body := decode(t, w)
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("响应缺少 token: %s", w.Body.String())
	}

	// 令牌必须真的能验，且 user_id 对得上
	uid, err := auth.NewTokenIssuer(testSecret, time.Hour).Verify(token)
	if err != nil {
		t.Fatalf("签发的令牌无法验证: %v", err)
	}
	user, _ := body["user"].(map[string]any)
	if int64(user["id"].(float64)) != uid {
		t.Errorf("响应 user.id = %v，令牌里是 %d", user["id"], uid)
	}

	// 邮箱应已归一化为小写
	if user["email"] != "a@example.com" {
		t.Errorf("email = %v，期望归一化为 a@example.com", user["email"])
	}
}

func TestRegister_重复邮箱返回409(t *testing.T) {
	r := newAuthRouter(t)
	const body = `{"email":"dup@example.com","password":"secret123"}`

	if w := postJSON(t, r, "/api/auth/register", body); w.Code != http.StatusCreated {
		t.Fatalf("首次注册应 201，实际 %d", w.Code)
	}

	w := postJSON(t, r, "/api/auth/register", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("重复注册状态码 = %d，期望 409", w.Code)
	}
	if decode(t, w)["error"] != "email_taken" {
		t.Errorf("error 字段 = %v，期望 email_taken", decode(t, w)["error"])
	}
}

func TestRegister_弱密码与坏邮箱返回400(t *testing.T) {
	cases := []struct{ name, body, wantErr string }{
		{"密码太短", `{"email":"a@b.com","password":"123"}`, "invalid_password"},
		{"邮箱无 @", `{"email":"not-an-email","password":"secret123"}`, "invalid_email"},
		{"邮箱无域名点", `{"email":"a@b","password":"secret123"}`, "invalid_email"},
		{"非法 JSON", `{oops`, "invalid_body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newAuthRouter(t)
			w := postJSON(t, r, "/api/auth/register", c.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d，期望 400", w.Code)
			}
			if got := decode(t, w)["error"]; got != c.wantErr {
				t.Errorf("error = %v，期望 %s", got, c.wantErr)
			}
		})
	}
}

func TestLogin_成功与失败(t *testing.T) {
	r := newAuthRouter(t)
	if w := postJSON(t, r, "/api/auth/register", `{"email":"u@example.com","password":"secret123"}`); w.Code != http.StatusCreated {
		t.Fatalf("准备数据失败: %d", w.Code)
	}

	t.Run("正确密码", func(t *testing.T) {
		w := postJSON(t, r, "/api/auth/login", `{"email":"u@example.com","password":"secret123"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，期望 200", w.Code)
		}
		if decode(t, w)["token"] == nil {
			t.Error("响应缺少 token")
		}
	})

	t.Run("密码错误返回401", func(t *testing.T) {
		w := postJSON(t, r, "/api/auth/login", `{"email":"u@example.com","password":"wrong-pass"}`)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("状态码 = %d，期望 401", w.Code)
		}
	})
}

// TestLogin_不泄漏账号是否存在 是安全断言：
// 「密码错」和「邮箱没注册」必须返回完全一样的响应体。
// 一旦不一样，攻击者就能拿登录接口当「这个邮箱注册过吗」的查询器。
func TestLogin_不泄漏账号是否存在(t *testing.T) {
	r := newAuthRouter(t)
	if w := postJSON(t, r, "/api/auth/register", `{"email":"exists@example.com","password":"secret123"}`); w.Code != http.StatusCreated {
		t.Fatalf("准备数据失败: %d", w.Code)
	}

	wrongPwd := postJSON(t, r, "/api/auth/login", `{"email":"exists@example.com","password":"nope-nope-nope"}`)
	noUser := postJSON(t, r, "/api/auth/login", `{"email":"ghost@example.com","password":"nope-nope-nope"}`)

	if wrongPwd.Code != noUser.Code {
		t.Errorf("状态码不同：密码错=%d，账号不存在=%d", wrongPwd.Code, noUser.Code)
	}
	if wrongPwd.Body.String() != noUser.Body.String() {
		t.Errorf("响应体不同，可被用来枚举账号：\n密码错: %s\n不存在: %s", wrongPwd.Body.String(), noUser.Body.String())
	}
}

func TestRequireAuth_拦截与放行(t *testing.T) {
	ti := auth.NewTokenIssuer(testSecret, time.Hour)
	valid, err := ti.Issue(99)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}

	r := gin.New()
	r.GET("/protected", RequireAuth(ti, nil), func(c *gin.Context) {
		uid, err := UserID(c)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"uid": uid})
	})

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"无头", "", http.StatusUnauthorized},
		{"格式错", "Token abc", http.StatusUnauthorized},
		{"垃圾令牌", "Bearer not-a-jwt", http.StatusUnauthorized},
		{"其他密钥签发", "Bearer " + mustIssue(t, "other-secret"), http.StatusUnauthorized},
		{"有效令牌", "Bearer " + valid, http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.want {
				t.Fatalf("状态码 = %d，期望 %d，body=%s", w.Code, c.want, w.Body.String())
			}
			if c.want == http.StatusOK {
				if got := decode(t, w)["uid"]; got != float64(99) {
					t.Errorf("uid = %v，期望 99", got)
				}
			}
		})
	}
}

func mustIssue(t *testing.T, secret string) string {
	t.Helper()
	tok, err := auth.NewTokenIssuer(secret, time.Hour).Issue(1)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	return tok
}

// TestRequireAuth_用户已删除时拒绝 复现真实故障：
// 令牌签发后用户被删（测试清库、账号注销），只验签名会让它一路放行，
// 直到写库撞外键约束才变成 500——用户看到「上传失败」而不是「请重新登录」。
func TestRequireAuth_用户已删除时拒绝(t *testing.T) {
	ti := auth.NewTokenIssuer(testSecret, time.Hour)
	users := newFakeUsers()

	u, err := users.Create(context.Background(), "gone@example.com", "hash")
	if err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	token, err := ti.Issue(u.ID)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}

	r := gin.New()
	r.GET("/protected", RequireAuth(ti, users), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// 删之前能过
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("删用户前应放行，实际 %d", w.Code)
	}

	// 删之后必须 401，而且响应体要提示重新登录
	users.Delete("gone@example.com")

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("用户已删除应返回 401，实际 %d，body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "重新登录") {
		t.Errorf("响应体应提示重新登录，实际: %s", w2.Body.String())
	}
}

// TestRequireAuth_查库失败返回503 是错误分类断言：
// 查不了库不是「你没登录」。返回 401 会把用户踢去重新登录，而重新登录同样查不了库，
// 用户会陷入死循环。
func TestRequireAuth_查库失败返回503(t *testing.T) {
	ti := auth.NewTokenIssuer(testSecret, time.Hour)
	token, _ := ti.Issue(1)

	users := newFakeUsers()
	users.existsErr = errors.New("数据库连不上")

	r := gin.New()
	r.GET("/protected", RequireAuth(ti, users), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("查库失败应返回 503，实际 %d，body=%s", w.Code, w.Body.String())
	}
}

// TestRegister_超长邮箱被拒 是数据污染防线。
// 实测过：5012 字符的「邮箱」能注册成功，直接进用户表。
// RFC 5321 规定邮箱最长 254 字符，超过的一定不是真邮箱。
func TestRegister_超长邮箱被拒(t *testing.T) {
	r := newAuthRouter(t)

	long := strings.Repeat("a", 300) + "@example.com" // 312 字符
	body, _ := json.Marshal(map[string]string{"email": long, "password": "secret123"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400，body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_email") {
		t.Errorf("应返回 invalid_email: %s", w.Body.String())
	}
}

// TestRegister_邮箱长度边界 验证 254 通过、255 拒绝。
func TestRegister_邮箱长度边界(t *testing.T) {
	// 构造正好 254 字符的邮箱：本地部分 242 + "@ex.com"（7）= 249，再补到 254
	local := strings.Repeat("a", 254-len("@example.com"))
	ok254 := local + "@example.com"

	if len(ok254) != 254 {
		t.Fatalf("构造错误，长度 = %d", len(ok254))
	}

	r := newAuthRouter(t)
	body, _ := json.Marshal(map[string]string{"email": ok254, "password": "secret123"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("254 字符应通过，实际 %d，body=%s", w.Code, w.Body.String())
	}
}
