package calibrate

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// seedInstrument 直接向台账写入一件器具并保存，供计划排序测试使用。
func seedInstrument(t *testing.T, l *Ledger, id string) {
	t.Helper()
	l.data.Instruments = append(l.data.Instruments, Instrument{
		ID:           id,
		Name:         "万用表-" + id,
		AllowedError: 0.5,
		Status:       StatusInUse,
		RegisteredAt: "2026-10-01T09:00:00+08:00",
	})
	if err := l.save(); err != nil {
		t.Fatalf("保存测试器具失败: %v", err)
	}
}

// seedPlans 直接向台账写入带不同时区偏移建立时间的计划并保存，
// 模拟同一台机器在不同系统时区下先后为同一器具建立的历史。
func seedPlans(t *testing.T, l *Ledger, plans ...Plan) {
	t.Helper()
	l.data.Plans = append(l.data.Plans, plans...)
	if err := l.save(); err != nil {
		t.Fatalf("保存测试计划失败: %v", err)
	}
}

func planNumbers(plans []PlanView) []string {
	got := make([]string, len(plans))
	for i, p := range plans {
		got[i] = p.Number
	}
	return got
}

func storedPlanNumbers(plans []Plan) []string {
	got := make([]string, len(plans))
	for i, p := range plans {
		got[i] = p.Number
	}
	return got
}

// testPlan 构造一项计划，默认带计划日期与说明，状态由调用方给定。
func testPlan(number, id, createdAt, status string) Plan {
	return Plan{
		Number:       number,
		InstrumentID: id,
		PlannedDate:  "2026-10-20",
		OriginalDate: "2026-10-20",
		Note:         "计划-" + number,
		CreatedAt:    createdAt,
		Status:       status,
	}
}

// TestPlansOrderByActualInstantAcrossOffsets 验证不同时区偏移下按建立的实际
// 时刻排序：2026-10-06T09:30:00+08:00（01:30Z）实际早半小时，应排在
// 2026-10-06T02:00:00Z 前面，即使按字符串比较 "+08:00" 的文字更"大"。
// 入库时故意把实际较晚的计划写在前面，排序不能沿用入库或文字顺序。
func TestPlansOrderByActualInstantAcrossOffsets(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedInstrument(t, l, "M-1")
	seedPlans(t, l,
		testPlan("PL-LATE", "M-1", "2026-10-06T02:00:00Z", PlanStatusOpen),
		testPlan("PL-EARLY", "M-1", "2026-10-06T09:30:00+08:00", PlanStatusDone),
	)

	want := []string{"PL-EARLY", "PL-LATE"}
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("应按建立实际时刻从早到晚排列，得到 %v，期望 %v", got, want)
	}

	// 公开入口与按器具核对给出的顺序必须一致。
	review, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := planNumbers(review.Plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("核对中的计划顺序应与按器具查询一致，得到 %v，期望 %v", got, want)
	}

	// 建立时间原文字与偏移保持保存时的内容。
	if plans[0].CreatedAt != "2026-10-06T09:30:00+08:00" ||
		plans[1].CreatedAt != "2026-10-06T02:00:00Z" {
		t.Fatalf("建立时间文字与偏移被改动：%+v", plans)
	}

	// 查询只重排返回的独立副本，不回写台账：台账内部仍按保存顺序保存。
	if got := storedPlanNumbers(l.data.Plans); !reflect.DeepEqual(got,
		[]string{"PL-LATE", "PL-EARLY"}) {
		t.Fatalf("查询不应重排台账内存储：%v", got)
	}
	// 重新打开同一文件后，先后仍按各自保存的实际时刻确定。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	plans2, err := reopened.Plans("M-1")
	if err != nil {
		t.Fatalf("reopened Plans: %v", err)
	}
	if got := planNumbers(plans2); !reflect.DeepEqual(got, want) {
		t.Fatalf("重开后排序应仍按实际时刻：%v", got)
	}
}

