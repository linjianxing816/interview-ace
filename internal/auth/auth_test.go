package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestHashPassword_正常往返(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}
	if hash == "correct-horse" {
		t.Fatal("哈希不能等于明文")
	}
	if err := CheckPassword(hash, "correct-horse"); err != nil {
		t.Errorf("正确密码应通过，实际: %v", err)
	}
	if err := CheckPassword(hash, "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("错误密码应返回 ErrInvalidCredentials，实际: %v", err)
	}
}

func TestHashPassword_同一密码两次哈希不同(t *testing.T) {
	h1, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("第一次哈希失败: %v", err)
	}
	h2, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("第二次哈希失败: %v", err)
	}
	if h1 == h2 {
		t.Fatal("bcrypt 每次应生成不同盐，两次哈希不该相同")
	}
	// 但两个哈希都能验证通过
	for i, h := range []string{h1, h2} {
		if err := CheckPassword(h, "same-password"); err != nil {
			t.Errorf("第 %d 个哈希验证失败: %v", i+1, err)
		}
	}
}

func TestHashPassword_长度边界(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"7 位太短", "1234567", true},
		{"8 位刚好", "12345678", false},
		{"72 字节刚好", strings.Repeat("a", 72), false},
		{"73 字节超限", strings.Repeat("a", 73), true},
	}
	for _, c := range cases {
		_, err := HashPassword(c.pw)
		if c.wantErr && err == nil {
			t.Errorf("%s：期望报错，实际成功", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s：期望成功，实际报错 %v", c.name, err)
		}
	}
}

func TestHashPassword_超长密码不会互相通过(t *testing.T) {
	// 73 字节会被 bcrypt 静默截断到 72，这里验证我们提前拦住了
	long := strings.Repeat("a", 72) + "X"
	if _, err := HashPassword(long); err == nil {
		t.Fatal("73 字节密码应被拒绝，否则前 72 字节相同的密码能互相登录")
	}
}

func TestIssueVerify_正常往返(t *testing.T) {
	ti := NewTokenIssuer("test-secret", time.Hour)

	token, err := ti.Issue(42)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	uid, err := ti.Verify(token)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if uid != 42 {
		t.Errorf("user_id = %d，期望 42", uid)
	}
}

func TestIssue_拒绝非法userID(t *testing.T) {
	ti := NewTokenIssuer("test-secret", time.Hour)
	for _, id := range []int64{0, -1} {
		if _, err := ti.Issue(id); err == nil {
			t.Errorf("user_id=%d 应被拒绝", id)
		}
	}
}

func TestVerify_换密钥后失效(t *testing.T) {
	token, err := NewTokenIssuer("secret-a", time.Hour).Issue(1)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	if _, err := NewTokenIssuer("secret-b", time.Hour).Verify(token); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("换密钥后应失效，实际: %v", err)
	}
}

func TestVerify_过期令牌被拒(t *testing.T) {
	// ttl 设成负数，签出来就是已过期的
	token, err := NewTokenIssuer("test-secret", -time.Minute).Issue(7)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	if _, err := NewTokenIssuer("test-secret", time.Hour).Verify(token); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("过期令牌应被拒绝，实际: %v", err)
	}
}

// TestVerify_拒绝algNone 覆盖算法混淆攻击：
// 攻击者把 header 改成 {"alg":"none"} 并去掉签名，期望服务端仍拒绝。
func TestVerify_拒绝algNone(t *testing.T) {
	ti := NewTokenIssuer("test-secret", time.Hour)

	claims := Claims{
		UserID: 999,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "999",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "interview-ace",
		},
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("构造 alg=none 令牌失败: %v", err)
	}

	if _, err := ti.Verify(unsigned); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("alg=none 令牌必须被拒绝，实际: %v", err)
	}
}

// TestVerify_拒绝非HS256算法 验证 WithValidMethods 真的在起作用：
// 用同一密钥签一个 HS512 令牌，服务端应拒绝。
// 没有 WithValidMethods 的话这个令牌会被正常接受（算法由签名方决定）。
func TestVerify_拒绝非HS256算法(t *testing.T) {
	ti := NewTokenIssuer("test-secret", time.Hour)

	claims := Claims{
		UserID: 555,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "555",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			Issuer:    "interview-ace",
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("构造 HS512 令牌失败: %v", err)
	}

	if _, err := ti.Verify(token); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("非 HS256 令牌必须被拒绝，实际: %v", err)
	}
}

func TestVerify_空串与垃圾串(t *testing.T) {
	ti := NewTokenIssuer("test-secret", time.Hour)
	for _, raw := range []string{"", "not-a-token", "a.b.c"} {
		if _, err := ti.Verify(raw); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("%q 应返回 ErrTokenInvalid，实际: %v", raw, err)
		}
	}
}
