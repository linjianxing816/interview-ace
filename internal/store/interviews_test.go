package store

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"interview-ace/internal/testutil/dbtest"
)

func TestInterviews_Create(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "i1@example.com")
	r, _, err := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	if err != nil {
		t.Fatalf("创建简历失败: %v", err)
	}

	iv, err := NewInterviews(pool).Create(ctx, u.ID, r.ID, "岗位描述", "backend", 12)
	if err != nil {
		t.Fatalf("创建面试失败: %v", err)
	}

	if iv.Status != "created" {
		t.Errorf("Status = %q，期望 created", iv.Status)
	}
	if iv.CurrentSeq != 0 {
		t.Errorf("CurrentSeq = %d，期望 0", iv.CurrentSeq)
	}
	if iv.MaxTurns != 12 {
		t.Errorf("MaxTurns = %d，期望 12", iv.MaxTurns)
	}
	if iv.FinishedAt != nil {
		t.Error("新会话的 FinishedAt 应为空")
	}
}

// TestInterviews_Transition_并发只有一个成功 是状态机最关键的并发断言：
// 两个请求同时点「开始面试」，只能有一个把 created 改成 asking。
// 用 update ... where status = $from（CAS）实现，先读再写会两个都成功。
func TestInterviews_Transition_并发只有一个成功(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "race@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	iv, _ := NewInterviews(pool).Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	const workers = 8
	var success, conflict int32
	var wg sync.WaitGroup

	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := NewInterviews(pool).Transition(ctx, iv.ID, "created", "asking", 1)
			switch {
			case err == nil:
				atomic.AddInt32(&success, 1)
			case errors.Is(err, ErrStatusConflict):
				atomic.AddInt32(&conflict, 1)
			default:
				t.Errorf("意外错误: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if success != 1 {
		t.Errorf("成功次数 = %d，期望恰好 1 次", success)
	}
	if conflict != workers-1 {
		t.Errorf("冲突次数 = %d，期望 %d", conflict, workers-1)
	}
}

func TestInterviews_Transition_状态不符返回冲突(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "st@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	iv, _ := NewInterviews(pool).Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	// created 状态下不能直接 finish
	if _, err := NewInterviews(pool).Transition(ctx, iv.ID, "asking", "answering", 1); !errors.Is(err, ErrStatusConflict) {
		t.Errorf("期望 ErrStatusConflict，实际: %v", err)
	}
}

func TestInterviews_Transition_不存在返回未找到(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	_, err := NewInterviews(pool).Transition(ctx, 999999, "created", "asking", 1)
	if !errors.Is(err, ErrInterviewNotFound) {
		t.Errorf("期望 ErrInterviewNotFound，实际: %v", err)
	}
}

func TestInterviews_AnswerTurn_重复提交被拒(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "ans@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	if _, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "第一题", "tech", ""); err != nil {
		t.Fatalf("加题失败: %v", err)
	}

	first, err := ivs.AnswerTurn(ctx, iv.ID, 1, "第一次回答")
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	if first.Answer != "第一次回答" {
		t.Errorf("Answer = %q", first.Answer)
	}
	if !first.Answered() {
		t.Error("AnsweredAt 应被填充")
	}

	// 重复提交（网络重试）
	_, err = ivs.AnswerTurn(ctx, iv.ID, 1, "第二次回答")
	if !errors.Is(err, ErrTurnAlreadyAnswered) {
		t.Fatalf("期望 ErrTurnAlreadyAnswered，实际: %v", err)
	}

	// 第一次的答案不能被覆盖
	again, _ := ivs.Turn(ctx, iv.ID, 1)
	if again.Answer != "第一次回答" {
		t.Errorf("答案被覆盖了: %q", again.Answer)
	}
}

func TestInterviews_AnswerTurn_轮次不存在(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "noexist@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	if _, err := ivs.AnswerTurn(ctx, iv.ID, 99, "答案"); !errors.Is(err, ErrTurnNotFound) {
		t.Errorf("期望 ErrTurnNotFound，实际: %v", err)
	}
}