// TestPlansOrderAcrossMidnightOffsets 验证跨日记录同样按实际时刻：
// 2026-10-06T00:15:00+08:00（2026-10-05T16:15Z）早于
// 2026-10-05T18:00:00Z，不能因为后者文字日期更小就把它放在前面。
func TestPlansOrderAcrossMidnightOffsets(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	seedPlans(t, l,
		// 文字日期更小但实际更晚的先入库。
		testPlan("PL-LATE", "M-1", "2026-10-05T18:00:00Z", PlanStatusCanceled),
		testPlan("PL-EARLY", "M-1", "2026-10-06T00:15:00+08:00", PlanStatusOpen),
	)
	want := []string{"PL-EARLY", "PL-LATE"}
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("跨日计划应按实际时刻排列，得到 %v，期望 %v", got, want)
	}
}

// TestPlansOrderNotGroupedByStatus 验证已完成、已取消与未完成计划一起按
// 建立时刻排列，不先按状态分组，也不按计划日期或完成时间排序。
func TestPlansOrderNotGroupedByStatus(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	earliest := testPlan("PL-DONE", "M-1", "2026-10-06T01:00:00Z", PlanStatusDone)
	earliest.CompletedAt = "2026-10-09T10:00:00Z"
	earliest.CertificateNumber = "C-1"
	middle := testPlan("PL-CANCEL", "M-1", "2026-10-06T02:00:00Z", PlanStatusCanceled)
	middle.CanceledAt = "2026-10-07T10:00:00Z"
	middle.CancelReason = "暂停送检"
	latest := testPlan("PL-OPEN", "M-1", "2026-10-06T03:00:00Z", PlanStatusOpen)
	// 入库顺序故意打乱；当前计划日期、改期与完成时间也不参与排序。
	latest.PlannedDate = "2026-10-10"
	seedPlans(t, l, latest, earliest, middle)

	want := []string{"PL-DONE", "PL-CANCEL", "PL-OPEN"}
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("不同状态计划应一起按建立时刻排列，得到 %v，期望 %v", got, want)
	}
	// 取消信息、关联证书与计划日期仍保留在各自记录上。
	byNumber := map[string]PlanView{}
	for _, p := range plans {
		byNumber[p.Number] = p
	}
	if byNumber["PL-DONE"].CertificateNumber != "C-1" ||
		byNumber["PL-DONE"].CompletedAt != "2026-10-09T10:00:00Z" {
		t.Fatalf("完成计划的关联证书或完成时间丢失：%+v", byNumber["PL-DONE"])
	}
	if byNumber["PL-CANCEL"].CancelReason != "暂停送检" ||
		byNumber["PL-CANCEL"].CanceledAt != "2026-10-07T10:00:00Z" {
		t.Fatalf("取消信息丢失：%+v", byNumber["PL-CANCEL"])
	}
	if byNumber["PL-OPEN"].PlannedDate != "2026-10-10" {
		t.Fatalf("当前计划日期被改动：%+v", byNumber["PL-OPEN"])
	}
}

// TestPlansSameInstantKeepsLedgerOrder 验证建立时间表示同一实际时刻时，
// 即使分别使用 Z、+00:00 或其他偏移，也保留它们在台账中的原有次序，
// 每项仍单独显示、不合并。
func TestPlansSameInstantKeepsLedgerOrder(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	seedPlans(t, l,
		testPlan("PL-Z", "M-1", "2026-10-06T02:00:00Z", PlanStatusOpen),
		testPlan("PL-ZERO", "M-1", "2026-10-06T02:00:00+00:00", PlanStatusDone),
		testPlan("PL-EIGHT", "M-1", "2026-10-06T10:00:00+08:00", PlanStatusCanceled),
	)
	want := []string{"PL-Z", "PL-ZERO", "PL-EIGHT"}
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("同一实际时刻应保留台账原有次序，得到 %v，期望 %v", got, want)
	}
	if len(plans) != 3 {
		t.Fatalf("同一时刻的多项计划必须全部保留、不得合并，得到 %d 项", len(plans))
	}

	// 同刻记录以相反顺序入库时，稳定排序应保留相反的原次序，
	// 时间的文字形式不产生额外优先级。
	l2 := newTestLedger(t, clock)
	seedInstrument(t, l2, "M-1")
	seedPlans(t, l2,
		testPlan("PL-EIGHT", "M-1", "2026-10-06T10:00:00+08:00", PlanStatusOpen),
		testPlan("PL-ZERO", "M-1", "2026-10-06T02:00:00+00:00", PlanStatusDone),
		testPlan("PL-Z", "M-1", "2026-10-06T02:00:00Z", PlanStatusCanceled),
	)
	want2 := []string{"PL-EIGHT", "PL-ZERO", "PL-Z"}
	plans2, err := l2.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans2); !reflect.DeepEqual(got, want2) {
		t.Fatalf("同刻计划应保留相反的入库原次序，文字形式不得加优先级，得到 %v，期望 %v", got, want2)
	}
}

