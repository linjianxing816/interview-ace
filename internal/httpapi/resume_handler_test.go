package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/config"
	"interview-ace/internal/llm"
	"interview-ace/internal/store"
	"interview-ace/internal/testutil"
)

// fakeResumes 是内存版 ResumeStore，跟 fakeUsers 一个思路：
// 这些用例测的是状态码和校验顺序，不该被数据库可用性影响。
type fakeResumes struct {
	items  []store.Resume
	nextID int64
}

func (f *fakeResumes) CreateOrGet(_ context.Context, userID int64, filename, rawText, contentHash string, parsed []byte, status string) (store.Resume, bool, error) {
	// 跟真实实现一样按 (userID, contentHash) 去重
	for _, it := range f.items {
		if it.UserID == userID && it.ContentHash == contentHash {
			return it, false, nil
		}
	}
	f.nextID++
	it := store.Resume{
		ID: f.nextID, UserID: userID, Filename: filename,
		RawText: rawText, Parsed: parsed, ParseStatus: status,
		ContentHash: contentHash, CreatedAt: time.Now(),
	}
	f.items = append(f.items, it)
	return it, true, nil
}

func (f *fakeResumes) ListByUser(_ context.Context, userID int64) ([]store.Resume, error) {
	var out []store.Resume
	for _, it := range f.items {
		if it.UserID == userID {
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeResumes) ByID(_ context.Context, id int64) (store.Resume, error) {
	for _, it := range f.items {
		if it.ID == id {
			return it, nil
		}
	}
	return store.Resume{}, store.ErrResumeNotFound
}

// fakeParser 返回固定的结构化结果，用来测「解析成功」这条分支。
type fakeParser struct {
	content  string
	contents []string // 多条时按调用顺序依次返回（测出题 → 评分的连续调用）
	err      error
	calls    int
	usage    llm.Usage
}

func (f *fakeParser) Chat(_ context.Context, _ []llm.Message) (llm.Reply, error) {
	f.calls++
	if f.err != nil {
		return llm.Reply{}, f.err
	}
	content := f.content
	if len(f.contents) > 0 {
		idx := f.calls - 1
		if idx >= len(f.contents) {
			idx = len(f.contents) - 1
		}
		content = f.contents[idx]
	}
	return llm.Reply{Content: content, Usage: f.usage}, nil
}

func newResumeRouter(t *testing.T, maxMB int) *gin.Engine {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: maxMB},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Deps{
		Config:  cfg,
		Log:     log,
		Users:   newFakeUsers(),
		Resumes: &fakeResumes{},
	})
}

