package interview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// goodReport 是一份合法评分：三维度 4/3/5 → 平均 4 → 80 分 → 良好。
const goodReport = `{
  "dimensions": [
    {"dimension":"accuracy","score":4,"comment":"唯一索引的说法正确"},
    {"dimension":"clarity","score":3,"comment":"先讲结论再展开，但中间有点绕"},
    {"dimension":"depth","score":5,"comment":"提到了 TOCTOU 和约束下沉"}
  ],
  "turn_scores": [
    {"seq":1,"scores":[
      {"dimension":"accuracy","score":4,"comment":"正确"},
      {"dimension":"clarity","score":3,"comment":"尚可"},
      {"dimension":"depth","score":5,"comment":"有原理"}
    ]}
  ],
  "strengths": ["讲清了 TOCTOU 窗口"],
  "weaknesses": ["没提隔离级别"],
  "suggestions": ["补充可重复读与串行化的区别"]
}`

func TestScoreInterview_正常评分(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodReport}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		RoleType: "backend",
		JDText:   "负责后端开发",
		Turns:    []QAPair{{Seq: 1, Question: "怎么防超卖", Answer: "唯一索引加事务"}},
		Samples:  1,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	// (4+3+5)/3 = 4 → 4/5*100 = 80
	if r.Total != 80 {
		t.Errorf("Total = %d，期望 80", r.Total)
	}
	if r.Level != "良好" {
		t.Errorf("Level = %q，期望 良好", r.Level)
	}
	if len(r.Dimensions) != 3 {
		t.Errorf("维度数 = %d，期望 3", len(r.Dimensions))
	}
	if len(r.TurnScores) != 1 {
		t.Errorf("轮次分 = %d 条，期望 1", len(r.TurnScores))
	}
	if len(r.Strengths) != 1 || len(r.Suggestions) != 1 {
		t.Errorf("优点/建议没解析出来: %+v", r)
	}
}

// TestScoreInterview_总分由程序算 是关键断言：
// 模型在输出里塞了一个 total 字段，程序必须无视它，用自己的公式。
func TestScoreInterview_总分由程序算(t *testing.T) {
	withFakeTotal := strings.Replace(goodReport, `"dimensions"`, `"total": 99, "dimensions"`, 1)
	fc := &fakeCompleter{contents: []string{withFakeTotal}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if r.Total != 80 {
		t.Errorf("Total = %d，期望 80（模型给的 99 应被忽略）", r.Total)
	}
}

func TestScoreInterview_维度必须齐全(t *testing.T) {
	missingDepth := `{
	  "dimensions": [
	    {"dimension":"accuracy","score":4,"comment":"x"},
	    {"dimension":"clarity","score":3,"comment":"y"}
	  ],
	  "turn_scores": [{"seq":1,"scores":[
	    {"dimension":"accuracy","score":4,"comment":"x"},
	    {"dimension":"clarity","score":3,"comment":"y"},
	    {"dimension":"depth","score":5,"comment":"z"}
	  ]}]
	}`
	fc := &fakeCompleter{contents: []string{missingDepth}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("错误信息应指出缺哪个维度: %v", err)
	}
}

func TestScoreInterview_分数越界被拒(t *testing.T) {
	out := strings.Replace(goodReport, `"score":5,"comment":"提到了 TOCTOU`, `"score":9,"comment":"提到了 TOCTOU`, 1)
	fc := &fakeCompleter{contents: []string{out}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
}

// TestScoreInterview_轮次必须全覆盖 防止「只评了前两轮」的报告蒙混过关。
func TestScoreInterview_轮次必须全覆盖(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodReport}} // 只有 1 轮

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns: []QAPair{
			{Seq: 1, Question: "q1", Answer: "a1"},
			{Seq: 2, Question: "q2", Answer: "a2"},
		},
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("错误信息应说明期望轮数: %v", err)
	}
}

func TestScoreInterview_未知维度被拒(t *testing.T) {
	out := strings.Replace(goodReport, `"dimension":"depth","score":5`, `"dimension":"luck","score":5`, 1)
	fc := &fakeCompleter{contents: []string{out}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
}

func TestScoreInterview_非法输出后重试成功(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"好的，我来评分", goodReport}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if r.Total != 80 {
		t.Errorf("Total = %d", r.Total)
	}
	if fc.calls != 2 {
		t.Errorf("调用次数 = %d，期望 2", fc.calls)
	}
}

