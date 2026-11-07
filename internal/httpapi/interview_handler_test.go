package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/config"
	"interview-ace/internal/llm"
	"interview-ace/internal/store"
	"interview-ace/internal/testutil"
)

// fakeInterviews 是内存版 InterviewStore，行为尽量贴近真实实现：
// Transition 校验前置状态、AnswerTurn 拒绝重复提交、AddTurn 拒绝重复 seq。
type fakeInterviews struct {
	items  map[int64]*store.Interview
	turns  map[int64][]store.Turn
	nextID int64
}

func newFakeInterviews() *fakeInterviews {
	return &fakeInterviews{
		items: map[int64]*store.Interview{},
		turns: map[int64][]store.Turn{},
	}
}

func (f *fakeInterviews) Create(_ context.Context, userID, resumeID int64, jd, role string, maxTurns int) (store.Interview, error) {
	f.nextID++
	iv := &store.Interview{
		ID: f.nextID, UserID: userID, ResumeID: resumeID, JDText: jd,
		RoleType: role, Status: "created", MaxTurns: maxTurns, CreatedAt: time.Now(),
	}
	f.items[iv.ID] = iv
	return *iv, nil
}

func (f *fakeInterviews) ByID(_ context.Context, id int64) (store.Interview, error) {
	iv, ok := f.items[id]
	if !ok {
		return store.Interview{}, store.ErrInterviewNotFound
	}
	return *iv, nil
}

func (f *fakeInterviews) ListByUser(_ context.Context, userID int64) ([]store.Interview, error) {
	var out []store.Interview
	for _, iv := range f.items {
		if iv.UserID == userID {
			out = append(out, *iv)
		}
	}
	return out, nil
}

func (f *fakeInterviews) Transition(_ context.Context, id int64, from, to string, seq int) (store.Interview, error) {
	iv, ok := f.items[id]
	if !ok {
		return store.Interview{}, store.ErrInterviewNotFound
	}
	if iv.Status != from {
		return store.Interview{}, store.ErrStatusConflict
	}
	iv.Status = to
	iv.CurrentSeq = seq
	return *iv, nil
}

func (f *fakeInterviews) Finish(_ context.Context, id int64, from, to string) (store.Interview, error) {
	iv, ok := f.items[id]
	if !ok {
		return store.Interview{}, store.ErrStatusConflict
	}
	if iv.Status != from {
		return store.Interview{}, store.ErrStatusConflict
	}
	now := time.Now()
	iv.Status = to
	iv.FinishedAt = &now
	return *iv, nil
}

func (f *fakeInterviews) AddTurn(_ context.Context, id int64, seq int, parent *int64, q, qType, followupKind string) (store.Turn, error) {
	for _, t := range f.turns[id] {
		if t.Seq == seq {
			return store.Turn{}, store.ErrTurnAlreadyAnswered
		}
	}
	t := store.Turn{ID: int64(seq), InterviewID: id, Seq: seq, ParentTurnID: parent,
		Question: q, QuestionType: qType, FollowupKind: followupKind, AskedAt: time.Now()}
	f.turns[id] = append(f.turns[id], t)
	return t, nil
}

func (f *fakeInterviews) AnswerTurn(_ context.Context, id int64, seq int, answer string) (store.Turn, error) {
	for i, t := range f.turns[id] {
		if t.Seq != seq {
			continue
		}
		if t.AnsweredAt != nil {
			return store.Turn{}, store.ErrTurnAlreadyAnswered
		}
		now := time.Now()
		f.turns[id][i].Answer = answer
		f.turns[id][i].AnsweredAt = &now
		return f.turns[id][i], nil
	}
	return store.Turn{}, store.ErrTurnNotFound
}

func (f *fakeInterviews) Turns(_ context.Context, id int64) ([]store.Turn, error) {
	return f.turns[id], nil
}

func (f *fakeInterviews) SaveReport(_ context.Context, id int64, report []byte) (store.Interview, error) {
	iv, ok := f.items[id]
	if !ok {
		return store.Interview{}, store.ErrStatusConflict
	}
	if iv.Status != "scoring" {
		return store.Interview{}, store.ErrStatusConflict
	}
	iv.Status = "done"
	iv.Report = report
	return *iv, nil
}

// fakeScores 是内存版 ScoreStore，记录写入的行方便断言。
type fakeScores struct {
	rows []store.ScoreRow
}

