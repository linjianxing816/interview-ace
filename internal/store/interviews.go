package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrInterviewNotFound 查不到面试会话。
	ErrInterviewNotFound = errors.New("面试会话不存在")
	// ErrStatusConflict 状态已被别的请求改掉，本次操作作废。
	ErrStatusConflict = errors.New("会话状态已变化")
	// ErrTurnAlreadyAnswered 这一轮已经答过了（重复提交）。
	ErrTurnAlreadyAnswered = errors.New("这一轮已经回答过了")
	// ErrTurnNotFound 轮次不存在。
	ErrTurnNotFound = errors.New("轮次不存在")
)

// Interview 是 interviews 表的一行。
type Interview struct {
	ID         int64
	UserID     int64
	ResumeID   int64
	JDText     string
	RoleType   string
	Status     string
	CurrentSeq int
	MaxTurns   int
	Report     json.RawMessage
	CreatedAt  time.Time
	FinishedAt *time.Time
}

// Turn 是 turns 表的一行。
type Turn struct {
	ID           int64
	InterviewID  int64
	Seq          int
	ParentTurnID *int64
	Question     string
	QuestionType string
	// FollowupKind 只在 QuestionType == "followup" 时非空，
	// 取值见 interview 包的七类追问常量。
	// 和 QuestionType 是两个正交维度：一个是话题，一个是挖法。
	FollowupKind string
	Answer       string
	AskedAt      time.Time
	AnsweredAt   *time.Time
}

// Answered 表示这一轮已经有回答。
func (t Turn) Answered() bool {
	return t.AnsweredAt != nil
}

// Interviews 是 interviews / turns 两张表的访问器。
type Interviews struct {
	pool *pgxpool.Pool
}

func NewInterviews(pool *pgxpool.Pool) *Interviews {
	return &Interviews{pool: pool}
}

const interviewColumns = `id, user_id, resume_id, jd_text, role_type, status,
	current_seq, max_turns, report, created_at, finished_at`

// Create 新建一场面试，初始状态 created。
func (i *Interviews) Create(ctx context.Context, userID, resumeID int64, jdText, roleType string, maxTurns int) (Interview, error) {
	var out Interview
	err := i.pool.QueryRow(ctx,
		`insert into interviews (user_id, resume_id, jd_text, role_type, status, max_turns)
		 values ($1, $2, $3, $4, 'created', $5)
		 returning `+interviewColumns,
		userID, resumeID, jdText, roleType, maxTurns,
	).Scan(&out.ID, &out.UserID, &out.ResumeID, &out.JDText, &out.RoleType, &out.Status,
		&out.CurrentSeq, &out.MaxTurns, &out.Report, &out.CreatedAt, &out.FinishedAt)

	if err != nil {
		return Interview{}, fmt.Errorf("创建面试失败: %w", err)
	}
	return out, nil
}

// ByID 按主键查。调用方必须自己校验 user_id。
func (i *Interviews) ByID(ctx context.Context, id int64) (Interview, error) {
	var out Interview
	err := i.pool.QueryRow(ctx,
		`select `+interviewColumns+` from interviews where id = $1`, id,
	).Scan(&out.ID, &out.UserID, &out.ResumeID, &out.JDText, &out.RoleType, &out.Status,
		&out.CurrentSeq, &out.MaxTurns, &out.Report, &out.CreatedAt, &out.FinishedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Interview{}, ErrInterviewNotFound
		}
		return Interview{}, fmt.Errorf("查询面试失败: %w", err)
	}
	return out, nil
}

