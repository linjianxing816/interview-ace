package interview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const followupJSON = `{"action":"followup","followup_kind":"verify","question":"你说加了兜底，怎么确认它真的生效了，而不是碰巧没触发？"}`

const nextJSON = `{"action":"next","type":"project","question":"你简历里写的评测平台，判定层是怎么设计的？"}`

func TestNextStep_追问(t *testing.T) {
	fc := &fakeCompleter{contents: []string{followupJSON}}

	q, err := NextStep(context.Background(), fc, StepRequest{
		LastQuestion: "你怎么做的兜底？",
		LastAnswer:   "加了兜底",
		Seq:          2,
	})
	if err != nil {
		t.Fatalf("决策失败: %v", err)
	}
	if q.Type != TypeFollowup {
		t.Errorf("Type = %q，期望 followup", q.Type)
	}
	if q.Kind != FollowupVerify {
		t.Errorf("Kind = %q，期望 verify", q.Kind)
	}
}

func TestNextStep_换话题(t *testing.T) {
	fc := &fakeCompleter{contents: []string{nextJSON}}

	q, err := NextStep(context.Background(), fc, StepRequest{Seq: 2})
	if err != nil {
		t.Fatalf("决策失败: %v", err)
	}
	if q.Type != TypeProject {
		t.Errorf("Type = %q，期望 project", q.Type)
	}
	if q.Kind != "" {
		t.Errorf("普通题不该有 Kind，实际 %q", q.Kind)
	}
}

// TestNextStep_深度到上限时拒绝追问 是防钻牛角尖的硬边界：
// 模型已经被告知上限，但如果它还是返回 followup，程序必须拦住。
func TestNextStep_深度到上限时拒绝追问(t *testing.T) {
	fc := &fakeCompleter{contents: []string{followupJSON, nextJSON}}

	q, err := NextStep(context.Background(), fc, StepRequest{
		FollowupDepth: MaxFollowupDepth,
		Seq:           5,
	})
	if err != nil {
		t.Fatalf("应该重试后换话题，实际: %v", err)
	}
	if q.Type == TypeFollowup {
		t.Error("深度到上限后不该再追问")
	}
	if fc.calls != 2 {
		t.Errorf("调用次数 = %d，期望 2（第一次被拒后重试）", fc.calls)
	}
}

func TestNextStep_深度到上限时prompt明说必须换话题(t *testing.T) {
	fc := &fakeCompleter{contents: []string{nextJSON}}

	if _, err := NextStep(context.Background(), fc, StepRequest{
		FollowupDepth: MaxFollowupDepth, Seq: 3,
	}); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if !strings.Contains(fc.lastUser, "必须换话题") {
		t.Errorf("prompt 里没强调上限: %s", fc.lastUser)
	}
}

func TestNextStep_七种追问类型都合法(t *testing.T) {
	for kind := range followupKinds {
		t.Run(kind, func(t *testing.T) {
			body := `{"action":"followup","followup_kind":"` + kind + `","question":"这个问题具体是怎么处理的？"}`
			fc := &fakeCompleter{contents: []string{body}}

			q, err := NextStep(context.Background(), fc, StepRequest{Seq: 2})
			if err != nil {
				t.Fatalf("类型 %s 应合法，实际: %v", kind, err)
			}
			if q.Kind != kind {
				t.Errorf("Kind = %q，期望 %q", q.Kind, kind)
			}
		})
	}
}

func TestNextStep_非法输出被拒(t *testing.T) {
	cases := []struct{ name, body string }{
		{"不是JSON", "好的，我来追问"},
		{"action 未知", `{"action":"maybe","question":"这个问题具体是怎么处理的？"}`},
		{"追问类型未知", `{"action":"followup","followup_kind":"luck","question":"这个问题具体是怎么处理的？"}`},
		{"换话题类型错", `{"action":"next","type":"followup","question":"这个问题具体是怎么处理的？"}`},
		{"题目太短", `{"action":"next","type":"tech","question":"嗯"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeCompleter{contents: []string{c.body}}

			_, err := NextStep(context.Background(), fc, StepRequest{Seq: 2})
			if !errors.Is(err, ErrBadStep) {
				t.Errorf("期望 ErrBadStep，实际: %v", err)
			}
		})
	}
}

func TestNextStep_模型错误不重试(t *testing.T) {
	sentinel := errors.New("超时")
	fc := &fakeCompleter{err: sentinel}

	_, err := NextStep(context.Background(), fc, StepRequest{Seq: 2})
	if !errors.Is(err, sentinel) {
		t.Fatalf("期望包装原始错误，实际: %v", err)
	}
	if fc.calls != 1 {
		t.Errorf("不该重试，实际 %d 次", fc.calls)
	}
}

// TestNextStep_prompt带上刚答的内容 是追问质量的前提：
// 不带回答内容，模型只能瞎猜该追什么。
func TestNextStep_prompt带上刚答的内容(t *testing.T) {
	fc := &fakeCompleter{contents: []string{followupJSON}}

	_, err := NextStep(context.Background(), fc, StepRequest{
		LastQuestion: "你怎么做的兜底？",
		LastAnswer:   "我加了超时重试和降级返回缓存",
		Seq:          2,
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if !strings.Contains(fc.lastUser, "超时重试和降级返回缓存") {
		t.Errorf("prompt 里没有刚答的内容: %s", fc.lastUser)
	}
	if !strings.Contains(fc.lastUser, "你怎么做的兜底") {
		t.Errorf("prompt 里没有刚问的题: %s", fc.lastUser)
	}
}

// TestNextStep_只带最近三轮 控制成本：更早的问答对「这句该怎么追」没帮助。
func TestNextStep_只带最近三轮(t *testing.T) {
	fc := &fakeCompleter{contents: []string{nextJSON}}

	history := []QAPair{
		{Seq: 1, Question: "第一轮问题", Answer: "第一轮答案"},
		{Seq: 2, Question: "第二轮问题", Answer: "第二轮答案"},
		{Seq: 3, Question: "第三轮问题", Answer: "第三轮答案"},
		{Seq: 4, Question: "第四轮问题", Answer: "第四轮答案"},
		{Seq: 5, Question: "第五轮问题", Answer: "第五轮答案"},
	}
	if _, err := NextStep(context.Background(), fc, StepRequest{History: history, Seq: 6}); err != nil {
		t.Fatalf("失败: %v", err)
	}

	if strings.Contains(fc.lastUser, "第一轮问题") {
		t.Error("太早的问答不该进 prompt")
	}
	if !strings.Contains(fc.lastUser, "第五轮问题") {
		t.Error("最近的问答应该进 prompt")
	}
}

func TestFollowupKinds_七种都在(t *testing.T) {
	if len(followupKinds) != 7 {
		t.Errorf("追问类型 = %d 种，期望 7", len(followupKinds))
	}
	for _, k := range []string{FollowupCause, FollowupProbe, FollowupValue,
		FollowupBlindspot, FollowupVerify, FollowupBoundary, FollowupTradeoff} {
		if _, ok := followupKinds[k]; !ok {
			t.Errorf("缺少追问类型 %s", k)
		}
	}
}
