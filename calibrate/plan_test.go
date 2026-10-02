package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 建立计划的基础输入：器具 M-1，计划日期 2026-10-10。
func basePlanInput() CreatePlanInput {
	return CreatePlanInput{
		InstrumentID: "M-1", Number: "P-1", PlanDate: "2026-10-10", Description: "例行校准",
	}
}

func TestCreatePlanValidation(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 0.2)

	valid := func(mut func(*CreatePlanInput)) CreatePlanInput {
		c := basePlanInput()
		mut(&c)
		return c
	}
	bad := []struct {
		name string
		in   CreatePlanInput
	}{
		{"未知器具", valid(func(c *CreatePlanInput) { c.InstrumentID = "X-9" })},
		{"空白计划编号", valid(func(c *CreatePlanInput) { c.Number = "   " })},
		{"空白说明", valid(func(c *CreatePlanInput) { c.Description = "\t" })},
		{"不存在的日期2月30日", valid(func(c *CreatePlanInput) { c.PlanDate = "2026-02-30" })},
		{"不存在的月份13月", valid(func(c *CreatePlanInput) { c.PlanDate = "2026-13-01" })},
		{"格式不符", valid(func(c *CreatePlanInput) { c.PlanDate = "2026-10-1" })},
		{"计划日期早于今天", valid(func(c *CreatePlanInput) { c.PlanDate = "2026-10-01" })},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			n := len(l.data.Plans)
			_, err := l.CreatePlan(tc.in)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if tc.name == "未知器具" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("未知器具应报 ErrNotFound，得到 %v", err)
				}
			} else if !IsValidation(err) {
				t.Fatalf("用例 %q 应返回校验错误，得到 %v", tc.name, err)
			}
			if len(l.data.Plans) != n {
				t.Fatalf("用例 %q 留下了计划记录", tc.name)
			}
		})
	}

	// 计划日期等于本机今天是允许的。
	todayPlan := valid(func(c *CreatePlanInput) { c.PlanDate = "2026-10-02" })
	if _, err := l.CreatePlan(todayPlan); err != nil {
		t.Fatalf("计划日期为今天应被接受: %v", err)
	}

	// 同一件器具不能再有第二项未完成计划。
	_, err := l.CreatePlan(valid(func(c *CreatePlanInput) { c.Number = "P-2"; c.PlanDate = "2026-11-01" }))
	if !IsValidation(err) || !strings.Contains(err.Error(), "已有未完成的计划") {
		t.Fatalf("同器具第二项未完成计划应被拒绝，得到 %v", err)
	}

	// 不同器具可以各自建立计划。
	other := basePlanInput()
	other.InstrumentID = "M-2"
	other.Number = "P-3"
	if _, err := l.CreatePlan(other); err != nil {
		t.Fatalf("不同器具应能建立计划: %v", err)
	}
}

