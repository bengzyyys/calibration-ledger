package calibrate

import (
	"testing"
	"time"
)

// TestCreatePlanSingleTimestampAcrossMidnight 验证每次建立申请只在开始处理时
// 取一次本机时间：申请在 2026-10-03 23:59:59 开始、保存完成时已进入 10-04，
// 计划日期 2026-10-03 仍合法，当前计划日期与最初计划日期都保留 10-03，
// 建立时间也属于 10-03，不会被改写成次日或保存结束时刻。
func TestCreatePlanSingleTimestampAcrossMidnight(t *testing.T) {
	day3late := time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local)
	day4 := time.Date(2026, 10, 4, 0, 0, 5, 0, time.Local)

	clock := &fakeClock{t: mustDate(t, "2026-10-03")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 建立申请开始时仍在 10-03，随后的读取（模拟保存阶段）已进入 10-04。
	midnight := &stepClock{times: []time.Time{day3late, day4, day4, day4}}
	l.now = midnight.now
	p, err := l.CreatePlan(PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "送检",
	})
	if err != nil {
		t.Fatalf("申请开始于 10-03 的当天计划应建立成功，得到 %v", err)
	}
	if p.PlannedDate != "2026-10-03" || p.OriginalDate != "2026-10-03" {
		t.Fatalf("当前计划日期与最初计划日期都应保留 2026-10-03，得到 %+v", p)
	}
	if p.Status != PlanStatusOpen {
		t.Fatalf("新计划应为未完成，得到 %s", p.Status)
	}
	if want := day3late.Format(time.RFC3339); p.CreatedAt != want {
		t.Fatalf("建立时间应取申请开始时刻 %s，得到 %s", want, p.CreatedAt)
	}
	created, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		t.Fatalf("建立时间应可解析: %v", err)
	}
	if day := created.Format(DateLayout); day != "2026-10-03" {
		t.Fatalf("建立时间应属于 10-03，得到 %s", day)
	}

	// 台账里保存的正式记录同样是 10-03 建立。
	saved := l.findPlan("PL-1")
	if saved == nil || saved.CreatedAt != p.CreatedAt || saved.PlannedDate != "2026-10-03" {
		t.Fatalf("台账内计划应与返回结果一致，得到 %+v", saved)
	}
}

// TestCreatePlanPastDateRejectedAcrossMidnight 验证同一跨午夜场景下填写
// 2026-10-02（申请开始日的前一天）仍被明确拒绝且不新增计划。
func TestCreatePlanPastDateRejectedAcrossMidnight(t *testing.T) {
	day3late := time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local)
	day4 := time.Date(2026, 10, 4, 0, 0, 5, 0, time.Local)

	clock := &fakeClock{t: mustDate(t, "2026-10-03")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	midnight := &stepClock{times: []time.Time{day3late, day4, day4}}
	l.now = midnight.now
	_, err := l.CreatePlan(PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-02", Note: "送检",
	})
	if !IsValidation(err) {
		t.Fatalf("填写 10-02 应报校验错误，得到 %v", err)
	}
	if len(l.data.Plans) != 0 {
		t.Fatalf("被拒绝的申请不应新增计划，得到 %d 项", len(l.data.Plans))
	}
}

// TestCreatePlanTimestampNotCarriedToNextRequest 验证申请采用的时间不延续到
// 下一次申请：10-04 新提交 10-03 的计划因日期已过而拒绝；此前跨午夜建立的
// 计划在 10-04 查询待办与核对时照常显示“逾期”；用校准日期为 10-03 的证书
// 完成该计划不会被误判为证书早于建立日期。
func TestCreatePlanTimestampNotCarriedToNextRequest(t *testing.T) {
	day3late := time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local)
	day4 := time.Date(2026, 10, 4, 0, 0, 5, 0, time.Local)

	clock := &fakeClock{t: mustDate(t, "2026-10-03")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "卡尺", 0.1)

	// 跨午夜建立 M-1 的当天计划。
	midnight := &stepClock{times: []time.Time{day3late, day4}}
	l.now = midnight.now
	p := mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "送检",
	})
	if p.OriginalDate != "2026-10-03" {
		t.Fatalf("最初计划日期应保留 2026-10-03，得到 %s", p.OriginalDate)
	}

	// 时钟拨到 10-04：上一次申请采用的时间不能延续。
	clock.t = day4
	l.now = clock.now
	if _, err := l.CreatePlan(PlanInput{
		Number: "PL-2", InstrumentID: "M-2", Date: "2026-10-03", Note: "送检",
	}); !IsValidation(err) {
		t.Fatalf("10-04 提交 10-03 的计划应因日期已过而拒绝，得到 %v", err)
	}
	if l.findPlan("PL-2") != nil {
		t.Fatal("被拒绝的计划不应新增")
	}

	// 10-04 查询待办与按器具核对：PL-1 照常显示“逾期”。
	todos, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoOverdue {
		t.Fatalf("PL-1 在 10-04 应标记为 %q，得到 %+v", TodoOverdue, todos)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("核对中 PL-1 在 10-04 应标记为 %q，得到 %+v", TodoOverdue, r.Plans)
	}

	// 校准日期为 10-03 的证书完成该计划：不得被判为早于建立日期。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-03",
		Expiry: "2027-10-03", Method: "m", Error: 0.1, Summary: "s",
	})
	done, idempotent, err := l.CompletePlan("PL-1", "C-1")
	if err != nil {
		t.Fatalf("10-03 的证书完成 10-03 建立的计划应成功，得到 %v", err)
	}
	if idempotent || done.Status != PlanStatusDone || done.CertificateNumber != "C-1" {
		t.Fatalf("完成结果异常：%+v idempotent=%v", done, idempotent)
	}
}
