// Package interview 是面试会话的状态机与领域规则。
//
// 这里只有纯函数：输入当前状态和事件，输出新状态。
// 不碰数据库、不调模型、不依赖 HTTP——所以能把所有流转组合穷举测一遍。
package interview

import (
	"errors"
	"fmt"
)

// Status 是面试会话的状态。取值跟数据库 interviews.status 的 check 约束一一对应，
// 改这里必须同时改迁移。
type Status string

const (
	// StatusCreated 建好了但还没开始，此时没有题目。
	StatusCreated Status = "created"
	// StatusAsking 有一道待回答的题。
	StatusAsking Status = "asking"
	// StatusAnswering 用户已交答案，系统还没决定下一题（可能要调模型追问）。
	// 单独设这个态而不是直接跳回 asking：生成下一题失败时能停在这里重试，
	// 不会出现「答案存了但题目丢了」的中间状态。
	StatusAnswering Status = "answering"
	// StatusScoring 面试结束，正在评分。
	StatusScoring Status = "scoring"
	// StatusDone 评分完成，可以出报告。
	StatusDone Status = "done"
	// StatusAborted 用户中途放弃。
	StatusAborted Status = "aborted"
)

// Event 是驱动状态变化的事件。
type Event string

const (
	// EventStart 开始面试（生成第一题）。
	EventStart Event = "start"
	// EventSubmitAnswer 提交一轮回答。
	EventSubmitAnswer Event = "submit_answer"
	// EventNextQuestion 系统出了下一题。
	EventNextQuestion Event = "next_question"
	// EventFinish 用户主动结束（或到达轮次上限）。
	EventFinish Event = "finish"
	// EventAbort 放弃面试。
	EventAbort Event = "abort"
	// EventScored 评分完成。
	EventScored Event = "scored"
)

// ErrInvalidTransition 表示当前状态下不接受这个事件。
var ErrInvalidTransition = errors.New("非法的状态流转")

// transitions 是完整的状态转移表。
//
// 写成表而不是一堆 if：所有合法路径一眼看全，加状态时也不会漏分支。
// 没列出来的组合一律非法——默认拒绝比默认允许安全。
var transitions = map[Status]map[Event]Status{
	StatusCreated: {
		EventStart: StatusAsking,
		EventAbort: StatusAborted,
	},
	StatusAsking: {
		EventSubmitAnswer: StatusAnswering,
		EventFinish:       StatusScoring,
		EventAbort:        StatusAborted,
	},
	StatusAnswering: {
		EventNextQuestion: StatusAsking,
		EventFinish:       StatusScoring,
		EventAbort:        StatusAborted,
	},
	StatusScoring: {
		EventScored: StatusDone,
	},
	// 终态不接受任何事件
	StatusDone:    {},
	StatusAborted: {},
}

// Apply 计算下一个状态。非法流转返回 ErrInvalidTransition。
func Apply(from Status, ev Event) (Status, error) {
	byEvent, ok := transitions[from]
	if !ok {
		return "", fmt.Errorf("%w: 未知状态 %q", ErrInvalidTransition, from)
	}

	next, ok := byEvent[ev]
	if !ok {
		return "", fmt.Errorf("%w: 状态 %q 不接受事件 %q", ErrInvalidTransition, from, ev)
	}

	return next, nil
}

// Can 判断当前状态是否接受某个事件，供 handler 提前返回 409 用。
func Can(from Status, ev Event) bool {
	_, err := Apply(from, ev)
	return err == nil
}

// Terminal 表示这个状态不会再有变化。
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusAborted
}

// InProgress 表示面试还在进行中（可以答题或即将答题）。
func (s Status) InProgress() bool {
	return s == StatusAsking || s == StatusAnswering
}

// Valid 判断是不是一个已知状态。用于把数据库里读出来的字符串做一次校验。
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Action 是「答完一题之后该干什么」。
type Action string

const (
	// ActionAskNext 出下一题。
	ActionAskNext Action = "ask_next"
	// ActionScore 结束答题，进入评分。
	ActionScore Action = "score"
)

// ErrTurnLimitReached 表示已经用完全部轮次。
var ErrTurnLimitReached = errors.New("已达到轮次上限")

// NextAction 决定答完一题之后是继续出题还是进入评分。
//
// currentSeq 是已经问过的题数，maxTurns 是上限。
// 轮次上限是防失控的第一道闸：模型追问停不下来时，这里是硬边界。
func NextAction(currentSeq, maxTurns int) Action {
	if maxTurns <= 0 || currentSeq >= maxTurns {
		return ActionScore
	}
	return ActionAskNext
}
