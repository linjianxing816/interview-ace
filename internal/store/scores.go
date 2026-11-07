package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ScoreRow 是 scores 表的一行。
// TurnID 为 nil 表示整场总评（数据库里 turn_id 是 NULL）。
type ScoreRow struct {
	TurnID    *int64
	Dimension string
	Score     int
	Comment   string
}

// Scores 是 scores 表的访问器。
type Scores struct {
	pool *pgxpool.Pool
}

func NewScores(pool *pgxpool.Pool) *Scores {
	return &Scores{pool: pool}
}

// ReplaceForInterview 用新结果整体替换一场面试的评分。
//
// 先删后插放在一个事务里：重新评分时不能留下上次的旧分数，
// 否则 (interview_id, turn_id, dimension) 的唯一约束会直接冲突。
func (s *Scores) ReplaceForInterview(ctx context.Context, interviewID int64, rows []ScoreRow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `delete from scores where interview_id = $1`, interviewID); err != nil {
		return fmt.Errorf("清理旧评分失败: %w", err)
	}

	for _, r := range rows {
		if _, err := tx.Exec(ctx,
			`insert into scores (interview_id, turn_id, dimension, score, comment)
			 values ($1, $2, $3, $4, $5)`,
			interviewID, r.TurnID, r.Dimension, r.Score, r.Comment); err != nil {
			return fmt.Errorf("写入评分失败（%s）: %w", r.Dimension, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("提交评分失败: %w", err)
	}
	return nil
}

// ByInterview 取出整场面试的评分。TurnID 为 nil 的行是总评。
func (s *Scores) ByInterview(ctx context.Context, interviewID int64) ([]ScoreRow, error) {
	rows, err := s.pool.Query(ctx,
		`select turn_id, dimension, score, coalesce(comment, '')
		 from scores where interview_id = $1
		 order by turn_id nulls first, dimension`, interviewID)
	if err != nil {
		return nil, fmt.Errorf("查询评分失败: %w", err)
	}
	defer rows.Close()

	var out []ScoreRow
	for rows.Next() {
		var r ScoreRow
		if err := rows.Scan(&r.TurnID, &r.Dimension, &r.Score, &r.Comment); err != nil {
			return nil, fmt.Errorf("读取评分行失败: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历评分失败: %w", err)
	}
	return out, nil
}
