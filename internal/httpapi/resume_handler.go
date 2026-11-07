package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/llm"
	"interview-ace/internal/parse"
	"interview-ace/internal/resume"
	"interview-ace/internal/store"
)

// ResumeStore 是简历 handler 需要的数据访问能力。
type ResumeStore interface {
	CreateOrGet(ctx context.Context, userID int64, filename, rawText, contentHash string, parsed []byte, status string) (store.Resume, bool, error)
	ListByUser(ctx context.Context, userID int64) ([]store.Resume, error)
	ByID(ctx context.Context, id int64) (store.Resume, error)
}

// ParseCacheStore 是解析缓存的最小能力。允许为 nil：不启用缓存。
type ParseCacheStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key, model string, parsed []byte) error
}

type resumeHandler struct {
	resumes ResumeStore
	parser  parse.Completer
	cache   ParseCacheStore // 允许为 nil：不启用缓存
	calls   LLMCallStore    // 允许为 nil：不记录模型调用
	model   string          // 缓存键的一部分
	maxMB   int
	log     *slog.Logger
}

// upload 处理 POST /api/resumes。
//
// 流程：读文件 → 抽文字 → 判断是不是扫描件 → 落库。
// 扫描件明确返回 422，不落库——存一份没有文字的简历没有意义，
// 而且会让用户在列表里看到一条永远解析不了的记录。
func (h *resumeHandler) upload(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_file", "reason": "请用 file 字段上传文件"})
		return
	}

	maxBytes := int64(h.maxMB) * 1024 * 1024
	if fileHeader.Size > maxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":  "file_too_large",
			"reason": "文件超过 " + strconv.Itoa(h.maxMB) + "MB",
		})
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		h.internalError(c, "open_upload_failed", err)
		return
	}
	defer func() { _ = f.Close() }()

	// 多读 1 字节用于判断「是不是真的没超」：
	// fileHeader.Size 来自客户端声明，不能全信。
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		h.internalError(c, "read_upload_failed", err)
		return
	}
	if int64(len(data)) > maxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":  "file_too_large",
			"reason": "文件超过 " + strconv.Itoa(h.maxMB) + "MB",
		})
		return
	}

	// 按文件头自动判断是 PDF 还是 docx，不看扩展名——
	// 用户把 docx 改名成 .pdf 上传的情况真实存在
	ex, err := resume.Extract(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if errors.Is(err, resume.ErrUnsupported) {
			c.JSON(http.StatusUnsupportedMediaType, gin.H{
				"error":  "unsupported_format",
				"reason": "只支持 PDF 和 Word(.docx)。老式 .doc 请另存为 .docx 或 PDF",
			})
			return
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "invalid_document",
			"reason": "无法解析这个文件，请确认是文本型简历（不是扫描件或图片）",
		})
		return
	}

	if resume.Scanned(ex.Text, ex.Pages) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "scanned_pdf",
			"reason": "这是扫描件或图片，读不出文字。请上传由 Word 导出的 PDF，或直接传 .docx",
		})
		return
	}

	// 结构化解析：成功就存结构化结果并标记 ok，
	// 失败只记日志、保留原文并标记 degraded——
	// 结构化只是「更好用」，不该让整次上传失败。
	parsedJSON, status, usage := h.parseText(c, ex.Text)
	h.recordLLM(c, usage, status)

	// 文件名可能是 GBK（Windows 客户端按本地代码页发），
	// 正文也可能带非法字节——两者都会让 PostgreSQL 报 22021
	filename := sanitizeFilename(fileHeader.Filename)
	rawText := toValidUTF8(ex.Text)

	saved, created, err := h.resumes.CreateOrGet(
		c.Request.Context(), userID, filename, rawText, store.ContentHash(rawText), parsedJSON, status)
	if err != nil {
		h.internalError(c, "save_resume_failed", err)
		return
	}

	// 重复上传同一份内容返回 200 + duplicate=true，不新建记录。
	// 用 200 而不是 409：这不是错误，用户重传一份已有的简历是正常操作。
	code := http.StatusCreated
	if !created {
		code = http.StatusOK
	}

	c.JSON(code, gin.H{
		"id":           saved.ID,
		"filename":     saved.Filename,
		"parse_status": saved.ParseStatus,
		"pages":        ex.Pages,
		"chars":        len([]rune(ex.Text)),
		"duplicate":    !created,
		"created_at":   saved.CreatedAt,
	})
}

