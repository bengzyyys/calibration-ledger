package calibrate

import (
	"strings"
	"testing"
	"time"
)

// localTime 按本机时区构造时刻，保证与台账保存的 RFC3339 建立时间处在同一时区。
func localTime(t *testing.T, year int, month time.Month, day, hour, min, sec int) time.Time {
	t.Helper()
	return time.Date(year, month, day, hour, min, sec, 0, time.Local)
}

// TestCreatePlanUsesSingleRequestTimeAcrossMidnight 验证建立计划时计划日期的
// 合法性与正式记录的建立时间对应同一次申请：申请在 2026-10-03 23:59:59 开始，
// 即使保存完成时已进入 10-04，填写 10-03 仍成功，当前/最初计划日期与建立时间
// 都属于 10-03。该时刻不延续到下一次申请：10-04 新提交 10-03 的计划按日期已
// 过去拒绝；建立的计划次日在待办与核对中显示“逾期”；校准日期为 10-03 的证书
// 完成该计划时不得被判为早于建立日期。
func TestCreatePlanUsesSingleRequestTimeAcrossMidnight(t *testing.T) {
	day3Morning := localTime(t, 2026, 10, 3, 9, 0, 0)
	clock := &fakeClock{t: day3Morning}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	// 校准日期为申请当天 10-03 的证书，留待次日完成跨午夜建立的计划。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-03",
		Expiry: "2027-10-03", Method: "m", Error: 0.1, Summary: "s",
	})

	// 换上跨午夜时钟处理建立申请：第一次读取为 10-03 23:59:59（申请开始），
	// 之后的读取都已进入 10-04（模拟校验与保存跨过午夜）。
	start := localTime(t, 2026, 10, 3, 23, 59, 59)
	nextDay := localTime(t, 2026, 10, 4, 0, 0, 1)
	l.now = (&stepClock{times: []time.Time{start, nextDay}}).now

	in := PlanInput{Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "当天送检"}
	p, err := l.CreatePlan(in)
	if err != nil {
		t.Fatalf("申请开始于 10-03、计划日期填 10-03，即使保存跨午夜也应成功：%v", err)
	}
	if p.Status != PlanStatusOpen {
		t.Fatalf("应建立未完成计划，得到状态 %s", p.Status)
	}
	if p.PlannedDate != "2026-10-03" || p.OriginalDate != "2026-10-03" {
		t.Fatalf("当前与最初计划日期都应保留 10-03，得到 current=%s original=%s",
			p.PlannedDate, p.OriginalDate)
	}
	if want := start.Format(time.RFC3339); p.CreatedAt != want {
		t.Fatalf("建立时间必须反映申请开始时刻 %s，不能换成保存结束时间，得到 %s",
			want, p.CreatedAt)
	}
	created, perr := time.Parse(time.RFC3339, p.CreatedAt)
	if perr != nil {
		t.Fatalf("建立时间无法解析：%v", perr)
	}
	if got := created.Format(DateLayout); got != "2026-10-03" {
		t.Fatalf("建立时间的本机日历日期必须仍属于 10-03，得到 %s", got)
	}
	// 台账内保存的是同一份建立时间，不只是返回副本如此。
	if sp := l.findPlan("PL-1"); sp == nil || sp.CreatedAt != p.CreatedAt ||
		sp.PlannedDate != "2026-10-03" || sp.OriginalDate != "2026-10-03" {
		t.Fatalf("台账内正式计划与返回结果不一致：%+v", sp)
	}

	// 新一次申请不沿用上一次跨午夜前的时刻：时钟固定在 10-04 后，
	// 为 M-2 提交 10-03 的计划应因日期已经过去而拒绝，且不新增计划、
	// 不占用编号或该器具的计划名额。
	clock.t = nextDay
	l.now = clock.now
	before := len(l.data.Plans)
	if _, err := l.CreatePlan(PlanInput{
		Number: "PL-2", InstrumentID: "M-2", Date: "2026-10-03", Note: "补昨天",
	}); err == nil || !IsValidation(err) || !strings.Contains(err.Error(), "不能早于本机今天") {
		t.Fatalf("10-04 新提交 10-03 的计划应按日期已过业务拒绝，得到 %v", err)
	}
	if l.findPlan("PL-2") != nil || len(l.data.Plans) != before {
		t.Fatalf("被拒绝的过去日期计划不得新增或留痕：%+v", l.data.Plans)
	}
	// 被拒绝的编号与名额仍可用于合法的未来日期申请。
	future, err := l.CreatePlan(PlanInput{
		Number: "PL-2", InstrumentID: "M-2", Date: "2026-10-10", Note: "未来安排",
	})
	if err != nil {
		t.Fatalf("未来日期应照常接受，得到 %v", err)
	}
	if future.PlannedDate != "2026-10-10" ||
		future.CreatedAt != nextDay.Format(time.RFC3339) {
		t.Fatalf("新计划应按 10-04 的新申请记录：%+v", future)
	}

	// 10-04 查询待办与按器具核对：10-03 建立、计划日 10-03 的计划必须显示
	// “逾期”，不能一直停留在创建时的“今天需校准”。
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	var pl1 *TodoItem
	for i := range items {
		if items[i].PlanNumber == "PL-1" {
			pl1 = &items[i]
		}
	}
	if pl1 == nil {
		t.Fatalf("PL-1 仍应是未结束待办：%+v", items)
	}
	if pl1.Marker != TodoOverdue {
		t.Fatalf("10-04 查询时计划日 10-03 应标记为 %q，得到 %q", TodoOverdue, pl1.Marker)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || !r.Plans[0].Open() || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("按器具核对中 PL-1 应未结束且标记 %q：%+v", TodoOverdue, r.Plans)
	}

	// 次日用属于该器具、校准日期为 10-03 且未用于其他计划的证书完成：
	// 不得仅因建立过程跨过午夜而被判为证书早于计划建立日期。
	done, idem, err := l.CompletePlan("PL-1", "C-1")
	if err != nil {
		t.Fatalf("校准日期等于计划建立日期的证书应能完成计划：%v", err)
	}
	if idem || done.Status != PlanStatusDone || done.CertificateNumber != "C-1" {
		t.Fatalf("应为首次成功完成并关联 C-1：idem=%v plan=%+v", idem, done)
	}
}

