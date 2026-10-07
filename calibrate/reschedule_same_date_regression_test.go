package calibrate

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRescheduleSameDateAfterNormalChangeStillRecords 守护一个容易被当成“没有
// 变化”而跳过的既有行为：计划已经有过一次正常改期（最初日期与当前日期不同），
// 再提交与“当前计划日期”完全相同的新日期，只要日期仍不早于本机今天且原因非空，
// 申请就应成功并新增一条改期记录。日期相同不等于没有发生操作：本次提交的原因与
// 操作时间必须留下；新增记录的前后日期都等于当前计划日期，而不是把最初日期当作
// 修改前日期。计划的当前日期、最初日期、说明和未完成状态不变，仍留在待办中，
// 待办标记继续由当前计划日期与查询当天的关系决定；核对中能看到前后日期相同的
// 新记录。器具状态、校准证书与历史使用记录不因改期变化。
func TestRescheduleSameDateAfterNormalChangeStillRecords(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "年度周期校准",
	})

	// 先让器具处于在用、持有一张合格证书并留下一次成功使用记录，改期后这些
	// 内容都不应变化。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-15",
		Expiry: "2027-09-15", Method: "规范A", Error: 0.1, Summary: "例行校准",
	})
	useBefore, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if !useBefore.Allowed {
		t.Fatalf("前置条件：证书合格未到期、器具在用时应允许使用：%+v", useBefore)
	}
	usageCountBefore := len(l.data.Usage)

	// 一次正常改期：10-10 → 10-20，最初日期与当前日期从此不同。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("正常改期应成功：%v", err)
	}
	if len(first.Changes) != 1 {
		t.Fatalf("前置条件：应有一条正常改期记录，得到 %+v", first.Changes)
	}
	firstChange := first.Changes[0]

	// 提交与当前计划日期（10-20）完全相同的新日期：不能因“日期没变”被跳过。
	clock.t = mustDate(t, "2026-10-04")
	same, err := l.ReschedulePlan("P-1", "2026-10-20", "确认维持 10-20 排期")
	if err != nil {
		t.Fatalf("新日期与当前计划日期相同但仍合法时应成功保存改期记录，得到 %v", err)
	}
	if same == nil {
		t.Fatal("同日期改期成功应返回保存后的计划")
	}

	// 计划的当前日期、最初日期、说明与未完成状态都不因这次申请改变。
	if same.PlannedDate != "2026-10-20" {
		t.Fatalf("当前计划日期应保持 2026-10-20，得到 %s", same.PlannedDate)
	}
	if same.OriginalDate != "2026-10-10" {
		t.Fatalf("最初计划日期应保持 2026-10-10，得到 %s", same.OriginalDate)
	}
	if same.Note != "年度周期校准" || !same.Open() {
		t.Fatalf("说明与未完成状态应保持原样，得到 %+v", same)
	}

	// 改期历史新增一条：新增记录的前后日期都等于当前计划日期 10-20，
	// 绝不能把最初日期 10-10 当作修改前日期。
	if len(same.Changes) != 2 {
		t.Fatalf("同日期改期应新增一条记录，共 2 条，得到 %+v", same.Changes)
	}
	if same.Changes[0] != firstChange {
		t.Fatalf("已有改期记录的日期、原因和时间应保持原样：原 %+v 现 %+v",
			firstChange, same.Changes[0])
	}
	added := same.Changes[1]
	if added.From != "2026-10-20" || added.To != "2026-10-20" {
		t.Fatalf("新增记录前后日期都应等于当前计划日期 2026-10-20，得到 %+v", added)
	}
	if added.Reason != "确认维持 10-20 排期" {
		t.Fatalf("新增记录应保留本次提交的原因，得到 %q", added.Reason)
	}
	if want := clock.t.Format(time.RFC3339); added.ChangedAt != want {
		t.Fatalf("新增记录的操作时间应属于本次申请 %s，得到 %s", want, added.ChangedAt)
	}
	if added.ChangedAt == firstChange.ChangedAt {
		t.Fatal("同日期改期的操作时间不能复用上一条记录的时间")
	}

	// 台账内正式存储与返回结果一致。
	saved := l.findPlan("P-1")
	if saved.PlannedDate != "2026-10-20" || saved.OriginalDate != "2026-10-10" ||
		saved.Note != "年度周期校准" || !saved.Open() {
		t.Fatalf("台账内计划字段应保持原样：%+v", saved)
	}
	if len(saved.Changes) != 2 || saved.Changes[0] != firstChange || saved.Changes[1] != added {
		t.Fatalf("台账内改期历史异常：%+v", saved.Changes)
	}

	// 计划仍留在待办：10-04 查询 10-20 为“未到计划日”，标记只由当前计划日期
	// 与查询当天的关系决定。
	items, err := l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-20" || items[0].Marker != TodoFuture {
		t.Fatalf("计划应仍在待办且按 10-20 标记未到计划日：%+v", items)
	}

	// 按器具核对必须看得到这条前后日期相同的新记录，不能因 from == to 被隐藏。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 {
		t.Fatalf("核对中应只有这一项计划，得到 %+v", r.Plans)
	}
	view := r.Plans[0]
	if view.PlannedDate != "2026-10-20" || view.OriginalDate != "2026-10-10" ||
		view.Marker != TodoFuture {
		t.Fatalf("核对中的计划日期与标记异常：%+v", view)
	}
	if len(view.Changes) != 2 || view.Changes[0] != firstChange || view.Changes[1] != added {
		t.Fatalf("核对中应展示前后日期相同的新增记录：%+v", view.Changes)
	}

	// Plans 查询结果与核对一致。
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || len(views[0].Changes) != 2 ||
		views[0].Changes[1] != added {
		t.Fatalf("计划查询应包含同日期改期记录：%+v err=%v", views, err)
	}

	// 到计划日当天，待办与核对标记重新判断为“今天需校准”。
	clock.t = mustDate(t, "2026-10-20")
	items, err = l.Todos("M-1")
	if err != nil || len(items) != 1 || items[0].Marker != TodoToday {
		t.Fatalf("10-20 当天应标记为 %q：%+v err=%v", TodoToday, items, err)
	}
	r, err = l.Review("M-1")
	if err != nil || r.Plans[0].Marker != TodoToday {
		t.Fatalf("核对中 10-20 当天应标记为 %q：%+v err=%v", TodoToday, r, err)
	}

	// 器具状态、校准证书与历史使用记录不因同日期改期变化。
	inst := l.findInstrument("M-1")
	if inst == nil || inst.Status != StatusInUse {
		t.Fatalf("器具状态不应变化：%+v", inst)
	}
	if r.Latest == nil || r.Latest.Number != "C-1" || len(r.History) != 1 {
		t.Fatalf("证书不应因改期变化：latest=%+v history=%+v", r.Latest, r.History)
	}
	if len(l.data.Usage) != usageCountBefore {
		t.Fatalf("历史使用记录数量不应变化：改期前 %d，改期后 %d",
			usageCountBefore, len(l.data.Usage))
	}
}