// uploadReq 构造一个 multipart 上传请求。
func uploadReq(t *testing.T, filename string, data []byte, token string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("构造表单失败: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("写入表单失败: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭表单失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/resumes", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// registerAndLogin 注册一个新用户并返回令牌。
func registerAndLogin(t *testing.T, r *gin.Engine, email string) string {
	t.Helper()
	w := postJSON(t, r, "/api/auth/register", `{"email":"`+email+`","password":"secret123"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("注册失败: %d %s", w.Code, w.Body.String())
	}
	return decode(t, w)["token"].(string)
}

func TestUpload_文本型PDF成功(t *testing.T) {
	r := newResumeRouter(t, 5)
	token := registerAndLogin(t, r, "up@example.com")

	pdf := testutil.BuildPDF("Zhang San, backend engineer, three years of Go experience and PostgreSQL")
	req := uploadReq(t, "cv.pdf", pdf, token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201，body=%s", w.Code, w.Body.String())
	}

	body := decode(t, w)
	if body["pages"] != float64(1) {
		t.Errorf("pages = %v，期望 1", body["pages"])
	}
	if body["parse_status"] != store.ParseDegraded {
		t.Errorf("parse_status = %v，期望 %s（结构化解析还没接）", body["parse_status"], store.ParseDegraded)
	}
	if chars, _ := body["chars"].(float64); chars < 20 {
		t.Errorf("chars = %v，抽出的文字太少", body["chars"])
	}
}

func TestUpload_未登录返回401(t *testing.T) {
	r := newResumeRouter(t, 5)

	req := uploadReq(t, "cv.pdf", testutil.BuildPDF("hello world"), "")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", w.Code)
	}
}

func TestUpload_扫描件返回422(t *testing.T) {
	r := newResumeRouter(t, 5)
	token := registerAndLogin(t, r, "s@example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "x.pdf", testutil.BuildPDF(""), token))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d，期望 422，body=%s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "scanned_pdf" {
		t.Errorf("error = %v，期望 scanned_pdf", got)
	}
}

// TestUpload_不支持的格式返回415 覆盖「传了 .doc / 图片 / 纯文本」的情况。
// 用 415 而不是 422：422 是「格式对但内容解析不了」，415 是「格式本身不支持」，
// 两者的用户处置方式不同（后者要换文件格式）。
func TestUpload_不支持的格式返回415(t *testing.T) {
	r := newResumeRouter(t, 5)
	token := registerAndLogin(t, r, "fmt@example.com")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "old.doc", []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, token))

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("状态码 = %d，期望 415，body=%s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "unsupported_format" {
		t.Errorf("error = %v，期望 unsupported_format", got)
	}
}

// TestUpload_Word文档可上传 验证 docx 走通整条链路。
func TestUpload_Word文档可上传(t *testing.T) {
	r := newResumeRouter(t, 5)
	token := registerAndLogin(t, r, "word@example.com")

	docx := testutil.BuildDOCX(
		"张三",
		"教育背景",
		"某大学 软件工程 本科",
		"项目经历",
		"用 Postman 设计接口测试用例，覆盖登录鉴权与商品查询",
	)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.docx", docx, token))

	if w.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201，body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if chars, _ := body["chars"].(float64); chars < 20 {
		t.Errorf("chars = %v，docx 抽出的文字太少", body["chars"])
	}
}

func TestUpload_超过大小上限返回413(t *testing.T) {
	// 上限设成 1MB，塞 2MB 数据
	r := newResumeRouter(t, 1)
	token := registerAndLogin(t, r, "big@example.com")

	huge := bytes.Repeat([]byte("A"), 2*1024*1024)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "big.pdf", huge, token))

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d，期望 413，body=%s", w.Code, w.Body.String())
	}
}

func TestUpload_缺文件字段返回400(t *testing.T) {
	r := newResumeRouter(t, 5)
	token := registerAndLogin(t, r, "nofile@example.com")

	req := httptest.NewRequest(http.MethodPost, "/api/resumes", bytes.NewBufferString(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xxx")
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400，body=%s", w.Code, w.Body.String())
	}
}

// TestDetail_越权访问返回404 是权限断言：
// 换个 id 不能读到别人的简历。返回 404 而不是 403，
// 是为了不透露「这个 id 存在但不属于你」。
func TestDetail_越权访问返回404(t *testing.T) {
	r := newResumeRouter(t, 5)

	owner := registerAndLogin(t, r, "owner@example.com")
	pdf := testutil.BuildPDF("owner resume content with enough characters here")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "owner.pdf", pdf, owner))
	if w.Code != http.StatusCreated {
		t.Fatalf("上传失败: %d %s", w.Code, w.Body.String())
	}
	resumeID := int(decode(t, w)["id"].(float64))

	// 换一个用户来读
	intruder := registerAndLogin(t, r, "intruder@example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/resumes/"+strconv.Itoa(resumeID), nil)
	req.Header.Set("Authorization", "Bearer "+intruder)

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)

	if w2.Code != http.StatusNotFound {
		t.Fatalf("越权访问状态码 = %d，期望 404，body=%s", w2.Code, w2.Body.String())
	}
	// 响应里不能出现简历正文
	if bytes.Contains(w2.Body.Bytes(), []byte("owner resume content")) {
		t.Error("越权响应泄漏了简历正文")
	}

	// 本人读则应该成功
	req3 := httptest.NewRequest(http.MethodGet, "/api/resumes/"+strconv.Itoa(resumeID), nil)
	req3.Header.Set("Authorization", "Bearer "+owner)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Errorf("本人读取状态码 = %d，期望 200", w3.Code)
	}
	var detail map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &detail); err != nil {
		t.Fatalf("详情响应不是合法 JSON: %v", err)
	}
	if detail["raw_text"] == "" {
		t.Error("详情应包含 raw_text")
	}
}

// TestUpload_解析成功时状态为ok 覆盖「模型正常返回」这条主路径：
// parsed 落库、状态从 degraded 变 ok。
func TestUpload_解析成功时状态为ok(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config:  cfg,
		Log:     log,
		Users:   newFakeUsers(),
		Resumes: fr,
		Parser:  &fakeParser{content: `{"name":"张三","skills":["Go"]}`},
	})

	token := registerAndLogin(t, r, "ok@example.com")
	pdf := testutil.BuildPDF("Zhang San resume with Go skills and some project details")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.pdf", pdf, token))

	if w.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201，body=%s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["parse_status"]; got != store.ParseOK {
		t.Errorf("parse_status = %v，期望 ok", got)
	}
	if len(fr.items) != 1 {
		t.Fatalf("应落库 1 条，实际 %d", len(fr.items))
	}
	if len(fr.items[0].Parsed) == 0 {
		t.Error("parsed 应为非空 JSON")
	}
}

// TestUpload_解析失败仍保存原文 是关键降级断言：
// 模型抽风不能让用户白传一次，原文必须留着，状态标 degraded。
func TestUpload_解析失败仍保存原文(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser: &fakeParser{err: errors.New("限流")},
	})

	token := registerAndLogin(t, r, "fail@example.com")
	pdf := testutil.BuildPDF("Zhang San resume with enough text to pass the scanned check")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.pdf", pdf, token))

	if w.Code != http.StatusCreated {
		t.Fatalf("解析失败不该影响上传，状态码 = %d", w.Code)
	}
	if got := decode(t, w)["parse_status"]; got != store.ParseDegraded {
		t.Errorf("parse_status = %v，期望 degraded", got)
	}
	if len(fr.items) != 1 || fr.items[0].RawText == "" {
		t.Error("原文必须保存")
	}
	if len(fr.items[0].Parsed) != 0 {
		t.Error("解析失败时 parsed 应为空")
	}
}

// fakeCache 是内存版解析缓存。
type fakeCache struct {
	data  map[string][]byte
	gets  int
	puts  int
	failG bool
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[string][]byte{}} }

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, error) {
	f.gets++
	if f.failG {
		return nil, errors.New("缓存坏了")
	}
	v, ok := f.data[key]
	if !ok {
		return nil, store.ErrCacheMiss
	}
	return v, nil
}

func (f *fakeCache) Put(_ context.Context, key, _ string, parsed []byte) error {
	f.puts++
	f.data[key] = parsed
	return nil
}

func newCachedRouter(t *testing.T, cache *fakeCache, parser *fakeParser) *gin.Engine {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "test-model"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(),
		Resumes: &fakeResumes{}, Parser: parser, Cache: cache,
	})
}

// TestUpload_缓存命中不再调模型 是省钱的核心断言：
// 同一份简历传第二次，不该再花一次 token。
func TestUpload_缓存命中不再调模型(t *testing.T) {
	cache := newFakeCache()
	parser := &fakeParser{content: `{"name":"张三","skills":["Go"]}`}
	r := newCachedRouter(t, cache, parser)

	token := registerAndLogin(t, r, "cache@example.com")
	pdf := testutil.BuildPDF("Zhang San resume with Go skills and project details here")

	// 第一次：未命中，调模型并写缓存
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, uploadReq(t, "cv.pdf", pdf, token))
	if w1.Code != http.StatusCreated {
		t.Fatalf("第一次上传失败: %d %s", w1.Code, w1.Body.String())
	}
	if cache.puts != 1 {
		t.Errorf("第一次应写缓存 1 次，实际 %d", cache.puts)
	}
	if parser.calls != 1 {
		t.Errorf("第一次应调模型 1 次，实际 %d", parser.calls)
	}

	// 第二次：内容相同 → 命中缓存 + 去重，返回 200 且 duplicate=true
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, uploadReq(t, "cv.pdf", pdf, token))
	if w2.Code != http.StatusOK {
		t.Fatalf("第二次上传应返回 200（重复），实际 %d %s", w2.Code, w2.Body.String())
	}
	if dup, _ := decode(t, w2)["duplicate"].(bool); !dup {
		t.Errorf("第二次上传 duplicate 应为 true，实际 body=%s", w2.Body.String())
	}
	if cache.puts != 1 {
		t.Errorf("命中后不该再写缓存，实际写了 %d 次", cache.puts)
	}
	// 最关键的一条：命中缓存后模型调用次数必须还是 1
	if parser.calls != 1 {
		t.Errorf("命中缓存后不该再调模型，实际调用 %d 次（说明白花了钱）", parser.calls)
	}
	if got := decode(t, w2)["parse_status"]; got != store.ParseOK {
		t.Errorf("命中缓存的状态 = %v，期望 ok", got)
	}
}

// TestUpload_缓存读取失败仍能解析 验证缓存故障不影响主流程。
func TestUpload_缓存读取失败仍能解析(t *testing.T) {
	cache := newFakeCache()
	cache.failG = true
	parser := &fakeParser{content: `{"name":"李四"}`}
	r := newCachedRouter(t, cache, parser)

	token := registerAndLogin(t, r, "cachefail@example.com")
	pdf := testutil.BuildPDF("Li Si resume with enough text to pass the scanned check")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.pdf", pdf, token))

	if w.Code != http.StatusCreated {
		t.Fatalf("缓存故障不该影响上传，状态码 = %d", w.Code)
	}
	if got := decode(t, w)["parse_status"]; got != store.ParseOK {
		t.Errorf("parse_status = %v，期望 ok", got)
	}
}

// TestUpload_重复上传不新建记录 是去重的核心断言：
// 同一份简历传两次，库里只有一条，第二次返回 duplicate=true。
func TestUpload_重复上传不新建记录(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
	})

	token := registerAndLogin(t, r, "dup@example.com")
	pdf := testutil.BuildPDF("Same resume content uploaded twice by the same user")

	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, uploadReq(t, "cv.pdf", pdf, token))
	if w1.Code != http.StatusCreated {
		t.Fatalf("首次上传应 201，实际 %d", w1.Code)
	}
	if dup, _ := decode(t, w1)["duplicate"].(bool); dup {
		t.Error("首次上传 duplicate 应为 false")
	}

	// 换个文件名再传一次，内容一样 → 仍应去重（按内容哈希，不按文件名）
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, uploadReq(t, "cv-rename.pdf", pdf, token))
	if w2.Code != http.StatusOK {
		t.Fatalf("重复上传应 200，实际 %d %s", w2.Code, w2.Body.String())
	}
	if len(fr.items) != 1 {
		t.Errorf("库里应只有 1 条，实际 %d 条", len(fr.items))
	}
	if id1, id2 := decode(t, w1)["id"], decode(t, w2)["id"]; id1 != id2 {
		t.Errorf("两次返回的 id 应相同，实际 %v vs %v", id1, id2)
	}
}

// fakeLLMCalls 记录调用，用于测统计接口和记账行为。
type fakeLLMCalls struct {
	records []store.LLMCall
}

func (f *fakeLLMCalls) Record(_ context.Context, c store.LLMCall) error {
	f.records = append(f.records, c)
	return nil
}

func (f *fakeLLMCalls) Summary(_ context.Context, since time.Time) (store.LLMUsageSummary, error) {
	sum := store.LLMUsageSummary{Since: since}
	byPurpose := map[string]*store.LLMUsageStat{}
	for _, r := range f.records {
		s, ok := byPurpose[r.Purpose]
		if !ok {
			s = &store.LLMUsageStat{Purpose: r.Purpose}
			byPurpose[r.Purpose] = s
		}
		s.Calls++
		s.PromptTokens += int64(r.PromptTokens)
		s.CompletionTokens += int64(r.CompletionTokens)
		if r.Status != store.StatusOK {
			s.Failures++
			sum.FailedCalls++
		}
		sum.TotalCalls++
		sum.TotalTokens += int64(r.PromptTokens + r.CompletionTokens)
	}
	for _, s := range byPurpose {
		sum.ByPurpose = append(sum.ByPurpose, *s)
	}
	return sum, nil
}

// TestUpload_记录模型用量 验证「成本能核算」不是空话：
// 真实调用一次就该落一条记录，缓存命中时不该重复记。
func TestUpload_记录模型用量(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "test-model"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := &fakeLLMCalls{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(),
		Resumes: &fakeResumes{},
		Parser:  &fakeParser{content: `{"name":"张三"}`, usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}},
		Cache:   newFakeCache(), LLMCalls: calls,
	})

	token := registerAndLogin(t, r, "usage@example.com")
	pdf := testutil.BuildPDF("Resume content for usage recording test")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.pdf", pdf, token))
	if w.Code != http.StatusCreated {
		t.Fatalf("上传失败: %d %s", w.Code, w.Body.String())
	}
	if len(calls.records) != 1 {
		t.Fatalf("应记录 1 次调用，实际 %d 次", len(calls.records))
	}
	if calls.records[0].Purpose != store.PurposeParse {
		t.Errorf("purpose = %q，期望 parse", calls.records[0].Purpose)
	}
	if calls.records[0].PromptTokens != 100 || calls.records[0].CompletionTokens != 20 {
		t.Errorf("用量没记对: %+v", calls.records[0])
	}

	// 第二次同样内容 → 命中缓存，不该再记（fakeParser 没有 usage，所以这里主要验证不新增）
	before := len(calls.records)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, uploadReq(t, "cv.pdf", pdf, token))
	if len(calls.records) != before {
		t.Errorf("缓存命中不该新增调用记录，实际从 %d 变成 %d", before, len(calls.records))
	}
}

func TestStats_LLM用量接口(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "test-model"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := &fakeLLMCalls{records: []store.LLMCall{
		{Purpose: store.PurposeParse, PromptTokens: 100, CompletionTokens: 20, Status: store.StatusOK},
		{Purpose: store.PurposeParse, PromptTokens: 50, CompletionTokens: 0, Status: store.StatusInvalidJSON},
	}}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(),
		Resumes: &fakeResumes{}, LLMCalls: calls,
	})

	token := registerAndLogin(t, r, "stats@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/stats/llm?days=7", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，body=%s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["total_calls"] != float64(2) {
		t.Errorf("total_calls = %v，期望 2", body["total_calls"])
	}
	if body["total_tokens"] != float64(170) {
		t.Errorf("total_tokens = %v，期望 170", body["total_tokens"])
	}
	if body["failed_calls"] != float64(1) {
		t.Errorf("failed_calls = %v，期望 1", body["failed_calls"])
	}
}

func TestStats_未登录返回401(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(),
		Resumes: &fakeResumes{}, LLMCalls: &fakeLLMCalls{},
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stats/llm", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", w.Code)
	}
}

func TestStats_非法days参数(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(),
		Resumes: &fakeResumes{}, LLMCalls: &fakeLLMCalls{},
	})
	token := registerAndLogin(t, r, "bad@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/stats/llm?days=999", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}
