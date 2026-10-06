package calibrate

import (
	"strings"
	"testing"
)

// 本文件为“用已到期证书完成校准计划”补充回归保障，重点保住两件不同的事：
//
//  1. 证书当前是否有效：截止日当天起即到期，到期证书不能再支持器具的使用申请。
//  2. 证书能否证明一次校准工作已完成：只要证书属于计划中的器具、校准日期不早于
//     计划最初建立的本机日期、且未用于完成其他计划，即使证书已经到期（截止日
//     当天或截止日已过），也必须允许完成，不能要求用户重新录入一张未到期证书。
//
// 完成只结束计划：不改变器具登记状态、允许误差，也不改变证书的校准日期、
// 截止日、测得误差；完成后的使用判断仍按器具状态与最近证书（按校准日期确定，
// 与录入先后无关）的结论和有效期执行。

// setupExpiredCertPlan 构造标准场景：器具 M-1 在用、允许误差 0.5；计划 PL-1
// 建立于 2026-09-01（时钟起始日）；证书 C-EXP 校准日期 2026-09-05、截止日
// 2026-10-01、测得误差 0.1（合格）。调用方把时钟拨到截止日当天或之后，
// 即可验证“已到期证书仍能完成计划”。
func setupExpiredCertPlan(t *testing.T, clock *fakeClock) *Ledger {
	t.Helper()
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("切换在用失败: %v", err)
	}
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
	})
	clock.t = mustDate(t, "2026-09-05")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-EXP", CalDate: "2026-09-05",
		Expiry: "2026-10-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	})
	return l
}

// TestExpiredCertCompletesPlanOnExpiryDayAndAfter 覆盖两种到期情形：完成操作
// 发生在截止日当天（当天起即到期，不能把当天当成仍有效的一天）以及截止日已过。
// 两种情形下完成都必须成功：计划显示已完成，保留实际使用的证书编号与本次完成
// 时间，从待办移除；按器具核对仍能查到该计划和证书；完成动作不改动器具状态、
// 允许误差与证书的校准日期、截止日、测得误差。
func TestExpiredCertCompletesPlanOnExpiryDayAndAfter(t *testing.T) {
	cases := []struct {
		name string
		day  string
	}{
		{"截止日当天已到期", "2026-10-01"},
		{"截止日已过", "2026-10-05"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{t: mustDate(t, "2026-09-01")}
			l := setupExpiredCertPlan(t, clock)

			// 完成当天：证书按本机日历日期判断已经到期，但计划仍未完成。
			clock.t = mustDate(t, tc.day)
			pre, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if pre.Latest == nil || pre.Latest.Number != "C-EXP" || !pre.Latest.Expired {
				t.Fatalf("完成前证书 C-EXP 在 %s 应已到期：%+v", tc.day, pre.Latest)
			}
			assertPlanStillOpen(t, l, "PL-1")

			// 已到期不是拒绝完成的理由：归属、校准日期（09-05 不早于建立日
			// 09-01）与未占用条件都满足，必须允许完成。
			done, idem, err := l.CompletePlan("PL-1", "C-EXP")
			if err != nil || idem {
				t.Fatalf("已到期证书应能完成计划，err=%v idempotent=%v", err, idem)
			}
			if done.Status != PlanStatusDone || done.CertificateNumber != "C-EXP" ||
				done.CompletedAt == "" {
				t.Fatalf("完成结果应保留证书编号与完成时间：%+v", done)
			}
			if !strings.HasPrefix(done.CompletedAt, tc.day+"T") {
				t.Fatalf("完成时间应取本次成功操作日期 %s，得到 %s", tc.day, done.CompletedAt)
			}
			completedAt := done.CompletedAt
			if items, _ := l.Todos(""); len(items) != 0 {
				t.Fatalf("完成后计划应从待办移除，得到 %+v", items)
			}

			// 同证书重复完成同一计划：幂等返回原结果，不刷新完成时间。
			again, idem2, err := l.CompletePlan("PL-1", "C-EXP")
			if err != nil || !idem2 {
				t.Fatalf("重复完成应幂等成功，err=%v idempotent=%v", err, idem2)
			}
			if again.CompletedAt != completedAt || again.CertificateNumber != "C-EXP" {
				t.Fatalf("重复完成不应刷新完成时间或换掉证书：%+v", again)
			}

			// 按器具核对：仍能查到该计划（已完成、关联 C-EXP、保留完成时间）
			// 和这张证书；证书仍如实显示已到期。
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
				r.Plans[0].CertificateNumber != "C-EXP" ||
				r.Plans[0].CompletedAt != completedAt {
				t.Fatalf("核对中的计划应显示已完成并保留 C-EXP 与完成时间：%+v", r.Plans)
			}
			if r.Latest == nil || r.Latest.Number != "C-EXP" || !r.Latest.Expired ||
				!r.Latest.Pass {
				t.Fatalf("最近证书应仍是已到期但合格的 C-EXP：%+v", r.Latest)
			}
			// 完成只结束计划：器具状态、允许误差与证书字段都保持原样。
			assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
				"C-EXP": {CalDate: "2026-09-05", Expiry: "2026-10-01", Error: 0.1},
			})
		})
	}
}