func (f *fakeScores) ReplaceForInterview(_ context.Context, _ int64, rows []store.ScoreRow) error {
	f.rows = rows
	return nil
}

func (f *fakeScores) ByInterview(_ context.Context, _ int64) ([]store.ScoreRow, error) {
	return f.rows, nil
}

// questionJSON 是假模型返回的题目。
const questionJSON = `{"question":"你说用 Postgres 做过并发写入，怎么避免超卖的？","type":"tech"}`

func newInterviewRouter(t *testing.T) (*gin.Engine, *fakeInterviews, *fakeResumes) {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "test-model"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fi := newFakeInterviews()
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{content: questionJSON},
		Interviews: fi,
	})
	return r, fi, fr
}

func doJSON(t *testing.T, r *gin.Engine, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// setupInterview 注册用户、传简历、建面试，返回 (token, resumeID, interviewID)。
func setupInterview(t *testing.T, r *gin.Engine, fr *fakeResumes, email string) (string, int64, int64) {
	t.Helper()
	token := registerAndLogin(t, r, email)

	pdf := testutil.BuildPDF("Resume content for interview creation test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, uploadReq(t, "cv.pdf", pdf, token))
	if w.Code != http.StatusCreated {
		t.Fatalf("上传简历失败: %d %s", w.Code, w.Body.String())
	}
	resumeID := int64(decode(t, w)["id"].(float64))

	w2 := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+strconv.FormatInt(resumeID, 10)+`,"jd_text":"负责后端服务开发与性能优化","role_type":"backend"}`,
		token)
	if w2.Code != http.StatusCreated {
		t.Fatalf("创建面试失败: %d %s", w2.Code, w2.Body.String())
	}
	return token, resumeID, int64(decode(t, w2)["id"].(float64))
}

func TestInterview_Create(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "ic@example.com")

	w := doJSON(t, r, http.MethodGet, "/api/interviews/"+strconv.FormatInt(id, 10), "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("查详情失败: %d %s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["status"] != "created" {
		t.Errorf("status = %v，期望 created", body["status"])
	}
	if body["max_turns"] != float64(defaultMaxTurns) {
		t.Errorf("max_turns = %v，期望 %d", body["max_turns"], defaultMaxTurns)
	}
}

func TestInterview_Create_校验(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, resumeID, _ := setupInterview(t, r, fr, "cv2@example.com")
	rid := strconv.FormatInt(resumeID, 10)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"缺 resume_id", `{"jd_text":"负责后端服务开发与性能优化"}`, http.StatusBadRequest},
		{"jd 太短", `{"resume_id":` + rid + `,"jd_text":"短"}`, http.StatusBadRequest},
		{"简历不存在", `{"resume_id":99999,"jd_text":"负责后端服务开发与性能优化"}`, http.StatusNotFound},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doJSON(t, r, http.MethodPost, "/api/interviews", c.body, token)
			if w.Code != c.want {
				t.Errorf("状态码 = %d，期望 %d，body=%s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestInterview_Create_别人的简历返回404 是越权断言：
// 拿别人的 resume_id 建面试必须失败，且返回 404 而不是 403。
func TestInterview_Create_别人的简历返回404(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	_, ownerResumeID, _ := setupInterview(t, r, fr, "owner2@example.com")

	intruder := registerAndLogin(t, r, "intruder2@example.com")
	w := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+strconv.FormatInt(ownerResumeID, 10)+`,"jd_text":"负责后端服务开发与性能优化"}`, intruder)

	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，body=%s", w.Code, w.Body.String())
	}
}

func TestInterview_Start_生成第一题(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "st2@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/start", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("开始失败: %d %s", w.Code, w.Body.String())
	}

	body := decode(t, w)
	if body["status"] != "asking" {
		t.Errorf("status = %v，期望 asking", body["status"])
	}
	q, _ := body["question"].(map[string]any)
	if q == nil || !strings.Contains(q["text"].(string), "Postgres") {
		t.Errorf("第一题没返回: %s", w.Body.String())
	}
}

// TestInterview_Start_重复开始返回409 覆盖 CAS：
// 已经 asking 的会话再点开始必须拒绝，否则会重复出题。
func TestInterview_Start_重复开始返回409(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "st3@example.com")
	path := "/api/interviews/" + strconv.FormatInt(id, 10) + "/start"

	if w := doJSON(t, r, http.MethodPost, path, "", token); w.Code != http.StatusOK {
		t.Fatalf("首次开始失败: %d", w.Code)
	}
	if w := doJSON(t, r, http.MethodPost, path, "", token); w.Code != http.StatusConflict {
		t.Errorf("重复开始状态码 = %d，期望 409", w.Code)
	}
}

