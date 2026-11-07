package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/auth"
)

// ctxUserID 是认证通过后写进 gin.Context 的键名。
const ctxUserID = "user_id"

// UserExister 判断令牌里的用户是否还在。允许为 nil：只验签名，不查库。
type UserExister interface {
	Exists(ctx context.Context, id int64) (bool, error)
}

// RequireAuth 校验 Authorization: Bearer <token>，通过则把 user_id 放进上下文。
//
// 验完签名还要确认用户仍在库里。为什么不能只验签名：
// 令牌签发后用户可能被删（测试清库、账号注销），这种令牌签名依然有效，
// 只验签名会让它一路放行，直到写库时撞外键约束才炸成 500——
// 用户看到的是「上传失败」而不是「登录已失效，请重新登录」。
//
// users 传 nil 时退化成只验签名（单元测试用）。
func RequireAuth(tokens *auth.TokenIssuer, users UserExister) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			abortUnauthorized(c, "缺少 Authorization 头")
			return
		}

		// 大小写不敏感地匹配 "Bearer "
		parts := strings.Fields(raw)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			abortUnauthorized(c, "Authorization 格式应为 Bearer <token>")
			return
		}

		userID, err := tokens.Verify(parts[1])
		if err != nil {
			abortUnauthorized(c, "令牌无效或已过期")
			return
		}

		if users != nil {
			ok, err := users.Exists(c.Request.Context(), userID)
			if err != nil {
				// 查不了库不是「你没登录」，是服务暂时不可用——
				// 返回 401 会让前端把人踢去重新登录，而重新登录也一样查不了库
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
					"error":  "unavailable",
					"reason": "暂时无法校验登录状态，请稍后重试",
				})
				return
			}
			if !ok {
				abortUnauthorized(c, "账号不存在或已被删除，请重新登录")
				return
			}
		}

		c.Set(ctxUserID, userID)
		c.Next()
	}
}

func abortUnauthorized(c *gin.Context, reason string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error":  "unauthorized",
		"reason": reason,
	})
}

// UserID 取出当前登录用户。调用前必须已经过 RequireAuth。
func UserID(c *gin.Context) (int64, error) {
	v, ok := c.Get(ctxUserID)
	if !ok {
		return 0, errors.New("上下文中没有 user_id：该路由缺少 RequireAuth")
	}
	id, ok := v.(int64)
	if !ok || id <= 0 {
		return 0, errors.New("上下文中的 user_id 类型或取值非法")
	}
	return id, nil
}
