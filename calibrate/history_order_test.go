package calibrate

import (
	"reflect"
	"testing"
)

// TestCompareHistoricalInstantSharedRule 直接锁定两处历史共用的时间先后
// 规则：实际时刻比较、同刻并列、可识别记录排在无法识别记录之前、无法识别
// 记录按原文字排列。
func TestCompareHistoricalInstantSharedRule(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		sign int // a 相对 b 的期望符号：-1 更早、0 并列、1 更晚
	}{
		{
			name: "带偏移实际更早则排在前",
			a:    "2026-10-06T09:30:00+08:00", // 01:30Z
			b:    "2026-10-06T02:00:00Z",      // 02:00Z
			sign: -1,
		},
		{"反向比较", "2026-10-06T02:00:00Z", "2026-10-06T09:30:00+08:00", 1},
		{"Z 与 +00:00 同一实际时刻并列", "2026-10-06T02:00:00Z", "2026-10-06T02:00:00+00:00", 0},
		{"其他偏移表示同一时刻并列", "2026-10-06T10:00:00+08:00", "2026-10-06T02:00:00Z", 0},
		{"可识别排在无法识别之前", "2026-10-06T02:00:00Z", "无法识别的时间", -1},
		{"无法识别排在可识别之后", "无法识别的时间", "2026-10-06T02:00:00Z", 1},
		{"两条无法识别按原文字升序", "坏时间-a", "坏时间-b", -1},
		{"两条无法识别原文字相同并列", "坏时间-同文", "坏时间-同文", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compareHistoricalInstant(tc.a, tc.b)
			sign := 0
			if got < 0 {
				sign = -1
			} else if got > 0 {
				sign = 1
			}
			if sign != tc.sign {
				t.Fatalf("compareHistoricalInstant(%q, %q) = %d，期望符号 %d", tc.a, tc.b, got, tc.sign)
			}
		})
	}
}

// TestUsageRecordsUnrecognizableTimesOrdering 验证全部使用申请记录中时间
// 无法识别的异常记录仍全部返回：可识别记录按实际时刻在前，无法识别记录排
// 在其后并按保存的时间原文字升序；原文字相同时沿用使用申请自身的同刻规则
// （先按器具编号，再由稳定排序保留台账原次序）。
func TestUsageRecordsUnrecognizableTimesOrdering(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	l := newTestLedger(t, clock)
	seedUsage(t, l,
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00+08:00"}, // 02:00Z
		UsageRecord{InstrumentID: "M-2", RequestedAt: "坏时间-b"},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T02:00:00Z"}, // 同刻、同器具
		UsageRecord{InstrumentID: "M-2", RequestedAt: "坏时间-a"},
		UsageRecord{InstrumentID: "M-0", RequestedAt: "2026-10-05T02:00:00+00:00"}, // 同刻、编号更小
		UsageRecord{InstrumentID: "M-1", RequestedAt: "坏时间-a"},                     // 同上一条坏时间原文、编号更小
	)
	recs := l.UsageRecords()
	want := [][2]string{
		// 可识别：同一实际时刻（均为 02:00Z）按器具编号升序；同器具同刻保留台账原次序。
		{"M-0", "2026-10-05T02:00:00+00:00"},
		{"M-1", "2026-10-05T10:00:00+08:00"},
		{"M-1", "2026-10-05T02:00:00Z"},
		// 无法识别：按原文字升序，同原文按器具编号升序。
		{"M-1", "坏时间-a"},
		{"M-2", "坏时间-a"},
		{"M-2", "坏时间-b"},
	}
	if got := usageKeys(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("无法识别时间应排在有效记录之后并按原文字、编号排列，得到 %v，期望 %v", got, want)
	}
	if len(recs) != 6 {
		t.Fatalf("无法识别的记录也必须全部返回，得到 %d 条", len(recs))
	}
}

// TestPlansUnrecognizableTimesOrdering 验证计划历史中建立时间无法识别的
// 异常记录仍全部返回：可识别计划按建立实际时刻在前，无法识别计划排在其后
// 并按原文字升序，原文字相同或建立时刻相同时直接保留台账原次序；按器具
// 查询与按器具核对的顺序一致。
func TestPlansUnrecognizableTimesOrdering(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	seedPlans(t, l,
		testPlan("P-LATE", "M-1", "2026-10-06T02:00:00Z", PlanStatusOpen),
		testPlan("P-BAD-B", "M-1", "坏建立时间-b", PlanStatusOpen),
		testPlan("P-EARLY", "M-1", "2026-10-06T09:30:00+08:00", PlanStatusDone),
		testPlan("P-BAD-A1", "M-1", "坏建立时间-a", PlanStatusCanceled),
		testPlan("P-SAME", "M-1", "2026-10-06T02:00:00+00:00", PlanStatusOpen),
		testPlan("P-BAD-A2", "M-1", "坏建立时间-a", PlanStatusOpen),
	)
	want := []string{"P-EARLY", "P-LATE", "P-SAME", "P-BAD-A1", "P-BAD-A2", "P-BAD-B"}
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("无法识别建立时间应排在有效计划之后并按原文字排列，得到 %v，期望 %v", got, want)
	}
	if len(plans) != 6 {
		t.Fatalf("无法识别建立时间的计划也必须全部返回，得到 %d 项", len(plans))
	}
	// 按器具核对中的计划顺序与按器具查询一致。
	review, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := planNumbers(review.Plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("核对中的异常时间计划顺序应与计划查询一致，得到 %v，期望 %v", got, want)
	}
	// 台账内部存储次序不被查询重排。
	if got := storedPlanNumbers(l.data.Plans); !reflect.DeepEqual(got,
		[]string{"P-LATE", "P-BAD-B", "P-EARLY", "P-BAD-A1", "P-SAME", "P-BAD-A2"}) {
		t.Fatalf("查询不应重排台账内存储：%v", got)
	}
}
