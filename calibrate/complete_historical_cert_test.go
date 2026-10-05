package calibrate

import (
	"strings"
	"testing"
)

// 本文件为“用历史证书完成校准计划”补充回归保障，重点保住两个各自独立的含义：
//
//  1. 计划关联证书：只要求证书属于该器具、校准日期不早于计划最初建立的本机
//     日期、且未用于完成其他计划——证书是不是“最近证书”、本身合格还是超差，
//     都不能成为额外的完成或拒绝理由；
//  2. 器具最近证书：永远按校准日期取最新的一张，器具能否使用只看状态、这张
//     最近证书的结论与有效期。计划关联了一张合格的历史证书，不会让最近证书
//     超差的器具变成可用；计划关联了一张超差历史证书，也不会连累最近证书
//     合格的器具。
//
// 典型顺序：先登记校准日期较新的证书，再补录较早的一次校准，用较早的历史
// 证书结束计划建立时就存在的计划。

const (
	histNewerCal = "2026-09-20"
	histOlderCal = "2026-09-10"
	histNewerExp = "2027-09-20"
	histOlderExp = "2027-09-10"
)

// setupHistoricalBackfillLedger 构造“计划先建立、证书后补录”的现场：
// 计划 2026-09-01 建立（计划日期 2026-10-10），器具在用；时钟拨到
// 2026-09-25 作为录入两张证书、完成计划与核对申请共同的“当天”。
func setupHistoricalBackfillLedger(t *testing.T) *Ledger {
	t.Helper()
	clock := &fakeClock{t: mustDate(t, "2026-09-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("切换在用: %v", err)
	}
	mustPlan(t, l, PlanInput{
		Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准",
	})
	clock.t = mustDate(t, "2026-09-25")
	return l
}

// assertCompletionDoesNotMutateBusinessData 核对完成动作只结束计划：
// 器具允许误差与状态、两张证书的测得误差和有效期都保持完成前的值。
func assertCompletionDoesNotMutateBusinessData(t *testing.T, l *Ledger) {
	t.Helper()
	inst := l.findInstrument("M-1")
	if inst.AllowedError != 0.5 || inst.Status != StatusInUse {
		t.Fatalf("完成计划不应改动器具允许误差或状态：%+v", inst)
	}
	newer := l.findCertificate("C-NEW")
	older := l.findCertificate("C-OLD")
	if newer == nil || older == nil {
		t.Fatalf("两张证书都应仍在台账中：newer=%v older=%v", newer, older)
	}
	if newer.CalDate != histNewerCal || newer.Expiry != histNewerExp {
		t.Fatalf("较新证书日期/有效期被改动：%+v", newer)
	}
	if older.CalDate != histOlderCal || older.Expiry != histOlderExp {
		t.Fatalf("较早证书日期/有效期被改动：%+v", older)
	}
}

// assertHistoryNewestFirst 核对历史证书仍按校准日期由近到远展示。
func assertHistoryNewestFirst(t *testing.T, history []CertificateView) {
	t.Helper()
	if len(history) != 2 {
		t.Fatalf("应有两张历史证书，得到 %d", len(history))
	}
	if history[0].Number != "C-NEW" || history[0].CalDate != histNewerCal {
		t.Fatalf("历史第一张应是校准日期较新的 C-NEW：%+v", history[0])
	}
	if history[1].Number != "C-OLD" || history[1].CalDate != histOlderCal {
		t.Fatalf("历史第二张应是补录的较早 C-OLD：%+v", history[1])
	}
}

// TestCompleteWithOlderPassingCertButNewerFailing 主场景：较早历史证书合格、
// 较新证书超差。用较早证书可以完成计划，但最近证书仍是较新的超差证书，
// 器具仍不能使用，拒绝原因必须指向较新证书。
func TestCompleteWithOlderPassingCertButNewerFailing(t *testing.T) {
	l := setupHistoricalBackfillLedger(t)

	// 先登记校准日期较新的超差证书，再补录较早的合格证书；两张证书截止日
	// 都晚于核对当天（2026-09-25）。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: histNewerCal,
		Expiry: histNewerExp, Method: "规范A", Error: 0.8, Summary: "较新超差",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: histOlderCal,
		Expiry: histOlderExp, Method: "规范A", Error: 0.1, Summary: "较早合格",
	})

	// 仅录入两张证书不会自动完成计划：计划保持未完成并留在待办。
	if p := l.findPlan("P-1"); p.Status != PlanStatusOpen ||
		p.CompletedAt != "" || p.CertificateNumber != "" {
		t.Fatalf("仅录入证书不应完成计划：%+v", p)
	}
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("未完成计划应留在待办：%+v", items)
	}

	// 用较早（非最近）的合格证书完成：它不是最近证书不构成拒绝理由。
	done, idem, err := l.CompletePlan("P-1", "C-OLD")
	if err != nil || idem {
		t.Fatalf("较早历史证书应能完成计划，err=%v idem=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" ||
		done.CompletedAt == "" {
		t.Fatalf("完成结果应保留历史证书编号和完成时间：%+v", done)
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后计划应从待办移除：%+v", items)
	}
}

// TestCompleteOlderPassingKeepsNewerFailingJudgement 承接上一场景，核对
// “计划已关联合格历史证书”不改变最近证书超差的使用判定与留痕。
func TestCompleteOlderPassingKeepsNewerFailingJudgement(t *testing.T) {
	l := setupHistoricalBackfillLedger(t)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: histNewerCal,
		Expiry: histNewerExp, Method: "规范A", Error: 0.8, Summary: "较新超差",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: histOlderCal,
		Expiry: histOlderExp, Method: "规范A", Error: 0.1, Summary: "较早合格",
	})
	done, _, err := l.CompletePlan("P-1", "C-OLD")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// 完成动作只结束计划，不更改器具与证书数据。
	assertCompletionDoesNotMutateBusinessData(t, l)

	// 按器具核对：计划显示已完成并保留 C-OLD 与完成时间；最近证书仍是
	// 校准日期较新的超差 C-NEW，器具不能使用，原因指向 C-NEW 而非 C-OLD。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-OLD" ||
		r.Plans[0].CompletedAt != done.CompletedAt {
		t.Fatalf("核对中的计划完成信息异常：%+v", r.Plans)
	}
	assertHistoryNewestFirst(t, r.History)
	if r.Latest == nil || r.Latest.Number != "C-NEW" || r.Latest.Pass {
		t.Fatalf("最近证书必须仍是较新的超差 C-NEW：%+v", r.Latest)
	}
	if r.CanUse {
		t.Fatalf("计划关联合格历史证书不应使器具可以使用：%v", r.Reasons)
	}
	if !containsReason(r.Reasons, "超差") || !containsReason(r.Reasons, "C-NEW") {
		t.Fatalf("拒绝原因应针对较新证书 C-NEW 超差：%v", r.Reasons)
	}
	if containsReason(r.Reasons, "C-OLD") {
		t.Fatalf("拒绝原因不应牵涉计划关联的合格历史证书 C-OLD：%v", r.Reasons)
	}

	// 随后申请使用：被拒绝并按较新证书超差留痕，不能因为计划关联了合格证
	// 而获准。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") ||
		!containsReason(d.Reasons, "C-NEW") || containsReason(d.Reasons, "C-OLD") {
		t.Fatalf("使用申请应因 C-NEW 超差被拒绝：allowed=%v reasons=%v",
			d.Allowed, d.Reasons)
	}
	r2, _ := l.Review("M-1")
	if len(r2.Rejections) != 1 {
		t.Fatalf("应留下 1 条拒绝留痕，得到 %d", len(r2.Rejections))
	}
	frozen := r2.Rejections[0]
	if frozen.Allowed || !containsReason(frozen.Reasons, "C-NEW") ||
		!containsReason(frozen.Reasons, "超差") {
		t.Fatalf("拒绝留痕应冻结较新证书超差的原因：%+v", frozen)
	}

	// 同证书重复完成同一计划：幂等返回原结果，完成时间不变。
	again, idem, err := l.CompletePlan("P-1", "C-OLD")
	if err != nil || !idem {
		t.Fatalf("同证书重复完成应幂等成功，err=%v idem=%v", err, idem)
	}
	if again.CompletedAt != done.CompletedAt || again.CertificateNumber != "C-OLD" {
		t.Fatalf("重复完成应返回原证书编号与完成时间：%+v", again)
	}
}

