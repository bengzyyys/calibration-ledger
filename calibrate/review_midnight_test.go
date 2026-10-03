package calibrate

import (
	"testing"
	"time"
)

// stepClock 按调用次序返回预设时间；用尽后停留在最后一个时间。
// 用来模拟一次核对过程中跨过午夜：第一次读取仍在当天，第二次读取已是次日。
type stepClock struct {
	calls int
	times []time.Time
}

func (c *stepClock) now() time.Time {
	i := c.calls
	if i >= len(c.times) {
		i = len(c.times) - 1
	}
	c.calls++
	return c.times[i]
}

// findHistoryCert 返回核对历史中指定编号的证书视图。
func findHistoryCert(t *testing.T, r *InstrumentReview, number string) CertificateView {
	t.Helper()
	for _, c := range r.History {
		if c.Number == number {
			return c
		}
	}
	t.Fatalf("历史证书中找不到 %s", number)
	return CertificateView{}
}

// TestReviewUsesSingleDateAcrossMidnight 验证整份核对只采用核对开始时的
// 一次本机日历日期：在用器具最近证书合格、截止日 2026-10-04，未结束计划的
// 当前计划日为 2026-10-03。核对于 10-03 结束前开始、10-04 才返回时，仍应
// 显示可用、两处证书均未到期、计划“今天需校准”。
func TestReviewUsesSingleDateAcrossMidnight(t *testing.T) {
	day3 := mustDate(t, "2026-10-03")
	day4 := mustDate(t, "2026-10-04")
	clock := &fakeClock{t: day3}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2026-10-04", Method: "m", Error: 0.1, Summary: "s",
	})
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "送检",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	// 核对开始前换上跨午夜时钟：第一次读取为 10-03（核对开始），之后均为 10-04。
	midnight := &stepClock{times: []time.Time{day3, day4, day4, day4}}
	l.now = midnight.now
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !r.CanUse {
		t.Fatalf("跨午夜的核对仍按 10-03 判断，应可使用，得到 %v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Expired {
		t.Fatalf("最近证书按核对开始日 10-03 应未到期，得到 %+v", r.Latest)
	}
	if h := findHistoryCert(t, r, "C-1"); h.Expired {
		t.Fatal("历史列表中的同一证书必须与最近证书一致，按 10-03 判未到期")
	}
	if len(r.Plans) != 1 {
		t.Fatalf("应有一项计划，得到 %d", len(r.Plans))
	}
	if p := r.Plans[0]; !p.Open() || p.Marker != TodoToday {
		t.Fatalf("未结束计划应按 10-03 标记为 %q，得到 open=%v marker=%q",
			TodoToday, p.Open(), p.Marker)
	}
}

// TestReviewNextDayReevaluates 验证新一次核对重新采用当时的本机日期：
// 同样的数据在 10-04 重新核对时，应不可用并列出到期原因，两处证书均已到期，
// 计划标记为“逾期”；无需重新打开台账。
func TestReviewNextDayReevaluates(t *testing.T) {
	day3 := mustDate(t, "2026-10-03")
	clock := &fakeClock{t: day3}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2026-10-04", Method: "m", Error: 0.1, Summary: "s",
	})
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "送检",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	r3, _ := l.Review("M-1")
	if !r3.CanUse || len(r3.Plans) != 1 || r3.Plans[0].Marker != TodoToday {
		t.Fatalf("10-03 核对应可用且计划今天需校准：canUse=%v marker=%v",
			r3.CanUse, r3.Plans)
	}

	// 同一台账对象，时钟拨到次日，无需重新打开文件。
	clock.t = mustDate(t, "2026-10-04")
	r4, _ := l.Review("M-1")
	if r4.CanUse {
		t.Fatalf("10-04（截止日当天）应不可用，reasons=%v", r4.Reasons)
	}
	if !containsReason(r4.Reasons, "到期") {
		t.Fatalf("应列出最近证书到期原因，得到 %v", r4.Reasons)
	}
	if r4.Latest == nil || !r4.Latest.Expired {
		t.Fatalf("最近证书 10-04 应已到期，得到 %+v", r4.Latest)
	}
	if h := findHistoryCert(t, r4, "C-1"); !h.Expired {
		t.Fatal("历史列表中的同一证书 10-04 也应已到期")
	}
	if len(r4.Plans) != 1 {
		t.Fatalf("应有一项计划，得到 %d", len(r4.Plans))
	}
	if p := r4.Plans[0]; !p.Open() || p.Marker != TodoOverdue {
		t.Fatalf("未结束计划 10-04 应标记为 %q，得到 marker=%q", TodoOverdue, p.Marker)
	}
}

// TestReviewFinishedPlansKeepStateAcrossDateChange 验证已结束计划不参与
// 日期标记，改期与完成等历史信息原样保留。
func TestReviewFinishedPlansKeepStateAcrossDateChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-03")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-03",
		Expiry: "2027-10-03", Method: "m", Error: 0.1, Summary: "s",
	})
	p := mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "送检",
	})
	if _, _, err := l.CompletePlan(p.Number, "C-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	clock.t = mustDate(t, "2026-12-01")
	r, _ := l.Review("M-1")
	if len(r.Plans) != 1 {
		t.Fatalf("应保留一项已结束计划，得到 %d", len(r.Plans))
	}
	got := r.Plans[0]
	if got.Open() || got.Status != PlanStatusDone {
		t.Fatalf("计划应仍为已完成，得到 %+v", got.Plan)
	}
	if got.Marker != "" {
		t.Fatalf("已结束计划不应带日期标记，得到 %q", got.Marker)
	}
	if got.CertificateNumber != "C-1" || got.CompletedAt == "" {
		t.Fatalf("已结束计划的历史信息应保留：%+v", got.Plan)
	}
}