func TestInterview_Answer_出下一题(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	fr := &fakeResumes{}
	fi := newFakeInterviews()
	r := NewRouter(Deps{
		Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users: newFakeUsers(), Resumes: fr, Interviews: fi,
		Parser: &fakeParser{contents: []string{parseJSON, questionJSON, stepJSON}},
	})
	token, _, id := setupInterview(t, r, fr, "ans2@example.com")
	base := "/api/interviews/" + strconv.FormatInt(id, 10)

	doJSON(t, r, http.MethodPost, base+"/start", "", token)

	w := doJSON(t, r, http.MethodPost, base+"/answer", `{"answer":"我用唯一索引加事务保证不超卖"}`, token)
	if w.Code != http.StatusOK {
		t.Fatalf("答题失败: %d %s", w.Code, w.Body.String())
	}

	body := decode(t, w)
	if body["finished"] != false {
		t.Errorf("finished = %v，期望 false", body["finished"])
	}
	if body["current_seq"] != float64(2) {
		t.Errorf("current_seq = %v，期望 2", body["current_seq"])
	}
}

// TestInterview_Answer_到轮次上限自动结束 是防失控断言：
// 轮次用完后必须进 scoring，不能一直出题。
func TestInterview_Answer_到轮次上限自动结束(t *testing.T) {
	r, fi, fr := newInterviewRouter(t)
	token, resumeID, _ := setupInterview(t, r, fr, "limit@example.com")

	// 建一个只有 1 轮的面试
	w := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+strconv.FormatInt(resumeID, 10)+`,"jd_text":"负责后端服务开发与性能优化","max_turns":1}`, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	id := int64(decode(t, w)["id"].(float64))
	base := "/api/interviews/" + strconv.FormatInt(id, 10)

	doJSON(t, r, http.MethodPost, base+"/start", "", token)

	w2 := doJSON(t, r, http.MethodPost, base+"/answer", `{"answer":"我的回答内容"}`, token)
	if w2.Code != http.StatusOK {
		t.Fatalf("答题失败: %d %s", w2.Code, w2.Body.String())
	}

	body := decode(t, w2)
	if body["finished"] != true {
		t.Errorf("finished = %v，期望 true", body["finished"])
	}
	if body["status"] != "scoring" {
		t.Errorf("status = %v，期望 scoring", body["status"])
	}
	if fi.items[id].FinishedAt == nil {
		t.Error("FinishedAt 应被填充")
	}
}

func TestInterview_Answer_状态不对返回409(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "bad@example.com")
	base := "/api/interviews/" + strconv.FormatInt(id, 10)

	// 还没 start 就答题
	w := doJSON(t, r, http.MethodPost, base+"/answer", `{"answer":"还没开始就答题"}`, token)
	if w.Code != http.StatusConflict {
		t.Errorf("状态码 = %d，期望 409，body=%s", w.Code, w.Body.String())
	}
}

func TestInterview_Answer_太短被拒(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "short@example.com")
	base := "/api/interviews/" + strconv.FormatInt(id, 10)
	doJSON(t, r, http.MethodPost, base+"/start", "", token)

	w := doJSON(t, r, http.MethodPost, base+"/answer", `{"answer":""}`, token)
	if w.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d，期望 400", w.Code)
	}
}

// TestInterview_Detail_越权返回404 验证换个 id 读不到别人的面试。
func TestInterview_Detail_越权返回404(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	_, _, ownerID := setupInterview(t, r, fr, "own3@example.com")

	intruder := registerAndLogin(t, r, "intr3@example.com")
	w := doJSON(t, r, http.MethodGet, "/api/interviews/"+strconv.FormatInt(ownerID, 10), "", intruder)

	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404，body=%s", w.Code, w.Body.String())
	}
}

