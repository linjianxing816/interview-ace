package interview

import (
	"context"
	"testing"
)

// 真实题目对照表：验证阈值 0.75 能把「改措辞」和「换话题」分开。
func TestSimilarity_真实题目对照(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		// 期望区间，不是精确值——Dice 系数会随字数微调，卡死数字会变脆
		min, max float64
	}{
		{
			name: "只改措辞，应该判重复",
			a:    "你说用 Postgres 做过并发写入，当时怎么避免超卖的？",
			b:    "你说用 Postgres 做过并发写入，当时是怎么防止超卖的？",
			min:  0.75, max: 1,
		},
		{
			name: "同一项目的另一个角度，不该误杀",
			a:    "你说用 Postgres 做过并发写入，当时怎么避免超卖的？",
			b:    "你的压测是怎么做的，TPS 最高到多少？",
			min: 0, max: 0.5,
		},
		{
			name: "完全不同的题",
			a:    "讲一次你和产品经理意见不一致的经历。",
			b:    "Redis 的持久化有哪几种方式？",
			min: 0, max: 0.3,
		},
		{
			name: "只差标点",
			a:    "你怎么保证这个接口的幂等性？",
			b:    "你怎么保证这个接口的幂等性?",
			min:  0.99, max: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := similarity(normalizeQuestion(c.a), normalizeQuestion(c.b))
			t.Logf("相似度 = %.3f（期望 %.2f ~ %.2f）", got, c.min, c.max)
			if got < c.min || got > c.max {
				t.Errorf("相似度 = %.3f，期望在 [%.2f, %.2f] 之间", got, c.min, c.max)
			}
		})
	}
}

func TestFindDuplicate_命中与放行(t *testing.T) {
	asked := []string{
		"你说用 Postgres 做过并发写入，当时怎么避免超卖的？",
		"你的压测是怎么做的，TPS 最高到多少？",
	}

	// 改措辞 → 命中
	dup, score := FindDuplicate("你说用 Postgres 做过并发写入，当时是怎么防止超卖的？", asked)
	if dup == "" {
		t.Fatalf("改措辞的题应该判为重复，实际放行了（最高相似度 %.2f）", score)
	}
	t.Logf("命中的原题：%s（相似度 %.2f）", dup, score)

	// 换话题 → 放行
	if dup, score := FindDuplicate("Redis 的持久化有哪几种方式？", asked); dup != "" {
		t.Errorf("不同话题的题被误判为重复：%s（相似度 %.2f）", dup, score)
	}
}

func TestFindDuplicate_短题目不查(t *testing.T) {
	// 太短的题目二元组太少，相似度没有统计意义，直接放行
	if dup, _ := FindDuplicate("介绍一下", []string{"介绍一下你自己"}); dup != "" {
		t.Errorf("短题目不该查重，实际命中 %q", dup)
	}
}

// 换话题时模型给了重复题 → 打回，第二次给新题 → 放行。
func TestNextStep_换话题时重复题被打回(t *testing.T) {
	dup := `{"action":"next","type":"project","question":"你说用 Postgres 做过并发写入，当时是怎么防止超卖的？"}`
	fresh := `{"action":"next","type":"behavior","question":"讲一次你和产品经理意见不一致的经历，最后怎么解决的？"}`
	fc := &fakeCompleter{contents: []string{dup, fresh}}

	q, err := NextStep(context.Background(), fc, StepRequest{
		RoleType: "backend",
		Asked:    []string{"你说用 Postgres 做过并发写入，当时怎么避免超卖的？"},
		Seq:      3,
	})
	if err != nil {
		t.Fatalf("应该打回后重出成功，实际报错: %v", err)
	}
	if q.Text != "讲一次你和产品经理意见不一致的经历，最后怎么解决的？" {
		t.Errorf("应该拿到第二道新题，实际 %q", q.Text)
	}
	if fc.calls != 2 {
		t.Errorf("调用次数 = %d，期望 2（第一次重复被打回）", fc.calls)
	}
}

// 追问和原题天然相似，不能被查重拦住。
func TestNextStep_追问不做查重(t *testing.T) {
	// 和原题几乎一样，但它是 followup，应该放行
	followup := `{"action":"followup","followup_kind":"probe","question":"你说用 Postgres 做过并发写入，当时具体是怎么防止超卖的？"}`
	fc := &fakeCompleter{contents: []string{followup}}

	q, err := NextStep(context.Background(), fc, StepRequest{
		RoleType: "backend",
		Asked:    []string{"你说用 Postgres 做过并发写入，当时怎么避免超卖的？"},
		Seq:      2,
	})
	if err != nil {
		t.Fatalf("追问不该被查重拦住: %v", err)
	}
	if q.Type != TypeFollowup {
		t.Errorf("type = %q，期望 followup", q.Type)
	}
	if fc.calls != 1 {
		t.Errorf("调用次数 = %d，期望 1（追问不查重，一次就过）", fc.calls)
	}
}
