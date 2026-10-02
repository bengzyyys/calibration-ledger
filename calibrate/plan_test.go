package calibrate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustPlan(t *testing.T, l *Ledger, in PlanInput) *Plan {
	t.Helper()
	p, err := l.CreatePlan(in)
	if err != nil {
		t.Fatalf("create plan %s: %v", in.Number, err)
	}
	return p
}

func TestCreatePlanValidation(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	base := PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准"}
	valid := func(mut func(*PlanInput)) PlanInput {
		p := base
		mut(&p)
		return p
	}
	bad := []struct {
		name     string
		in       PlanInput
		notFound bool
	}{
		{"未知器具", valid(func(p *PlanInput) { p.InstrumentID = "X-9" }), true},
		{"空白计划编号", valid(func(p *PlanInput) { p.Number = "  " }), false},
		{"空白器具编号", valid(func(p *PlanInput) { p.InstrumentID = "\t" }), false},
		{"空白说明", valid(func(p *PlanInput) { p.Note = "" }), false},
		{"不存在的日期2月30日", valid(func(p *PlanInput) { p.Date = "2026-02-30" }), false},
		{"不存在的月份13月", valid(func(p *PlanInput) { p.Date = "2026-13-01" }), false},
		{"格式不符", valid(func(p *PlanInput) { p.Date = "2026-10-1" }), false},
		{"日期早于今天", valid(func(p *PlanInput) { p.Date = "2026-10-01" }), false},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			n := len(l.data.Plans)
			_, err := l.CreatePlan(tc.in)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if tc.notFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("未知器具应报 ErrNotFound，得到 %v", err)
				}
			} else if !IsValidation(err) {
				t.Fatalf("用例 %q 应报校验错误，得到 %v", tc.name, err)
			}
			if len(l.data.Plans) != n {
				t.Fatalf("用例 %q 留下了计划记录", tc.name)
			}
		})
	}

	// 计划日期等于本机今天允许。
	p := mustPlan(t, l, PlanInput{Number: "P-ok", InstrumentID: "M-1", Date: "2026-10-02", Note: "今天"})
	if p.OriginalDate != "2026-10-02" || p.Status != PlanStatusOpen {
		t.Fatalf("新计划字段异常：%+v", p)
	}

	// 每件器具最多一项未结束计划。
	if _, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-11-01", Note: "二"}); !IsValidation(err) {
		t.Fatalf("同器具第二项未结束计划应拒绝，得到 %v", err)
	}
	if l.findPlan("P-2") != nil {
		t.Fatal("被拒绝的计划不应存在")
	}
}

func TestPlanNumberUniqueAndNotReused(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	// 进行中编号不能被别的器具复用。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-10", Note: "占用"}); !IsValidation(err) {
		t.Fatalf("进行中的编号应拒绝复用，得到 %v", err)
	}

	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 编号即使对应已完成计划也不能复用（即使换器具）。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-10", Note: "复用"}); !IsValidation(err) {
		t.Fatalf("已结束计划编号应拒绝复用，得到 %v", err)
	}

	// 取消的计划编号同样不能复用；但原器具可以另建新计划。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-10-11", Note: "二"})
	if _, err := l.CancelPlan("P-2", "暂停送检"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-10-12", Note: "复用"}); !IsValidation(err) {
		t.Fatalf("已取消计划编号应拒绝复用，得到 %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-10-12", Note: "重新安排"})
}

func TestRescheduleRecordsHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	// 未知计划拒绝且不改动。
	if _, err := l.ReschedulePlan("NOPE", "2026-10-20", "原因"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知计划应报 ErrNotFound，得到 %v", err)
	}
	// 空白原因、过去日期、相同日期都拒绝。
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "  "); !IsValidation(err) {
		t.Fatalf("空白原因应拒绝，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-01", "提前"); !IsValidation(err) {
		t.Fatalf("早于操作当天的日期应拒绝，得到 %v", err)
	}
	if got := l.findPlan("P-1"); len(got.Changes) != 0 || got.PlannedDate != "2026-10-10" {
		t.Fatalf("失败的改期改动了记录：%+v", got)
	}

	// 合法改期：保留修改前后日期、操作时间和原因；最初日期不变。
	clock.t = mustDate(t, "2026-10-03")
	p2, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if p2.PlannedDate != "2026-10-20" || p2.OriginalDate != "2026-10-10" {
		t.Fatalf("改期后日期字段异常：%+v", p2)
	}
	if len(p2.Changes) != 1 || p2.Changes[0].From != "2026-10-10" ||
		p2.Changes[0].To != "2026-10-20" || p2.Changes[0].Reason != "实验室排期冲突" ||
		p2.Changes[0].ChangedAt == "" {
		t.Fatalf("改期记录不完整：%+v", p2.Changes)
	}
}

func TestEndedPlansCannotChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})

	if _, err := l.CancelPlan("P-1", "停产"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// 已取消：不能再改期、取消或重新开启。
	for _, act := range []func() error{
		func() error { _, e := l.ReschedulePlan("P-1", "2026-10-20", "x"); return e },
		func() error { _, e := l.CancelPlan("P-1", "x"); return e },
	} {
		if err := act(); !IsValidation(err) {
			t.Fatalf("已取消计划再操作应拒绝，得到 %v", err)
		}
	}
	c := l.findPlan("P-1")
	if c.Status != PlanStatusCanceled || c.CanceledAt == "" || c.CancelReason != "停产" {
		t.Fatalf("取消信息保存不完整：%+v", c)
	}

	// 已完成计划不能再改期、取消或换证书。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-11-10", Note: "二"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	done, _, err := l.CompletePlan("P-2", "C-1")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := l.ReschedulePlan("P-2", "2026-12-01", "x"); !IsValidation(err) {
		t.Fatalf("已完成计划改期应拒绝，得到 %v", err)
	}
	if _, err := l.CancelPlan("P-2", "x"); !IsValidation(err) {
		t.Fatalf("已完成计划取消应拒绝，得到 %v", err)
	}
	if l.findPlan("P-2").CompletedAt != done.CompletedAt {
		t.Fatal("拒绝后完成数据被改动")
	}
}

func TestRetiredInstrumentKeepsPlansActionable(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	// 停用器具不会自动取消计划，仍可查询和处理。
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("retire: %v", err)
	}
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" || items[0].Status != StatusRetired {
		t.Fatalf("停用器具的计划仍应出现在待办：%+v", items)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "停用前调整"); err != nil {
		t.Fatalf("停用器具的计划应仍可改期：%v", err)
	}
}

func TestCompletePlanRules(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})

	// 证书不存在。
	if _, _, err := l.CompletePlan("P-1", "C-NO"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的证书应报 ErrNotFound，得到 %v", err)
	}
	// 计划不存在。
	if _, _, err := l.CompletePlan("P-NO", "C-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的计划应报 ErrNotFound，得到 %v", err)
	}
	// 证书属于别的器具。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-OTHER", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-OTHER"); !IsValidation(err) {
		t.Fatalf("他器具证书应拒绝，得到 %v", err)
	}
	// 校准日期早于计划建立的本机日期。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-OLD"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("早于计划建立日的证书应拒绝，得到 %v", err)
	}
	if l.findPlan("P-1").Status != PlanStatusOpen {
		t.Fatal("失败的完成不应改动计划")
	}

	// 符合条件：校准日期等于建立日期也可以。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem {
		t.Fatalf("首次完成应成功且非幂等，err=%v idem=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CompletedAt == "" || done.CertificateNumber != "C-1" {
		t.Fatalf("完成信息不完整：%+v", done)
	}

	// 时钟前进后再次用同一证书完成同一计划：返回原结果，不增加记录、不刷新时间。
	clock.t = mustDate(t, "2026-10-05")
	again, idem2, err := l.CompletePlan("P-1", "C-1")
	if err != nil || !idem2 {
		t.Fatalf("同证书重复完成应幂等成功，err=%v idem=%v", err, idem2)
	}
	if again.CompletedAt != done.CompletedAt {
		t.Fatalf("重复完成刷新了完成时间：原 %s 现 %s", done.CompletedAt, again.CompletedAt)
	}
	if n := len(l.data.Plans); n != 1 {
		t.Fatalf("重复完成增加了记录，共 %d 项计划", n)
	}

	// 已完成计划改用另一证书：拒绝且保留原数据。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-10-03",
		Expiry: "2027-10-03", Method: "m", Error: 0.1, Summary: "s2",
	})
	if _, _, err := l.CompletePlan("P-1", "C-2"); !IsValidation(err) {
		t.Fatalf("已完成计划换证书应拒绝，得到 %v", err)
	}
	if l.findPlan("P-1").CertificateNumber != "C-1" {
		t.Fatal("拒绝换证书后原关联被改动")
	}

	// 同一证书不能用于完成两项计划：M-2 的 P-2 用 C-OTHER 完成后，
	// M-2 再建新计划也不能再用 C-OTHER。
	clock.t = mustDate(t, "2026-10-02")
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-10-10", Note: "二"})
	if _, _, err := l.CompletePlan("P-2", "C-OTHER"); err != nil {
		t.Fatalf("C-OTHER 应能完成 P-2：%v", err)
	}
	if _, _, err := l.CompletePlan("P-2", "C-1"); !IsValidation(err) {
		t.Fatalf("他器具证书不能完成本器具计划，得到 %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-11-10", Note: "三"})
	if _, _, err := l.CompletePlan("P-3", "C-OTHER"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "已用于完成计划") {
		t.Fatalf("已用于完成其他计划的证书应拒绝再用，得到 %v", err)
	}
}

