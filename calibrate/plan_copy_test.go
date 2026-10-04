package calibrate

import (
	"path/filepath"
	"testing"
)

// findView 从计划视图列表中按编号取出视图，测试辅助。
func findView(t *testing.T, views []PlanView, number string) *PlanView {
	t.Helper()
	for i := range views {
		if views[i].Number == number {
			return &views[i]
		}
	}
	t.Fatalf("计划视图中找不到 %s：%+v", number, views)
	return nil
}

// TestReturnedPlanMutationCannotRewriteOfficial 对应核心场景：一项未结束计划
// 已成功改期，调用方把返回计划改成另一日期、已取消状态并替换改期原因，
// 正式计划的待办日期、未结束状态与真实改期原因都不能被改写；之后真正提交
// 合法改期时从最后保存的日期产生新记录，已取得的旧结果不跟着变化。
func TestReturnedPlanMutationCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	// 10-03 成功改期：10-10 → 10-20。
	clock.t = mustDate(t, "2026-10-03")
	op, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	// 分别取得操作结果之前/之后的计划查询与核对结果。
	queryBefore, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	reviewBefore, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	// 调用方为显示而整理操作结果：改当前日期、状态、改期记录各字段，
	// 追加一条展示用记录并调换次序。
	op.PlannedDate = "2026-11-01"
	op.Status = PlanStatusCanceled
	op.Changes[0].To = "2026-12-01"
	op.Changes[0].ChangedAt = "2099-01-01T00:00:00Z"
	op.Changes[0].Reason = "显示文字"
	op.Changes = append(op.Changes, PlanChange{
		From: "2026-11-01", To: "2026-12-01", ChangedAt: "2099-01-02T00:00:00Z",
		Reason: "展示用追加",
	})
	op.Changes[0], op.Changes[1] = op.Changes[1], op.Changes[0]

	// 待办仍按正式日期 10-20 与未结束状态展示：10-20 当天为“今天需校准”。
	clock.t = mustDate(t, "2026-10-20")
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-20" || items[0].Marker != TodoToday {
		t.Fatalf("待办被返回结果改写：%+v", items)
	}

	// 核对与按器具查询仍保留正式日期、未结束状态和真实改期原因。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	cur := findView(t, r.Plans, "P-1")
	if cur.PlannedDate != "2026-10-20" || cur.Status != PlanStatusOpen ||
		len(cur.Changes) != 1 || cur.Changes[0].Reason != "实验室排期冲突" ||
		cur.Changes[0].To != "2026-10-20" {
		t.Fatalf("核对结果中的正式计划被改写：%+v", cur)
	}
	views, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	qv := findView(t, views, "P-1")
	if qv.PlannedDate != "2026-10-20" || qv.Status != PlanStatusOpen ||
		len(qv.Changes) != 1 || qv.Changes[0].Reason != "实验室排期冲突" {
		t.Fatalf("计划查询被操作返回结果改写：%+v", qv)
	}
	// 此前分别取得的查询、核对结果也没有跟着变化。
	qb := findView(t, queryBefore, "P-1")
	if qb.PlannedDate != "2026-10-20" || len(qb.Changes) != 1 ||
		qb.Changes[0].Reason != "实验室排期冲突" {
		t.Fatalf("整理操作结果影响了此前取得的计划查询：%+v", qb)
	}
	rb := findView(t, reviewBefore.Plans, "P-1")
	if rb.PlannedDate != "2026-10-20" || len(rb.Changes) != 1 ||
		rb.Changes[0].Reason != "实验室排期冲突" {
		t.Fatalf("整理操作结果影响了此前取得的核对结果：%+v", rb)
	}

	// 整理不释放编号：P-1 仍被占用；也不改变每件器具一项未结束计划的判断。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-21", Note: "x"}); !IsValidation(err) {
		t.Fatalf("被“释放”的编号 P-1 仍应占用，得到 %v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-10-21", Note: "x"}); !IsValidation(err) {
		t.Fatalf("被改成已取消的正式计划仍应占位，同器具第二项计划应拒绝，得到 %v", err)
	}

	// 10-21 真正提交合法改期：从最后保存的 10-20 产生新记录，共两条。
	clock.t = mustDate(t, "2026-10-21")
	next, err := l.ReschedulePlan("P-1", "2026-11-05", "改造完成")
	if err != nil {
		t.Fatalf("合法改期应从未结束的正式计划继续：%v", err)
	}
	if next.PlannedDate != "2026-11-05" || len(next.Changes) != 2 ||
		next.Changes[1].From != "2026-10-20" || next.Changes[1].To != "2026-11-05" ||
		next.Changes[1].Reason != "改造完成" {
		t.Fatalf("新改期应从最后保存的日期出发：%+v", next)
	}
	// 旧操作结果继续表示取得时的内容：没有长出新记录，调用方自己的整理保留。
	if len(op.Changes) != 2 || op.Status != PlanStatusCanceled || op.PlannedDate != "2026-11-01" {
		t.Fatalf("已取得的旧结果不应随新操作变化：%+v", op)
	}
	if op.Changes[0].Reason != "展示用追加" {
		t.Fatalf("旧结果中调用方自己的整理被覆盖：%+v", op.Changes)
	}
	if len(qb.Changes) != 1 || qb.Changes[0].Reason != "实验室排期冲突" {
		t.Fatal("新改期不应影响此前取得的查询结果")
	}

	// 其他正常操作写盘后，重开文件仍是正式内容，展示文字不进台账。
	_ = l.SetStatus("M-2", StatusInUse)
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	off := reopened.findPlan("P-1")
	if off.Status != PlanStatusOpen || off.PlannedDate != "2026-11-05" || len(off.Changes) != 2 {
		t.Fatalf("写盘后正式计划被展示内容污染：%+v", off)
	}
	for _, ch := range off.Changes {
		if ch.Reason == "显示文字" || ch.Reason == "展示用追加" {
			t.Fatalf("展示用改期原因进入台账：%+v", off.Changes)
		}
	}
}