func TestPlanNumberUniqueEvenAfterEnded(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	p, err := l.CreatePlan(basePlanInput())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 改期后计划仍未结束，编号当然不能重复。
	if _, err := l.ReschedulePlan(p.Number, "2026-10-20", "延期"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	dup := basePlanInput()
	if _, err := l.CreatePlan(dup); !IsValidation(err) {
		t.Fatalf("未结束计划的编号重复应被拒绝，得到 %v", err)
	}
	// 取消后计划结束，原编号仍不能再次使用。
	if _, err := l.CancelPlan(p.Number, "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := l.CreatePlan(dup); !IsValidation(err) || !strings.Contains(err.Error(), "不能重复使用") {
		t.Fatalf("已结束计划的编号也不能重复使用，得到 %v", err)
	}
	// 器具没有未完成计划后，可以建立新计划（新编号）。
	dup.Number = "P-2"
	if _, err := l.CreatePlan(dup); err != nil {
		t.Fatalf("结束旧计划后应能建立新计划: %v", err)
	}

	// 已完成计划的编号同样不能重复使用。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-2", "C-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	dup.Number = "P-2"
	if _, err := l.CreatePlan(dup); !IsValidation(err) {
		t.Fatalf("已完成计划的编号不能重复使用，得到 %v", err)
	}
}

func TestReschedulePlan(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 空白原因、早于今天的新日期、找不到的计划：一律拒绝且不改动记录。
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "   "); !IsValidation(err) {
		t.Fatalf("空白原因应被拒绝，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-01", "提前"); !IsValidation(err) {
		t.Fatalf("早于操作当天的日期应被拒绝，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-X", "2026-10-20", "找不着"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("找不到的计划应报 ErrNotFound，得到 %v", err)
	}
	before := l.findPlan("P-1")
	if before.PlanDate != "2026-10-10" || len(before.Changes) != 0 {
		t.Fatal("失败的改期不应改动记录")
	}

	// 新日期等于操作当天是允许的。
	if _, err := l.ReschedulePlan("P-1", "2026-10-02", "改到今天"); err != nil {
		t.Fatalf("改期到当天应被接受: %v", err)
	}

	// 再次改期：历史累积，每次都保留前后日期、操作时间与原因。
	clock.t = mustDate(t, "2026-10-03")
	p, err := l.ReschedulePlan("P-1", "2026-10-20", "设备延期")
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if p.PlanDate != "2026-10-20" {
		t.Fatalf("计划日期未更新，得到 %s", p.PlanDate)
	}
	if len(p.Changes) != 2 {
		t.Fatalf("应保留 2 次改期记录，得到 %d 次", len(p.Changes))
	}
	first := p.Changes[0]
	if first.OldDate != "2026-10-10" || first.NewDate != "2026-10-02" ||
		first.Reason != "改到今天" || first.At == "" {
		t.Fatalf("第一次改期记录不完整: %+v", first)
	}
	second := p.Changes[1]
	if second.OldDate != "2026-10-02" || second.NewDate != "2026-10-20" ||
		second.Reason != "设备延期" || second.At == "" {
		t.Fatalf("第二次改期记录不完整: %+v", second)
	}

	// 取消后不能再改期。
	if _, err := l.CancelPlan("P-1", "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-30", "还想改"); !IsValidation(err) {
		t.Fatalf("已取消计划不能改期，得到 %v", err)
	}
}

func TestCancelPlan(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := l.CancelPlan("P-1", ""); !IsValidation(err) {
		t.Fatalf("空白原因应被拒绝，得到 %v", err)
	}
	if _, err := l.CancelPlan("P-X", "找不着"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("找不到的计划应报 ErrNotFound，得到 %v", err)
	}

	clock.t = mustDate(t, "2026-10-05")
	p, err := l.CancelPlan("P-1", "设备故障停用")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if p.Status != PlanCancelled {
		t.Fatalf("状态应为已取消，得到 %s", p.Status)
	}
	if p.PlanDate != "2026-10-10" {
		t.Fatalf("取消应保留原计划日期，得到 %s", p.PlanDate)
	}
	if p.CancelReason != "设备故障停用" || p.CancelledAt == "" {
		t.Fatalf("取消时间与原因应保留: %+v", p)
	}

	// 取消后不再列入待办。
	todos, err := l.TodoPlans("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("已取消计划不应列入待办，得到 %d 项", len(todos))
	}

	// 已取消计划不能再次取消、改期或完成。
	if _, err := l.CancelPlan("P-1", "再取消"); !IsValidation(err) {
		t.Fatalf("已取消计划不能再次取消，得到 %v", err)
	}
	if _, _, err := l.CompletePlan("P-1", "C-1"); !IsValidation(err) {
		t.Fatalf("已取消计划不能完成，得到 %v", err)
	}
}

func TestCompletePlan(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	// C-1 校准日期早于计划建立日期；C-2 为计划建立当天；C-3 属于其他器具。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.1, Summary: "s",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-3", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})

	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 不存在的证书。
	if _, _, err := l.CompletePlan("P-1", "C-X"); !IsValidation(err) {
		t.Fatalf("不存在的证书应被拒绝，得到 %v", err)
	}
	// 证书属于其他器具。
	if _, _, err := l.CompletePlan("P-1", "C-3"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "不一致") {
		t.Fatalf("其他器具的证书应被拒绝，得到 %v", err)
	}
	// 校准日期早于计划最初建立日期。
	if _, _, err := l.CompletePlan("P-1", "C-1"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "最初建立日期") {
		t.Fatalf("早于计划建立日期的证书应被拒绝，得到 %v", err)
	}
	// 空白证书编号。
	if _, _, err := l.CompletePlan("P-1", "  "); !IsValidation(err) {
		t.Fatalf("空白证书编号应被拒绝，得到 %v", err)
	}
	// 找不到的计划。
	if _, _, err := l.CompletePlan("P-X", "C-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("找不到的计划应报 ErrNotFound，得到 %v", err)
	}
	// 以上失败都不应改动计划。
	if p := l.findPlan("P-1"); p.Status != PlanActive {
		t.Fatalf("失败的完成不应改动计划，得到状态 %s", p.Status)
	}

	// 符合条件的证书（校准日期不早于计划建立日期、器具一致）完成计划。
	p, duplicate, err := l.CompletePlan("P-1", "C-2")
	if err != nil || duplicate {
		t.Fatalf("符合条件的证书应能完成计划，err=%v duplicate=%v", err, duplicate)
	}
	if p.Status != PlanCompleted || p.CertificateNumber != "C-2" || p.CompletedAt == "" {
		t.Fatalf("完成后应保存完成时间与证书编号: %+v", p)
	}

	// 再次用同一证书完成同一计划：幂等返回，不刷新完成时间。
	completedAt := p.CompletedAt
	again, dup, err := l.CompletePlan("P-1", "C-2")
	if err != nil || !dup {
		t.Fatalf("重复完成应幂等返回，err=%v dup=%v", err, dup)
	}
	if again.CompletedAt != completedAt {
		t.Fatalf("重复完成不应刷新完成时间：%s → %s", completedAt, again.CompletedAt)
	}

	// 已完成计划改用其他证书：拒绝。
	if _, _, err := l.CompletePlan("P-1", "C-1"); !IsValidation(err) {
		t.Fatalf("已完成计划改用其他证书应被拒绝，得到 %v", err)
	}

	// P-1 已结束，建立 P-2（建立日期仍为 2026-10-02，C-2 日期条件满足）。
	other := basePlanInput()
	other.Number = "P-2"
	other.PlanDate = "2026-11-01"
	if _, err := l.CreatePlan(other); err != nil {
		t.Fatalf("create second plan: %v", err)
	}
	// C-2 已用于完成 P-1，不能重复用于 P-2（日期、器具条件均满足）。
	if _, _, err := l.CompletePlan("P-2", "C-2"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "重复用于") {
		t.Fatalf("已用于完成的证书不能重复完成其他计划，得到 %v", err)
	}
	// C-1 校准日期早于 P-2 建立日期，同样被拒。
	if _, _, err := l.CompletePlan("P-2", "C-1"); !IsValidation(err) {
		t.Fatalf("早于计划建立日期的证书应被拒绝，得到 %v", err)
	}

	// 时钟拨到 2026-10-05 后补录超差证书 C-4（校准日期 2026-10-05，
	// 与 C-2 不是同一天）。超差证书也能完成计划：校准工作已完成，
	// 但器具是否可用仍按现有状态、最近证书结论与有效期判断。
	clock.t = mustDate(t, "2026-10-05")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-4", CalDate: "2026-10-05",
		Expiry: "2027-10-05", Method: "m", Error: 5, Summary: "超差",
	})
	p2, dup2, err := l.CompletePlan("P-2", "C-4")
	if err != nil || dup2 {
		t.Fatalf("超差证书应能完成计划，err=%v dup2=%v", err, dup2)
	}
	if p2.Status != PlanCompleted || p2.CertificateNumber != "C-4" {
		t.Fatalf("超差证书完成后应保存证书编号: %+v", p2)
	}
	// 已完成计划再改用其他证书：拒绝。
	if _, _, err := l.CompletePlan("P-2", "C-2"); !IsValidation(err) {
		t.Fatalf("已完成计划改用其他证书应被拒绝，得到 %v", err)
	}

	// 已取消的计划不能完成。
	other.Number = "P-3"
	other.PlanDate = "2026-12-01"
	if _, err := l.CreatePlan(other); err != nil {
		t.Fatalf("create third plan: %v", err)
	}
	if _, err := l.CancelPlan("P-3", "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, err := l.CompletePlan("P-3", "C-4"); !IsValidation(err) {
		t.Fatalf("已取消计划不能完成，得到 %v", err)
	}
}

