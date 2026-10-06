package calibrate

import (
	"strings"
	"testing"
	"time"
)

// rescheduleMidnightZone 是改期跨午夜场景使用的本机时区：固定 +08:00，
// 使留痕的 RFC3339 时间必然带显式本机偏移，且测试结果与运行机器的 TZ 无关。
var rescheduleMidnightZone = time.FixedZone("UTC+08:00", 8*3600)

// TestRescheduleSingleTimestampAcrossMidnight 验证每次改期申请只在开始处理时
// 取一次本机时间：未结束计划原定下一年一月十日，用户在本机十二月三十一日
// 深夜申请改到当天并填写有效原因，即使处理完成时已经是一月一日，申请仍应
// 成功——当前计划日期变为十二月三十一日，最初计划日期仍是一月十日，改期
// 历史只增加一条（前一个日期一月十日、后一个日期十二月三十一日、原因保留
// 本次提交内容），操作时间属于申请开始的十二月三十一日并带当时的本机时区
// 偏移，不能被记成一月一日。计划编号、器具归属和未完成状态保持原样。
func TestRescheduleSingleTimestampAcrossMidnight(t *testing.T) {
	dec31Late := time.Date(2026, 12, 31, 23, 59, 59, 0, rescheduleMidnightZone)
	jan1 := time.Date(2027, 1, 1, 0, 0, 5, 0, rescheduleMidnightZone)

	clock := &fakeClock{t: time.Date(2026, 12, 1, 10, 0, 0, 0, rescheduleMidnightZone)}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2027-01-10", Note: "年度例行校准",
	})

	// 改期申请开始时仍在 12-31，随后的读取（模拟校验后保存阶段）已进入 1-01。
	midnight := &stepClock{times: []time.Time{dec31Late, jan1, jan1, jan1}}
	l.now = midnight.now
	p, err := l.ReschedulePlan("PL-1", "2026-12-31", "实验室年末紧急插单")
	if err != nil {
		t.Fatalf("申请开始于 12-31 改到当天应成功，得到 %v", err)
	}
	if p.Number != "PL-1" || p.InstrumentID != "M-1" {
		t.Fatalf("改期不应改动计划编号与器具归属：%+v", p)
	}
	if !p.Open() {
		t.Fatalf("改期后计划仍应为未完成，得到 %s", p.Status)
	}
	if p.PlannedDate != "2026-12-31" || p.OriginalDate != "2027-01-10" {
		t.Fatalf("当前日期应为 12-31、最初日期仍为 2027-01-10，得到 %+v", p)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("改期历史只应增加一条，得到 %d 条：%+v", len(p.Changes), p.Changes)
	}
	wantFirst := PlanChange{
		From:      "2027-01-10",
		To:        "2026-12-31",
		Reason:    "实验室年末紧急插单",
		ChangedAt: dec31Late.Format(time.RFC3339),
	}
	if p.Changes[0] != wantFirst {
		t.Fatalf("改期记录应保留前后日期、原因与申请开始时刻：%+v", p.Changes[0])
	}

	// 操作时间必须属于申请开始的 12-31，并带当时的本机时区偏移 +08:00；
	// 若校验取一次时间、留痕另取一次已跨入 1-01 的时间，这里就会失败。
	changed, err := time.Parse(time.RFC3339, p.Changes[0].ChangedAt)
	if err != nil {
		t.Fatalf("操作时间应可解析: %v", err)
	}
	if day := changed.Format(DateLayout); day != "2026-12-31" {
		t.Fatalf("操作时间应属于申请开始的 12-31，得到 %s（%s）",
			day, p.Changes[0].ChangedAt)
	}
	if _, off := changed.Zone(); off != 8*3600 {
		t.Fatalf("操作时间应保留申请开始时的本机时区偏移 +08:00，得到偏移 %d（%s）",
			off, p.Changes[0].ChangedAt)
	}
	if !strings.HasSuffix(p.Changes[0].ChangedAt, "+08:00") {
		t.Fatalf("操作时间文字应带显式本机偏移，得到 %s", p.Changes[0].ChangedAt)
	}
	if changed.Equal(jan1) {
		t.Fatalf("操作时间不能被记成保存完成时的 1-01：%s", p.Changes[0].ChangedAt)
	}

	// 台账里保存的正式记录与返回结果一致：仍是 12-31 的那一次改期。
	saved := l.findPlan("PL-1")
	if saved == nil || saved.PlannedDate != "2026-12-31" ||
		saved.OriginalDate != "2027-01-10" || len(saved.Changes) != 1 ||
		saved.Changes[0] != wantFirst {
		t.Fatalf("台账内计划应与返回结果一致，得到 %+v", saved)
	}
}

