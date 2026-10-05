package calibrate

import (
	"strings"
	"testing"
)

// 本文件为“用历史证书完成校准计划”补充回归保障，重点保住两个各自独立的含义：
//
//  1. 计划关联证书：只是“当时建立的计划由哪一张证书完成”的留痕，只要求该证书
//     属于该器具、校准日期不早于计划最初建立的本机日期、且未用于完成其他计划；
//     证书不是最近一张、本身超差，都不能成为额外拒绝理由。
//  2. 器具最近证书：按校准日期确定（与录入先后无关），器具能否使用只认最近证书
//     的结论与有效期以及器具状态，绝不回退到计划关联的那张更早证书。
//
// 场景为用户先登记校准日期较新的证书、再补录较早的一次校准，并用这张历史证书
// 结束当时（更早）建立的计划。

// assertPlanStillOpen 是日期不符等拒绝后的统一核对：计划仍未完成、留在待办，
// 没有写入完成时间或关联证书。
func assertPlanStillOpen(t *testing.T, l *Ledger, number string) {
	t.Helper()
	p := l.findPlan(number)
	if p == nil {
		t.Fatalf("计划 %s 应仍然存在", number)
	}
	if p.Status != PlanStatusOpen {
		t.Fatalf("计划 %s 应仍为未完成，得到 %s", number, p.Status)
	}
	if p.CompletedAt != "" || p.CertificateNumber != "" {
		t.Fatalf("被拒绝后不应写入完成时间或关联证书：%+v", p)
	}
	items, err := l.Todos(p.InstrumentID)
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != number {
		t.Fatalf("计划 %s 应继续留在待办，得到 %+v", number, items)
	}
}

// assertCertAndInstrumentUnchanged 核对完成动作只结束计划：不更改器具允许误差、
// 状态，也不更改两张证书的测得误差与有效期。
func assertCertAndInstrumentUnchanged(t *testing.T, l *Ledger, want map[string]Certificate) {
	t.Helper()
	inst := l.findInstrument("M-1")
	if inst == nil {
		t.Fatal("器具 M-1 不存在")
	}
	if inst.AllowedError != 0.5 {
		t.Fatalf("完成计划不应更改允许误差，得到 %g", inst.AllowedError)
	}
	if inst.Status != StatusInUse {
		t.Fatalf("完成计划不应更改器具状态，得到 %s", inst.Status)
	}
	for number, w := range want {
		got := l.findCertificate(number)
		if got == nil {
			t.Fatalf("证书 %s 不应丢失", number)
		}
		if got.Error != w.Error || got.Expiry != w.Expiry || got.CalDate != w.CalDate {
			t.Fatalf("证书 %s 不应被完成动作改动：得到 %+v， want %+v", number, got, w)
		}
	}
}

