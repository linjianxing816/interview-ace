package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"interview-ace/internal/interview"
	"interview-ace/internal/llm"
	"interview-ace/internal/parse"
	"interview-ace/internal/store"
)

// 默认轮次上限。12 轮大概 25-40 分钟，接近真实技术面长度。
const defaultMaxTurns = 12

// maxTurnsLimit 是轮次上限的硬边界，防止有人传 1000 把成本打爆。
const maxTurnsLimit = 30

// maxJDRunes 是岗位描述的长度上限。
//
// 不限制的后果实测过：10 万字的 JD 能成功建面试并落库。
// 出题时虽然会截断到 3200 字，但库里存的 10 万字全是垃圾，
// 而且用户多半是误粘贴了一整篇文章。
const maxJDRunes = 5000

// maxAnswerRunes 是单次回答的长度上限。
//
// 同样实测过：10 万字的回答会被接受。评分时截断到 800 字，
// 但落库、前端渲染、详情接口返回都要为这 10 万字买单。
const maxAnswerRunes = 5000

// validRoleTypes 是允许的岗位方向。空值走默认 backend。
//
// 为什么要白名单：role_type 会原样回显到前端（面试列表、报告页）。
// 不校验的话，一个带 HTML 标签的 role_type 就能在前端执行脚本。
// 后端校验是第一道防线，前端转义是第二道——两道都要有。
var validRoleTypes = map[string]bool{
	"test":    true,
	"backend": true,
	"ai":      true,
}

// InterviewStore 是面试 handler 需要的数据访问能力。
type InterviewStore interface {
	Create(ctx context.Context, userID, resumeID int64, jdText, roleType string, maxTurns int) (store.Interview, error)
	ByID(ctx context.Context, id int64) (store.Interview, error)
	ListByUser(ctx context.Context, userID int64) ([]store.Interview, error)
	Transition(ctx context.Context, id int64, from, to string, currentSeq int) (store.Interview, error)
	Finish(ctx context.Context, id int64, from, to string) (store.Interview, error)
	AddTurn(ctx context.Context, interviewID int64, seq int, parentID *int64, question, qType, followupKind string) (store.Turn, error)
	AnswerTurn(ctx context.Context, interviewID int64, seq int, answer string) (store.Turn, error)
	Turns(ctx context.Context, interviewID int64) ([]store.Turn, error)
	SaveReport(ctx context.Context, id int64, report []byte) (store.Interview, error)
}

// ScoreStore 是评分落库的能力。
type ScoreStore interface {
	ReplaceForInterview(ctx context.Context, interviewID int64, rows []store.ScoreRow) error
	ByInterview(ctx context.Context, interviewID int64) ([]store.ScoreRow, error)
}

type interviewHandler struct {
	interviews InterviewStore
	resumes    ResumeStore
	questions  interview.Completer
	calls      LLMCallStore // 允许为 nil：不记录出题用量
	scores     ScoreStore   // 允许为 nil：不落库评分明细
	log        *slog.Logger
}

// usageRecorder 包一层模型客户端，累计一次请求内的 token 用量。
//
// 出题可能重试 2-3 次，只记最后一次会漏账。用完 take() 取走，
// 保证一次请求只记一条汇总记录。
type usageRecorder struct {
	inner interview.Completer
	usage llm.Usage
	calls int
}

func (u *usageRecorder) Chat(ctx context.Context, msgs []llm.Message) (llm.Reply, error) {
	reply, err := u.inner.Chat(ctx, msgs)
	u.calls++
	u.usage.PromptTokens += reply.Usage.PromptTokens
	u.usage.CompletionTokens += reply.Usage.CompletionTokens
	u.usage.TotalTokens += reply.Usage.TotalTokens
	return reply, err
}

func (u *usageRecorder) take() (llm.Usage, int) {
	usage, calls := u.usage, u.calls
	u.usage, u.calls = llm.Usage{}, 0
	return usage, calls
}

type createInterviewRequest struct {
	ResumeID int64  `json:"resume_id"`
	JDText   string `json:"jd_text"`
	RoleType string `json:"role_type"`
	MaxTurns int    `json:"max_turns"`
}