// TestCreatePlanAcrossMidnightRejectsPastAndAcceptsFuture 验证跨午夜申请的
// 日期边界：以申请开始的 10-03 为准，10-02 明确拒绝且不新增计划，格式错误与
// 不存在的日期仍按原样拒绝，未来日期照常接受。
func TestCreatePlanAcrossMidnightRejectsPastAndAcceptsFuture(t *testing.T) {
	day3Morning := localTime(t, 2026, 10, 3, 9, 0, 0)
	clock := &fakeClock{t: day3Morning}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	start := localTime(t, 2026, 10, 3, 23, 59, 59)
	nextDay := localTime(t, 2026, 10, 4, 0, 0, 1)

	for _, tc := range []struct {
		name string
		date string
		want string
	}{
		{"前一天日期拒绝", "2026-10-02", "不能早于本机今天"},
		{"格式不符拒绝", "2026-10-3", "YYYY-MM-DD"},
		{"不存在的日期拒绝", "2026-02-30", "实际存在的日期"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l.now = (&stepClock{times: []time.Time{start, nextDay}}).now
			before := len(l.data.Plans)
			_, err := l.CreatePlan(PlanInput{
				Number: "PL-X", InstrumentID: "M-1", Date: tc.date, Note: "x",
			})
			if err == nil || !IsValidation(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("用例 %q 应业务拒绝（含 %q），得到 %v", tc.name, tc.want, err)
			}
			if l.findPlan("PL-X") != nil || len(l.data.Plans) != before {
				t.Fatalf("用例 %q 不得新增计划：%+v", tc.name, l.data.Plans)
			}
		})
	}

	// 未来日期在跨午夜申请中照常接受，建立时间属于申请开始的 10-03。
	l.now = (&stepClock{times: []time.Time{start, nextDay}}).now
	p, err := l.CreatePlan(PlanInput{
		Number: "PL-F", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行",
	})
	if err != nil {
		t.Fatalf("未来日期跨午夜申请应成功：%v", err)
	}
	if p.PlannedDate != "2026-10-20" || p.OriginalDate != "2026-10-20" {
		t.Fatalf("计划日期异常：%+v", p)
	}
	if p.CreatedAt != start.Format(time.RFC3339) {
		t.Fatalf("建立时间应取申请开始时刻，得到 %s", p.CreatedAt)
	}
}