func TestScoreInterview_重试到上限仍失败(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"始终不是 JSON"}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
	if fc.calls != maxScoreAttempts {
		t.Errorf("调用次数 = %d，期望 %d", fc.calls, maxScoreAttempts)
	}
}

func TestScoreInterview_模型错误不重试(t *testing.T) {
	sentinel := errors.New("超时")
	fc := &fakeCompleter{err: sentinel}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 1,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("期望包装原始错误，实际: %v", err)
	}
	if fc.calls != 1 {
		t.Errorf("不该重试，实际调用 %d 次", fc.calls)
	}
}

func TestScoreInterview_没有问答直接报错(t *testing.T) {
	fc := &fakeCompleter{contents: []string{goodReport}}

	if _, err := ScoreInterview(context.Background(), fc, ScoreRequest{}); err == nil {
		t.Fatal("没有问答应报错")
	}
	if fc.calls != 0 {
		t.Errorf("不该调模型，实际调用 %d 次", fc.calls)
	}
}

func TestComputeTotal_边界(t *testing.T) {
	cases := []struct {
		name   string
		scores []int
		want   int
	}{
		{"全 5 分", []int{5, 5, 5}, 100},
		{"全 1 分", []int{1, 1, 1}, 20},
		{"4/3/5", []int{4, 3, 5}, 80},
		{"3/3/4", []int{3, 3, 4}, 67}, // 3.333/5*100 = 66.67 → 67
		{"2/2/3", []int{2, 2, 3}, 47}, // 2.333/5*100 = 46.67 → 47
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dims := make([]Score, len(c.scores))
			for i, s := range c.scores {
				dims[i] = Score{Dimension: Dimensions[i], Score: s}
			}
			if got := computeTotal(dims); got != c.want {
				t.Errorf("computeTotal(%v) = %d，期望 %d", c.scores, got, c.want)
			}
		})
	}
}

func TestLevelOf_阈值(t *testing.T) {
	cases := map[int]string{
		100: "优秀", 85: "优秀", 84: "良好",
		70: "良好", 69: "一般", 55: "一般",
		54: "待提升", 0: "待提升",
	}
	for total, want := range cases {
		if got := levelOf(total); got != want {
			t.Errorf("levelOf(%d) = %q，期望 %q", total, got, want)
		}
	}
}

func TestBuildScorePrompt_截断超长回答(t *testing.T) {
	long := strings.Repeat("字", 5000)
	prompt := buildScorePrompt(ScoreRequest{
		JDText: long,
		Turns:  []QAPair{{Seq: 1, Question: long, Answer: long}},
	})
	if len([]rune(prompt)) > 2500 {
		t.Errorf("prompt 太长（%d 字），应该截断", len([]rune(prompt)))
	}
}