// create 处理 POST /api/interviews。
func (h *interviewHandler) create(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return
	}

	var in createInterviewRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "reason": "请求格式不正确，请刷新页面重试"})
		return
	}

	if in.ResumeID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_resume_id", "reason": "请先选择一份简历"})
		return
	}
	if len([]rune(in.JDText)) < 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jd_too_short", "reason": "岗位描述至少 10 个字"})
		return
	}
	if len([]rune(in.JDText)) > maxJDRunes {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "jd_too_long",
			"reason": "岗位描述最多 5000 字，请只粘贴目标岗位的核心要求",
		})
		return
	}

	// 简历必须存在且属于当前用户
	resume, err := h.resumes.ByID(c.Request.Context(), in.ResumeID)
	if err != nil {
		if errors.Is(err, store.ErrResumeNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "resume_not_found", "reason": "找不到这份简历，请重新上传"})
			return
		}
		h.internalError(c, "load_resume_failed", err)
		return
	}
	if resume.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "resume_not_found", "reason": "找不到这份简历，请重新上传"})
		return
	}

	maxTurns := in.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	if maxTurns > maxTurnsLimit {
		maxTurns = maxTurnsLimit
	}

	roleType := in.RoleType
	if roleType == "" {
		roleType = "backend"
	}
	if !validRoleTypes[roleType] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_role_type",
			"reason": "岗位方向只能是 test / backend / ai",
		})
		return
	}

	iv, err := h.interviews.Create(c.Request.Context(), userID, resume.ID, in.JDText, roleType, maxTurns)
	if err != nil {
		h.internalError(c, "create_interview_failed", err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":         iv.ID,
		"status":     iv.Status,
		"max_turns":  iv.MaxTurns,
		"role_type":  iv.RoleType,
		"created_at": iv.CreatedAt,
	})
}

// list 处理 GET /api/interviews。
func (h *interviewHandler) list(c *gin.Context) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return
	}

	items, err := h.interviews.ListByUser(c.Request.Context(), userID)
	if err != nil {
		h.internalError(c, "list_interviews_failed", err)
		return
	}

	out := make([]gin.H, 0, len(items))
	for _, it := range items {
		out = append(out, gin.H{
			"id":          it.ID,
			"status":      it.Status,
			"role_type":   it.RoleType,
			"current_seq": it.CurrentSeq,
			"max_turns":   it.MaxTurns,
			"created_at":  it.CreatedAt,
			"finished_at": it.FinishedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{"items": out, "total": len(out)})
}

// detail 处理 GET /api/interviews/:id，返回会话 + 全部轮次（断点恢复靠它）。
func (h *interviewHandler) detail(c *gin.Context) {
	iv, ok := h.loadOwned(c)
	if !ok {
		return
	}

	turns, err := h.interviews.Turns(c.Request.Context(), iv.ID)
	if err != nil {
		h.internalError(c, "load_turns_failed", err)
		return
	}

	turnList := make([]gin.H, 0, len(turns))
	for _, t := range turns {
		turnList = append(turnList, gin.H{
			"seq":           t.Seq,
			"question":      t.Question,
			"question_type": t.QuestionType,
			"answer":        t.Answer,
			"answered":      t.Answered(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          iv.ID,
		"status":      iv.Status,
		"role_type":   iv.RoleType,
		"jd_text":     iv.JDText,
		"current_seq": iv.CurrentSeq,
		"max_turns":   iv.MaxTurns,
		"report":      iv.Report,
		"turns":       turnList,
	})
}

// start 处理 POST /api/interviews/:id/start：created → asking，并生成第一题。
func (h *interviewHandler) start(c *gin.Context) {
	iv, ok := h.loadOwned(c)
	if !ok {
		return
	}

	if !interview.Can(interview.Status(iv.Status), interview.EventStart) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "invalid_state",
			"reason": "只有 created 状态的面试可以开始，当前是 " + iv.Status,
		})
		return
	}

	// CAS：两个请求同时点开始，只有一个能拿到 asking
	updated, err := h.interviews.Transition(c.Request.Context(), iv.ID, iv.Status, string(interview.StatusAsking), 1)
	if err != nil {
		if errors.Is(err, store.ErrStatusConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "invalid_state", "reason": "会话状态已被其他请求改变"})
			return
		}
		h.internalError(c, "transition_failed", err)
		return
	}

	q, err := h.generateQuestion(c, updated, nil, 1)
	if err != nil {
		// 出题失败：把状态退回 created，让用户可以重试。
		// 不退回的话会话卡在 asking 却没有题目，用户没法继续。
		if _, rbErr := h.interviews.Transition(c.Request.Context(), updated.ID,
			string(interview.StatusAsking), string(interview.StatusCreated), 0); rbErr != nil {
			h.log.Error("rollback_to_created_failed", "interview_id", updated.ID, "error", rbErr)
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "question_generation_failed", "reason": "出题失败，请重试"})
		return
	}

	turn, err := h.interviews.AddTurn(c.Request.Context(), updated.ID, 1, nil, q.Text, q.Type, q.Kind)
	if err != nil {
		h.internalError(c, "add_turn_failed", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          updated.ID,
		"status":      updated.Status,
		"current_seq": updated.CurrentSeq,
		"question": gin.H{
			"seq":  turn.Seq,
			"text": turn.Question,
			"type": turn.QuestionType,
		},
	})
}