func TestTodoPlansOrderingAndLabels(t *testing.T) {
	// 计划在 2026-09-25 建立（日期 10-01/10-02/10-05 均不早于当天），
	// 查询时把时钟拨到 2026-10-02，以验证逾期/今天/未到的标记。
	// P-2、P-4 同在 10-01 但分属不同器具，用于验证同日按器具编号排序。
	clock := &fakeClock{t: mustDate(t, "2026-09-25")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 0.2)
	mustRegister(t, l, "M-3", "信号源", 0.3)
	mustRegister(t, l, "M-4", "频谱仪", 0.4)

	mkplan := func(num, inst, date string) {
		t.Helper()
		in := basePlanInput()
		in.Number = num
		in.InstrumentID = inst
		in.PlanDate = date
		if _, err := l.CreatePlan(in); err != nil {
			t.Fatalf("create %s: %v", num, err)
		}
	}
	mkplan("P-1", "M-1", "2026-10-02") // 查询日当天 → 今天需校准
	mkplan("P-2", "M-2", "2026-10-01") // 已过 → 逾期
	mkplan("P-3", "M-3", "2026-10-05") // 未来 → 未到计划日
	mkplan("P-4", "M-4", "2026-10-01") // 逾期（同日，按器具编号排在 M-2 之后）

	clock.t = mustDate(t, "2026-10-02")
	todos, err := l.TodoPlans("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	wantOrder := []string{"P-2", "P-4", "P-1", "P-3"}
	if len(todos) != len(wantOrder) {
		t.Fatalf("待办应为 %d 项，得到 %d", len(wantOrder), len(todos))
	}
	for i, num := range wantOrder {
		if todos[i].Number != num {
			t.Fatalf("排序第 %d 项应为 %s，得到 %s", i, num, todos[i].Number)
		}
	}
	labels := map[string]string{}
	for _, p := range todos {
		labels[p.Number] = p.DueLabel
		if p.InstrumentName == "" || p.InstrumentStatus == "" {
			t.Fatalf("待办应显示器具名称与状态: %+v", p)
		}
	}
	if labels["P-1"] != "今天需校准" || labels["P-2"] != "逾期" ||
		labels["P-4"] != "逾期" || labels["P-3"] != "未到计划日" {
		t.Fatalf("到期标记错误: %v", labels)
	}

	// 按器具编号筛选。
	m1, err := l.TodoPlans("M-1")
	if err != nil {
		t.Fatalf("filter todos: %v", err)
	}
	if len(m1) != 1 || m1[0].Number != "P-1" {
		t.Fatalf("按器具筛选结果错误: %+v", m1)
	}
	if _, err := l.TodoPlans("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按未知器具筛选应报 ErrNotFound，得到 %v", err)
	}

	// 查询不产生使用记录，也不改动计划。
	before := len(l.UsageRecords())
	if _, err := l.TodoPlans(""); err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(l.UsageRecords()) != before {
		t.Fatal("待办查询不应产生使用记录")
	}

	// 完成后不再列入待办；全部结束后返回空切片（明确无待办）。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	rest, _ := l.TodoPlans("")
	if len(rest) != 3 {
		t.Fatalf("完成后待办应剩 3 项，得到 %d", len(rest))
	}
	if _, err := l.CancelPlan("P-3", "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	none, _ := l.TodoPlans("M-1")
	if len(none) != 0 {
		t.Fatalf("无待办应返回空结果，得到 %d 项", len(none))
	}
}

func TestReviewIncludesPlans(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})

	// P-1 建立后改期，随后取消（保留改期记录）。
	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "延期"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if _, err := l.CancelPlan("P-1", "取消"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// 旧计划结束后才能建立 P-2 并完成。
	other := basePlanInput()
	other.Number = "P-2"
	other.PlanDate = "2026-11-01"
	if _, err := l.CreatePlan(other); err != nil {
		t.Fatalf("create P-2: %v", err)
	}
	if _, _, err := l.CompletePlan("P-2", "C-1"); err != nil {
		t.Fatalf("complete P-2: %v", err)
	}

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 2 {
		t.Fatalf("核对应展示 2 项计划，得到 %d", len(r.Plans))
	}
	byNum := map[string]Plan{}
	for _, p := range r.Plans {
		byNum[p.Number] = p
	}
	// P-1 已取消，带改期记录。
	p1 := byNum["P-1"]
	if p1.Status != PlanCancelled || len(p1.Changes) != 1 ||
		p1.Changes[0].OldDate != "2026-10-10" || p1.Changes[0].NewDate != "2026-10-20" {
		t.Fatalf("已取消计划应带改期记录: %+v", p1)
	}
	// P-2 已完成，带关联证书编号。
	p2 := byNum["P-2"]
	if p2.Status != PlanCompleted || p2.CertificateNumber != "C-1" || p2.CompletedAt == "" {
		t.Fatalf("已完成计划应带证书编号与完成时间: %+v", p2)
	}
}