// TestHistoricalPassingCertCompletesButNewerFailingCertStillGovernsUse 覆盖主场景：
// 较早证书合格、较新证书超差，两张证书校准日期不同、截止日都晚于核对当天，器具
// 在用。用较早（非最近）证书完成计划应成功并保留该证书编号与完成时间、移出待办；
// 但按器具核对与使用申请仍以较新的超差证书为准，申请被拒绝并按较新证书超差留痕，
// 不能因为计划关联了一张合格证书而获准。
func TestHistoricalPassingCertCompletesButNewerFailingCertStillGovernsUse(t *testing.T) {
	// 计划建立于 2026-09-01：完成规则只要求证书校准日期不早于这一本机日期。
	clock := &fakeClock{t: mustDate(t, "2026-09-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
	})

	// 先登记校准日期较新的证书：测得误差 0.8，超过允许误差 0.5，判超差。
	clock.t = mustDate(t, "2026-09-20")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-20",
		Expiry: "2027-09-20", Method: "规范A", Error: 0.8, Summary: "较新一次校准",
	})
	// 再补录较早的一次校准：09-10，测得误差 -0.1，合格；截止日晚于核对当天。
	clock.t = mustDate(t, "2026-09-25")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-09-10",
		Expiry: "2027-09-10", Method: "规范A", Error: -0.1, Summary: "补录的较早校准",
	})

	// 仅录入两张证书：计划不会自动完成，仍未完成并留在待办。
	assertPlanStillOpen(t, l, "PL-1")

	// 用较早的合格证书（并非最近一张）完成当时建立的计划：归属、校准日期
	// （09-10 不早于建立日 09-01）与未占用条件都满足，“不是最近一张”不构成
	// 拒绝理由；完成时间取本次成功操作时刻。
	done, idem, err := l.CompletePlan("PL-1", "C-OLD")
	if err != nil || idem {
		t.Fatalf("较早合格证书应能完成计划，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" || done.CompletedAt == "" {
		t.Fatalf("完成结果应保留历史证书编号与完成时间：%+v", done)
	}
	if !strings.HasPrefix(done.CompletedAt, "2026-09-25T") {
		t.Fatalf("完成时间应取本次成功操作时刻 2026-09-25，得到 %s", done.CompletedAt)
	}
	completedAt := done.CompletedAt
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后计划应从待办移除，得到 %+v", items)
	}

	// 时钟前进后同证书重复完成同一计划：幂等返回原结果，不刷新完成时间。
	clock.t = mustDate(t, "2026-10-05")
	again, idem2, err := l.CompletePlan("PL-1", "C-OLD")
	if err != nil || !idem2 {
		t.Fatalf("同证书重复完成应幂等成功，err=%v idempotent=%v", err, idem2)
	}
	if again.Status != PlanStatusDone || again.CertificateNumber != "C-OLD" ||
		again.CompletedAt != completedAt {
		t.Fatalf("重复完成应原样返回历史证书编号与原完成时间：%+v", again)
	}

	// 按器具核对（10-05，两张证书均未到期）：计划展示已完成并关联 C-OLD；
	// 最近证书仍是校准日期较新的超差 C-NEW，器具不可使用，原因指向 C-NEW
	// 超差而不是计划关联的 C-OLD；历史证书按校准日期由近到远排列。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse {
		t.Fatalf("最近证书超差时器具不应可用，得到 reasons=%v", r.Reasons)
	}
	if !containsReason(r.Reasons, "超差") || !containsReason(r.Reasons, "C-NEW") {
		t.Fatalf("拒绝原因应针对较新证书 C-NEW 超差，得到 %v", r.Reasons)
	}
	if containsReason(r.Reasons, "C-OLD") {
		t.Fatalf("计划关联的较早合格证书不应出现在使用拒绝原因中：%v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Number != "C-NEW" || r.Latest.Pass || r.Latest.Expired {
		t.Fatalf("最近证书应仍是较新的超差、未到期证书 C-NEW：%+v", r.Latest)
	}
	if len(r.History) != 2 {
		t.Fatalf("历史应有两张证书，得到 %d", len(r.History))
	}
	if r.History[0].Number != "C-NEW" || r.History[0].Pass {
		t.Fatalf("历史第一张应为较新超差证书 C-NEW，得到 %+v", r.History[0])
	}
	if r.History[1].Number != "C-OLD" || !r.History[1].Pass || r.History[1].Expired {
		t.Fatalf("历史第二张应为较早合格未到期证书 C-OLD，得到 %+v", r.History[1])
	}
	if len(r.Plans) != 1 {
		t.Fatalf("应只有一项计划，得到 %d", len(r.Plans))
	}
	p := r.Plans[0]
	if p.Status != PlanStatusDone ||
		p.CertificateNumber != "C-OLD" || p.CompletedAt != completedAt {
		t.Fatalf("核对中的计划应显示已完成并保留 C-OLD 与原完成时间：%+v", p.Plan)
	}
	assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
		"C-NEW": {CalDate: "2026-09-20", Expiry: "2027-09-20", Error: 0.8},
		"C-OLD": {CalDate: "2026-09-10", Expiry: "2027-09-10", Error: -0.1},
	})

	// 随后申请使用：被拒绝，拒绝原因针对较新证书 C-NEW 超差，并冻结留痕；
	// 不会因为计划关联了合格的 C-OLD 而获准。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") || !containsReason(d.Reasons, "C-NEW") {
		t.Fatalf("使用申请应因较新证书超差被拒绝，得到 allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	if containsReason(d.Reasons, "C-OLD") {
		t.Fatalf("拒绝原因不应涉及计划关联的 C-OLD：%v", d.Reasons)
	}
	r2, _ := l.Review("M-1")
	if len(r2.Rejections) != 1 {
		t.Fatalf("应留下 1 条拒绝记录，得到 %d", len(r2.Rejections))
	}
	if !containsReason(r2.Rejections[0].Reasons, "超差") ||
		!containsReason(r2.Rejections[0].Reasons, "C-NEW") {
		t.Fatalf("留痕原因应保留较新证书超差：%+v", r2.Rejections[0])
	}

	// 已用于完成 PL-1 的 C-OLD 不能再完成其他计划。这里直接在台账中补建一项
	// 建立于 2026-09-10 的未结束计划（使归属与校准日期边界都通过），确认
	// “未用于完成其他计划”仍是一条独立的拒绝条件，与证书是否最近无关。
	l.data.Plans = append(l.data.Plans, Plan{
		Number: "PL-2", InstrumentID: "M-1",
		PlannedDate: "2026-09-15", OriginalDate: "2026-09-15",
		Note: "下一轮", CreatedAt: "2026-09-10T00:00:00Z", Status: PlanStatusOpen,
	})
	if _, _, err := l.CompletePlan("PL-2", "C-OLD"); !IsValidation(err) ||
		!strings.Contains(err.Error(), "已用于完成计划") {
		t.Fatalf("已完成过 PL-1 的 C-OLD 应被拒绝用于 PL-2，得到 %v", err)
	}
	if p2 := l.findPlan("PL-2"); p2.Status != PlanStatusOpen ||
		p2.CompletedAt != "" || p2.CertificateNumber != "" {
		t.Fatalf("被拒绝的 PL-2 不应写入完成信息：%+v", p2)
	}
	if p1 := l.findPlan("PL-1"); p1.CertificateNumber != "C-OLD" {
		t.Fatalf("PL-1 与 C-OLD 的关联不应被影响：%+v", p1)
	}
}