func TestInterviews_AddTurn_同一seq重复被拒(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "dupseq@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	if _, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "题一", "tech", ""); err != nil {
		t.Fatalf("加题失败: %v", err)
	}
	if _, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "重复题", "tech", ""); !errors.Is(err, ErrTurnAlreadyAnswered) {
		t.Errorf("期望 ErrTurnAlreadyAnswered，实际: %v", err)
	}
}

func TestInterviews_Turns_按顺序返回(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "turns@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	for seq := 1; seq <= 3; seq++ {
		if _, err := ivs.AddTurn(ctx, iv.ID, seq, nil, "题目", "tech", ""); err != nil {
			t.Fatalf("加题失败: %v", err)
		}
	}

	turns, err := ivs.Turns(ctx, iv.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("轮次数 = %d，期望 3", len(turns))
	}
	for i, tr := range turns {
		if tr.Seq != i+1 {
			t.Errorf("第 %d 个轮次 seq = %d，期望 %d", i, tr.Seq, i+1)
		}
	}
}

func TestInterviews_Finish_写完成时间(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "fin@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)
	if _, err := ivs.Transition(ctx, iv.ID, "created", "asking", 1); err != nil {
		t.Fatalf("开始失败: %v", err)
	}

	done, err := ivs.Finish(ctx, iv.ID, "asking", "scoring")
	if err != nil {
		t.Fatalf("结束失败: %v", err)
	}
	if done.Status != "scoring" {
		t.Errorf("Status = %q，期望 scoring", done.Status)
	}
	if done.FinishedAt == nil {
		t.Error("FinishedAt 应被填充")
	}
}

func TestInterviews_SaveReport_只能从scoring推进(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "rep@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	// 还在 created，不能存报告
	if _, err := ivs.SaveReport(ctx, iv.ID, []byte(`{}`)); !errors.Is(err, ErrStatusConflict) {
		t.Errorf("非 scoring 状态应拒绝，实际: %v", err)
	}

	ivs.Transition(ctx, iv.ID, "created", "asking", 1)
	ivs.Finish(ctx, iv.ID, "asking", "scoring")

	done, err := ivs.SaveReport(ctx, iv.ID, []byte(`{"total":88}`))
	if err != nil {
		t.Fatalf("存报告失败: %v", err)
	}
	if done.Status != "done" {
		t.Errorf("Status = %q，期望 done", done.Status)
	}
	if len(done.Report) == 0 {
		t.Error("报告没写进去")
	}
}

func TestInterviews_ListByUser_只返回自己的(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	users := NewUsers(pool)
	resumes := NewResumes(pool)
	ivs := NewInterviews(pool)

	alice := mustUser(t, ctx, users, "ia@example.com")
	bob := mustUser(t, ctx, users, "ib@example.com")
	ra, _, _ := resumes.CreateOrGet(ctx, alice.ID, "a.pdf", "A", ContentHash("A"), nil, ParseDegraded)
	rb, _, _ := resumes.CreateOrGet(ctx, bob.ID, "b.pdf", "B", ContentHash("B"), nil, ParseDegraded)

	ivs.Create(ctx, alice.ID, ra.ID, "jd-a", "backend", 12)
	ivs.Create(ctx, bob.ID, rb.ID, "jd-b", "backend", 12)

	list, err := ivs.ListByUser(ctx, alice.ID)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("alice 应看到 1 条，实际 %d 条", len(list))
	}
	if list[0].UserID != alice.ID {
		t.Errorf("混进了别人的面试: user_id=%d", list[0].UserID)
	}
}