// TestCompleteWithExpiredLatestCertStillDeniesUse 保住完成计划后的使用判断：
// 器具在用，用于完成计划的合格证书同时也是最近证书且已经到期——计划虽然完成，
// 核对仍显示不能使用，使用申请因这张最近证书到期被拒绝，原因对应到该证书，
// 已保存的拒绝记录在时钟继续前进后仍保持这个结果。
func TestCompleteWithExpiredLatestCertStillDeniesUse(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-09-01")}
	l := setupExpiredCertPlan(t, clock)

	// 截止日已过（10-02）完成计划：计划正常结束。
	clock.t = mustDate(t, "2026-10-02")
	done, _, err := l.CompletePlan("PL-1", "C-EXP")
	if err != nil || done.Status != PlanStatusDone {
		t.Fatalf("已到期证书应能完成计划，err=%v plan=%+v", err, done)
	}

	// 完成不改变使用资格：最近证书 C-EXP 已到期，核对显示不能使用，
	// 原因对应到这张证书及其截止日，而不是超差（证书本身合格）。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse {
		t.Fatalf("最近证书已到期，计划完成不代表可以使用：reasons=%v", r.Reasons)
	}
	if !containsReason(r.Reasons, "C-EXP") || !containsReason(r.Reasons, "到期") ||
		!containsReason(r.Reasons, "2026-10-01") {
		t.Fatalf("不可使用原因应指出最近证书 C-EXP 于 2026-10-01 到期，得到 %v", r.Reasons)
	}
	if containsReason(r.Reasons, "超差") {
		t.Fatalf("证书合格，到期拒绝不应混入超差原因：%v", r.Reasons)
	}

	// 使用申请同样因这张最近证书到期被拒绝并留痕。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "C-EXP") ||
		!containsReason(d.Reasons, "到期") {
		t.Fatalf("使用申请应因 C-EXP 到期被拒绝，得到 allowed=%v reasons=%v",
			d.Allowed, d.Reasons)
	}

	// 时钟继续前进后：计划保持已完成，已保存的拒绝记录仍保持申请当时的结果，
	// 原因冻结不被重写。
	clock.t = mustDate(t, "2026-11-01")
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review later: %v", err)
	}
	if len(r2.Plans) != 1 || r2.Plans[0].Status != PlanStatusDone ||
		r2.Plans[0].CertificateNumber != "C-EXP" {
		t.Fatalf("计划应保持已完成并关联 C-EXP：%+v", r2.Plans)
	}
	if len(r2.Rejections) != 1 {
		t.Fatalf("应保留 1 条拒绝记录，得到 %d", len(r2.Rejections))
	}
	rej := r2.Rejections[0]
	if rej.Allowed || !strings.HasPrefix(rej.RequestedAt, "2026-10-02T") ||
		!containsReason(rej.Reasons, "C-EXP") || !containsReason(rej.Reasons, "到期") {
		t.Fatalf("拒绝记录应冻结为 2026-10-02 因 C-EXP 到期被拒绝：%+v", rej)
	}
}

