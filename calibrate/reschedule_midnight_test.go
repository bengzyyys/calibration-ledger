package calibrate

import (
	"testing"
	"time"
)

// TestRescheduleSingleTimestampAcrossMidnight 验证每次改期申请只在开始处理时
// 取一次本机时间：未结束计划原定 2027-01-10，申请在 2026-12-31 23:59:59 开始、
// 保存完成时已进入 2027-01-01，新日期 2026-12-31（申请当天）仍合法。当前计划
// 日期变为 12-31，最初计划日期保留 01-10，改期历史只增加这一条：前后日期、
// 本次原因完整保留，操作时间属于申请开始的 12-31 并带当时的本机时区偏移，
// 不会被改写成次日或保存结束时刻；计划编号、器具归属与未完成状态保持原样。
func TestRescheduleSingleTimestampAcrossMidnight(t *testing.T) {
	dec31late := time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local)
	jan1 := time.Date(2027, 1, 1, 0, 0, 5, 0, time.Local)

	clock := &fakeClock{t: mustDate(t, "2026-12-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2027-01-10", Note: "年度送检",
	})

	// 改期申请开始时仍在 12-31 深夜，随后的读取（模拟保存阶段）已进入 01-01。
	midnight := &stepClock{times: []time.Time{dec31late, jan1, jan1, jan1}}
	l.now = midnight.now
	p, err := l.ReschedulePlan("PL-1", "2026-12-31", "实验室提前有空档")
	if err != nil {
		t.Fatalf("申请开始于 12-31 的当天改期应成功，得到 %v", err)
	}
	if p.Number != "PL-1" || p.InstrumentID != "M-1" || p.Status != PlanStatusOpen {
		t.Fatalf("计划编号、器具归属与未完成状态应保持原样，得到 %+v", p)
	}
	if p.PlannedDate != "2026-12-31" || p.OriginalDate != "2027-01-10" {
		t.Fatalf("当前计划日期应为 2026-12-31、最初计划日期保留 2027-01-10，得到 %+v", p)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("改期历史应只增加一条，得到 %+v", p.Changes)
	}
	change := p.Changes[0]
	if change.From != "2027-01-10" || change.To != "2026-12-31" {
		t.Fatalf("改期记录的前后日期应为 2027-01-10 → 2026-12-31，得到 %+v", change)
	}
	if change.Reason != "实验室提前有空档" {
		t.Fatalf("改期原因应保留本次提交的内容，得到 %q", change.Reason)
	}
	// 操作时间取申请开始时刻，文字中自带当时的本机时区偏移。
	if want := dec31late.Format(time.RFC3339); change.ChangedAt != want {
		t.Fatalf("操作时间应取申请开始时刻 %s（含本机偏移），得到 %s", want, change.ChangedAt)
	}
	changed, err := time.Parse(time.RFC3339, change.ChangedAt)
	if err != nil {
		t.Fatalf("操作时间应可解析: %v", err)
	}
	if day := changed.Format(DateLayout); day != "2026-12-31" {
		t.Fatalf("操作时间应属于申请开始的 12-31，得到 %s", day)
	}

	// 台账里保存的正式记录与返回结果一致。
	saved := l.findPlan("PL-1")
	if saved == nil || len(saved.Changes) != 1 || saved.Changes[0] != change ||
		saved.PlannedDate != "2026-12-31" || saved.OriginalDate != "2027-01-10" {
		t.Fatalf("台账内计划应与返回结果一致，得到 %+v", saved)
	}
}