// TestInterviews_Trend_只返回已完成且按时间正序 是复盘功能的数据源断言。
func TestInterviews_Trend_只返回已完成(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "trend@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)

	// 一场走完的
	done, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)
	ivs.Transition(ctx, done.ID, "created", "asking", 1)
	ivs.Finish(ctx, done.ID, "asking", "scoring")
	if _, err := ivs.SaveReport(ctx, done.ID, []byte(`{"total":80,"level":"良好","dimensions":[{"dimension":"accuracy","score":4}]}`)); err != nil {
		t.Fatalf("存报告失败: %v", err)
	}

	// 一场没开始的
	ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	points, err := ivs.Trend(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("查询趋势失败: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("只应有 1 场已完成，实际 %d 场", len(points))
	}
	if points[0].Total != 80 {
		t.Errorf("Total = %d，期望 80", points[0].Total)
	}
	if points[0].Level != "良好" {
		t.Errorf("Level = %q，期望 良好", points[0].Level)
	}
}

func TestInterviews_Trend_按时间正序(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "trend2@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)

	for _, total := range []int{60, 70, 90} {
		iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)
		ivs.Transition(ctx, iv.ID, "created", "asking", 1)
		ivs.Finish(ctx, iv.ID, "asking", "scoring")
		ivs.SaveReport(ctx, iv.ID, []byte(`{"total":`+strconv.Itoa(total)+`}`))
	}

	points, err := ivs.Trend(ctx, u.ID, 10)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("应 3 场，实际 %d", len(points))
	}
	// 实现返回的是时间倒序（最新在前）
	if points[0].Total != 90 {
		t.Errorf("第一条应是最近一场（90），实际 %d", points[0].Total)
	}
	if points[2].Total != 60 {
		t.Errorf("最后一条应是最早一场（60），实际 %d", points[2].Total)
	}
}

func TestInterviews_AddTurn_追问类型能存能读(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "kind@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	// 第 1 题是普通题，没有追问类型
	first, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "怎么防超卖", "project", "")
	if err != nil {
		t.Fatalf("加第一题失败: %v", err)
	}
	if first.FollowupKind != "" {
		t.Errorf("普通题的追问类型应为空，实际 %q", first.FollowupKind)
	}

	// 第 2 题是边界类追问
	second, err := ivs.AddTurn(ctx, iv.ID, 2, &first.ID, "并发涨十倍还成立吗", "followup", "boundary")
	if err != nil {
		t.Fatalf("加追问失败: %v", err)
	}
	if second.FollowupKind != "boundary" {
		t.Errorf("追问类型 = %q，期望 boundary", second.FollowupKind)
	}

	// 从库里重新读一遍，确认真的落盘了（不是只在返回值里）
	got, err := ivs.Turn(ctx, iv.ID, 2)
	if err != nil {
		t.Fatalf("查询追问失败: %v", err)
	}
	if got.FollowupKind != "boundary" {
		t.Errorf("重新读出的追问类型 = %q，期望 boundary", got.FollowupKind)
	}

	// 整场取出也要带上类型
	all, err := ivs.Turns(ctx, iv.ID)
	if err != nil {
		t.Fatalf("查询全部轮次失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("应 2 轮，实际 %d", len(all))
	}
	if all[0].FollowupKind != "" || all[1].FollowupKind != "boundary" {
		t.Errorf("整场读出的类型不对: 第1轮=%q 第2轮=%q", all[0].FollowupKind, all[1].FollowupKind)
	}
}

func TestInterviews_AddTurn_非法追问类型被数据库拒绝(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	u := mustUser(t, ctx, NewUsers(pool), "badkind@example.com")
	r, _, _ := NewResumes(pool).CreateOrGet(ctx, u.ID, "cv.pdf", "正文", ContentHash("正文"), nil, ParseDegraded)
	ivs := NewInterviews(pool)
	iv, _ := ivs.Create(ctx, u.ID, r.ID, "jd", "backend", 12)

	// 校验写在数据库的 check 约束里，应用层传错值必须被拒
	// （应用层已经校验过一遍，这里是第二道闸）
	_, err := ivs.AddTurn(ctx, iv.ID, 1, nil, "随便问问", "followup", "乱写的类型")
	if err == nil {
		t.Fatal("非法追问类型应被数据库拒绝，实际成功了")
	}
}
