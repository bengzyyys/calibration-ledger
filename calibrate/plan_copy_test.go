package calibrate

import (
	"path/filepath"
	"testing"
)

// TestReturnedRescheduleCannotRewriteOfficial 验证调用方整理 ReschedulePlan
// 返回的计划（改日期、改状态、替换改期原因）不会改写台账里的正式计划：
// 待办仍按正式日期与未结束状态展示，核对仍保留真实改期原因；之后真正提交
// 合法改期时从最后保存的日期产生新记录，此前取得的旧结果保持取得时的内容。
func TestReturnedRescheduleCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-05", Note: "周期校准"})

	p, err := l.ReschedulePlan("P-1", "2026-10-20", "送检排队")
	if err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	// 调用方整理返回结果：改日期、改状态为已取消、替换改期原因与记录。
	p.PlannedDate = "2026-11-01"
	p.Status = PlanStatusCanceled
	p.Changes[0].Reason = "展示用文字"
	p.Changes[0].To = "2026-12-01"
	p.Changes = append(p.Changes, PlanChange{From: "2026-12-01", To: "2026-12-31", Reason: "伪造记录"})

	// 待办仍按正式计划的日期与未结束状态展示。
	todos, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].PlannedDate != "2026-10-20" || todos[0].Marker != TodoFuture {
		t.Fatalf("待办被返回结果改写：%+v", todos)
	}
	// 核对中仍保留真实的改期原因与记录数。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || !r.Plans[0].Open() || r.Plans[0].PlannedDate != "2026-10-20" {
		t.Fatalf("核对中的计划被返回结果改写：%+v", r.Plans)
	}
	if len(r.Plans[0].Changes) != 1 || r.Plans[0].Changes[0].Reason != "送检排队" ||
		r.Plans[0].Changes[0].To != "2026-10-20" {
		t.Fatalf("核对中的改期历史被返回结果改写：%+v", r.Plans[0].Changes)
	}

	// 之前取得的查询结果不受后续操作影响；真正提交合法改期时，
	// 新记录从最后成功保存的日期产生。
	before := r.Plans[0]
	if _, err := l.ReschedulePlan("P-1", "2026-10-25", "再次排队"); err != nil {
		t.Fatalf("第二次改期: %v", err)
	}
	if before.PlannedDate != "2026-10-20" || len(before.Changes) != 1 {
		t.Fatalf("此前取得的核对结果随后续操作变化：%+v", before)
	}
	after, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	if len(after) != 1 || len(after[0].Changes) != 2 {
		t.Fatalf("改期历史应新增一条记录：%+v", after)
	}
	last := after[0].Changes[1]
	if last.From != "2026-10-20" || last.To != "2026-10-25" || last.Reason != "再次排队" {
		t.Fatalf("新改期记录应从最后保存的日期产生：%+v", last)
	}
}

// TestReturnedCreateCannotRewriteOfficial 验证整理 CreatePlan 返回的计划
// 不占用或释放正式计划编号，也不影响每件器具最多一项未结束计划的判断；
// 之后正常保存时整理过的显示内容不会被带入台账文件。
func TestReturnedCreateCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.5)

	p := mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-05", Note: "周期校准"})
	// 调用方整理返回结果：改编号、改器具、改状态。
	p.Number = "P-伪造"
	p.InstrumentID = "M-2"
	p.Status = PlanStatusCanceled

	// 编号占用不变：P-1 仍被占用，伪造编号并不存在。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-06", Note: "n"}); !IsValidation(err) {
		t.Fatalf("计划编号 P-1 仍应被占用，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-伪造", "2026-10-06", "r"); err == nil {
		t.Fatal("伪造编号不应成为可操作的计划")
	}
	// 每件器具最多一项未结束计划的判断不变：M-1 仍有未结束计划，M-2 没有。
	if _, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-10-06", Note: "n"}); !IsValidation(err) {
		t.Fatalf("M-1 已有未结束计划应拒绝，得到 %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-10-06", Note: "n"})

	// 台账因其他正常业务操作保存后，从文件重开仍是正式内容。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	plans, err := reopened.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	if len(plans) != 1 || plans[0].Number != "P-1" || !plans[0].Open() || plans[0].InstrumentID != "M-1" {
		t.Fatalf("整理过的显示内容被带入台账文件：%+v", plans)
	}
}