type answerRequest struct {
	Answer string `json:"answer"`
}

// minAnswerRunes 是回答的最短长度。太短的答案直接拒，
// 不然模型只能对着「不知道」追问，白白烧 token。
const minAnswerRunes = 2

// answer 处理 POST /api/interviews/:id/answer。
//
// 流程：asking → answering（CAS）→ 存答案 → 决定出下一题还是进评分。
// 出题失败时停在 answering，用户可以重试，答案不会丢。
func (h *interviewHandler) answer(c *gin.Context) {
	iv, ok := h.loadOwned(c)
	if !ok {
		return
	}

	var in answerRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "reason": "请求格式不正确，请刷新页面重试"})
		return
	}
	if len([]rune(in.Answer)) < minAnswerRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "answer_too_short", "reason": "回答太短了，至少写一句完整的话"})
		return
	}
	if len([]rune(in.Answer)) > maxAnswerRunes {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "answer_too_long",
			"reason": "单次回答最多 5000 字",
		})
		return
	}

	if !interview.Can(interview.Status(iv.Status), interview.EventSubmitAnswer) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "invalid_state",
			"reason": "当前状态不接受回答：" + iv.Status,
		})
		return
	}

	seq := iv.CurrentSeq

	updated, err := h.interviews.Transition(c.Request.Context(), iv.ID,
		iv.Status, string(interview.StatusAnswering), seq)
	if err != nil {
		if errors.Is(err, store.ErrStatusConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "invalid_state", "reason": "会话状态已被其他请求改变"})
			return
		}
		h.internalError(c, "transition_failed", err)
		return
	}

	// 存答案。重复提交会拿到 ErrTurnAlreadyAnswered → 409，不会覆盖第一次
	_, err = h.interviews.AnswerTurn(c.Request.Context(), updated.ID, seq, in.Answer)
	if err != nil {
		if errors.Is(err, store.ErrTurnAlreadyAnswered) {
			c.JSON(http.StatusConflict, gin.H{"error": "already_answered", "reason": "这一轮已经回答过了"})
			return
		}
		h.internalError(c, "save_answer_failed", err)
		return
	}

	turns, err := h.interviews.Turns(c.Request.Context(), updated.ID)
	if err != nil {
		h.internalError(c, "load_turns_failed", err)
		return
	}

	// 轮次上限是防失控的第一道硬边界
	if interview.NextAction(seq, updated.MaxTurns) == interview.ActionScore {
		finished, err := h.interviews.Finish(c.Request.Context(), updated.ID,
			string(interview.StatusAnswering), string(interview.StatusScoring))
		if err != nil {
			h.internalError(c, "finish_failed", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":   finished.Status,
			"finished": true,
			"reason":   "已达到本次面试的题目上限，进入评分",
		})
		return
	}

	q, parentID, err := h.nextStep(c, updated, turns, seq, in.Answer)
	if err != nil {
		// 答案已经存好了，只是下一题没出来。停在 answering 让用户重试。
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  "question_generation_failed",
			"reason": "答案已保存，下一题生成失败，请重试",
		})
		return
	}

	turn, err := h.interviews.AddTurn(c.Request.Context(), updated.ID, seq+1, parentID, q.Text, q.Type, q.Kind)
	if err != nil {
		h.internalError(c, "add_turn_failed", err)
		return
	}

	back, err := h.interviews.Transition(c.Request.Context(), updated.ID,
		string(interview.StatusAnswering), string(interview.StatusAsking), seq+1)
	if err != nil {
		h.internalError(c, "transition_failed", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      back.Status,
		"finished":    false,
		"current_seq": back.CurrentSeq,
		"question": gin.H{
			"seq":  turn.Seq,
			"text": turn.Question,
			"type": turn.QuestionType,
		},
	})
}