// TestInterview_Start_出题失败回滚状态 是关键降级断言：
// 出题失败后会话不能卡在 asking（有状态没题目），必须退回 created 让用户重试。
func TestInterview_Start_出题失败回滚状态(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fi := newFakeInterviews()
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{err: errors.New("模型挂了")},
		Interviews: fi,
	})

	token, _, id := setupInterview(t, r, fr, "rollback@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/start", "", token)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", w.Code)
	}
	if got := fi.items[id].Status; got != "created" {
		t.Errorf("出题失败后状态 = %q，期望回滚到 created", got)
	}
	if got := fi.items[id].CurrentSeq; got != 0 {
		t.Errorf("current_seq = %d，期望回滚到 0", got)
	}
}

// TestInterview_出题用量被记录 验证出题的 token 也进账。
// 之前只记了简历解析的调用，出题两次调用完全没算钱——统计会少一大块。
func TestInterview_出题用量被记录(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := &fakeLLMCalls{}
	fi := newFakeInterviews()
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{content: questionJSON, usage: llm.Usage{PromptTokens: 800, CompletionTokens: 120, TotalTokens: 920}},
		Interviews: fi, LLMCalls: calls,
	})

	token, _, id := setupInterview(t, r, fr, "usage2@example.com")
	doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/start", "", token)

	var questionCalls []store.LLMCall
	for _, c := range calls.records {
		if c.Purpose == store.PurposeQuestion {
			questionCalls = append(questionCalls, c)
		}
	}
	if len(questionCalls) != 1 {
		t.Fatalf("应记录 1 次出题调用，实际 %d 次（全部记录: %+v）", len(questionCalls), calls.records)
	}
	if questionCalls[0].PromptTokens != 800 || questionCalls[0].CompletionTokens != 120 {
		t.Errorf("用量没记对: %+v", questionCalls[0])
	}
	if questionCalls[0].InterviewID == nil || *questionCalls[0].InterviewID != id {
		t.Errorf("应带上 interview_id=%d，实际 %v", id, questionCalls[0].InterviewID)
	}
}

// reachScoring 走到 scoring 状态：建 1 轮的面试 → start → answer。
func reachScoring(t *testing.T, r *gin.Engine, fr *fakeResumes, email string) (string, int64) {
	t.Helper()
	token, resumeID, _ := setupInterview(t, r, fr, email)

	w := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+strconv.FormatInt(resumeID, 10)+`,"jd_text":"负责后端服务开发与性能优化","max_turns":1}`, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	id := int64(decode(t, w)["id"].(float64))
	base := "/api/interviews/" + strconv.FormatInt(id, 10)

	doJSON(t, r, http.MethodPost, base+"/start", "", token)
	w2 := doJSON(t, r, http.MethodPost, base+"/answer", `{"answer":"唯一索引加事务"}`, token)
	if w2.Code != http.StatusOK || decode(t, w2)["status"] != "scoring" {
		t.Fatalf("应进入 scoring，实际 %d %s", w2.Code, w2.Body.String())
	}
	return token, id
}

// stepJSON 是答完一题后假模型返回的「下一步」：换话题。
const stepJSON = `{"action":"next","type":"project","question":"你简历里的评测平台，判定层是怎么设计的？"}`

// followupStepJSON 是追问。
const followupStepJSON = `{"action":"followup","followup_kind":"verify","question":"你说加了兜底，怎么确认它真的生效了？"}`

// parseJSON 是上传简历时假模型返回的结构化结果。
const parseJSON = `{"name":"张三","skills":["Go"]}`

const scoreJSON = `{
  "dimensions": [
    {"dimension":"accuracy","score":4,"comment":"正确"},
    {"dimension":"clarity","score":4,"comment":"清楚"},
    {"dimension":"depth","score":5,"comment":"有深度"}
  ],
  "turn_scores": [
    {"seq":1,"scores":[
      {"dimension":"accuracy","score":4,"comment":"正确"},
      {"dimension":"clarity","score":4,"comment":"清楚"},
      {"dimension":"depth","score":5,"comment":"有深度"}
    ]}
  ],
  "strengths": ["讲清了约束下沉"],
  "weaknesses": ["没提隔离级别"],
  "suggestions": ["补充 MVCC"]
}`

