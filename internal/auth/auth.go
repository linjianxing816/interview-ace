// Package auth 提供密码哈希与 JWT 签发/校验。
//
// 刻意不含 HTTP handler：把「怎么算」和「怎么接」分开，
// 核心逻辑可以脱离网络和数据库单测。
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials 是所有「登录失败」的统一出口。
// 不区分「用户不存在」和「密码错误」——区分了就等于给攻击者一个用户枚举接口。
var ErrInvalidCredentials = errors.New("凭据无效")

// ErrTokenInvalid 表示令牌不合法（签名错、过期、格式错、算法不符）。
var ErrTokenInvalid = errors.New("令牌无效")

const (
	// bcrypt 只取前 72 字节，超出部分被静默丢弃。
	// 不显式拦住的话，两个前 72 字节相同的长密码会互相能登进对方账号。
	maxPasswordBytes = 72

	// 下限是产品决策：8 位是最低可接受强度，再低不如不设密码。
	minPasswordBytes = 8

	// bcrypt cost。10 是默认值，单次约 50-80ms——
	// 够挡住离线爆破，又不至于让登录接口成为 DoS 入口。
	bcryptCost = 10
)

// HashPassword 生成 bcrypt 哈希。
// 返回的错误可以原样给客户端（都是长度问题，不含敏感信息）。
func HashPassword(plain string) (string, error) {
	if len(plain) < minPasswordBytes {
		return "", fmt.Errorf("密码至少 %d 位", minPasswordBytes)
	}
	if len(plain) > maxPasswordBytes {
		return "", fmt.Errorf("密码不能超过 %d 字节", maxPasswordBytes)
	}

	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(h), nil
}

// CheckPassword 比对明文与哈希。
// 一律返回 ErrInvalidCredentials，不把 bcrypt 的原始错误往上抛。
func CheckPassword(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// Claims 是令牌里携带的信息。
// 只放 user_id：邮箱会变、角色后面才加，放进去就得重新签发。
type Claims struct {
	UserID int64 `json:"uid"`
	jwt.RegisteredClaims
}

// TokenIssuer 负责签发和校验令牌。
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer 建一个签发器。ttl 建议 24h（求职工具，用户不会天天登）。
func NewTokenIssuer(secret string, ttl time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}
}

// Issue 给指定用户签一个 HS256 令牌。
func (t *TokenIssuer) Issue(userID int64) (string, error) {
	if userID <= 0 {
		return "", fmt.Errorf("user_id 非法: %d", userID)
	}

	now := time.Now()
	claims := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
			Issuer:    "interview-ace",
		},
	}

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("签发令牌失败: %w", err)
	}
	return token, nil
}

// Verify 校验令牌并取出 user_id。
//
// WithValidMethods 限定只接受 HS256。这是纵深防御：
// jwt/v5 本身会挡住 alg=none（密钥类型不匹配），
// 但不限定算法的话，用同一密钥签的 HS512 令牌会被接受——
// 算法由攻击者选的场景，限定算法集是唯一能收紧的地方。
func (t *TokenIssuer) Verify(raw string) (int64, error) {
	if raw == "" {
		return 0, ErrTokenInvalid
	}

	var claims Claims
	_, err := jwt.ParseWithClaims(raw, &claims,
		func(token *jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer("interview-ace"),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return 0, ErrTokenInvalid
	}

	if claims.UserID <= 0 {
		return 0, ErrTokenInvalid
	}

	return claims.UserID, nil
}