// TestRescheduleTimestampNotCarriedToNextRequest 验证改期申请采用的时间不延续到
// 下一次申请：同一台账进入 01-01 后，再申请改到 12-31 因日期已过明确拒绝——既不
// 沿用上次申请的“今天”，也不因新日期恰好等于当前计划日期而绕过日期要求；拒绝后
// 当前日期、最初日期与全部改期历史保持原样。随后申请改到 01-01 仍成功，新增记录
// 从最后成功保存的 12-31 接到 01-01，操作时间取这次新申请而非上一条记录。待办与
// 核对标记按查询当天判断：停留在 12-31 时显示“逾期”，改到 01-01 后显示
// “今天需校准”，核对中的历史改期时间按申请时保留。
func TestRescheduleTimestampNotCarriedToNextRequest(t *testing.T) {
	dec31late := time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local)
	jan1early := time.Date(2027, 1, 1, 0, 0, 5, 0, time.Local)
	jan1day := time.Date(2027, 1, 1, 9, 30, 0, 0, time.Local)

	clock := &fakeClock{t: mustDate(t, "2026-12-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2027-01-10", Note: "年度送检",
	})

	// 跨午夜改期到 12-31 成功。
	midnight := &stepClock{times: []time.Time{dec31late, jan1early}}
	l.now = midnight.now
	first, err := l.ReschedulePlan("PL-1", "2026-12-31", "实验室提前有空档")
	if err != nil {
		t.Fatalf("跨午夜改期应成功，得到 %v", err)
	}
	if len(first.Changes) != 1 {
		t.Fatalf("应只有一条改期记录，得到 %+v", first.Changes)
	}

	// 时钟拨到 01-01：上一次申请采用的时间不能延续。
	clock.t = jan1day
	l.now = clock.now

	// 再申请改到 12-31：日期已过，明确拒绝；新日期恰好等于当前计划日期
	// 也不能绕过日期要求。
	if _, err := l.ReschedulePlan("PL-1", "2026-12-31", "还想再提前"); !IsValidation(err) {
		t.Fatalf("01-01 申请改到 12-31 应因日期已过而拒绝，得到 %v", err)
	}
	saved := l.findPlan("PL-1")
	if saved.PlannedDate != "2026-12-31" || saved.OriginalDate != "2027-01-10" {
		t.Fatalf("被拒绝的改期不应改动当前与最初日期，得到 %+v", saved)
	}
	if len(saved.Changes) != 1 || saved.Changes[0] != first.Changes[0] {
		t.Fatalf("被拒绝的改期不应留下本次原因或操作时间，得到 %+v", saved.Changes)
	}

	// 01-01 查询：计划仍停留在 12-31，待办与核对都显示“逾期”。
	todos, err := l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoOverdue {
		t.Fatalf("停留在 12-31 的计划在 01-01 应标记为 %q，得到 %+v", TodoOverdue, todos)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("核对中停留在 12-31 的计划应标记为 %q，得到 %+v", TodoOverdue, r.Plans)
	}

	// 随后申请改到 01-01 当天：仍应成功。
	second, err := l.ReschedulePlan("PL-1", "2027-01-01", "按新年排期调整")
	if err != nil {
		t.Fatalf("改到 01-01 当天应成功，得到 %v", err)
	}
	if second.PlannedDate != "2027-01-01" || second.OriginalDate != "2027-01-10" {
		t.Fatalf("当前计划日期应为 2027-01-01、最初计划日期保留 2027-01-10，得到 %+v", second)
	}
	if len(second.Changes) != 2 {
		t.Fatalf("应新增一条改期记录，得到 %+v", second.Changes)
	}
	// 既有改期历史的日期、原因和时间不能被重写。
	if second.Changes[0] != first.Changes[0] {
		t.Fatalf("既有改期记录应保持原样：原 %+v 现 %+v", first.Changes[0], second.Changes[0])
	}
	// 新增记录从最后成功保存的 12-31 接到 01-01，操作时间属于这次新申请。
	added := second.Changes[1]
	if added.From != "2026-12-31" || added.To != "2027-01-01" {
		t.Fatalf("新记录应从 2026-12-31 接到 2027-01-01，得到 %+v", added)
	}
	if added.Reason != "按新年排期调整" {
		t.Fatalf("新记录应保留本次提交的原因，得到 %q", added.Reason)
	}
	if want := jan1day.Format(time.RFC3339); added.ChangedAt != want {
		t.Fatalf("新记录的操作时间应取本次申请时刻 %s，得到 %s", want, added.ChangedAt)
	}
	if added.ChangedAt == first.Changes[0].ChangedAt {
		t.Fatal("新记录的操作时间不能复用上一条记录的时间")
	}

	// 成功改到 01-01 后：待办与核对显示“今天需校准”，核对中能看到这两条
	// 正式改期记录，历史时间按申请时保留。
	todos, err = l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoToday {
		t.Fatalf("改到 01-01 后应标记为 %q，得到 %+v", TodoToday, todos)
	}
	r, err = l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoToday {
		t.Fatalf("核对中改到 01-01 的计划应标记为 %q，得到 %+v", TodoToday, r.Plans)
	}
	view := r.Plans[0]
	if len(view.Changes) != 2 ||
		view.Changes[0].ChangedAt != dec31late.Format(time.RFC3339) ||
		view.Changes[1].ChangedAt != jan1day.Format(time.RFC3339) {
		t.Fatalf("核对中的改期历史时间应按申请时保留，得到 %+v", view.Changes)
	}
}
