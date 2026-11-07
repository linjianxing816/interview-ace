package store

import (
	"context"
	"testing"

	"interview-ace/internal/testutil/dbtest"
)

func TestScores_ReplaceForInterview(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewPool(t)
	u := mustUser(t, ctx, NewUsers(pool), "sc1@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)
	turn, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "第一题", "tech", "")
	if err != nil {
		t.Fatalf("加题失败: %v", err)
	}

	scores := NewScores(pool)
	turnID := turn.ID
	rows := []ScoreRow{
		{Dimension: "accuracy", Score: 4, Comment: "总评-准确"},
		{Dimension: "clarity", Score: 4, Comment: "总评-清晰"},
		{Dimension: "depth", Score: 5, Comment: "总评-深度"},
		{TurnID: &turnID, Dimension: "accuracy", Score: 4, Comment: "单轮-准确"},
		{TurnID: &turnID, Dimension: "clarity", Score: 3, Comment: "单轮-清晰"},
		{TurnID: &turnID, Dimension: "depth", Score: 5, Comment: "单轮-深度"},
	}
	if err := scores.ReplaceForInterview(ctx, iv.ID, rows); err != nil {
		t.Fatalf("写入评分失败: %v", err)
	}

	got, err := scores.ByInterview(ctx, iv.ID)
	if err != nil {
		t.Fatalf("查询评分失败: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("评分行 = %d，期望 6", len(got))
	}

	overall := 0
	for _, row := range got {
		if row.TurnID == nil {
			overall++
		}
	}
	if overall != 3 {
		t.Errorf("总评行 = %d，期望 3（turn_id 为 NULL）", overall)
	}
}

// TestScores_ReplaceForInterview_重新评分不冲突 是关键断言：
// 同一场面试评两次，第一次的旧分数必须被清掉，
// 否则 (interview_id, turn_id, dimension) 唯一约束会直接报错。
func TestScores_ReplaceForInterview_重新评分不冲突(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewPool(t)
	u := mustUser(t, ctx, NewUsers(pool), "sc2@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	scores := NewScores(pool)
	first := []ScoreRow{
		{Dimension: "accuracy", Score: 3, Comment: "第一次"},
		{Dimension: "clarity", Score: 3, Comment: "第一次"},
		{Dimension: "depth", Score: 3, Comment: "第一次"},
	}
	if err := scores.ReplaceForInterview(ctx, iv.ID, first); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	second := []ScoreRow{
		{Dimension: "accuracy", Score: 5, Comment: "第二次"},
		{Dimension: "clarity", Score: 5, Comment: "第二次"},
		{Dimension: "depth", Score: 5, Comment: "第二次"},
	}
	if err := scores.ReplaceForInterview(ctx, iv.ID, second); err != nil {
		t.Fatalf("重新评分失败（旧分数没清干净？）: %v", err)
	}

	got, _ := scores.ByInterview(ctx, iv.ID)
	if len(got) != 3 {
		t.Fatalf("评分行 = %d，期望 3（旧的应被清掉）", len(got))
	}
	for _, row := range got {
		if row.Score != 5 {
			t.Errorf("应读到第二次的分，实际 %s=%d", row.Dimension, row.Score)
		}
	}
}

func TestScores_ByInterview_空结果不是nil(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewPool(t)
	u := mustUser(t, ctx, NewUsers(pool), "sc3@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	iv, _ := NewInterviews(pool).Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	got, err := NewScores(pool).ByInterview(ctx, iv.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望空，实际 %d 条", len(got))
	}
}