// TestOlderExpiredCertCompletesButNewerValidCertKeepsUsable 保住另一种情形：
// 用于完成计划的是较早的已到期证书，而器具还有校准日期更近的合格未到期证书。
// 此时仍可完成计划；最近证书继续是较新的一张（按校准日期确定，与录入先后无关），
// 器具保持可用，不能因为计划关联了旧的已到期证书而被判到期。
func TestOlderExpiredCertCompletesButNewerValidCertKeepsUsable(t *testing.T) {
	for _, backfill := range []bool{false, true} {
		name := "按校准日期先后录入"
		if backfill {
			name = "较早证书事后补录"
		}
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{t: mustDate(t, "2026-09-01")}
			l := newTestLedger(t, clock)
			mustRegister(t, l, "M-1", "万用表", 0.5)
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("切换在用失败: %v", err)
			}
			mustPlan(t, l, PlanInput{
				Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
			})

			// 较新证书 C-NEW：校准 09-20，截止 2027-09-20（整个场景内未到期），合格。
			newer := CertificateInput{
				InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-20",
				Expiry: "2027-09-20", Method: "规范A", Error: 0.1, Summary: "较新一次校准",
			}
			// 较早证书 C-OLD：校准 09-10，截止 2026-09-30（完成时已到期），合格。
			older := CertificateInput{
				InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-09-10",
				Expiry: "2026-09-30", Method: "规范A", Error: 0.2, Summary: "较早一次校准",
			}
			if backfill {
				clock.t = mustDate(t, "2026-09-20")
				mustAddCert(t, l, newer)
				clock.t = mustDate(t, "2026-09-25")
				mustAddCert(t, l, older)
			} else {
				clock.t = mustDate(t, "2026-09-10")
				mustAddCert(t, l, older)
				clock.t = mustDate(t, "2026-09-20")
				mustAddCert(t, l, newer)
			}

			// 完成当天（10-05）：C-OLD 已到期、C-NEW 仍未到期。用较早的已到期
			// 证书完成计划：校准日期 09-10 不早于建立日 09-01，必须允许。
			clock.t = mustDate(t, "2026-10-05")
			done, idem, err := l.CompletePlan("PL-1", "C-OLD")
			if err != nil || idem {
				t.Fatalf("较早的已到期证书应能完成计划，err=%v idempotent=%v", err, idem)
			}
			if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" {
				t.Fatalf("完成结果应保留较早证书编号：%+v", done)
			}

			// 最近证书继续是校准日期较新的 C-NEW（录入先后不改变这一规则），
			// 器具保持可用，不因计划关联了已到期的 C-OLD 而被判到期。
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if !r.CanUse || len(r.Reasons) != 0 {
				t.Fatalf("最近证书合格未到期，器具应保持可用，得到 reasons=%v", r.Reasons)
			}
			if r.Latest == nil || r.Latest.Number != "C-NEW" || !r.Latest.Pass ||
				r.Latest.Expired {
				t.Fatalf("最近证书应仍是较新的合格未到期 C-NEW：%+v", r.Latest)
			}
			if len(r.History) != 2 || r.History[0].Number != "C-NEW" ||
				r.History[1].Number != "C-OLD" || !r.History[1].Expired {
				t.Fatalf("历史应由近到远为 C-NEW、已到期 C-OLD：%+v", r.History)
			}
			if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
				r.Plans[0].CertificateNumber != "C-OLD" {
				t.Fatalf("计划应显示已完成并关联 C-OLD：%+v", r.Plans)
			}

			// 使用申请获准：计划关联的旧证书到期不影响最近证书的判断。
			d, err := l.RequestUse("M-1")
			if err != nil || !d.Allowed {
				t.Fatalf("应获准使用，err=%v decision=%+v", err, d)
			}
			assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
				"C-NEW": {CalDate: "2026-09-20", Expiry: "2027-09-20", Error: 0.1},
				"C-OLD": {CalDate: "2026-09-10", Expiry: "2026-09-30", Error: 0.2},
			})
		})
	}
}

// TestExpiredCertDateBoundaryAroundCreationDay 保留完成操作原有的日期门槛：
// 已到期证书的校准日期恰好等于计划建立日期时可以完成；早一天必须明确拒绝，
// 计划仍未完成、继续留在待办，也不留下完成时间或关联证书编号。
func TestExpiredCertDateBoundaryAroundCreationDay(t *testing.T) {
	// 计划建立于 2026-09-10；两张证书截止日都是 2026-09-15，完成操作发生在
	// 2026-09-25，两张证书都已到期——到期不改变日期门槛的既有判断。
	clock := &fakeClock{t: mustDate(t, "2026-09-10")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-09-20", Note: "周期校准",
	})

	clock.t = mustDate(t, "2026-09-25")
	// 校准日期比建立日期早一天的已到期证书：明确拒绝。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-TOO-EARLY", CalDate: "2026-09-09",
		Expiry: "2026-09-15", Method: "m", Error: 0.1, Summary: "早一天",
	})
	_, _, err := l.CompletePlan("PL-1", "C-TOO-EARLY")
	if !IsValidation(err) ||
		!strings.Contains(err.Error(), "早于") || !strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("校准日期早于建立日期一天应明确拒绝并说明边界，得到 %v", err)
	}
	assertPlanStillOpen(t, l, "PL-1")

	// 校准日期恰好等于建立日期的已到期证书：可以完成，完成时间取本次操作。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-ON-DAY", CalDate: "2026-09-10",
		Expiry: "2026-09-15", Method: "m", Error: 0.1, Summary: "建立当天",
	})
	done, idem, err := l.CompletePlan("PL-1", "C-ON-DAY")
	if err != nil || idem {
		t.Fatalf("校准日期等于建立日期的已到期证书应可完成，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-ON-DAY" ||
		!strings.HasPrefix(done.CompletedAt, "2026-09-25T") {
		t.Fatalf("边界当天完成结果异常：%+v", done)
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后待办应为空，得到 %+v", items)
	}
}