// TestHistoricalFailingCertCompletesButNewerPassingCertStillGovernsUse 保住相反
// 结论：较早证书超差、较新证书合格且未到期，器具在用。较早的超差证书同样可以
// 完成符合条件的计划；核对与使用申请仍以较新证书为准，申请获准并留痕。完成动作
// 不更改器具允许误差、状态、两张证书的测得误差或有效期。
func TestHistoricalFailingCertCompletesButNewerPassingCertStillGovernsUse(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-09-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
	})

	// 较新证书合格。
	clock.t = mustDate(t, "2026-09-20")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-20",
		Expiry: "2027-09-20", Method: "规范A", Error: 0.1, Summary: "较新一次校准",
	})
	// 补录较早的超差证书（误差 5）。
	clock.t = mustDate(t, "2026-09-25")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-09-10",
		Expiry: "2027-09-10", Method: "规范A", Error: 5, Summary: "补录的较早校准",
	})
	assertPlanStillOpen(t, l, "PL-1")

	// 较早的超差证书同样表示校准工作已完成，可以结束计划。
	done, idem, err := l.CompletePlan("PL-1", "C-OLD")
	if err != nil || idem {
		t.Fatalf("较早超差证书也应能完成计划，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" || done.CompletedAt == "" {
		t.Fatalf("完成结果应保留这张超差历史证书的编号与完成时间：%+v", done)
	}

	// 核对（10-05）：最近证书是较新的合格 C-NEW，器具可以使用；历史按
	// 校准日期由近到远，较早的 C-OLD 仍如实显示超差。
	clock.t = mustDate(t, "2026-10-05")
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !r.CanUse || len(r.Reasons) != 0 {
		t.Fatalf("最近证书合格未到期时应可以使用，得到 reasons=%v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Number != "C-NEW" || !r.Latest.Pass || r.Latest.Expired {
		t.Fatalf("最近证书应是较新的合格未到期证书 C-NEW：%+v", r.Latest)
	}
	if len(r.History) != 2 ||
		r.History[0].Number != "C-NEW" || !r.History[0].Pass ||
		r.History[1].Number != "C-OLD" || r.History[1].Pass {
		t.Fatalf("历史应由近到远为合格 C-NEW、超差 C-OLD：%+v", r.History)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-OLD" {
		t.Fatalf("计划应保留与较早超差证书的关联：%+v", r.Plans)
	}
	assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
		"C-NEW": {CalDate: "2026-09-20", Expiry: "2027-09-20", Error: 0.1},
		"C-OLD": {CalDate: "2026-09-10", Expiry: "2027-09-10", Error: 5},
	})

	// 使用申请以较新证书为准，获准并留痕。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if !d.Allowed || len(d.Reasons) != 0 {
		t.Fatalf("应获准使用，得到 allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	r2, _ := l.Review("M-1")
	if len(r2.Rejections) != 0 {
		t.Fatalf("获准使用不应产生拒绝记录，得到 %d 条", len(r2.Rejections))
	}
	recs := l.UsageRecords()
	if len(recs) != 1 || !recs[0].Allowed {
		t.Fatalf("应留下 1 条获准记录，得到 %+v", recs)
	}
}

// TestCompletePlanCertDateBoundaryAroundCreationDay 日期边界围绕计划最初建立的
// 本机日期：校准日期等于建立日期可完成，早一天则明确拒绝；拒绝后计划继续未完成
// 并留在待办，不写入完成时间或关联证书，随后仍可用边界当天的证书正常完成。
func TestCompletePlanCertDateBoundaryAroundCreationDay(t *testing.T) {
	// 计划建立于 2026-09-10；完成动作发生在更晚的 09-15，比较只以保存的
	// 建立日期为准，而不是操作当天。
	clock := &fakeClock{t: mustDate(t, "2026-09-10")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-09-20", Note: "周期校准",
	})

	clock.t = mustDate(t, "2026-09-15")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-TOO-EARLY", CalDate: "2026-09-09",
		Expiry: "2027-09-09", Method: "m", Error: 0.1, Summary: "早一天",
	})
	_, _, err := l.CompletePlan("PL-1", "C-TOO-EARLY")
	if !IsValidation(err) ||
		!strings.Contains(err.Error(), "早于") || !strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("校准日期早于建立日期一天应明确拒绝并说明边界，得到 %v", err)
	}
	assertPlanStillOpen(t, l, "PL-1")
	// 被拒绝的证书本身仍是一张正常历史证书，不被删除。
	if l.findCertificate("C-TOO-EARLY") == nil {
		t.Fatal("日期不符的拒绝不应删除证书")
	}

	// 校准日期恰好等于建立日期：可以完成；完成时间取本次成功操作。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-ON-DAY", CalDate: "2026-09-10",
		Expiry: "2027-09-10", Method: "m", Error: 0.1, Summary: "建立当天",
	})
	done, idem, err := l.CompletePlan("PL-1", "C-ON-DAY")
	if err != nil || idem {
		t.Fatalf("校准日期等于建立日期应可完成，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-ON-DAY" ||
		!strings.HasPrefix(done.CompletedAt, "2026-09-15T") {
		t.Fatalf("边界当天完成结果异常：%+v", done)
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后待办应为空，得到 %+v", items)
	}

	// 已完成后再用那张早一天的证书试图更换关联：仍拒绝，原完成信息不变。
	_, _, err = l.CompletePlan("PL-1", "C-TOO-EARLY")
	if !IsValidation(err) {
		t.Fatalf("已完成计划换证书应拒绝，得到 %v", err)
	}
	p := l.findPlan("PL-1")
	if p.Status != PlanStatusDone || p.CertificateNumber != "C-ON-DAY" ||
		p.CompletedAt != done.CompletedAt {
		t.Fatalf("拒绝换证书后原完成信息被改动：%+v", p)
	}
}
