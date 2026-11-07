package interview

import (
	"errors"
	"testing"
)

// TestApply_所有合法流转 把转移表里的每一条都跑一遍。
// 表驱动的好处：加状态时只要补一行，这里自动覆盖。
func TestApply_所有合法流转(t *testing.T) {
	cases := []struct {
		from Status
		ev   Event
		want Status
	}{
		{StatusCreated, EventStart, StatusAsking},
		{StatusCreated, EventAbort, StatusAborted},

		{StatusAsking, EventSubmitAnswer, StatusAnswering},
		{StatusAsking, EventFinish, StatusScoring},
		{StatusAsking, EventAbort, StatusAborted},

		{StatusAnswering, EventNextQuestion, StatusAsking},
		{StatusAnswering, EventFinish, StatusScoring},
		{StatusAnswering, EventAbort, StatusAborted},

		{StatusScoring, EventScored, StatusDone},
	}

	for _, c := range cases {
		t.Run(string(c.from)+"_"+string(c.ev), func(t *testing.T) {
			got, err := Apply(c.from, c.ev)
			if err != nil {
				t.Fatalf("期望合法，实际报错: %v", err)
			}
			if got != c.want {
				t.Errorf("Apply(%s, %s) = %s，期望 %s", c.from, c.ev, got, c.want)
			}
		})
	}
}

// TestApply_非法流转一律拒绝 穷举所有 (状态, 事件) 组合，
// 只允许转移表里明确列出的那些。默认拒绝比默认允许安全。
func TestApply_非法流转一律拒绝(t *testing.T) {
	allStatuses := []Status{StatusCreated, StatusAsking, StatusAnswering, StatusScoring, StatusDone, StatusAborted}
	allEvents := []Event{EventStart, EventSubmitAnswer, EventNextQuestion, EventFinish, EventAbort, EventScored}

	legal := map[string]bool{
		"created|start":           true,
		"created|abort":           true,
		"asking|submit_answer":    true,
		"asking|finish":           true,
		"asking|abort":            true,
		"answering|next_question": true,
		"answering|finish":        true,
		"answering|abort":         true,
		"scoring|scored":          true,
	}

	checked := 0
	for _, s := range allStatuses {
		for _, e := range allEvents {
			key := string(s) + "|" + string(e)
			_, err := Apply(s, e)

			if legal[key] {
				if err != nil {
					t.Errorf("%s 应合法，实际报错: %v", key, err)
				}
				continue
			}

			checked++
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s 应返回 ErrInvalidTransition，实际: %v", key, err)
			}
		}
	}

	// 36 种组合 - 9 种合法 = 27 种非法
	if checked != 27 {
		t.Errorf("检查了 %d 种非法组合，期望 27", checked)
	}
}

// TestApply_终态不接受任何事件 是最重要的一组：
// done 之后再提交答案必须被拒绝，否则会出现「面试结束还能继续答」。
func TestApply_终态不接受任何事件(t *testing.T) {
	for _, terminal := range []Status{StatusDone, StatusAborted} {
		for _, ev := range []Event{EventStart, EventSubmitAnswer, EventNextQuestion, EventFinish, EventAbort, EventScored} {
			if _, err := Apply(terminal, ev); !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("终态 %s 收到 %s 应被拒绝，实际: %v", terminal, ev, err)
			}
		}
	}
}

func TestApply_未知状态(t *testing.T) {
	if _, err := Apply(Status("nonsense"), EventStart); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("未知状态应返回 ErrInvalidTransition，实际: %v", err)
	}
}

func TestCan_与Apply一致(t *testing.T) {
	if !Can(StatusCreated, EventStart) {
		t.Error("created 应接受 start")
	}
	if Can(StatusDone, EventSubmitAnswer) {
		t.Error("done 不该接受 submit_answer")
	}
}

func TestStatus_Terminal(t *testing.T) {
	cases := map[Status]bool{
		StatusCreated:   false,
		StatusAsking:    false,
		StatusAnswering: false,
		StatusScoring:   false,
		StatusDone:      true,
		StatusAborted:   true,
	}
	for s, want := range cases {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v，期望 %v", s, got, want)
		}
	}
}

func TestStatus_InProgress(t *testing.T) {
	cases := map[Status]bool{
		StatusCreated:   false,
		StatusAsking:    true,
		StatusAnswering: true,
		StatusScoring:   false,
		StatusDone:      false,
		StatusAborted:   false,
	}
	for s, want := range cases {
		if got := s.InProgress(); got != want {
			t.Errorf("%s.InProgress() = %v，期望 %v", s, got, want)
		}
	}
}

func TestStatus_Valid(t *testing.T) {
	for _, s := range []Status{StatusCreated, StatusAsking, StatusAnswering, StatusScoring, StatusDone, StatusAborted} {
		if !s.Valid() {
			t.Errorf("%s 应合法", s)
		}
	}
	if Status("weird").Valid() {
		t.Error("weird 不该合法")
	}
}

func TestNextAction(t *testing.T) {
	cases := []struct {
		name       string
		currentSeq int
		maxTurns   int
		want       Action
	}{
		{"刚开始，还有轮次", 0, 12, ActionAskNext},
		{"差一题到上限", 11, 12, ActionAskNext},
		{"正好到上限", 12, 12, ActionScore},
		{"超过上限", 15, 12, ActionScore},
		{"上限为 0 视为不限但立即结束", 0, 0, ActionScore},
		{"上限为负", 1, -1, ActionScore},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NextAction(c.currentSeq, c.maxTurns); got != c.want {
				t.Errorf("NextAction(%d, %d) = %s，期望 %s",
					c.currentSeq, c.maxTurns, got, c.want)
			}
		})
	}
}