// ListByUser 按时间倒序列出用户的面试（不含报告正文）。
func (i *Interviews) ListByUser(ctx context.Context, userID int64) ([]Interview, error) {
	rows, err := i.pool.Query(ctx,
		`select id, user_id, resume_id, jd_text, role_type, status,
		        current_seq, max_turns, created_at, finished_at
		 from interviews where user_id = $1
		 order by created_at desc, id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询面试列表失败: %w", err)
	}
	defer rows.Close()

	var out []Interview
	for rows.Next() {
		var it Interview
		if err := rows.Scan(&it.ID, &it.UserID, &it.ResumeID, &it.JDText, &it.RoleType,
			&it.Status, &it.CurrentSeq, &it.MaxTurns, &it.CreatedAt, &it.FinishedAt); err != nil {
			return nil, fmt.Errorf("读取面试行失败: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历面试列表失败: %w", err)
	}
	return out, nil
}

// Transition 做一次「带前置状态校验」的状态迁移。
//
// 用 update ... where status = $from 实现（CAS），不是先读再写：
// 两个请求同时点「开始面试」时，只有一个能把 created 改成 asking，
// 另一个拿到 ErrStatusConflict。先读再写会两个都成功。
func (i *Interviews) Transition(ctx context.Context, id int64, from, to string, currentSeq int) (Interview, error) {
	var out Interview
	err := i.pool.QueryRow(ctx,
		`update interviews
		 set status = $3, current_seq = $4
		 where id = $1 and status = $2
		 returning `+interviewColumns,
		id, from, to, currentSeq,
	).Scan(&out.ID, &out.UserID, &out.ResumeID, &out.JDText, &out.RoleType, &out.Status,
		&out.CurrentSeq, &out.MaxTurns, &out.Report, &out.CreatedAt, &out.FinishedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 要么会话不存在，要么状态已经不是 from
			if _, getErr := i.ByID(ctx, id); errors.Is(getErr, ErrInterviewNotFound) {
				return Interview{}, ErrInterviewNotFound
			}
			return Interview{}, ErrStatusConflict
		}
		return Interview{}, fmt.Errorf("更新面试状态失败: %w", err)
	}
	return out, nil
}

// Finish 结束面试：写 finished_at 并进入目标状态。
func (i *Interviews) Finish(ctx context.Context, id int64, from, to string) (Interview, error) {
	var out Interview
	err := i.pool.QueryRow(ctx,
		`update interviews
		 set status = $3, finished_at = now()
		 where id = $1 and status = $2
		 returning `+interviewColumns,
		id, from, to,
	).Scan(&out.ID, &out.UserID, &out.ResumeID, &out.JDText, &out.RoleType, &out.Status,
		&out.CurrentSeq, &out.MaxTurns, &out.Report, &out.CreatedAt, &out.FinishedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Interview{}, ErrStatusConflict
		}
		return Interview{}, fmt.Errorf("结束面试失败: %w", err)
	}
	return out, nil
}

// SaveReport 写入评分报告并把状态从 scoring 推到 done。
func (i *Interviews) SaveReport(ctx context.Context, id int64, report []byte) (Interview, error) {
	var out Interview
	err := i.pool.QueryRow(ctx,
		`update interviews
		 set report = $2, status = 'done'
		 where id = $1 and status = 'scoring'
		 returning `+interviewColumns,
		id, report,
	).Scan(&out.ID, &out.UserID, &out.ResumeID, &out.JDText, &out.RoleType, &out.Status,
		&out.CurrentSeq, &out.MaxTurns, &out.Report, &out.CreatedAt, &out.FinishedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Interview{}, ErrStatusConflict
		}
		return Interview{}, fmt.Errorf("保存报告失败: %w", err)
	}
	return out, nil
}

// AddTurn 追加一轮提问。
//
// followupKind 只在追问时传，普通题目传空串（写进库是 null）。
//
// 并发重复提交靠 (interview_id, seq) 唯一约束兜底：
// 两个请求同时出同一题，第二个拿到 ErrStatusConflict 的语义（这里翻成 ErrTurnExists）。
func (i *Interviews) AddTurn(ctx context.Context, interviewID int64, seq int, parentID *int64, question, questionType, followupKind string) (Turn, error) {
	var out Turn
	// 空串要转成 null：数据库的 check 约束只认七个枚举值或 null，
	// 直接塞空串会被约束拒绝。
	var kind *string
	if followupKind != "" {
		kind = &followupKind
	}

	err := i.pool.QueryRow(ctx,
		`insert into turns (interview_id, seq, parent_turn_id, question, question_type, followup_kind)
		 values ($1, $2, $3, $4, $5, $6)
		 returning id, interview_id, seq, parent_turn_id, question, question_type,
		           coalesce(followup_kind, ''), coalesce(answer, ''), asked_at, answered_at`,
		interviewID, seq, parentID, question, questionType, kind,
	).Scan(&out.ID, &out.InterviewID, &out.Seq, &out.ParentTurnID, &out.Question,
		&out.QuestionType, &out.FollowupKind, &out.Answer, &out.AskedAt, &out.AnsweredAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Turn{}, ErrTurnAlreadyAnswered
		}
		return Turn{}, fmt.Errorf("写入轮次失败: %w", err)
	}
	return out, nil
}

// AnswerTurn 提交一轮回答。
//
// 只更新 answer 还是空的行：重复提交（网络重试）第二次影响 0 行，
// 返回 ErrTurnAlreadyAnswered，不会把第一次的答案覆盖掉。
func (i *Interviews) AnswerTurn(ctx context.Context, interviewID int64, seq int, answer string) (Turn, error) {
	var out Turn
	err := i.pool.QueryRow(ctx,
		`update turns set answer = $3, answered_at = now()
		 where interview_id = $1 and seq = $2 and answered_at is null
		 returning id, interview_id, seq, parent_turn_id, question, question_type,
		           coalesce(followup_kind, ''), coalesce(answer, ''), asked_at, answered_at`,
		interviewID, seq, answer,
	).Scan(&out.ID, &out.InterviewID, &out.Seq, &out.ParentTurnID, &out.Question,
		&out.QuestionType, &out.FollowupKind, &out.Answer, &out.AskedAt, &out.AnsweredAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 这一轮不存在，或者已经答过了
			existing, getErr := i.Turn(ctx, interviewID, seq)
			if errors.Is(getErr, ErrTurnNotFound) {
				return Turn{}, ErrTurnNotFound
			}
			if existing.Answered() {
				return Turn{}, ErrTurnAlreadyAnswered
			}
			return Turn{}, fmt.Errorf("提交回答失败: %w", err)
		}
		return Turn{}, fmt.Errorf("提交回答失败: %w", err)
	}
	return out, nil
}

// Turn 查某一轮。
func (i *Interviews) Turn(ctx context.Context, interviewID int64, seq int) (Turn, error) {
	var out Turn
	err := i.pool.QueryRow(ctx,
		`select id, interview_id, seq, parent_turn_id, question, question_type,
		        coalesce(followup_kind, ''), coalesce(answer, ''), asked_at, answered_at
		 from turns where interview_id = $1 and seq = $2`, interviewID, seq,
	).Scan(&out.ID, &out.InterviewID, &out.Seq, &out.ParentTurnID, &out.Question,
		&out.QuestionType, &out.FollowupKind, &out.Answer, &out.AskedAt, &out.AnsweredAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Turn{}, ErrTurnNotFound
		}
		return Turn{}, fmt.Errorf("查询轮次失败: %w", err)
	}
	return out, nil
}

// Turns 按顺序取出整场面试的所有轮次（断点恢复要用）。
func (i *Interviews) Turns(ctx context.Context, interviewID int64) ([]Turn, error) {
	rows, err := i.pool.Query(ctx,
		`select id, interview_id, seq, parent_turn_id, question, question_type,
		        coalesce(followup_kind, ''), coalesce(answer, ''), asked_at, answered_at
		 from turns where interview_id = $1 order by seq`, interviewID)
	if err != nil {
		return nil, fmt.Errorf("查询轮次列表失败: %w", err)
	}
	defer rows.Close()

	var out []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.InterviewID, &t.Seq, &t.ParentTurnID, &t.Question,
			&t.QuestionType, &t.FollowupKind, &t.Answer, &t.AskedAt, &t.AnsweredAt); err != nil {
			return nil, fmt.Errorf("读取轮次行失败: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历轮次失败: %w", err)
	}
	return out, nil
}

// TrendPoint 是一场已完成面试的成绩快照，用于复盘趋势。
type TrendPoint struct {
	InterviewID int64           `json:"interview_id"`
	CreatedAt   time.Time       `json:"created_at"`
	RoleType    string          `json:"role_type"`
	Total       int             `json:"total"`
	Level       string          `json:"level"`
	Dimensions  json.RawMessage `json:"dimensions"`
}

// Trend 按时间倒序取最近 n 场已完成面试的成绩。
//
// 只查 status='done'：没出报告的面试没有 total，混进来会把趋势线打断。
func (i *Interviews) Trend(ctx context.Context, userID int64, limit int) ([]TrendPoint, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	rows, err := i.pool.Query(ctx,
		`select id, created_at, role_type,
		        coalesce((report->>'total')::int, 0),
		        coalesce(report->>'level', ''),
		        coalesce(report->'dimensions', '[]'::jsonb)
		 from interviews
		 where user_id = $1 and status = 'done' and report is not null
		 order by created_at desc, id desc
		 limit $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询成绩趋势失败: %w", err)
	}
	defer rows.Close()

	var out []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.InterviewID, &p.CreatedAt, &p.RoleType, &p.Total, &p.Level, &p.Dimensions); err != nil {
			return nil, fmt.Errorf("读取趋势行失败: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历趋势失败: %w", err)
	}
	return out, nil
}
