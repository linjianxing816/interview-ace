// Package httpapi 组装 HTTP 路由与中间件。
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"interview-ace/internal/auth"
	"interview-ace/internal/config"
	"interview-ace/internal/db"
	"interview-ace/internal/parse"
	"interview-ace/web"
)

// buildVersion 会在编译时用 -ldflags 覆盖；默认值用于本地开发。
var buildVersion = "dev"

// readyTimeout 是就绪探针查库的超时。比客户端等待时间短，
// 保证探针本身不会变成新的慢点。
const readyTimeout = 3 * time.Second

// tokenTTL 是登录令牌有效期。求职工具不是天天用的产品，
// 24 小时能覆盖「投一批简历、连着面几场」的节奏，又不用做刷新令牌。
const tokenTTL = 24 * time.Hour

// Deps 是路由需要的全部依赖。用结构体传参，
// 后面加依赖（缓存、LLM 客户端）不用改签名、不用改所有调用点。
type Deps struct {
	Config   *config.Config
	Log      *slog.Logger
	Pool     *pgxpool.Pool   // 允许为 nil：单测里不连库
	Users    UserStore       // 允许为 nil：不注册认证路由
	Resumes  ResumeStore     // 允许为 nil：不注册简历路由
	Parser   parse.Completer // 允许为 nil：只存简历原文，不做结构化
	Cache    ParseCacheStore // 允许为 nil：不启用解析缓存
	LLMCalls LLMCallStore    // 允许为 nil：不记录模型调用、不提供统计接口
	// Interviews 允许为 nil：不注册面试路由
	Interviews InterviewStore
	// Scores 允许为 nil：评分明细不落库（报告仍然会存）
	Scores ScoreStore
	// Trends 允许为 nil：不提供复盘趋势接口
	Trends InterviewTrendStore
}

// NewRouter 返回装配好中间件的路由。
func NewRouter(d Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.Use(requestID(), accessLog(d.Log), recoverPanic(d.Log))

	// 前端：Vite 构建产物嵌在二进制里。
	//
	// 为什么用 NoRoute 兜底而不是 StaticFile：
	// SPA 只有 index.html 一个入口，用户刷新 /anything 时服务器必须回 index.html，
	// 让前端自己决定渲染什么；直接 404 会让刷新页面变成白屏。
	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic("前端构建产物缺失，先跑 npm run build：" + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))

	r.GET("/", gin.WrapH(fileServer))

	r.NoRoute(func(c *gin.Context) {
		// /api 下的未知路径是「接口不存在」，不能回 index.html——
		// 否则前端拿到一坨 HTML 去 JSON.parse，报错信息会非常迷惑
		if strings.HasPrefix(c.Request.URL.Path, "/api") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": "接口不存在"})
			return
		}

		// dist 里真实存在的文件（favicon.svg、robots.txt、assets/*）直接返回。
		//
		// 这里不用逐条注册路由：之前只注册了 /favicon.ico，结果新增 favicon.svg 时
		// 忘了加，请求落到兜底逻辑回了 index.html——浏览器把 HTML 当图标解析失败，
		// 表现是「图标不显示」，但控制台不报错，很难查。
		// 按文件是否存在来判断，以后往 public/ 里加任何静态文件都不用改代码。
		if p := strings.TrimPrefix(c.Request.URL.Path, "/"); p != "" {
			if f, err := distFS.Open(p); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		// 其余路径回 index.html，让前端决定渲染什么（刷新页面不会白屏）
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})

	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(d))
	r.GET("/version", version)

	// 没有 user store 就不挂认证路由：让「依赖没接好」在启动时就暴露，
	// 而不是等请求进来才发现 nil panic。
	if d.Users != nil {
		h := &authHandler{
			users:  d.Users,
			tokens: auth.NewTokenIssuer(d.Config.Server.JWTSecret, tokenTTL),
			log:    d.Log,
		}
		api := r.Group("/api")
		api.POST("/auth/register", h.register)
		api.POST("/auth/login", h.login)

		// 简历路由必须登录后才能访问
		if d.Resumes != nil {
			rh := &resumeHandler{
				resumes: d.Resumes,
				parser:  d.Parser,
				cache:   d.Cache,
				calls:   d.LLMCalls,
				model:   d.Config.LLM.Model,
				maxMB:   d.Config.Upload.MaxMB,
				log:     d.Log,
			}
			authed := api.Group("", RequireAuth(auth.NewTokenIssuer(d.Config.Server.JWTSecret, tokenTTL), d.Users))
			authed.POST("/resumes", rh.upload)
			authed.GET("/resumes", rh.list)
			authed.GET("/resumes/:id", rh.detail)

			if d.LLMCalls != nil {
				sh := &statsHandler{calls: d.LLMCalls, trends: d.Trends, log: d.Log}
				authed.GET("/stats/llm", sh.llmUsage)
				if d.Trends != nil {
					authed.GET("/stats/trend", sh.trend)
				}
			}

			if d.Interviews != nil {
				ih := &interviewHandler{
					interviews: d.Interviews,
					resumes:    d.Resumes,
					questions:  d.Parser, // 复用同一个模型客户端
					calls:      d.LLMCalls,
					scores:     d.Scores,
					log:        d.Log,
				}
				authed.POST("/interviews", ih.create)
				authed.GET("/interviews", ih.list)
				authed.GET("/interviews/:id", ih.detail)
				authed.POST("/interviews/:id/start", ih.start)
				authed.POST("/interviews/:id/answer", ih.answer)
				authed.POST("/interviews/:id/score", ih.score)
			}
		}
	}

	return r
}

// healthz 是存活探针：只要进程能回 HTTP 就返回 200。
// 它刻意不查数据库——数据库挂了不代表进程要重启，那是 readyz 的职责。
func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// readyz 是就绪探针：数据库连不上就返回 503，让负载均衡把流量摘走。
// 它和 healthz 的区别是「要不要重启」vs「要不要接流量」。
func readyz(d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), readyTimeout)
		defer cancel()

		if err := db.Health(ctx, d.Pool); err != nil {
			// 具体原因只进日志，不回给客户端——不暴露内部拓扑
			d.Log.Warn("readyz_db_unavailable",
				"request_id", c.GetString("request_id"),
				"error", err,
			)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unavailable",
				"reason": "database",
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}

func version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"version": buildVersion,
		"service": "interview-ace",
	})
}

// requestID 给每个请求分配一个 ID，写进响应头和 gin.Context，
// 这样一次请求的所有日志行都能串起来。
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				// 随机源失败不该挡住业务，退化成时间戳
				id = time.Now().Format("20060102150405.000000")
			} else {
				id = hex.EncodeToString(b)
			}
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

// accessLog 记录一行结构化访问日志。
func accessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		log.Info("http_request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		)
	}
}

// recoverPanic 兜住 handler 里的 panic，返回 500 而不是让连接直接断掉。
// 堆栈只进日志，不回给客户端。
func recoverPanic(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic_recovered",
					"request_id", c.GetString("request_id"),
					"path", c.Request.URL.Path,
					"error", rec,
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": "internal server error",
				})
			}
		}()
		c.Next()
	}
}