func TestRetiredInstrumentPlanStillOperable(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("retire: %v", err)
	}
	// 停用不自动取消计划：待办中仍可见。
	todos, _ := l.TodoPlans("M-1")
	if len(todos) != 1 {
		t.Fatalf("停用后计划不应自动取消，得到 %d 项", len(todos))
	}
	// 停用器具的计划仍可改期、取消。
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "停用后改期"); err != nil {
		t.Fatalf("停用器具的计划应可改期: %v", err)
	}
	if _, err := l.CancelPlan("P-1", "停用后取消"); err != nil {
		t.Fatalf("停用器具的计划应可取消: %v", err)
	}
}

func TestPlanPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	clock := func() time.Time { return mustDate(t, "2026-10-02") }

	l, err := openAt(path, clock)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, err := l.CreatePlan(basePlanInput()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "延期"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	if _, _, err := l.CompletePlan("P-1", "C-1"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// 重开后计划及改期、完成历史保留。
	reopened, err := openAt(path, clock)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review after reopen: %v", err)
	}
	if len(r.Plans) != 1 {
		t.Fatalf("重开后计划丢失，得到 %d 项", len(r.Plans))
	}
	p := r.Plans[0]
	if p.Status != PlanCompleted || p.CertificateNumber != "C-1" ||
		p.PlanDate != "2026-10-20" || len(p.Changes) != 1 {
		t.Fatalf("重开后计划历史不完整: %+v", p)
	}

	// 旧台账（无 plans 字段）按无计划处理，其余数据不受影响。
	oldPath := filepath.Join(dir, "old.json")
	oldLedger := `{
  "version": 1,
  "instruments": [{"id":"M-9","name":"旧器具","allowed_error":0.3,"status":"在用","registered_at":"2026-01-01T00:00:00+08:00"}],
  "certificates": [],
  "usage": []
}`
	if err := os.WriteFile(oldPath, []byte(oldLedger), 0o644); err != nil {
		t.Fatalf("write old ledger: %v", err)
	}
	old, err := openAt(oldPath, clock)
	if err != nil {
		t.Fatalf("open old ledger: %v", err)
	}
	todos, err := old.TodoPlans("")
	if err != nil {
		t.Fatalf("todos on old ledger: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("旧台账应按无计划处理，得到 %d 项", len(todos))
	}
	if _, err := old.CreatePlan(CreatePlanInput{
		InstrumentID: "M-9", Number: "P-1", PlanDate: "2026-10-10", Description: "新计划",
	}); err != nil {
		t.Fatalf("在旧台账上建立计划应成功: %v", err)
	}
}