func TestCompleteCanceledPlanRejected(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, err := l.CancelPlan("P-1", "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, err := l.CompletePlan("P-1", "C-1"); !IsValidation(err) {
		t.Fatalf("完成已取消计划应拒绝，得到 %v", err)
	}
	c := l.findPlan("P-1")
	if c.Status != PlanStatusCanceled || c.CompletedAt != "" || c.CertificateNumber != "" {
		t.Fatalf("取消计划被完成动作改动：%+v", c)
	}
}

func TestOutOfToleranceCertCompletesButJudgementUnchanged(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})

	// 仅录入证书不会自动完成计划。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-BAD", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 5, Summary: "超差",
	})
	if l.findPlan("P-1").Status != PlanStatusOpen {
		t.Fatal("仅录入证书不应自动完成计划")
	}
	// 超差证书也能表示校准工作已完成。
	done, _, err := l.CompletePlan("P-1", "C-BAD")
	if err != nil || done.Status != PlanStatusDone {
		t.Fatalf("超差证书应能完成计划，err=%v plan=%+v", err, done)
	}
	// 但器具是否可用仍按状态、最近证书结论和有效期判断：超差仍被拒绝。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("计划完成不应改变超差判定，得到 allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	r, _ := l.Review("M-1")
	if r.Latest == nil || r.Latest.Pass {
		t.Fatal("最近证书仍应判超差")
	}
}

func TestTodosOrderingMarkersAndFilter(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustRegister(t, l, "M-3", "信号源", 0.2)
	mustRegister(t, l, "M-4", "频率计", 0.3)
	// 三件未结束计划：M-3 在 10-10，M-1 在 10-20，M-2 也在 10-20。
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-3", Date: "2026-10-10", Note: "n3"})
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "n1"})
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-10-20", Note: "n2"})
	// 已取消和已完成的计划不列入待办。
	mustPlan(t, l, PlanInput{Number: "P-X", InstrumentID: "M-4", Date: "2026-10-11", Note: "取消项"})
	if _, err := l.CancelPlan("P-X", "x"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	usageBefore := len(l.data.Usage)
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("应有 3 项待办，得到 %d", len(items))
	}
	// 按日期从早到晚，同日按器具编号：M-3, M-1, M-2。
	wantOrder := []string{"M-3", "M-1", "M-2"}
	for i, want := range wantOrder {
		if items[i].InstrumentID != want {
			t.Fatalf("第 %d 项应为 %s，得到 %s", i, want, items[i].InstrumentID)
		}
		if items[i].Marker != TodoFuture {
			t.Fatalf("10-02 查询 10-10/20 应为未到计划日，得到 %s", items[i].Marker)
		}
	}
	// 查询不产生使用记录。
	if len(l.data.Usage) != usageBefore {
		t.Fatal("待办查询不应产生使用记录")
	}

	// 按器具筛选。
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].PlanNumber != "P-1" {
		t.Fatalf("按器具筛选异常：%+v err=%v", only, err)
	}
	if _, err := l.Todos("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("筛选未知器具应报 ErrNotFound，得到 %v", err)
	}

	// 标记每次查询按本机日期重新判断：到 10-10 当天 M-3 为“今天需校准”，
	// 10-20 的两项仍为未到计划日。
	clock.t = mustDate(t, "2026-10-10")
	items, _ = l.Todos("")
	got := map[string]string{}
	for _, it := range items {
		got[it.PlanNumber] = it.Marker
	}
	if got["P-3"] != TodoToday || got["P-1"] != TodoFuture || got["P-2"] != TodoFuture {
		t.Fatalf("10-10 当天标记异常：%v", got)
	}
	// 再过十天：M-3 逾期，M-1/M-2 今天需校准；顺序仍为日期、器具编号。
	clock.t = mustDate(t, "2026-10-20")
	items, _ = l.Todos("")
	got = map[string]string{}
	for _, it := range items {
		got[it.PlanNumber] = it.Marker
	}
	if got["P-3"] != TodoOverdue || got["P-1"] != TodoToday || got["P-2"] != TodoToday {
		t.Fatalf("10-20 标记异常：%v", got)
	}
	if items[0].PlanNumber != "P-3" || items[1].InstrumentID != "M-1" || items[2].InstrumentID != "M-2" {
		t.Fatalf("逾期项仍应按日期排在最前：%+v", items)
	}

	// 全部结束后待办为空。
	clock.t = mustDate(t, "2026-10-02")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-1"); err != nil {
		t.Fatalf("complete P-1: %v", err)
	}
	if _, err := l.CancelPlan("P-2", "x"); err != nil {
		t.Fatalf("cancel P-2: %v", err)
	}
	if _, err := l.CancelPlan("P-3", "x"); err != nil {
		t.Fatalf("cancel P-3: %v", err)
	}
	empty, err := l.Todos("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("无未结束计划时应返回空，得到 %+v err=%v", empty, err)
	}
}

func TestReviewIncludesPlanHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-OLD", InstrumentID: "M-1", Date: "2026-10-10", Note: "旧"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-OLD", "C-1"); err != nil {
		t.Fatalf("complete old: %v", err)
	}
	// 新的未结束计划，含一次改期。
	mustPlan(t, l, PlanInput{Number: "P-NEW", InstrumentID: "M-1", Date: "2026-11-01", Note: "新"})
	if _, err := l.ReschedulePlan("P-NEW", "2026-11-05", "车间占用"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 2 {
		t.Fatalf("核对应同时展示当前及已结束计划，得到 %d", len(r.Plans))
	}
	var old, cur *PlanView
	for i := range r.Plans {
		switch r.Plans[i].Number {
		case "P-OLD":
			old = &r.Plans[i]
		case "P-NEW":
			cur = &r.Plans[i]
		}
	}
	if old == nil || old.Status != PlanStatusDone || old.CertificateNumber != "C-1" {
		t.Fatalf("已结束计划及关联证书展示异常：%+v", old)
	}
	if cur == nil || cur.PlannedDate != "2026-11-05" || cur.OriginalDate != "2026-11-01" {
		t.Fatalf("当前计划日期展示异常：%+v", cur)
	}
	if len(cur.Changes) != 1 || cur.Changes[0].Reason != "车间占用" {
		t.Fatalf("改期记录未展示：%+v", cur)
	}
	if cur.Marker != TodoFuture {
		t.Fatalf("未来计划标记应为未到计划日，得到 %s", cur.Marker)
	}
}

func TestPlansPersistAndOldLedgerHasNoPlans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := func() time.Time { return mustDate(t, "2026-10-02") }

	l, err := openAt(path, clock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "改期留痕"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	// 关闭后重开：计划与改期历史保留。
	reopened, err := openAt(path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	p := reopened.findPlan("P-1")
	if p == nil || p.PlannedDate != "2026-10-20" || p.OriginalDate != "2026-10-10" {
		t.Fatalf("重开后计划丢失：%+v", p)
	}
	if len(p.Changes) != 1 || p.Changes[0].Reason != "改期留痕" {
		t.Fatalf("重开后改期历史丢失：%+v", p)
	}
	// 原有证书、使用记录入口仍可用。
	mustAddCert(t, reopened, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := reopened.CompletePlan("P-1", "C-1"); err != nil {
		t.Fatalf("重开后应能继续完成计划：%v", err)
	}

	// 旧台账文件（没有 plans 字段）按无计划处理，且可以正常新增计划。
	oldPath := filepath.Join(dir, "old.json")
	oldRaw, _ := json.Marshal(map[string]any{
		"version": 1,
		"instruments": []Instrument{{
			ID: "OLD-1", Name: "老表", AllowedError: 0.5,
			Status: StatusPending, RegisteredAt: "2026-09-01T00:00:00Z",
		}},
		"certificates": []Certificate{},
		"usage":        []UsageRecord{},
	})
	if err := os.WriteFile(oldPath, oldRaw, 0o644); err != nil {
		t.Fatalf("write old ledger: %v", err)
	}
	oldLedger, err := openAt(oldPath, clock)
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	items, err := oldLedger.Todos("")
	if err != nil || len(items) != 0 {
		t.Fatalf("旧台账应按无计划处理，items=%+v err=%v", items, err)
	}
	plans, err := oldLedger.Plans("OLD-1")
	if err != nil || len(plans) != 0 {
		t.Fatalf("旧台账器具应无计划，plans=%+v err=%v", plans, err)
	}
	if _, err := oldLedger.CreatePlan(PlanInput{
		Number: "P-OLD-1", InstrumentID: "OLD-1", Date: "2026-10-10", Note: "补建",
	}); err != nil {
		t.Fatalf("旧台账应能新增计划：%v", err)
	}
}