// score 处理 POST /api/interviews/:id/score：评分并出报告。
//
// 单独一个接口而不是答题后自动触发：评分要调一次大模型（几秒），
// 塞进 answer 里会让最后一题的回答请求变得很慢，而且失败后不好重试。
func (h *interviewHandler) score(c *gin.Context) {
	iv, ok := h.loadOwned(c)
	if !ok {
		return
	}

	// 只能从 scoring 打分：asking/answering 还没答完，created 还没开始
	if iv.Status != string(interview.StatusScoring) {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "invalid_state",
			"reason": "只有 scoring 状态可以评分，当前是 " + iv.Status,
		})
		return
	}

	turns, err := h.interviews.Turns(c.Request.Context(), iv.ID)
	if err != nil {
		h.internalError(c, "load_turns_failed", err)
		return
	}

	pairs := make([]interview.QAPair, 0, len(turns))
	for _, t := range turns {
		if !t.Answered() {
			continue // 没答的轮次不参与评分
		}
		pairs = append(pairs, interview.QAPair{Seq: t.Seq, Question: t.Question, Answer: t.Answer, Kind: t.FollowupKind})
	}
	if len(pairs) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no_answers", "reason": "没有已作答的轮次，无法评分"})
		return
	}

	// 采样次数可由客户端指定（默认 3）：多采样能看出评分稳不稳，代价是 token 翻倍
	samples := interview.DefaultSamples
	if raw := c.Query("samples"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			samples = n
		}
	}

	rec := &usageRecorder{inner: h.questions}
	report, err := interview.ScoreInterview(c.Request.Context(), rec, interview.ScoreRequest{
		RoleType: iv.RoleType,
		JDText:   iv.JDText,
		Turns:    pairs,
		Samples:  samples,
	})
	h.recordScoreUsage(c, iv.ID, rec)

	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  "scoring_failed",
			"reason": "评分失败，请重试",
		})
		return
	}

	if report.SampleFailed > 0 {
		h.log.Warn("score_partial_failure",
			"request_id", c.GetString("request_id"),
			"interview_id", iv.ID,
			"succeeded", report.SampleCount,
			"failed", report.SampleFailed,
		)
	}

	// 先存明细再改状态：万一明细写失败，会话还停在 scoring，可以重试
	if h.scores != nil {
		if err := h.scores.ReplaceForInterview(c.Request.Context(), iv.ID, h.toScoreRows(report, turns)); err != nil {
			h.internalError(c, "save_scores_failed", err)
			return
		}
	}

	raw, err := json.Marshal(report)
	if err != nil {
		h.internalError(c, "marshal_report_failed", err)
		return
	}

	done, err := h.interviews.SaveReport(c.Request.Context(), iv.ID, raw)
	if err != nil {
		h.internalError(c, "save_report_failed", err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": done.Status,
		"report": report,
	})
}