// TestConsecutiveSameDateReschedulesEachRecorded 验证同一计划连续提交两次合法的
// 同日期改期时，每次都应分别增加一条记录，并按提交顺序保留各自的原因和时间；
// 即使两次原因完全相同，也不能合并为一条，更不能直接返回上次结果。
func TestConsecutiveSameDateReschedulesEachRecorded(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准",
	})

	// 先做一次正常改期，使当前日期停在 10-20。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("正常改期应成功：%v", err)
	}

	// 第一次同日期改期。
	clock.t = mustDate(t, "2026-10-04")
	second, err := l.ReschedulePlan("P-1", "2026-10-20", "同一重申原因")
	if err != nil {
		t.Fatalf("第一次同日期改期应成功：%v", err)
	}
	if len(second.Changes) != 2 {
		t.Fatalf("第一次同日期改期后应有 2 条记录，得到 %+v", second.Changes)
	}
	if &second.Changes[1] == &first.Changes[0] {
		t.Fatal("不应通过返回上次结果来响应新的同日期申请")
	}

	// 第二次同日期改期，原因与第一次完全相同。
	clock.t = mustDate(t, "2026-10-05")
	third, err := l.ReschedulePlan("P-1", "2026-10-20", "同一重申原因")
	if err != nil {
		t.Fatalf("第二次同日期改期应成功：%v", err)
	}
	if len(third.Changes) != 3 {
		t.Fatalf("第二次同日期改期应再增加一条记录，共 3 条，得到 %+v", third.Changes)
	}

	want := []PlanChange{
		{From: "2026-10-10", To: "2026-10-20", Reason: "实验室排期冲突",
			ChangedAt: mustDate(t, "2026-10-03").Format(time.RFC3339)},
		{From: "2026-10-20", To: "2026-10-20", Reason: "同一重申原因",
			ChangedAt: mustDate(t, "2026-10-04").Format(time.RFC3339)},
		{From: "2026-10-20", To: "2026-10-20", Reason: "同一重申原因",
			ChangedAt: mustDate(t, "2026-10-05").Format(time.RFC3339)},
	}
	for i, w := range want {
		if third.Changes[i] != w {
			t.Fatalf("第 %d 条改期记录应为 %+v，得到 %+v", i, w, third.Changes[i])
		}
	}
	// 两条同日期记录原因相同也必须各自保留、互不合并；操作时间按申请次序区分。
	if third.Changes[1].ChangedAt == third.Changes[2].ChangedAt {
		t.Fatal("连续两次同日期改期的操作时间应分别属于各次申请")
	}
	if third.PlannedDate != "2026-10-20" || third.OriginalDate != "2026-10-10" ||
		!third.Open() {
		t.Fatalf("计划日期与状态不应变化：%+v", third)
	}

	// 核对按提交顺序看到全部三条记录，包括两条前后日期相同的记录。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans[0].Changes) != 3 {
		t.Fatalf("核对中应保留全部 3 条改期记录，得到 %+v", r.Plans[0].Changes)
	}
	for i, w := range want {
		if r.Plans[0].Changes[i] != w {
			t.Fatalf("核对中第 %d 条记录应为 %+v，得到 %+v",
				i, w, r.Plans[0].Changes[i])
		}
	}
}