// TestSeparatelyFetchedPlanResultsIndependent 验证操作结果、两次按器具查询、
// 两次核对各自独立：整理其中一份的字段或增删调换改期记录，其他份与台账内
// 正式计划都不变化。
func TestSeparatelyFetchedPlanResultsIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "真实原因"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	op, _ := l.ReschedulePlan("P-1", "2026-10-25", "第二次改期")
	q1, _ := l.Plans("M-1")
	q2, _ := l.Plans("M-1")
	r1, _ := l.Review("M-1")
	r2, _ := l.Review("M-1")

	// 整理操作结果：删改改期记录。
	op.Changes[0].Reason = "操作结果改写"
	op.Changes = op.Changes[:1]
	op.PlannedDate = "2030-01-01"
	for _, views := range [][]PlanView{q1, q2, r1.Plans, r2.Plans} {
		v := findView(t, views, "P-1")
		if v.PlannedDate != "2026-10-25" || len(v.Changes) != 2 ||
			v.Changes[0].Reason != "真实原因" {
			t.Fatalf("整理操作结果影响了其他已取得结果：%+v", v)
		}
	}

	// 整理第一份查询：调换并清空改期记录。
	v1 := findView(t, q1, "P-1")
	v1.Changes[0], v1.Changes[1] = v1.Changes[1], v1.Changes[0]
	v1.Changes = nil
	v1.Number = "P-伪造"
	for _, views := range [][]PlanView{q2, r1.Plans, r2.Plans} {
		v := findView(t, views, "P-1")
		if len(v.Changes) != 2 || v.Changes[0].Reason != "真实原因" ||
			v.Changes[1].Reason != "第二次改期" {
			t.Fatalf("整理第一份查询影响了其他结果：%+v", v.Changes)
		}
	}
	if got := l.findPlan("P-1"); got == nil || got.PlannedDate != "2026-10-25" {
		t.Fatalf("整理查询结果回写了台账：%+v", got)
	}

	// 整理第一份核对：追加展示用计划与改期记录，不影响第二份核对与查询。
	r1.Plans[0].Changes = append(r1.Plans[0].Changes, PlanChange{Reason: "核对追加"})
	r1.Plans = append(r1.Plans, PlanView{Plan: Plan{Number: "P-展示"}})
	if len(r2.Plans) != 1 || len(r2.Plans[0].Changes) != 2 {
		t.Fatalf("整理第一份核对影响了第二份核对：%+v", r2.Plans)
	}
	if len(q2) != 1 || len(q2[0].Changes) != 2 {
		t.Fatal("整理核对结果影响了已取得的查询结果")
	}
	if n := len(l.data.Plans); n != 1 {
		t.Fatalf("整理核对结果改变了台账内计划数量：%d", n)
	}
}