// parseText 调模型做结构化解析。失败返回 nil 和 degraded，不向上抛错。
//
// 先查缓存：同一份简历重复上传不该重复花钱。
func (h *resumeHandler) parseText(c *gin.Context, text string) ([]byte, string, llm.Usage) {
	if h.parser == nil {
		return nil, store.ParseDegraded, llm.Usage{}
	}

	ctx := c.Request.Context()
	key := parse.CacheKey(h.model, text)

	if h.cache != nil {
		cached, err := h.cache.Get(ctx, key)
		switch {
		case err == nil:
			h.log.Info("resume_parse_cache_hit", "request_id", c.GetString("request_id"))
			return cached, store.ParseOK, llm.Usage{}
		case !errors.Is(err, store.ErrCacheMiss):
			// 缓存坏了不该影响主流程，记一笔继续解析
			h.log.Warn("resume_parse_cache_read_failed",
				"request_id", c.GetString("request_id"), "error", err)
		}
	}

	res, err := parse.Resume(c.Request.Context(), h.parser, text)
	if err != nil {
		h.log.Warn("resume_parse_failed",
			"request_id", c.GetString("request_id"),
			"error", err,
		)
		return nil, store.ParseDegraded, res.Usage
	}

	raw, err := json.Marshal(res.Data)
	if err != nil {
		h.log.Warn("resume_parse_marshal_failed",
			"request_id", c.GetString("request_id"),
			"error", err,
		)
		return nil, store.ParseDegraded, res.Usage
	}

	if h.cache != nil {
		if err := h.cache.Put(ctx, key, h.model, raw); err != nil {
			// 写缓存失败只记日志，不影响本次结果
			h.log.Warn("resume_parse_cache_write_failed",
				"request_id", c.GetString("request_id"), "error", err)
		}
	}

	return raw, store.ParseOK, res.Usage
}

// recordLLM 把这次解析的用量落库。
// 缓存命中时 usage 为零、不记——没有真实调用，记了会虚增成本。
func (h *resumeHandler) recordLLM(c *gin.Context, usage llm.Usage, parseStatus string) {
	if h.calls == nil || usage.TotalTokens == 0 {
		return
	}

	status := store.StatusOK
	if parseStatus != store.ParseOK {
		status = store.StatusInvalidJSON
	}

	if err := h.calls.Record(c.Request.Context(), store.LLMCall{
		Purpose:          store.PurposeParse,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		Status:           status,
	}); err != nil {
		// 记账失败不该让上传失败
		h.log.Warn("record_llm_call_failed", "request_id", c.GetString("request_id"), "error", err)
	}
}

// list 处理 GET /api/resumes。
func (h *resumeHandler) list(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return
	}

	items, err := h.resumes.ListByUser(c.Request.Context(), userID)
	if err != nil {
		h.internalError(c, "list_resumes_failed", err)
		return
	}

	out := make([]gin.H, 0, len(items))
	for _, it := range items {
		out = append(out, gin.H{
			"id":           it.ID,
			"filename":     it.Filename,
			"parse_status": it.ParseStatus,
			"created_at":   it.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

// detail 处理 GET /api/resumes/:id。
func (h *resumeHandler) detail(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "reason": "编号不合法"})
		return
	}

	r, err := h.resumes.ByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrResumeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": "找不到这份简历"})
			return
		}
		h.internalError(c, "get_resume_failed", err)
		return
	}

	// 越权检查：只能看自己的简历。
	// 不做的话，改一个 id 就能读到别人的简历正文。
	if r.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": "找不到这份简历"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           r.ID,
		"filename":     r.Filename,
		"parse_status": r.ParseStatus,
		"raw_text":     r.RawText,
		"parsed":       r.Parsed,
		"created_at":   r.CreatedAt,
	})
}

func (h *resumeHandler) internalError(c *gin.Context, event string, err error) {
	h.log.Error(event,
		"request_id", c.GetString("request_id"),
		"path", c.Request.URL.Path,
		"error", err,
	)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
}