func TestInterview_Score_成功出报告(t *testing.T) {
	// 调用顺序：上传简历时解析 → start 出题 → score 评分
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fi := newFakeInterviews()
	fs := &fakeScores{}
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{contents: []string{parseJSON, questionJSON, scoreJSON}},
		Interviews: fi, Scores: fs,
	})

	token, id := reachScoring(t, r, fr, "sc@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/score", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("评分失败: %d %s", w.Code, w.Body.String())
	}

	body := decode(t, w)
	if body["status"] != "done" {
		t.Errorf("status = %v，期望 done", body["status"])
	}
	report, _ := body["report"].(map[string]any)
	if report == nil {
		t.Fatalf("响应里没有 report: %s", w.Body.String())
	}
	// (4+4+5)/3 = 4.33 → 86.67 → 87
	if report["total"] != float64(87) {
		t.Errorf("total = %v，期望 87", report["total"])
	}

	// 明细：3 条总评 + 1 轮 × 3 维度 = 6 行
	if len(fs.rows) != 6 {
		t.Errorf("落库评分行 = %d，期望 6", len(fs.rows))
	}
	overall := 0
	for _, row := range fs.rows {
		if row.TurnID == nil {
			overall++
		}
	}
	if overall != 3 {
		t.Errorf("总评行 = %d，期望 3（turn_id 为 NULL）", overall)
	}
}

// TestInterview_Score_状态不对返回409 覆盖「没答完就想评分」。
func TestInterview_Score_状态不对返回409(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users: newFakeUsers(), Resumes: fr,
		Parser: &fakeParser{contents: []string{parseJSON, questionJSON}}, Interviews: newFakeInterviews(),
	})
	token, _, id := setupInterview(t, r, fr, "sc2@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/score", "", token)
	if w.Code != http.StatusConflict {
		t.Errorf("状态码 = %d，期望 409，body=%s", w.Code, w.Body.String())
	}
}

// TestInterview_Score_评分失败停在scoring 是关键降级断言：
// 评分挂了会话不能变 done，必须留在 scoring 让用户重试。
func TestInterview_Score_评分失败停在scoring(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fi := newFakeInterviews()
	fr := &fakeResumes{}
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{contents: []string{parseJSON, questionJSON, "始终不是 JSON"}},
		Interviews: fi, Scores: &fakeScores{},
	})

	token, id := reachScoring(t, r, fr, "sc3@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/score", "", token)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502，body=%s", w.Code, w.Body.String())
	}
	if got := fi.items[id].Status; got != "scoring" {
		t.Errorf("评分失败后状态 = %q，期望停在 scoring", got)
	}
}

// TestInterview_Score_采样次数可指定 验证 ?samples=2 真的跑两次。
// 采样次数不生效的话，「评分一致性」这个能力就是假的。
func TestInterview_Score_采样次数可指定(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		LLM:    config.LLMConfig{Model: "m"},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fi := newFakeInterviews()
	fr := &fakeResumes{}
	// 解析 → 出题 → 评分（第 1 次）→ 评分（第 2 次）
	r := NewRouter(Deps{
		Config: cfg, Log: log, Users: newFakeUsers(), Resumes: fr,
		Parser:     &fakeParser{contents: []string{parseJSON, questionJSON, scoreJSON, scoreJSON}},
		Interviews: fi, Scores: &fakeScores{},
	})

	token, id := reachScoring(t, r, fr, "samples@example.com")

	w := doJSON(t, r, http.MethodPost, "/api/interviews/"+strconv.FormatInt(id, 10)+"/score?samples=2", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("评分失败: %d %s", w.Code, w.Body.String())
	}

	report, _ := decode(t, w)["report"].(map[string]any)
	if report == nil {
		t.Fatalf("没有 report: %s", w.Body.String())
	}
	if report["sample_count"] != float64(2) {
		t.Errorf("sample_count = %v，期望 2（?samples=2 没生效）", report["sample_count"])
	}
}

// TestInterview_Create_JD超长被拒 是数据污染 + 成本防线。
// 实测过：10 万字的 JD 能建面试并落库；出题时虽然截断到 3200 字，
// 但库里那 10 万字全是垃圾，用户多半是误粘贴了一整篇文章。
func TestInterview_Create_JD超长被拒(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, resumeID, _ := setupInterview(t, r, fr, "jdlong@example.com")

	huge := strings.Repeat("岗", maxJDRunes+1)
	w := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+strconv.FormatInt(resumeID, 10)+`,"jd_text":"`+huge+`","role_type":"test"}`, token)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400，body=%s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "jd_too_long" {
		t.Errorf("error = %v，期望 jd_too_long", got)
	}
}

