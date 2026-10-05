package calibrate

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// seedPlans 直接向台账写入带不同时区偏移的计划并保存，模拟同一台机器在
// 不同系统时区下先后留下的计划历史。
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

func storedPlanNumbers(l *Ledger) []string {
	got := make([]string, len(l.data.Plans))
	for i, p := range l.data.Plans {
		got[i] = p.Number
	}
	return got
}

// TestPlansOrderByActualInstantAcrossOffsets 验证同一件器具的计划按建立的
// 实际时刻排序，不能按时间文字中的日期、小时排序：
//   - PL-B 2026-10-06T09:30:00+08:00（01:30Z）实际早于
//     PL-A 2026-10-06T02:00:00Z，虽然后者文字小时更小；
//   - PL-D 2026-10-06T00:15:00+08:00（2026-10-05T16:15Z）早于
//     PL-C 2026-10-05T18:00:00Z，不能因后者文字日期更小就排在前面。
//
// 已完成、已取消与未完成计划一起按建立时刻排列，不先按状态分组。
func TestPlansOrderByActualInstantAcrossOffsets(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	// 按错误的字符串/入库顺序写入：实际从早到晚应为 D、C、B、A。
	seedPlans(t, l,
		Plan{ // 02:00Z，最晚
			Number: "PL-A", InstrumentID: "M-1",
			PlannedDate: "2026-11-01", OriginalDate: "2026-11-01", Note: "a",
			CreatedAt: "2026-10-06T02:00:00Z", Status: PlanStatusCanceled,
			CanceledAt: "2026-10-06T03:00:00Z", CancelReason: "取消a",
		},
		Plan{ // 18:00Z，第二早（跨日例中的较晚者）
			Number: "PL-C", InstrumentID: "M-1",
			PlannedDate: "2026-11-03", OriginalDate: "2026-11-03", Note: "c",
			CreatedAt: "2026-10-05T18:00:00Z", Status: PlanStatusCanceled,
			CanceledAt: "2026-10-06T10:00:00+08:00", CancelReason: "取消c",
		},
		Plan{ // 01:30Z，第三早：文字小时 09 更大但实际早于 PL-A
			Number: "PL-B", InstrumentID: "M-1",
			PlannedDate: "2026-11-05", OriginalDate: "2026-11-02", Note: "b",
			CreatedAt: "2026-10-06T09:30:00+08:00", Status: PlanStatusDone,
			Changes: []PlanChange{{
				From: "2026-11-02", To: "2026-11-05",
				ChangedAt: "2026-10-07T09:00:00+08:00", Reason: "排期冲突",
			}},
			CompletedAt: "2026-11-05T10:00:00+08:00", CertificateNumber: "C-1",
		},
		Plan{ // 2026-10-05T16:15Z，最早：文字日期更晚但实际更早
			Number: "PL-D", InstrumentID: "M-1",
			PlannedDate: "2026-11-06", OriginalDate: "2026-11-06", Note: "d",
			CreatedAt: "2026-10-06T00:15:00+08:00", Status: PlanStatusOpen,
		},
	)

	want := []string{"PL-D", "PL-C", "PL-B", "PL-A"}
	views, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if got := planNumbers(views); !reflect.DeepEqual(got, want) {
		t.Fatalf("应按建立实际时刻从早到晚排列，得到 %v，期望 %v", got, want)
	}

	// review 与公开 Plans 入口的同一器具计划顺序一致。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got := planNumbers(r.Plans); !reflect.DeepEqual(got, want) {
		t.Fatalf("核对结果顺序应与 Plans 一致，得到 %v，期望 %v", got, want)
	}

	// 原建立时间文字与偏移、计划日期、改期历史、取消信息及关联证书全部保留。
	byNumber := map[string]PlanView{}
	for _, v := range views {
		byNumber[v.Number] = v
	}
	if byNumber["PL-A"].CreatedAt != "2026-10-06T02:00:00Z" ||
		byNumber["PL-A"].CancelReason != "取消a" {
		t.Fatalf("取消计划的建立时间文字或取消信息被改动：%+v", byNumber["PL-A"])
	}
	if byNumber["PL-B"].CreatedAt != "2026-10-06T09:30:00+08:00" ||
		byNumber["PL-B"].CertificateNumber != "C-1" ||
		len(byNumber["PL-B"].Changes) != 1 ||
		byNumber["PL-B"].Changes[0].ChangedAt != "2026-10-07T09:00:00+08:00" {
		t.Fatalf("完成计划的偏移文字、改期历史或关联证书被改动：%+v", byNumber["PL-B"])
	}
	if byNumber["PL-D"].CreatedAt != "2026-10-06T00:15:00+08:00" ||
		byNumber["PL-D"].Marker == "" {
		t.Fatalf("未结束计划的偏移文字或日期标记异常：%+v", byNumber["PL-D"])
	}

	// 查询只调整展示顺序，不重排或写回台账中的存储记录。
	if got := storedPlanNumbers(l); !reflect.DeepEqual(got, []string{"PL-A", "PL-C", "PL-B", "PL-D"}) {
		t.Fatalf("查询不应重排台账内存储：%v", got)
	}

	// 查询当天的系统时区变化不能改变历史先后。
	for _, z := range []*time.Location{
		time.UTC,
		time.FixedZone("UTC+05:30", 5*3600+1800),
		time.FixedZone("UTC-07:00", -7*3600),
	} {
		loc := z
		opened, err := openAt(path, func() time.Time { return clock.t.In(loc) })
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		gotViews, err := opened.Plans("M-1")
		if err != nil {
			t.Fatalf("Plans after reopen: %v", err)
		}
		if got := planNumbers(gotViews); !reflect.DeepEqual(got, want) {
			t.Fatalf("在时区 %v 查询改变了历史顺序：%v", loc, got)
		}
		gotReview, err := opened.Review("M-1")
		if err != nil {
			t.Fatalf("Review after reopen: %v", err)
		}
		if got := planNumbers(gotReview.Plans); !reflect.DeepEqual(got, want) {
			t.Fatalf("在时区 %v 查询改变了核对顺序：%v", loc, got)
		}
	}

	// 器具没有计划时返回空结果；未知器具明确报告不存在；查询不产生使用记录。
	empty, err := l.Plans("M-2")
	if err != nil || len(empty) != 0 {
		t.Fatalf("无计划器具应返回空结果，plans=%+v err=%v", empty, err)
	}
	emptyReview, err := l.Review("M-2")
	if err != nil || len(emptyReview.Plans) != 0 {
		t.Fatalf("无计划器具核对应无计划，plans=%+v err=%v", emptyReview, err)
	}
	if _, err := l.Plans("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知器具应报 ErrNotFound，得到 %v", err)
	}
	if _, err := l.Review("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("核对未知器具应报 ErrNotFound，得到 %v", err)
	}
	if len(l.data.Usage) != 0 {
		t.Fatalf("计划查询不应产生使用记录，得到 %d 条", len(l.data.Usage))
	}
}