// reportWith 造一份指定维度分的报告 JSON。
func reportWith(acc, cla, dep int) string {
	return `{"dimensions":[
	  {"dimension":"accuracy","score":` + itoa(acc) + `,"comment":"准确 ` + itoa(acc) + `"},
	  {"dimension":"clarity","score":` + itoa(cla) + `,"comment":"清晰 ` + itoa(cla) + `"},
	  {"dimension":"depth","score":` + itoa(dep) + `,"comment":"深度 ` + itoa(dep) + `"}
	],"turn_scores":[{"seq":1,"scores":[
	  {"dimension":"accuracy","score":` + itoa(acc) + `,"comment":"a"},
	  {"dimension":"clarity","score":` + itoa(cla) + `,"comment":"c"},
	  {"dimension":"depth","score":` + itoa(dep) + `,"comment":"d"}
	]}],"strengths":["优点A"],"weaknesses":["不足A"],"suggestions":["建议A"]}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestScoreInterview_多次采样取中位数 是「评分可以跑多次」的核心：
// 三次给 3/5/4 分，中位数应该是 4，不是平均的 4（巧合）也不是极端值。
func TestScoreInterview_多次采样取中位数(t *testing.T) {
	fc := &fakeCompleter{contents: []string{
		reportWith(3, 3, 3),
		reportWith(5, 5, 5),
		reportWith(4, 4, 4),
	}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 3,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	if fc.calls != 3 {
		t.Errorf("调用次数 = %d，期望 3", fc.calls)
	}
	if r.SampleCount != 3 {
		t.Errorf("SampleCount = %d，期望 3", r.SampleCount)
	}
	for _, d := range r.Dimensions {
		if d.Score != 4 {
			t.Errorf("%s = %d，期望中位数 4", d.Dimension, d.Score)
		}
	}
	if r.Total != 80 {
		t.Errorf("Total = %d，期望 80", r.Total)
	}
}

// TestScoreInterview_分差大标记为不稳定 是「一致性」的度量：
// 三次给 2/4/5，差 3 分，必须标出来这个维度的分不可信。
func TestScoreInterview_分差大标记为不稳定(t *testing.T) {
	fc := &fakeCompleter{contents: []string{
		reportWith(2, 3, 3),
		reportWith(4, 3, 3),
		reportWith(5, 3, 3),
	}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 3,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	if r.Consistent {
		t.Error("accuracy 分差 3 分，应标记为不一致")
	}
	if len(r.UnstableDims) != 1 || r.UnstableDims[0] != DimAccuracy {
		t.Errorf("UnstableDims = %v，期望 [accuracy]", r.UnstableDims)
	}
}

func TestScoreInterview_分差不超过1算稳定(t *testing.T) {
	fc := &fakeCompleter{contents: []string{
		reportWith(4, 3, 3),
		reportWith(5, 3, 3),
		reportWith(4, 4, 3),
	}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 3,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if !r.Consistent {
		t.Errorf("分差都 ≤1，应算稳定，实际不稳定维度: %v", r.UnstableDims)
	}
}

// TestScoreInterview_部分采样失败仍出结果 是降级断言：
// 采样中途挂掉时，用已经拿到的结果出报告，不让用户重跑整场面试。
//
// 采样改成并发后，「哪一次采样失败」取决于调度，没法断言。
// 这里让第 1 次调用失败（Chat 层错误不重试，网络重试在 llm 客户端内部做），
// 只断言与调度无关的性质：
//   - 整体不失败（还有成功的采样）
//   - 成功数 + 失败数 = 采样数（一次都不能丢）
func TestScoreInterview_部分采样失败仍出结果(t *testing.T) {
	fc := &fakeCompleter{
		contents:  []string{reportWith(4, 4, 4)},
		failFirst: 1,
	}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 2,
	})
	if err != nil {
		t.Fatalf("部分失败不该整体失败: %v", err)
	}
	if r.SampleCount < 1 {
		t.Fatalf("SampleCount = %d，至少要有一次成功", r.SampleCount)
	}
	if r.SampleCount+r.SampleFailed != 2 {
		t.Errorf("成功 %d + 失败 %d ≠ 2 次采样，有采样被静默丢了",
			r.SampleCount, r.SampleFailed)
	}
	if r.Total != 80 {
		t.Errorf("Total = %d，期望 80（成功采样的分数不能受影响）", r.Total)
	}
}

func TestScoreInterview_全部采样失败才报错(t *testing.T) {
	fc := &fakeCompleter{contents: []string{"始终不是 JSON"}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 2,
	})
	if !errors.Is(err, ErrBadReport) {
		t.Fatalf("期望 ErrBadReport，实际: %v", err)
	}
}

func TestScoreInterview_采样次数上限(t *testing.T) {
	fc := &fakeCompleter{contents: []string{reportWith(4, 4, 4)}}

	_, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns:   []QAPair{{Seq: 1, Question: "q", Answer: "a"}},
		Samples: 99, // 超过上限
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}
	if fc.calls != MaxSamples {
		t.Errorf("调用次数 = %d，期望被压到上限 %d", fc.calls, MaxSamples)
	}
}

// followupReport 覆盖 4 轮：1 道普通题 + 1 道归因追问 + 2 道边界追问。
//
// 分数是设计过的，专门用来验证「分母是轮次×3」而不是「轮次数」：
//   边界类：(2+2+2 + 3+3+3) / 6 = 2.5      ← 正确
//   如果分母错成轮次数：(6+9) / 2 = 7.5   ← 会溢出 5 分，一眼能看出错
const followupReport = `{
  "dimensions": [
    {"dimension":"accuracy","score":3,"comment":"大体正确"},
    {"dimension":"clarity","score":3,"comment":"表达尚可"},
    {"dimension":"depth","score":3,"comment":"深度一般"}
  ],
  "turn_scores": [
    {"seq":1,"scores":[
      {"dimension":"accuracy","score":5,"comment":"普通题答得好"},
      {"dimension":"clarity","score":5,"comment":"条理清楚"},
      {"dimension":"depth","score":5,"comment":"有原理"}
    ]},
    {"seq":2,"scores":[
      {"dimension":"accuracy","score":4,"comment":"归因题答得不错"},
      {"dimension":"clarity","score":4,"comment":"条理清楚"},
      {"dimension":"depth","score":4,"comment":"有深度"}
    ]},
    {"seq":3,"scores":[
      {"dimension":"accuracy","score":2,"comment":"边界题没想过"},
      {"dimension":"clarity","score":2,"comment":"说得很乱"},
      {"dimension":"depth","score":2,"comment":"没讲边界"}
    ]},
    {"seq":4,"scores":[
      {"dimension":"accuracy","score":3,"comment":"边界题勉强"},
      {"dimension":"clarity","score":3,"comment":"还算清楚"},
      {"dimension":"depth","score":3,"comment":"浅尝辄止"}
    ]}
  ],
  "strengths": ["普通题答得完整"],
  "weaknesses": ["边界类追问明显吃力"],
  "suggestions": ["准备量级变化时的方案"]
}`

func TestScoreInterview_追问归因按最弱排序(t *testing.T) {
	fc := &fakeCompleter{contents: []string{followupReport}}

	r, err := ScoreInterview(context.Background(), fc, ScoreRequest{
		Turns: []QAPair{
			{Seq: 1, Question: "怎么防超卖", Answer: "唯一索引", Kind: ""},
			{Seq: 2, Question: "为什么唯一索引能防住", Answer: "因为约束下沉", Kind: FollowupCause},
			{Seq: 3, Question: "并发涨十倍还成立吗", Answer: "应该还行", Kind: FollowupBoundary},
			{Seq: 4, Question: "数据量涨十倍呢", Answer: "加索引", Kind: FollowupBoundary},
		},
		Samples: 1,
	})
	if err != nil {
		t.Fatalf("评分失败: %v", err)
	}

	if len(r.FollowupStats) != 2 {
		t.Fatalf("追问归因应有 2 类，实际 %d 类: %+v", len(r.FollowupStats), r.FollowupStats)
	}

	// 最弱的排第一：边界 2.5 分 < 归因 4.0 分
	first := r.FollowupStats[0]
	if first.Kind != FollowupBoundary {
		t.Errorf("第一名应是 boundary，实际 %q", first.Kind)
	}
	if first.Label != "边界" {
		t.Errorf("边界类的中文标签 = %q，期望 边界", first.Label)
	}
	if first.Count != 2 {
		t.Errorf("边界类轮次 = %d，期望 2", first.Count)
	}
	// 分母必须是 6（2 轮 × 3 维度），写成轮次数会得到 7.5
	if first.AvgScore != 2.5 {
		t.Errorf("边界类平均分 = %v，期望 2.5（分母是 轮次×3）", first.AvgScore)
	}

	second := r.FollowupStats[1]
	if second.Kind != FollowupCause {
		t.Errorf("第二名应是 cause，实际 %q", second.Kind)
	}
	if second.AvgScore != 4.0 {
		t.Errorf("归因类平均分 = %v，期望 4.0", second.AvgScore)
	}
}

func TestFollowupStats_普通题不参与归因(t *testing.T) {
	// 第 1 题是普通题（Kind 为空），分数 5 分；
	// 如果它被算进去，边界类平均分会从 2.0 被拉到 3.5
	pairs := []QAPair{
		{Seq: 1, Kind: ""},
		{Seq: 2, Kind: FollowupBoundary},
	}
	scores := []TurnScore{
		{Seq: 1, Scores: []Score{{DimAccuracy, 5, "c"}, {DimClarity, 5, "c"}, {DimDepth, 5, "c"}}},
		{Seq: 2, Scores: []Score{{DimAccuracy, 2, "c"}, {DimClarity, 2, "c"}, {DimDepth, 2, "c"}}},
	}

	got := followupStats(pairs, scores)
	if len(got) != 1 {
		t.Fatalf("只应有 1 类追问，实际 %d 类: %+v", len(got), got)
	}
	if got[0].AvgScore != 2.0 {
		t.Errorf("边界类平均分 = %v，期望 2.0（普通题不能混进来）", got[0].AvgScore)
	}
}

func TestFollowupStats_没有追问返回nil(t *testing.T) {
	pairs := []QAPair{{Seq: 1, Kind: ""}, {Seq: 2, Kind: ""}}
	scores := []TurnScore{
		{Seq: 1, Scores: []Score{{DimAccuracy, 4, "c"}}},
		{Seq: 2, Scores: []Score{{DimAccuracy, 4, "c"}}},
	}

	if got := followupStats(pairs, scores); got != nil {
		t.Errorf("没有追问时应返回 nil，实际 %+v", got)
	}
}