// TestSameDateRescheduleBoundaryAndRejections 覆盖同日期改期的日期边界与两类
// 失败：日期恰好等于本机今天时同日期改期仍可成功；当前计划日期已经早于操作
// 当天时，再提交这个原日期必须按“日期已过”明确拒绝，不能因为日期没有改变
// 而绕过限制；日期合法但原因为空白时同样拒绝，不能生成没有原因的历史记录。
// 两类失败都保留原计划日期与全部改期历史，失败申请的原因和时间不进入核对。
func TestSameDateRescheduleBoundaryAndRejections(t *testing.T) {
	t.Run("日期恰好等于今天", func(t *testing.T) {
		clock := &fakeClock{t: mustDate(t, "2026-10-02")}
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		mustPlan(t, l, PlanInput{
			Number: "P-1", InstrumentID: "M-1", Date: "2026-10-02", Note: "当天计划",
		})

		p, err := l.ReschedulePlan("P-1", "2026-10-02", "当天重申一次")
		if err != nil {
			t.Fatalf("新日期恰好等于本机今天时同日期改期应成功，得到 %v", err)
		}
		if p.PlannedDate != "2026-10-02" || p.OriginalDate != "2026-10-02" {
			t.Fatalf("当前与最初日期都应保持 2026-10-02，得到 %+v", p)
		}
		if len(p.Changes) != 1 || p.Changes[0].From != "2026-10-02" ||
			p.Changes[0].To != "2026-10-02" || p.Changes[0].Reason != "当天重申一次" ||
			p.Changes[0].ChangedAt == "" {
			t.Fatalf("应留下前后日期相同的改期记录：%+v", p.Changes)
		}
		items, err := l.Todos("M-1")
		if err != nil || len(items) != 1 || items[0].Marker != TodoToday {
			t.Fatalf("当天计划应标记为 %q：%+v err=%v", TodoToday, items, err)
		}
		r, err := l.Review("M-1")
		if err != nil || r.Plans[0].Marker != TodoToday ||
			len(r.Plans[0].Changes) != 1 {
			t.Fatalf("核对中当天计划应标记 %q 且展示同日期记录：%+v err=%v",
				TodoToday, r, err)
		}
	})

	t.Run("当前日期已过再提交原日期必须拒绝", func(t *testing.T) {
		clock := &fakeClock{t: mustDate(t, "2026-10-02")}
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		// 先有一条正常改期：10-10 → 10-20，随后时钟走过 10-20。
		mustPlan(t, l, PlanInput{
			Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准",
		})
		clock.t = mustDate(t, "2026-10-03")
		first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
		if err != nil {
			t.Fatalf("前置正常改期应成功：%v", err)
		}

		// 操作当天已晚于当前计划日期：提交与当前日期完全相同的 10-20，
		// 必须按日期已过拒绝，而不是以“日期没有改变”为由放行。
		clock.t = mustDate(t, "2026-10-21")
		p, err := l.ReschedulePlan("P-1", "2026-10-20", "日期没变应可重申")
		if err == nil {
			t.Fatalf("当前计划日期早于操作当天时同日期申请必须被拒绝，得到计划 %+v", p)
		}
		if !IsValidation(err) || !strings.Contains(err.Error(), "不能早于本机今天") {
			t.Fatalf("应明确按日期已过（不能早于本机今天）拒绝，得到 %v", err)
		}
		if p != nil {
			t.Fatalf("业务拒绝不能返回计划结果：%+v", p)
		}

		saved := l.findPlan("P-1")
		if saved.PlannedDate != "2026-10-20" || saved.OriginalDate != "2026-10-10" {
			t.Fatalf("被拒绝后当前与最初日期应保持原样：%+v", saved)
		}
		if len(saved.Changes) != 1 || saved.Changes[0] != first.Changes[0] {
			t.Fatalf("失败申请的原因和时间不能进入改期历史：%+v", saved.Changes)
		}
		// 计划仍留在待办，但当前日期已过，标记为“逾期”。
		items, err := l.Todos("M-1")
		if err != nil || len(items) != 1 || items[0].Marker != TodoOverdue {
			t.Fatalf("过期未改的计划应标记为 %q：%+v err=%v", TodoOverdue, items, err)
		}
		r, err := l.Review("M-1")
		if err != nil || r.Plans[0].Marker != TodoOverdue ||
			len(r.Plans[0].Changes) != 1 {
			t.Fatalf("核对中应保持逾期标记且失败申请不入历史：%+v err=%v", r, err)
		}
	})

	t.Run("合法日期但原因空白必须拒绝", func(t *testing.T) {
		clock := &fakeClock{t: mustDate(t, "2026-10-02")}
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		mustPlan(t, l, PlanInput{
			Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准",
		})
		clock.t = mustDate(t, "2026-10-03")
		first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
		if err != nil {
			t.Fatalf("前置正常改期应成功：%v", err)
		}

		// 日期合法（恰为当前日期、未早于今天）但原因为空白：拒绝且不留记录。
		for _, blank := range []string{"", "   ", "\t"} {
			p, err := l.ReschedulePlan("P-1", "2026-10-20", blank)
			if err == nil {
				t.Fatalf("空白原因（%q）必须被拒绝，得到计划 %+v", blank, p)
			}
			if !IsValidation(err) {
				t.Fatalf("空白原因应是业务校验错误，得到 %v", err)
			}
			if p != nil {
				t.Fatalf("业务拒绝不能返回计划结果：%+v", p)
			}
		}

		saved := l.findPlan("P-1")
		if saved.PlannedDate != "2026-10-20" || saved.OriginalDate != "2026-10-10" {
			t.Fatalf("被拒绝后计划日期应保持原样：%+v", saved)
		}
		if len(saved.Changes) != 1 || saved.Changes[0] != first.Changes[0] {
			t.Fatalf("不能生成没有原因的历史记录：%+v", saved.Changes)
		}
		r, err := l.Review("M-1")
		if err != nil || len(r.Plans[0].Changes) != 1 {
			t.Fatalf("核对中不应出现失败申请：%+v err=%v", r, err)
		}
		// 未知计划仍按不存在处理，与同日期逻辑无关。
		if _, err := l.ReschedulePlan("NOPE", "2026-10-20", "原因"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("未知计划应报 ErrNotFound，得到 %v", err)
		}
	})
}