// TestPlansOrderingUnaffectedByCurrentZone 验证查询当天的系统时区变化不能
// 改变历史顺序：同一份文件分别用不同时区的"现在"打开，先后一致。
func TestPlansOrderingUnaffectedByCurrentZone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedInstrument(t, l, "M-1")
	seedPlans(t, l,
		testPlan("PL-LATE", "M-1", "2026-10-06T02:00:00Z", PlanStatusOpen),
		testPlan("PL-EARLY", "M-1", "2026-10-06T09:30:00+08:00", PlanStatusDone),
	)
	want := []string{"PL-EARLY", "PL-LATE"}
	zones := []struct {
		name string
		loc  *time.Location
	}{
		{"UTC", time.UTC},
		{"UTC+05:30", time.FixedZone("UTC+05:30", 5*3600+1800)},
		{"UTC-07:00", time.FixedZone("UTC-07:00", -7*3600)},
	}
	for _, z := range zones {
		loc := z.loc
		opened, err := openAt(path, func() time.Time { return clock.t.In(loc) })
		if err != nil {
			t.Fatalf("reopen in %s: %v", z.name, err)
		}
		plans, err := opened.Plans("M-1")
		if err != nil {
			t.Fatalf("Plans in %s: %v", z.name, err)
		}
		if got := planNumbers(plans); !reflect.DeepEqual(got, want) {
			t.Fatalf("在时区 %s 查询改变了历史顺序：%v", z.name, got)
		}
		review, err := opened.Review("M-1")
		if err != nil {
			t.Fatalf("Review in %s: %v", z.name, err)
		}
		if got := planNumbers(review.Plans); !reflect.DeepEqual(got, want) {
			t.Fatalf("在时区 %s 核对改变了历史顺序：%v", z.name, got)
		}
	}
}

// TestPlansEmptyAndUnknownInstrument 验证器具没有计划时返回空结果，
// 未知器具明确报告不存在。
func TestPlansEmptyAndUnknownInstrument(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("器具没有计划时应返回空结果，得到 %+v", plans)
	}
	if _, err := l.Plans("NOPE"); err == nil {
		t.Fatalf("未知器具应报告不存在")
	}
	if _, err := l.Review("NOPE"); err == nil {
		t.Fatalf("核对未知器具应报告不存在")
	}
}

// TestPlansOrderPreservesRescheduleHistory 验证排序只改展示顺序，
// 改期历史等记录内容原样保留。
func TestPlansOrderPreservesRescheduleHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	seedInstrument(t, l, "M-1")
	withChanges := testPlan("PL-EARLY", "M-1", "2026-10-06T09:30:00+08:00", PlanStatusOpen)
	withChanges.OriginalDate = "2026-10-20"
	withChanges.PlannedDate = "2026-11-05"
	withChanges.Changes = []PlanChange{{
		From: "2026-10-20", To: "2026-11-05",
		ChangedAt: "2026-10-07T09:00:00+08:00", Reason: "实验室排期冲突",
	}}
	seedPlans(t, l,
		testPlan("PL-LATE", "M-1", "2026-10-06T02:00:00Z", PlanStatusDone),
		withChanges,
	)
	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(plans); !reflect.DeepEqual(got, []string{"PL-EARLY", "PL-LATE"}) {
		t.Fatalf("排序错误：%v", got)
	}
	p0 := plans[0]
	if p0.OriginalDate != "2026-10-20" || p0.PlannedDate != "2026-11-05" ||
		len(p0.Changes) != 1 || p0.Changes[0].Reason != "实验室排期冲突" ||
		p0.Changes[0].ChangedAt != "2026-10-07T09:00:00+08:00" {
		t.Fatalf("改期历史或计划日期未被原样保留：%+v", p0)
	}
}