// TestInterview_Create_JD长度边界 验证 5000 通过、5001 拒绝。
func TestInterview_Create_JD长度边界(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, resumeID, _ := setupInterview(t, r, fr, "jdedge@example.com")
	rid := strconv.FormatInt(resumeID, 10)

	ok := strings.Repeat("岗", maxJDRunes)
	w := doJSON(t, r, http.MethodPost, "/api/interviews",
		`{"resume_id":`+rid+`,"jd_text":"`+ok+`","role_type":"test"}`, token)
	if w.Code != http.StatusCreated {
		t.Fatalf("5000 字应通过，实际 %d，body=%s", w.Code, w.Body.String())
	}
}

// TestInterview_Create_roleType白名单 是 XSS 的第一道防线。
//
// role_type 会原样回显到前端（面试列表、报告页）。
// 不校验的话，传一个带 HTML 标签的值就能在前端执行脚本——
// 前端不转义是第二道防线，但后端不该把脏数据放进来。
func TestInterview_Create_roleType白名单(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, resumeID, _ := setupInterview(t, r, fr, "role@example.com")
	rid := strconv.FormatInt(resumeID, 10)

	bad := []string{"hacker", "BACKEND", "<img src=x onerror=alert(1)>", "backend "}
	for _, role := range bad {
		t.Run(role, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"resume_id": resumeID,
				"jd_text":   "负责后端服务开发与性能优化",
				"role_type": role,
			})
			w := doJSON(t, r, http.MethodPost, "/api/interviews", string(body), token)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("role_type=%q 应被拒绝，实际 %d，body=%s", role, w.Code, w.Body.String())
			}
			if got := decode(t, w)["error"]; got != "invalid_role_type" {
				t.Errorf("error = %v，期望 invalid_role_type", got)
			}
		})
	}

	// 三个合法值都应该过
	for _, role := range []string{"test", "backend", "ai"} {
		t.Run("合法-"+role, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"resume_id": resumeID,
				"jd_text":   "负责后端服务开发与性能优化",
				"role_type": role,
			})
			w := doJSON(t, r, http.MethodPost, "/api/interviews", string(body), token)
			if w.Code != http.StatusCreated {
				t.Errorf("role_type=%q 应通过，实际 %d，body=%s", role, w.Code, w.Body.String())
			}
		})
	}
	_ = rid
}

// TestInterview_Answer_超长答案被拒 与 JD 同理：实测 10 万字答案会被接受。
func TestInterview_Answer_超长答案被拒(t *testing.T) {
	r, _, fr := newInterviewRouter(t)
	token, _, id := setupInterview(t, r, fr, "anslong@example.com")
	base := "/api/interviews/" + strconv.FormatInt(id, 10)

	doJSON(t, r, http.MethodPost, base+"/start", "", token)

	huge := strings.Repeat("答", maxAnswerRunes+1)
	body, _ := json.Marshal(map[string]string{"answer": huge})
	w := doJSON(t, r, http.MethodPost, base+"/answer", string(body), token)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400，body=%s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["error"]; got != "answer_too_long" {
		t.Errorf("error = %v，期望 answer_too_long", got)
	}
}

// fakeTrend 是趋势接口的最小假实现，只用来让路由注册上。
type fakeTrend struct{}

func (f *fakeTrend) Trend(_ context.Context, _ int64, _ int) ([]store.TrendPoint, error) {
	return nil, nil
}

// TestTrend_limit超范围报错 与 days 的行为保持一致。
// 之前 limit=999 会静默压成 10，调用方以为拿到了 999 条，实际只有 10 条。
func TestTrend_limit超范围报错(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080, JWTSecret: testSecret},
		Upload: config.UploadConfig{Dir: t.TempDir(), MaxMB: 5},
	}
	r := NewRouter(Deps{
		Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Users: newFakeUsers(), Resumes: &fakeResumes{},
		LLMCalls: &fakeLLMCalls{}, Trends: &fakeTrend{},
	})
	token := registerAndLogin(t, r, "limit@example.com")

	for _, q := range []string{"limit=0", "limit=-5", "limit=999", "limit=abc"} {
		t.Run(q, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/stats/trend?"+q, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s 应返回 400，实际 %d，body=%s", q, w.Code, w.Body.String())
			}
		})
	}

	// 合法值应该通
	req := httptest.NewRequest(http.MethodGet, "/api/stats/trend?limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("limit=5 应通过，实际 %d，body=%s", w.Code, w.Body.String())
	}
}