// toScoreRows 把报告转成 scores 表的行。seq → turn_id 的映射在这里做。
func (h *interviewHandler) toScoreRows(report interview.Report, turns []store.Turn) []store.ScoreRow {
	turnIDBySeq := make(map[int]int64, len(turns))
	for _, t := range turns {
		turnIDBySeq[t.Seq] = t.ID
	}

	rows := make([]store.ScoreRow, 0, len(report.Dimensions)+len(report.TurnScores)*len(interview.Dimensions))

	// 总评：turn_id 为 NULL
	for _, d := range report.Dimensions {
		rows = append(rows, store.ScoreRow{Dimension: d.Dimension, Score: d.Score, Comment: d.Comment})
	}

	for _, ts := range report.TurnScores {
		id, ok := turnIDBySeq[ts.Seq]
		if !ok {
			continue // 对不上的轮次直接跳过，不让它把整批写入搞挂
		}
		for _, d := range ts.Scores {
			turnID := id
			rows = append(rows, store.ScoreRow{
				TurnID: &turnID, Dimension: d.Dimension, Score: d.Score, Comment: d.Comment,
			})
		}
	}

	return rows
}

// recordScoreUsage 记录评分这次调用的用量。
func (h *interviewHandler) recordScoreUsage(c *gin.Context, interviewID int64, rec *usageRecorder) {
	usage, calls := rec.take()
	if h.calls == nil || calls == 0 {
		return
	}
	status := store.StatusOK
	if usage.TotalTokens == 0 {
		status = store.StatusError
	}
	id := interviewID
	if err := h.calls.Record(c.Request.Context(), store.LLMCall{
		InterviewID: &id, Purpose: store.PurposeScore,
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		Status: status,
	}); err != nil {
		h.log.Warn("record_llm_call_failed", "request_id", c.GetString("request_id"), "error", err)
	}
}

// nextStep 决定答完这一题是追问还是换话题。
//
// 返回的 parentID 在追问时指向被追问的那一轮，普通新题返回 nil——
// 这个父子关系是「这题为什么被追问」的唯一记录。
func (h *interviewHandler) nextStep(c *gin.Context, iv store.Interview, turns []store.Turn, lastSeq int, lastAnswer string) (interview.Question, *int64, error) {
	summary := h.resumeSummary(c, iv.ResumeID)

	asked := make([]string, 0, len(turns))
	history := make([]interview.QAPair, 0, len(turns))
	var lastTurn store.Turn
	for _, t := range turns {
		asked = append(asked, t.Question)
		if t.Answered() {
			history = append(history, interview.QAPair{Seq: t.Seq, Question: t.Question, Answer: t.Answer})
		}
		if t.Seq == lastSeq {
			lastTurn = t
		}
	}

	depth := followupDepth(turns, lastSeq)

	rec := &usageRecorder{inner: h.questions}
	q, err := interview.NextStep(c.Request.Context(), rec, interview.StepRequest{
		RoleType:      iv.RoleType,
		JDText:        iv.JDText,
		ResumeSummary: summary,
		Asked:         asked,
		History:       history,
		LastQuestion:  lastTurn.Question,
		LastAnswer:    lastAnswer,
		FollowupDepth: depth,
		Seq:           lastSeq + 1,
	})
	h.recordQuestionUsage(c, iv.ID, rec)

	if err != nil {
		return interview.Question{}, nil, err
	}

	if q.Type != interview.TypeFollowup {
		return q, nil, nil
	}

	// 追问挂到被追问的那一轮下面
	id := lastTurn.ID
	return q, &id, nil
}

// followupDepth 算当前话题已经追问了几层。
//
// 沿着 parent_turn_id 往上走，数连续 followup 的层数。
// 不走到底就是 0（说明当前话题是普通题，没追问过）。
func followupDepth(turns []store.Turn, seq int) int {
	bySeq := make(map[int]store.Turn, len(turns))
	byID := make(map[int64]store.Turn, len(turns))
	for _, t := range turns {
		bySeq[t.Seq] = t
		byID[t.ID] = t
	}

	cur, ok := bySeq[seq]
	if !ok {
		return 0
	}

	depth := 0
	// 上限 10 层只是防环，正常不会走到
	for i := 0; i < 10; i++ {
		if cur.QuestionType != interview.TypeFollowup {
			break
		}
		depth++
		if cur.ParentTurnID == nil {
			break
		}
		parent, ok := byID[*cur.ParentTurnID]
		if !ok {
			break
		}
		cur = parent
	}
	return depth
}