// TestRescheduleAcrossMidnightNotCarriedToNextRequest 验证一次申请采用的
// 时间不延续到下一次申请：跨午夜改到 12-31 成功后，同一台账进入 1-01，
// 再次申请改到 12-31 必须按日期已经过去明确拒绝——不能沿用上次申请的
// “今天”，也不能因为新日期恰好等于当前计划日期而绕过日期要求；拒绝后
// 当前日期、最初日期与全部改期历史（含第一条的日期、原因和时间）保持
// 原样，不留下本次原因或操作时间。随后改到 1-01 仍应成功，新增记录从
// 最后成功保存的 12-31 接到 1-01，操作时间属于这次新申请，不能复用
// 上一条记录的时间。待办标记继续按查询当天判断：1-01 查询尚停留在
// 12-31 的计划显示“逾期”，成功改到 1-01 后显示“今天需校准”。
func TestRescheduleAcrossMidnightNotCarriedToNextRequest(t *testing.T) {
	dec31Late := time.Date(2026, 12, 31, 23, 59, 59, 0, rescheduleMidnightZone)
	jan1 := time.Date(2027, 1, 1, 0, 0, 5, 0, rescheduleMidnightZone)
	jan1Morning := time.Date(2027, 1, 1, 9, 30, 0, 0, rescheduleMidnightZone)

	clock := &fakeClock{t: time.Date(2026, 12, 1, 10, 0, 0, 0, rescheduleMidnightZone)}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2027-01-10", Note: "年度例行校准",
	})

	// 跨午夜的第一次申请：1-10 → 12-31，操作时间属于 12-31。
	l.now = (&stepClock{times: []time.Time{dec31Late, jan1, jan1}}).now
	first, err := l.ReschedulePlan("PL-1", "2026-12-31", "实验室年末紧急插单")
	if err != nil {
		t.Fatalf("跨午夜改到当天应成功，得到 %v", err)
	}
	wantFirst := first.Changes[0]
	if wantFirst.ChangedAt != dec31Late.Format(time.RFC3339) {
		t.Fatalf("第一条操作时间应属于 12-31，得到 %s", wantFirst.ChangedAt)
	}

	// 时钟进入 1-01：上一次申请采用的“今天”不能延续。
	clock.t = jan1
	l.now = clock.now

	// 1-01 查询仍停留在 12-31 的计划：待办与核对都应显示“逾期”。
	todos, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoOverdue ||
		todos[0].PlannedDate != "2026-12-31" {
		t.Fatalf("1-01 查询 12-31 的计划应标记为 %q，得到 %+v", TodoOverdue, todos)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("核对中计划在 1-01 应标记为 %q，得到 %+v", TodoOverdue, r.Plans)
	}

	// 再次申请改到 12-31：日期已经过去，必须明确拒绝；该日期恰好等于
	// 当前计划日期也不能绕过“新日期不能早于操作当天”的要求。
	if _, err := l.ReschedulePlan("PL-1", "2026-12-31", "想把今天留在昨天"); !IsValidation(err) {
		t.Fatalf("1-01 申请改到 12-31 应按业务校验拒绝，得到 %v", err)
	} else if !strings.Contains(err.Error(), "不能早于") {
		t.Fatalf("拒绝原因应明确指出日期不能早于操作当天，得到 %v", err)
	}

	// 拒绝后当前日期、最初日期和全部改期历史保持原样，不留本次原因或时间。
	cur := l.findPlan("PL-1")
	if cur == nil || cur.PlannedDate != "2026-12-31" ||
		cur.OriginalDate != "2027-01-10" || !cur.Open() {
		t.Fatalf("拒绝后计划状态异常：%+v", cur)
	}
	if len(cur.Changes) != 1 || cur.Changes[0] != wantFirst {
		t.Fatalf("拒绝后改期历史必须原样保留，得到 %+v（原 %+v）",
			cur.Changes, wantFirst)
	}

	// 随后改到 1-01 仍应成功：新增记录从最后成功保存的 12-31 接到 1-01，
	// 操作时间属于这次新申请，不能复用上一条记录的时间。
	clock.t = jan1Morning
	l.now = clock.now
	second, err := l.ReschedulePlan("PL-1", "2027-01-01", "新年首个校准窗口")
	if err != nil {
		t.Fatalf("1-01 当天申请改到 1-01 应成功，得到 %v", err)
	}
	if second.PlannedDate != "2027-01-01" || second.OriginalDate != "2027-01-10" ||
		!second.Open() || second.Number != "PL-1" || second.InstrumentID != "M-1" {
		t.Fatalf("第二次改期后计划字段异常：%+v", second)
	}
	if len(second.Changes) != 2 {
		t.Fatalf("改期历史应只有两条，得到 %d 条：%+v", len(second.Changes), second.Changes)
	}
	if second.Changes[0] != wantFirst {
		t.Fatalf("既有改期记录的日期、原因和时间不能被重写：原 %+v 现 %+v",
			wantFirst, second.Changes[0])
	}
	wantSecond := PlanChange{
		From:      "2026-12-31",
		To:        "2027-01-01",
		Reason:    "新年首个校准窗口",
		ChangedAt: jan1Morning.Format(time.RFC3339),
	}
	if second.Changes[1] != wantSecond {
		t.Fatalf("新增记录应从 12-31 接到 1-01 并采用本次申请时间：%+v",
			second.Changes[1])
	}
	if second.Changes[1].ChangedAt == wantFirst.ChangedAt {
		t.Fatalf("第二次操作时间不能复用上一条记录的时间：%s",
			second.Changes[1].ChangedAt)
	}
	if day := parseRFC3339Day(t, second.Changes[1].ChangedAt); day != "2027-01-01" {
		t.Fatalf("第二条操作时间应属于新申请的 1-01，得到 %s", day)
	}

	// 按器具核对：正式改期记录都可见，历史时间按申请时保留。
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review after second reschedule: %v", err)
	}
	if len(r2.Plans) != 1 {
		t.Fatalf("应只有一项计划，得到 %d 项", len(r2.Plans))
	}
	got := r2.Plans[0]
	if len(got.Changes) != 2 ||
		got.Changes[0].ChangedAt != wantFirst.ChangedAt ||
		got.Changes[1].ChangedAt != wantSecond.ChangedAt {
		t.Fatalf("核对中的改期历史时间应按申请时保留：%+v", got.Changes)
	}
	// 成功改到 1-01 后，同一天查询显示“今天需校准”。
	if !got.Open() || got.Marker != TodoToday {
		t.Fatalf("计划在 1-01 当天应标记为 %q，得到 open=%v marker=%q",
			TodoToday, got.Open(), got.Marker)
	}
	todos2, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos after second reschedule: %v", err)
	}
	if len(todos2) != 1 || todos2[0].Marker != TodoToday ||
		todos2[0].PlannedDate != "2027-01-01" {
		t.Fatalf("改到 1-01 后待办应标记为 %q，得到 %+v", TodoToday, todos2)
	}
}

// parseRFC3339Day 解析留痕时间并返回其日历日期，失败即终止测试。
func parseRFC3339Day(t *testing.T, s string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("时间 %q 应可解析: %v", s, err)
	}
	return parsed.Format(DateLayout)
}