// TestReturnedCompletedPlanCannotReopenOrFreeCertificate 验证已完成计划的
// 返回结果被改成未完成或换掉证书编号后，正式计划仍保持完成，原证书仍算
// 已用于该计划；同一证书再次完成返回的原结果同样受保护。
func TestReturnedCompletedPlanCannotReopenOrFreeCertificate(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem {
		t.Fatalf("首次完成应成功且非幂等，err=%v idem=%v", err, idem)
	}
	savedCompletedAt := done.CompletedAt

	// 调用方把完成结果改成未完成、换掉证书编号、清空完成时间。
	done.Status = PlanStatusOpen
	done.CertificateNumber = "C-伪造"
	done.CompletedAt = ""

	// 正式计划仍完成、原证书仍占用：同器具的新计划不能再用 C-1 完成。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-11-10", Note: "二"})
	if _, _, err := l.CompletePlan("P-2", "C-1"); !IsValidation(err) {
		t.Fatalf("被“释放”的 C-1 仍应算已用于 P-1，得到 %v", err)
	}
	// 已完成计划仍不能改期或取消。
	if _, err := l.ReschedulePlan("P-1", "2026-11-01", "x"); !IsValidation(err) {
		t.Fatalf("被改成未完成的返回结果不应重新开放改期，得到 %v", err)
	}
	if _, err := l.CancelPlan("P-1", "x"); !IsValidation(err) {
		t.Fatalf("已完成计划不应能取消，得到 %v", err)
	}
	off := l.findPlan("P-1")
	if off.Status != PlanStatusDone || off.CertificateNumber != "C-1" ||
		off.CompletedAt != savedCompletedAt {
		t.Fatalf("正式完成计划被返回结果改写：%+v", off)
	}
	// 待办不含已完成计划。
	if items, _ := l.Todos(""); len(items) != 1 || items[0].PlanNumber != "P-2" {
		t.Fatalf("待办不应包含被改成未完成的已完成计划：%+v", items)
	}

	// 时钟前进后再次用同一证书完成：仍按正式记录幂等返回原结果、原完成时间，
	// 且这份原结果也是独立副本。
	clock.t = mustDate(t, "2026-10-05")
	again, idem2, err := l.CompletePlan("P-1", "C-1")
	if err != nil || !idem2 {
		t.Fatalf("重复完成应幂等成功，err=%v idem=%v", err, idem2)
	}
	if again.Status != PlanStatusDone || again.CertificateNumber != "C-1" ||
		again.CompletedAt != savedCompletedAt {
		t.Fatalf("幂等返回应是正式原结果：%+v", again)
	}
	again.Status = PlanStatusOpen
	again.CertificateNumber = "C-伪造2"
	if off := l.findPlan("P-1"); off.Status != PlanStatusDone || off.CertificateNumber != "C-1" {
		t.Fatalf("整理幂等返回结果改写了正式计划：%+v", off)
	}
	third, idem3, _ := l.CompletePlan("P-1", "C-1")
	if !idem3 || third.Status != PlanStatusDone || third.CertificateNumber != "C-1" ||
		third.CompletedAt != savedCompletedAt {
		t.Fatalf("再次幂等返回应仍为正式原结果：%+v idem=%v", third, idem3)
	}

	// 写盘重开后仍是完成、原证书归属不变。
	_ = l.SetStatus("M-1", StatusRetired)
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rop := reopened.findPlan("P-1")
	if rop.Status != PlanStatusDone || rop.CertificateNumber != "C-1" ||
		rop.CompletedAt != savedCompletedAt {
		t.Fatalf("写盘后完成计划被污染：%+v", rop)
	}
}