// resumeSummary 取简历摘要，供出题/追问用。取不到就退回原文。
func (h *interviewHandler) resumeSummary(c *gin.Context, resumeID int64) string {
	resume, err := h.resumes.ByID(c.Request.Context(), resumeID)
	if err != nil {
		return ""
	}
	if len(resume.Parsed) == 0 {
		return resume.RawText
	}

	var parsed parse.Parsed
	if err := json.Unmarshal(resume.Parsed, &parsed); err != nil {
		h.log.Warn("resume_parsed_decode_failed", "resume_id", resumeID, "error", err)
		return resume.RawText
	}

	if s := interview.SummarizeResume(parsed); s != "" {
		return s
	}
	return resume.RawText
}

// generateQuestion 组装出题请求。
func (h *interviewHandler) generateQuestion(c *gin.Context, iv store.Interview, turns []store.Turn, seq int) (interview.Question, error) {
	summary := ""
	if resume, err := h.resumes.ByID(c.Request.Context(), iv.ResumeID); err == nil {
		var parsed parse.Parsed
		if len(resume.Parsed) > 0 {
			if err := json.Unmarshal(resume.Parsed, &parsed); err != nil {
				h.log.Warn("resume_parsed_decode_failed", "resume_id", resume.ID, "error", err)
			}
		}
		summary = interview.SummarizeResume(parsed)
		if summary == "" {
			// 没结构化结果就退回原文前 1500 字，别让出题无米下锅
			summary = resume.RawText
		}
	}

	asked := make([]string, 0, len(turns))
	for _, t := range turns {
		asked = append(asked, t.Question)
	}

	// 包一层 recorder 累计 token：出题内部可能重试多次，都要算钱
	rec := &usageRecorder{inner: h.questions}
	q, err := interview.Generate(c.Request.Context(), rec, interview.QuestionRequest{
		RoleType:      iv.RoleType,
		JDText:        iv.JDText,
		ResumeSummary: summary,
		Asked:         asked,
		Seq:           seq,
	})

	h.recordQuestionUsage(c, iv.ID, rec)

	return q, err
}

// recordQuestionUsage 把出题的 token 用量落库。失败只记日志，不影响出题结果。
func (h *interviewHandler) recordQuestionUsage(c *gin.Context, interviewID int64, rec *usageRecorder) {
	usage, calls := rec.take()
	if h.calls == nil || calls == 0 {
		return
	}

	status := store.StatusOK
	if usage.TotalTokens == 0 {
		status = store.StatusError
	}

	id := interviewID
	if err := h.calls.Record(c.Request.Context(), store.LLMCall{
		InterviewID:      &id,
		Purpose:          store.PurposeQuestion,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		Status:           status,
	}); err != nil {
		h.log.Warn("record_llm_call_failed", "request_id", c.GetString("request_id"), "error", err)
	}
}

// loadOwned 加载会话并校验归属。返回 false 表示已经写过响应了。
func (h *interviewHandler) loadOwned(c *gin.Context) (store.Interview, bool) {
	userID, err := UserID(c)
	if err != nil {
		h.internalError(c, "missing_user_id", err)
		return store.Interview{}, false
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id", "reason": "编号不合法"})
		return store.Interview{}, false
	}

	iv, err := h.interviews.ByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrInterviewNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": "找不到这场面试"})
			return store.Interview{}, false
		}
		h.internalError(c, "load_interview_failed", err)
		return store.Interview{}, false
	}

	// 越权返回 404，不透露「这个 id 存在但不属于你」
	if iv.UserID != userID {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": "找不到这场面试"})
		return store.Interview{}, false
	}

	return iv, true
}

func (h *interviewHandler) internalError(c *gin.Context, event string, err error) {
	h.log.Error(event, "request_id", c.GetString("request_id"), "error", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "reason": "服务端出错了，请稍后重试"})
}