// TestPlansSameInstantKeepsLedgerOrder 验证多项计划建立时间表示同一实际时刻
// 时（即使分别使用 Z、+00:00 或 +08:00），保留它们在台账中的原有次序，
// 每项仍单独显示、不合并，时间文字形式不产生额外优先级。
func TestPlansSameInstantKeepsLedgerOrder(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	seed := func(t *testing.T, numbers []string, created []string) *Ledger {
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		plans := make([]Plan, len(numbers))
		for i := range numbers {
			plans[i] = Plan{
				Number: numbers[i], InstrumentID: "M-1",
				PlannedDate: "2026-11-01", OriginalDate: "2026-11-01", Note: "n",
				CreatedAt: created[i], Status: PlanStatusOpen,
			}
		}
		seedPlans(t, l, plans...)
		return l
	}

	l := seed(t,
		[]string{"PL-E", "PL-F", "PL-G"},
		[]string{
			"2026-10-06T10:00:00Z",
			"2026-10-06T10:00:00+00:00",
			"2026-10-06T18:00:00+08:00",
		},
	)
	views, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	want := []string{"PL-E", "PL-F", "PL-G"}
	if got := planNumbers(views); !reflect.DeepEqual(got, want) {
		t.Fatalf("同一实际时刻应保留台账原次序，得到 %v，期望 %v", got, want)
	}
	if len(views) != 3 {
		t.Fatalf("同一时刻的多项计划必须每项单独显示、不合并，得到 %d 项", len(views))
	}

	// 以相反顺序入库时稳定排序应保留相反次序。
	l2 := seed(t,
		[]string{"PL-G", "PL-F", "PL-E"},
		[]string{
			"2026-10-06T18:00:00+08:00",
			"2026-10-06T10:00:00+00:00",
			"2026-10-06T10:00:00Z",
		},
	)
	views2, err := l2.Plans("M-1")
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	want2 := []string{"PL-G", "PL-F", "PL-E"}
	if got := planNumbers(views2); !reflect.DeepEqual(got, want2) {
		t.Fatalf("同刻计划应保留相反的入库原次序，文字形式不得加优先级，得到 %v，期望 %v", got, want2)
	}
}

// TestPlanCreatedLocalDateStillGovernsCertCompletion 验证排序修正不放宽证书
// 日期条件：完成计划时建立日期仍按该计划保存的本机日历日期理解。
// 2026-10-06T00:30:00+08:00 的本机日历日期是 2026-10-06（即使 UTC 还是
// 10-05），校准日期 2026-10-05 的证书仍必须被拒绝，10-06 的可以完成。
func TestPlanCreatedLocalDateStillGovernsCertCompletion(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-06")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	seedPlans(t, l, Plan{
		Number: "PL-1", InstrumentID: "M-1",
		PlannedDate: "2026-10-20", OriginalDate: "2026-10-20", Note: "n",
		CreatedAt: "2026-10-06T00:30:00+08:00", Status: PlanStatusOpen,
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-10-05",
		Expiry: "2027-10-05", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("PL-1", "C-OLD"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("校准日期早于计划保存的本机建立日期应拒绝，得到 %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-10-06",
		Expiry: "2027-10-06", Method: "m", Error: 0.1, Summary: "s",
	})
	done, _, err := l.CompletePlan("PL-1", "C-NEW")
	if err != nil {
		t.Fatalf("校准日期等于建立的本机日期应能完成：%v", err)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-NEW" {
		t.Fatalf("完成结果异常：%+v", done)
	}
}
