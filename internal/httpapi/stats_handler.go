package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/store"
)

// LLMCallStore 是模型调用记录的最小能力。
type LLMCallStore interface {
	Record(ctx context.Context, c store.LLMCall) error
	Summary(ctx context.Context, since time.Time) (store.LLMUsageSummary, error)
}

// defaultStatsDays 是统计的默认时间窗口。
const defaultStatsDays = 30

type statsHandler struct {
	calls  LLMCallStore
	trends InterviewTrendStore
	log    *slog.Logger
}

// llmUsage 处理 GET /api/stats/llm?days=30。
//
// 只给 token 数，不换算成钱——单价会变，而且不同渠道价不同。
// 要算成本自己乘一下，别让接口里藏一个会过期的价格表。
func (h *statsHandler) llmUsage(c *gin.Context) {
	days := defaultStatsDays
	if raw := c.Query("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 365 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_days", "reason": "days 取值 1-365"})
			return
		}
		days = n
	}

	since := time.Now().AddDate(0, 0, -days)
	summary, err := h.calls.Summary(c.Request.Context(), since)
	if err != nil {
		h.log.Error("llm_usage_summary_failed",
			"request_id", c.GetString("request_id"), "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
		return
	}

	byPurpose := make([]gin.H, 0, len(summary.ByPurpose))
	for _, s := range summary.ByPurpose {
		byPurpose = append(byPurpose, gin.H{
			"purpose":           s.Purpose,
			"calls":             s.Calls,
			"prompt_tokens":     s.PromptTokens,
			"completion_tokens": s.CompletionTokens,
			"failures":          s.Failures,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"since":        summary.Since,
		"days":         days,
		"total_calls":  summary.TotalCalls,
		"total_tokens": summary.TotalTokens,
		"failed_calls": summary.FailedCalls,
		"by_purpose":   byPurpose,
	})
}

// InterviewTrendStore 是复盘趋势需要的能力。
type InterviewTrendStore interface {
	Trend(ctx context.Context, userID int64, limit int) ([]store.TrendPoint, error)
}

// trend 处理 GET /api/stats/trend?limit=10。
//
// 只返回已完成面试的成绩，按时间倒序。前端自己决定画折线还是列表。
func (h *statsHandler) trend(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.log.Error("missing_user_id", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
		return
	}

	// limit 的校验跟 days 保持一致：超范围报错，不静默压成默认值。
	// 静默纠正会让调用方以为「传 999 就拿到 999 条」，实际只拿到 10 条。
	limit := 10
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > 50 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_limit", "reason": "limit 取值 1-50"})
			return
		}
		limit = n
	}

	points, err := h.trends.Trend(c.Request.Context(), userID, limit)
	if err != nil {
		h.log.Error("trend_failed", "request_id", c.GetString("request_id"), "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
		return
	}

	// 按时间正序返回，前端画折线不用再翻
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}

	c.JSON(http.StatusOK, gin.H{"items": points, "total": len(points)})
}