// TestReturnedCompleteCannotRewriteOfficial 验证整理 CompletePlan 返回的计划
// （含同一证书再次完成返回的原结果）不会改写正式计划：已完成计划保持完成，
// 原证书仍算已用于该计划，不能拿去完成另一项计划。
func TestReturnedCompleteCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-05", Note: "周期校准"})

	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem {
		t.Fatalf("complete: err=%v idem=%v", err, idem)
	}
	// 同一证书再次完成同一计划，返回原结果。
	again, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || !idem {
		t.Fatalf("幂等完成: err=%v idem=%v", err, idem)
	}

	// 整理两份返回结果：改成未完成、换掉证书编号。
	done.Status = PlanStatusOpen
	done.CertificateNumber = "C-伪造"
	again.Status = PlanStatusOpen
	again.CertificateNumber = "C-伪造"

	// 正式计划仍保持完成，不再列入待办。
	todos, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("已完成计划不应列入待办：%+v", todos)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone || r.Plans[0].CertificateNumber != "C-1" {
		t.Fatalf("正式计划被返回结果改写：%+v", r.Plans)
	}

	// 原证书仍算已用于该计划：不能拿去完成另一项计划。
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-1", Date: "2026-10-07", Note: "后续"})
	if _, _, err := l.CompletePlan("P-3", "C-1"); !IsValidation(err) {
		t.Fatalf("证书 C-1 仍应算已用于 P-1，得到 %v", err)
	}
	// 已完成计划不能改用另一证书，也不能因返回结果被当成未完成而改期。
	if _, _, err := l.CompletePlan("P-1", "C-伪造"); !IsValidation(err) {
		t.Fatalf("已完成计划改用另一证书应拒绝，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-10-08", "r"); !IsValidation(err) {
		t.Fatalf("已结束计划不能再改期，得到 %v", err)
	}
}

// TestPlanQueryResultsAreIndependentCopies 验证 Plans 与 Review 返回的计划
// 列表各自独立：调整顺序、删去记录、追加显示内容或整理改期历史，只影响
// 调用方手中的那一份；重新查询仍得到正式内容，此前取得的结果也不变。
func TestPlanQueryResultsAreIndependentCopies(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-05", Note: "周期校准"})
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "送检排队"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}

	first, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	review, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(first) != 1 || len(review.Plans) != 1 {
		t.Fatalf("应各有一项计划：%+v / %+v", first, review.Plans)
	}

	// 整理两份结果：删改记录、追加显示内容、替换改期历史。
	first[0].Changes[0].Reason = "展示用文字"
	first[0].Note = "展示用说明"
	review.Plans[0].PlannedDate = "2026-12-01"
	review.Plans[0].Changes = append(review.Plans[0].Changes, PlanChange{Reason: "伪造"})

	// 重新查询仍得到正式内容，且与此前取得的结果互不影响。
	second, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans again: %v", err)
	}
	if len(second) != 1 || len(second[0].Changes) != 1 ||
		second[0].Changes[0].Reason != "送检排队" || second[0].Note != "周期校准" ||
		second[0].PlannedDate != "2026-10-20" {
		t.Fatalf("重新查询被此前整理影响：%+v", second)
	}
	if first[0].PlannedDate != "2026-10-20" || review.Plans[0].Changes[0].Reason != "送检排队" {
		t.Fatal("两份查询结果共享了底层数据")
	}
}

// TestPlansEmptyResultUnchanged 验证没有计划时仍保持现有空结果表现。
func TestPlansEmptyResultUnchanged(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	plans, err := l.Plans("M-1")
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("没有计划时应为空结果，得到 %+v", plans)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 0 {
		t.Fatalf("没有计划时核对应为空结果，得到 %+v", r.Plans)
	}
}
