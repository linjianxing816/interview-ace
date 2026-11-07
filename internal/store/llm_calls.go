package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LLM 调用的用途，跟数据库 check 约束一一对应。
const (
	PurposeQuestion = "question"
	PurposeFollowup = "followup"
	PurposeScore    = "score"
	PurposeParse    = "parse"
)

// LLM 调用状态。
const (
	StatusOK          = "ok"
	StatusTimeout     = "timeout"
	StatusInvalidJSON = "invalid_json"
	StatusRetried     = "retried"
	StatusError       = "error"
)

// LLMCall 是一次模型调用的记录。
type LLMCall struct {
	InterviewID      *int64 // 简历解析时没有面试，为 nil
	Purpose          string
	PromptTokens     int
	CompletionTokens int
	LatencyMS        int
	Status           string
}

// LLMCalls 是 llm_calls 表的访问器。
//
// 这张表存在的意义：成本能核算、故障能排查、「防失控」不是嘴上说的。
type LLMCalls struct {
	pool *pgxpool.Pool
}

func NewLLMCalls(pool *pgxpool.Pool) *LLMCalls {
	return &LLMCalls{pool: pool}
}

// Record 写一条调用记录。
//
// 刻意不返回错误给调用方中断业务：记日志失败不该让用户的上传/答题失败。
// 调用方可以选择只打日志。
func (l *LLMCalls) Record(ctx context.Context, c LLMCall) error {
	_, err := l.pool.Exec(ctx,
		`insert into llm_calls
		 (interview_id, purpose, prompt_tokens, completion_tokens, latency_ms, status)
		 values ($1, $2, $3, $4, $5, $6)`,
		c.InterviewID, c.Purpose, c.PromptTokens, c.CompletionTokens, c.LatencyMS, c.Status)
	if err != nil {
		return fmt.Errorf("记录模型调用失败: %w", err)
	}
	return nil
}

// LLMUsageStat 是按用途聚合的用量。
type LLMUsageStat struct {
	Purpose          string
	Calls            int64
	PromptTokens     int64
	CompletionTokens int64
	Failures         int64 // status != 'ok' 的次数
}

// LLMUsageSummary 是整体用量。
type LLMUsageSummary struct {
	TotalCalls  int64
	TotalTokens int64
	FailedCalls int64
	ByPurpose   []LLMUsageStat
	Since       time.Time
}

// Summary 汇总 since 之后的模型用量。
//
// 用 SQL 聚合而不是把行拉回来在 Go 里算：这张表会一直长，
// 拉回来再算迟早变成内存问题。
func (l *LLMCalls) Summary(ctx context.Context, since time.Time) (LLMUsageSummary, error) {
	out := LLMUsageSummary{Since: since}

	rows, err := l.pool.Query(ctx,
		`select purpose,
		        count(*),
		        coalesce(sum(prompt_tokens), 0),
		        coalesce(sum(completion_tokens), 0),
		        count(*) filter (where status <> 'ok')
		 from llm_calls
		 where created_at >= $1
		 group by purpose
		 order by purpose`, since)
	if err != nil {
		return out, fmt.Errorf("汇总模型用量失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var s LLMUsageStat
		if err := rows.Scan(&s.Purpose, &s.Calls, &s.PromptTokens, &s.CompletionTokens, &s.Failures); err != nil {
			return out, fmt.Errorf("读取用量行失败: %w", err)
		}
		out.ByPurpose = append(out.ByPurpose, s)
		out.TotalCalls += s.Calls
		out.TotalTokens += s.PromptTokens + s.CompletionTokens
		out.FailedCalls += s.Failures
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("遍历用量行失败: %w", err)
	}

	return out, nil
}