// TestCompleteWithOlderFailingCertButNewerPassing 反方向：较早历史证书超差、
// 较新证书合格且未到期。超差历史证书同样可以完成计划；核对与使用申请仍以
// 较新证书为准，申请获准并留痕。
func TestCompleteWithOlderFailingCertButNewerPassing(t *testing.T) {
	l := setupHistoricalBackfillLedger(t)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: histNewerCal,
		Expiry: histNewerExp, Method: "规范A", Error: -0.1, Summary: "较新合格",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: histOlderCal,
		Expiry: histOlderExp, Method: "规范A", Error: 5, Summary: "较早超差",
	})

	// 较早的超差证书同样可以完成符合条件的计划；超差只说明证书结论，
	// 不代表校准工作未完成。
	done, idem, err := l.CompletePlan("P-1", "C-OLD")
	if err != nil || idem {
		t.Fatalf("较早超差历史证书也应能完成计划，err=%v idem=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" ||
		done.CompletedAt == "" {
		t.Fatalf("完成结果异常：%+v", done)
	}
	assertCompletionDoesNotMutateBusinessData(t, l)
	if c := l.findCertificate("C-OLD"); c.Error != 5 {
		t.Fatalf("完成计划不应改写历史证书的测得误差：%+v", c)
	}

	// 核对以较新证书为准：最近证书是合格未到期的 C-NEW，器具可以使用；
	// 历史按校准日期由近到远，较早的 C-OLD 仍显示超差。
	r, _ := l.Review("M-1")
	if r.Latest == nil || r.Latest.Number != "C-NEW" || !r.Latest.Pass || r.Latest.Expired {
		t.Fatalf("最近证书应是合格未到期的 C-NEW：%+v", r.Latest)
	}
	assertHistoryNewestFirst(t, r.History)
	verdicts := map[string]bool{}
	for _, h := range r.History {
		verdicts[h.Number] = h.Pass
		if h.Expired {
			t.Fatalf("证书 %s 截止日晚于核对当天，不应到期", h.Number)
		}
	}
	if verdicts["C-NEW"] != true || verdicts["C-OLD"] != false {
		t.Fatalf("两张证书应各自保留结论：%v", verdicts)
	}
	if !r.CanUse || len(r.Reasons) != 0 {
		t.Fatalf("较新证书合格未到期时应可以使用：canUse=%v reasons=%v",
			r.CanUse, r.Reasons)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-OLD" {
		t.Fatalf("计划仍应关联较早的超差证书：%+v", r.Plans)
	}

	// 使用申请获准并留痕。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if !d.Allowed || len(d.Reasons) != 0 {
		t.Fatalf("应以较新合格证书为准获准使用：allowed=%v reasons=%v",
			d.Allowed, d.Reasons)
	}
	recs := l.UsageRecords()
	last := recs[len(recs)-1]
	if last.InstrumentID != "M-1" || !last.Allowed || last.RequestedAt == "" {
		t.Fatalf("获准使用应留痕：%+v", last)
	}
}