// TestReturnedCanceledPlanStaysCanceled 验证取消结果被改回未完成、清空取消
// 信息后，正式计划仍保持已取消：不列入待办，不能改期或再次取消，编号不释放。
func TestReturnedCanceledPlanStaysCanceled(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})

	canceled, err := l.CancelPlan("P-1", "停产")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	canceled.Status = PlanStatusOpen
	canceled.CanceledAt = ""
	canceled.CancelReason = ""

	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("被改回未完成的取消结果不应重新进入待办：%+v", items)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "x"); !IsValidation(err) {
		t.Fatalf("已取消计划不应能改期，得到 %v", err)
	}
	if _, err := l.CancelPlan("P-1", "x"); !IsValidation(err) {
		t.Fatalf("已取消计划不应能再次取消，得到 %v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "复用"}); !IsValidation(err) {
		t.Fatalf("取消计划编号不应被释放，得到 %v", err)
	}
	off := l.findPlan("P-1")
	if off.Status != PlanStatusCanceled || off.CancelReason != "停产" || off.CanceledAt == "" {
		t.Fatalf("正式取消计划被返回结果改写：%+v", off)
	}
}

// TestReturnedCreatedPlanMutationHasNoOfficialEffect 验证建立计划返回结果的
// 整理不占用或释放正式编号、不改变未结束计划数量判断，也不被后续写盘带入文件。
func TestReturnedCreatedPlanMutationHasNoOfficialEffect(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	created := mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	created.Number = "P-FAKE"
	created.Status = PlanStatusDone
	created.PlannedDate = "2026-12-31"
	created.Changes = []PlanChange{{From: "x", To: "y", Reason: "展示用"}}

	// 正式编号仍占用、伪造编号未占用；M-1 仍有一项未结束计划。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-11", Note: "x"}); !IsValidation(err) {
		t.Fatalf("P-1 仍应被正式占用，得到 %v", err)
	}
	if l.findPlan("P-FAKE") != nil {
		t.Fatal("只改返回结果的编号不应在台账中占用伪造编号")
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-10-11", Note: "x"}); !IsValidation(err) {
		t.Fatalf("M-1 的正式计划仍未结束，第二项应拒绝，得到 %v", err)
	}
	if items, _ := l.Todos(""); len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("被改成已完成的返回结果不应影响待办，得到 %+v", items)
	}

	_ = l.SetStatus("M-1", StatusRetired)
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	off := reopened.findPlan("P-1")
	if off == nil || off.Status != PlanStatusOpen || off.PlannedDate != "2026-10-10" ||
		len(off.Changes) != 0 {
		t.Fatalf("写盘后展示内容被带入台账：%+v", off)
	}
	if reopened.findPlan("P-FAKE") != nil {
		t.Fatal("展示用伪造编号不应进入台账")
	}
}

// TestPlanEmptyResultsStayEmpty 验证没有计划或没有改期历史时维持既有空结果
// 表现，且对空返回的整理不影响台账。
func TestPlanEmptyResultsStayEmpty(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})

	// 没有改期历史：各入口的 Changes 保持空，追加展示记录不回写。
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || len(views[0].Changes) != 0 {
		t.Fatalf("无改期历史时应保持空：%+v err=%v", views, err)
	}
	views[0].Changes = append(views[0].Changes, PlanChange{Reason: "展示用"})
	r, _ := l.Review("M-1")
	if len(r.Plans[0].Changes) != 0 {
		t.Fatal("向空查询追加展示记录改写了核对结果")
	}
	if len(l.findPlan("P-1").Changes) != 0 {
		t.Fatal("向空查询追加展示记录改写了台账")
	}

	// 没有计划的器具：查询与核对中的计划列表保持空。
	mustRegister(t, l, "M-2", "示波器", 1.0)
	if empty, err := l.Plans("M-2"); err != nil || len(empty) != 0 {
		t.Fatalf("无计划器具应返回空计划列表：%+v err=%v", empty, err)
	}
	r2, _ := l.Review("M-2")
	if len(r2.Plans) != 0 {
		t.Fatalf("无计划器具核对中不应出现计划：%+v", r2.Plans)
	}
	if items, err := l.Todos("M-2"); err != nil || len(items) != 0 {
		t.Fatalf("无计划器具应无待办：%+v err=%v", items, err)
	}
}
