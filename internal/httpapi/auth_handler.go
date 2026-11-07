package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/auth"
	"interview-ace/internal/store"
)

// UserStore 是认证 handler 需要的数据访问能力。
// 用接口而不是具体类型：单测能塞假实现，不用为了测一个状态码起数据库。
type UserStore interface {
	Create(ctx context.Context, email, passwordHash string) (store.User, error)
	ByEmail(ctx context.Context, email string) (store.User, error)
	// Exists 供认证中间件确认令牌里的用户还在。
	Exists(ctx context.Context, id int64) (bool, error)
}

type authHandler struct {
	users  UserStore
	tokens *auth.TokenIssuer
	log    *slog.Logger
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// dummyHash 用于「用户不存在」时也走一次 bcrypt 比对。
// 不这么做的话，不存在的账号会在几毫秒内返回，而存在的账号要等 bcrypt 算完——
// 攻击者靠响应时间就能枚举出哪些邮箱注册过。
var dummyHash = func() string {
	h, err := auth.HashPassword("timing-equalizer-placeholder")
	if err != nil {
		return ""
	}
	return h
}()

// register 处理 POST /api/auth/register。
func (h *authHandler) register(c *gin.Context) {
	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "reason": "请求体不是合法 JSON"})
		return
	}

	in.Email = store.NormalizeEmail(in.Email)
	if !looksLikeEmail(in.Email) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email", "reason": "邮箱格式不正确"})
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		// HashPassword 的错只可能是长度问题，可以原样给客户端
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_password", "reason": err.Error()})
		return
	}

	u, err := h.users.Create(c.Request.Context(), in.Email, hash)
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "email_taken", "reason": "该邮箱已注册"})
			return
		}
		h.internalError(c, "create_user_failed", err)
		return
	}

	token, err := h.tokens.Issue(u.ID)
	if err != nil {
		h.internalError(c, "issue_token_failed", err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user":  gin.H{"id": u.ID, "email": u.Email},
		"token": token,
	})
}

// login 处理 POST /api/auth/login。
//
// 所有失败路径返回同一个响应体，不区分「邮箱不存在」和「密码错误」——
// 区分了就等于对外提供一个「这个邮箱注册过吗」的查询接口。
func (h *authHandler) login(c *gin.Context) {
	var in credentials
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "reason": "请求体不是合法 JSON"})
		return
	}

	in.Email = store.NormalizeEmail(in.Email)

	u, err := h.users.ByEmail(c.Request.Context(), in.Email)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			// 走一次假比对，把响应时间拉到和真实账号一致
			_ = auth.CheckPassword(dummyHash, in.Password)
			h.rejectLogin(c, "邮箱或密码错误")
			return
		}
		h.internalError(c, "query_user_failed", err)
		return
	}

	if err := auth.CheckPassword(u.PasswordHash, in.Password); err != nil {
		h.rejectLogin(c, "邮箱或密码错误")
		return
	}

	token, err := h.tokens.Issue(u.ID)
	if err != nil {
		h.internalError(c, "issue_token_failed", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":  gin.H{"id": u.ID, "email": u.Email},
		"token": token,
	})
}

func (h *authHandler) rejectLogin(c *gin.Context, reason string) {
	c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials", "reason": reason})
}

// internalError 把真实错误写日志，只给客户端一个笼统的 500。
func (h *authHandler) internalError(c *gin.Context, event string, err error) {
	h.log.Error(event,
		"request_id", c.GetString("request_id"),
		"path", c.Request.URL.Path,
		"error", err,
	)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
}

// looksLikeEmail 只做最低限度校验。
// 完整 RFC 5322 正则又长又容易误杀，真正的验证靠发确认邮件。
// maxEmailLen 是邮箱的最大长度。RFC 5321 §4.5.3.1.3 规定 254 字符
// （路径上限 256 减去尖括号）。不限制的话，一个 5000 字符的「邮箱」也能注册成功，
// 直接污染用户表，日志和列表里全是噪音。
const maxEmailLen = 254

func looksLikeEmail(s string) bool {
	if len(s) > maxEmailLen {
		return false
	}

	at := strings.Index(s, "@")
	if at <= 0 || at == len(s)-1 {
		return false
	}
	// 不能有空格，且 @ 之后要有 "."
	return !strings.ContainsAny(s, " \t\r\n") && strings.Contains(s[at+1:], ".")
}