// TestCompleteHistoricalCertDateBoundary 日期边界围绕所选历史证书：
// 校准日期等于计划最初建立的本机日期可完成；早一天明确拒绝，计划继续
// 未完成并留在待办，不能写入完成时间或关联证书。
func TestCompleteHistoricalCertDateBoundary(t *testing.T) {
	// 等于建立日期：可以完成，即使该证书不是最近证书（随后再补更晚证书）。
	equal := setupHistoricalCreatedAt(t, "2026-09-01")
	mustAddCert(t, equal, CertificateInput{
		InstrumentID: "M-1", Number: "C-EQ", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.1, Summary: "当天校准",
	})
	done, _, err := equal.CompletePlan("P-1", "C-EQ")
	if err != nil || done.Status != PlanStatusDone ||
		done.CertificateNumber != "C-EQ" || done.CompletedAt == "" {
		t.Fatalf("校准日期等于建立日期应可完成：err=%v plan=%+v", err, done)
	}

	// 早一天：明确拒绝，计划继续未完成、留在待办，完成时间与关联证书为空。
	before := setupHistoricalCreatedAt(t, "2026-09-01")
	mustAddCert(t, before, CertificateInput{
		InstrumentID: "M-1", Number: "C-BEFORE", CalDate: "2026-08-31",
		Expiry: "2027-08-31", Method: "m", Error: 0.1, Summary: "早一天",
	})
	_, _, err = before.CompletePlan("P-1", "C-BEFORE")
	if !IsValidation(err) || !strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("早一天的证书应被明确拒绝，得到 %v", err)
	}
	p := before.findPlan("P-1")
	if p.Status != PlanStatusOpen || p.CompletedAt != "" || p.CertificateNumber != "" {
		t.Fatalf("拒绝后不得写入完成时间或关联证书：%+v", p)
	}
	if items, _ := before.Todos(""); len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("被拒绝的计划应继续留在待办：%+v", items)
	}
	// 早一天的证书没有被“占用”：但它仍不符合本计划条件；此后用符合条件的
	// 证书完成同一计划应正常成功，且关联的是新证书而非被拒绝的那张。
	mustAddCert(t, before, CertificateInput{
		InstrumentID: "M-1", Number: "C-OK", CalDate: "2026-09-10",
		Expiry: "2027-09-10", Method: "m", Error: 0.1, Summary: "符合条件",
	})
	done2, _, err := before.CompletePlan("P-1", "C-OK")
	if err != nil || done2.CertificateNumber != "C-OK" || done2.CompletedAt == "" {
		t.Fatalf("符合条件的证书应能完成曾被拒绝的计划：err=%v plan=%+v", err, done2)
	}
	if items, _ := before.Todos(""); len(items) != 0 {
		t.Fatalf("完成后应移出待办：%+v", items)
	}
}

// setupHistoricalCreatedAt 建立一件器具与一项计划，并把时钟停在建立当天之后
// 的 2026-09-25 供补录证书与完成操作使用。
func setupHistoricalCreatedAt(t *testing.T, createdDay string) *Ledger {
	t.Helper()
	clock := &fakeClock{t: mustDate(t, createdDay)}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期校准",
	})
	clock.t = mustDate(t, "2026-09-25")
	return l
}
