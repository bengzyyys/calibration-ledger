package calibrate

import (
	"strings"
	"testing"
)

// 本文件为“用已到期证书完成校准计划”补充回归保障，重点保住两个各自独立的含义：
//
//  1. 证书“当前是否有效”（截止日当天起即到期）与它“能否证明一次校准工作已经
//     完成”是两件事：用户可能直到证书截止日当天或之后，才把早已录入的校准结果
//     关联到仍未完成的计划。完成条件只有既有的三条——证书属于计划中的器具、
//     校准日期不早于计划最初建立的本机日期、未用于完成其他计划；证书已到期不是
//     额外的拒绝理由，也不要求用户重新录入一张未到期证书。
//  2. 完成计划只结束计划：器具状态、允许误差、证书原有的校准日期、截止日、测得
//     误差与结论都不变；器具能否使用仍只按状态与“最近证书”的结论、有效期判断。
//     用于完成计划的到期证书恰为最近证书时，计划完成后核对仍显示不能使用，使用
//     申请仍因这张最近证书到期被拒绝并按该证书留痕；若最近另有校准日期更近的
//     合格未到期证书，器具保持可用，关联旧证书不改变最近证书的选择规则。
//
// 截止日的判断一律按本机日历日期：截止日当天即到期，不能把当天当成仍有效的一天。

// expectExpiredLatestRejection 核对一次“不能使用”的结论针对最近证书到期：
// 决策被拒绝、原因中能对应到指定证书编号与到期说明，且最近证书视图本身合格、
// 已到期（排除“超差”才是原因的歧义）。
func expectExpiredLatestRejection(t *testing.T, d *UsageDecision, certNumber string) {
	t.Helper()
	if d == nil {
		t.Fatal("使用决策不应为空")
	}
	if d.Allowed {
		t.Fatalf("最近证书 %s 已到期时不应允许使用，得到 reasons=%v", certNumber, d.Reasons)
	}
	if !containsReason(d.Reasons, "到期") || !containsReason(d.Reasons, certNumber) {
		t.Fatalf("拒绝原因应能对应到证书 %s 到期，得到 %v", certNumber, d.Reasons)
	}
	if containsReason(d.Reasons, "超差") {
		t.Fatalf("合格证书到期的拒绝原因不应混入超差：%v", d.Reasons)
	}
	if d.Latest == nil || d.Latest.Number != certNumber {
		t.Fatalf("拒绝所依据的最近证书应为 %s，得到 %+v", certNumber, d.Latest)
	}
	if !d.Latest.Expired || !d.Latest.Pass {
		t.Fatalf("最近证书 %s 应是合格但已到期，得到 %+v", certNumber, d.Latest)
	}
}

// TestExpiredLatestCertCompletesPlanButUseStillRejected 主场景：器具在用，用于
// 完成计划的合格证书同时也是最近证书，且完成时已经到期。覆盖截止日当天已经到期
// 与截止日已过两种情况：完成计划都成功，计划显示已完成并保留证书编号与本次完成
// 时间、移出待办，核对仍能查到该计划与证书；但核对仍判不能使用，使用申请仍因
// 这张最近证书到期被拒绝并留痕。完成动作不改动器具状态、允许误差与证书字段。
func TestExpiredLatestCertCompletesPlanButUseStillRejected(t *testing.T) {
	cases := []struct {
		name   string
		expiry string
	}{
		{"截止日当天已经到期", "2026-10-06"},
		{"截止日已过", "2026-10-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 计划建立于 2026-10-01：完成规则只看证书校准日期是否早于这一日期，
			// 与完成操作当天证书是否仍在有效期无关。
			clock := &fakeClock{t: mustDate(t, "2026-10-01")}
			l := newTestLedger(t, clock)
			mustRegister(t, l, "M-1", "万用表", 0.5)
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("set in-use: %v", err)
			}
			mustPlan(t, l, PlanInput{
				Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
			})

			// 2026-10-06 录入一张合格证书：校准日期 10-02（不早于计划建立日），
			// 截止日按子用例取当天或更早；录入时它已是一张到期证书。
			clock.t = mustDate(t, "2026-10-06")
			mustAddCert(t, l, CertificateInput{
				InstrumentID: "M-1", Number: "C-EXP", CalDate: "2026-10-02",
				Expiry: tc.expiry, Method: "规范A", Error: 0.1, Summary: "到期前完成的校准",
			})

			expiryDay := mustDate(t, tc.expiry)
			// 截止日前一天：证书尚有效（CanUse 只评估不留痕，不影响随后计数）。
			clock.t = expiryDay.AddDate(0, 0, -1)
			if before, err := l.CanUse("M-1"); err != nil || !before.Allowed {
				t.Fatalf("截止日前一天证书应仍有效，err=%v d=%+v", err, before)
			}
			// 截止日当天：不修改任何数据，仅时钟前进，即判到期——不能把截止日
			// 当天当成仍有效的一天。
			clock.t = expiryDay
			atExpiry, err := l.CanUse("M-1")
			if err != nil {
				t.Fatalf("can use at expiry: %v", err)
			}
			expectExpiredLatestRejection(t, atExpiry, "C-EXP")

			// 仅录入证书不会自动完成计划。
			assertPlanStillOpen(t, l, "PL-1")

			// 完成操作发生在 2026-10-06（截止日当天或之后）：到期不构成拒绝理由，
			// 不要求重新录入一张未到期证书。
			clock.t = mustDate(t, "2026-10-06")
			done, idem, err := l.CompletePlan("PL-1", "C-EXP")
			if err != nil || idem {
				t.Fatalf("已到期证书仍应能完成符合条件的计划，err=%v idempotent=%v", err, idem)
			}
			if done.Status != PlanStatusDone || done.CertificateNumber != "C-EXP" {
				t.Fatalf("完成结果应保留实际使用的到期证书编号：%+v", done)
			}
			if !strings.HasPrefix(done.CompletedAt, "2026-10-06T") || done.CompletedAt == "" {
				t.Fatalf("应记录本次完成时间（2026-10-06），得到 %q", done.CompletedAt)
			}
			completedAt := done.CompletedAt
			if items, _ := l.Todos(""); len(items) != 0 {
				t.Fatalf("完成后计划应从校准待办移除，得到 %+v", items)
			}

			// 按器具核对：计划显示已完成并保留证书与本次完成时间，证书仍可在
			// 最近证书与历史中查到；但器具仍不能使用，原因对应这张到期证书。
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			expectExpiredLatestRejection(t, &UsageDecision{
				Allowed: r.CanUse, Reasons: r.Reasons, Latest: r.Latest,
			}, "C-EXP")
			if len(r.History) != 1 {
				t.Fatalf("历史应仍能查到这张证书，得到 %d 张", len(r.History))
			}
			h := r.History[0]
			if h.Number != "C-EXP" || !h.Expired || !h.Pass ||
				h.CalDate != "2026-10-02" || h.Expiry != tc.expiry || h.Error != 0.1 {
				t.Fatalf("证书原有的校准日期、截止日、误差与结论不应被完成动作改动：%+v", h)
			}
			if len(r.Plans) != 1 {
				t.Fatalf("核对中应仍能查到该计划，得到 %d 项", len(r.Plans))
			}
			pv := r.Plans[0]
			if pv.Status != PlanStatusDone || pv.CertificateNumber != "C-EXP" ||
				pv.CompletedAt != completedAt {
				t.Fatalf("核对中的计划应显示已完成并保留证书编号与完成时间：%+v", pv.Plan)
			}
			assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
				"C-EXP": {CalDate: "2026-10-02", Expiry: tc.expiry, Error: 0.1},
			})

			// 使用申请：计划虽已完成，仍因最近证书到期被拒绝，原因对应 C-EXP，
			// 并按申请当时的情况冻结留痕。
			d, err := l.RequestUse("M-1")
			if err != nil {
				t.Fatalf("request use: %v", err)
			}
			expectExpiredLatestRejection(t, d, "C-EXP")
			r2, _ := l.Review("M-1")
			if len(r2.Rejections) != 1 {
				t.Fatalf("应留下 1 条拒绝记录，得到 %d", len(r2.Rejections))
			}
			expectExpiredLatestRejection(t, &UsageDecision{
				Allowed: r2.Rejections[0].Allowed,
				Reasons: r2.Rejections[0].Reasons,
				Latest:  r2.Latest,
			}, "C-EXP")
			if r2.Rejections[0].Allowed {
				t.Fatalf("留痕必须是被拒绝的申请：%+v", r2.Rejections[0])
			}

			// 时钟走到截止日之后：同一台账上计划保持完成，同证书重复完成幂等
			// 返回原结果、不刷新完成时间；核对与使用申请仍按这张到期最近证书拒绝，
			// 已保存的拒绝记录持续保持同一结果。
			clock.t = mustDate(t, "2026-10-07")
			r3, _ := l.Review("M-1")
			if r3.CanUse || r3.Latest == nil || !r3.Latest.Expired ||
				r3.Plans[0].Status != PlanStatusDone ||
				r3.Plans[0].CertificateNumber != "C-EXP" ||
				r3.Plans[0].CompletedAt != completedAt {
				t.Fatalf("截止日之后核对结果应保持：计划完成、器具不可用：%+v", r3)
			}
			again, idem2, err := l.CompletePlan("PL-1", "C-EXP")
			if err != nil || !idem2 || again.CompletedAt != completedAt {
				t.Fatalf("截止日之后同证书重复完成应幂等返回原完成时间，err=%v idem=%v %+v",
					err, idem2, again)
			}
			d2, err := l.RequestUse("M-1")
			if err != nil {
				t.Fatalf("request use after expiry: %v", err)
			}
			expectExpiredLatestRejection(t, d2, "C-EXP")
			r4, _ := l.Review("M-1")
			if len(r4.Rejections) != 2 {
				t.Fatalf("两次申请都应留痕，得到 %d 条", len(r4.Rejections))
			}
			for i, rec := range r4.Rejections {
				if rec.Allowed || !containsReason(rec.Reasons, "到期") ||
					!containsReason(rec.Reasons, "C-EXP") {
					t.Fatalf("第 %d 条拒绝记录应保持因 C-EXP 到期而拒绝：%+v", i+1, rec)
				}
			}
		})
	}
}

// TestExpiredOlderCertCompletesPlanWhileNewerValidCertKeepsUsable 另一关键情况：
// 用于完成计划的是校准日期较早、已到期的合格证书，但器具还有一张校准日期更近的
// 合格未到期证书。计划仍可用旧的到期证书完成；最近证书继续按校准日期取较新一张，
// 器具保持可用，使用申请获准，不能因为关联了旧证书就被判到期。证书录入先后
// （先新后旧 / 先旧后新）不改变按校准日期确定最近证书的规则。
func TestExpiredOlderCertCompletesPlanWhileNewerValidCertKeepsUsable(t *testing.T) {
	for _, order := range []string{"先录入旧到期证书再补新证书", "先录入新证书再补旧到期证书"} {
		t.Run(order, func(t *testing.T) {
			// 计划建立于 2026-09-01。
			clock := &fakeClock{t: mustDate(t, "2026-09-01")}
			l := newTestLedger(t, clock)
			mustRegister(t, l, "M-1", "万用表", 0.5)
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("set in-use: %v", err)
			}
			mustPlan(t, l, PlanInput{
				Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "年度例行校准",
			})

			// C-OLD：校准 09-05、截止 09-20（在 10-06 已到期），合格。
			addOld := func() {
				mustAddCert(t, l, CertificateInput{
					InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-09-05",
					Expiry: "2026-09-20", Method: "规范A", Error: -0.1, Summary: "较早的校准",
				})
			}
			// C-NEW：校准 10-01、截止次年，合格未到期。
			addNew := func() {
				mustAddCert(t, l, CertificateInput{
					InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-10-01",
					Expiry: "2027-10-01", Method: "规范A", Error: 0.2, Summary: "最近一次校准",
				})
			}
			switch order {
			case "先录入旧到期证书再补新证书":
				clock.t = mustDate(t, "2026-09-06")
				addOld()
				clock.t = mustDate(t, "2026-10-05")
				addNew()
			case "先录入新证书再补旧到期证书":
				clock.t = mustDate(t, "2026-10-02")
				addNew()
				// 补录的是更早的校准；录入时点（10-05）这张证书已经到期，
				// 但录入规则只要求截止日晚于校准日期，不要求证书当前有效。
				clock.t = mustDate(t, "2026-10-05")
				addOld()
			}

			// 两张证书都录入后、完成前：计划仍未完成；最近证书按校准日期已是 C-NEW。
			assertPlanStillOpen(t, l, "PL-1")
			if latest := l.LatestCertificate("M-1"); latest == nil || latest.Number != "C-NEW" {
				t.Fatalf("最近证书应只按校准日期确定为 C-NEW，与录入先后无关：%+v", latest)
			}

			// 2026-10-06 用较早、已到期的 C-OLD 完成计划：到期不是拒绝理由，
			// 校准日期 09-05 不早于计划建立日 09-01。
			clock.t = mustDate(t, "2026-10-06")
			done, idem, err := l.CompletePlan("PL-1", "C-OLD")
			if err != nil || idem {
				t.Fatalf("较早的已到期证书应能完成计划，err=%v idempotent=%v", err, idem)
			}
			if done.Status != PlanStatusDone || done.CertificateNumber != "C-OLD" ||
				!strings.HasPrefix(done.CompletedAt, "2026-10-06T") {
				t.Fatalf("完成结果应保留旧到期证书编号与本次完成时间：%+v", done)
			}
			if items, _ := l.Todos(""); len(items) != 0 {
				t.Fatalf("完成后计划应移出待办，得到 %+v", items)
			}

			// 核对：计划与 C-OLD 的关联保留；最近证书继续是较新的合格未到期
			// C-NEW，器具保持可用；历史中 C-OLD 如实显示已到期，C-NEW 未到期。
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if !r.CanUse || len(r.Reasons) != 0 {
				t.Fatalf("最近证书合格未到期时关联旧到期证书不应限制使用：%v", r.Reasons)
			}
			if r.Latest == nil || r.Latest.Number != "C-NEW" ||
				!r.Latest.Pass || r.Latest.Expired ||
				r.Latest.CalDate != "2026-10-01" || r.Latest.Error != 0.2 {
				t.Fatalf("最近证书应继续是较新的合格未到期 C-NEW：%+v", r.Latest)
			}
			if len(r.History) != 2 ||
				r.History[0].Number != "C-NEW" || r.History[0].Expired ||
				r.History[1].Number != "C-OLD" || !r.History[1].Expired {
				t.Fatalf("历史应由近到远为未到期 C-NEW、已到期 C-OLD：%+v", r.History)
			}
			if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
				r.Plans[0].CertificateNumber != "C-OLD" || r.Plans[0].CompletedAt == "" {
				t.Fatalf("核对应显示计划由旧到期证书 C-OLD 完成：%+v", r.Plans)
			}
			assertCertAndInstrumentUnchanged(t, l, map[string]Certificate{
				"C-NEW": {CalDate: "2026-10-01", Expiry: "2027-10-01", Error: 0.2},
				"C-OLD": {CalDate: "2026-09-05", Expiry: "2026-09-20", Error: -0.1},
			})

			// 使用申请只认最近证书：获准并留痕，拒绝记录为空。
			d, err := l.RequestUse("M-1")
			if err != nil {
				t.Fatalf("request use: %v", err)
			}
			if !d.Allowed || len(d.Reasons) != 0 ||
				d.Latest == nil || d.Latest.Number != "C-NEW" {
				t.Fatalf("应按较新未到期证书 C-NEW 获准使用，得到 %+v", d)
			}
			r2, _ := l.Review("M-1")
			if len(r2.Rejections) != 0 {
				t.Fatalf("获准使用不应留下拒绝记录，得到 %d 条", len(r2.Rejections))
			}
			recs := l.UsageRecords()
			if len(recs) != 1 || !recs[0].Allowed {
				t.Fatalf("应留下 1 条获准记录，得到 %+v", recs)
			}

			// 已用于完成 PL-1 的到期证书同样不能再完成另一项计划——“未用于
			// 完成其他计划”仍是一条独立于有效期的既有条件。
			l.data.Plans = append(l.data.Plans, Plan{
				Number: "PL-2", InstrumentID: "M-1",
				PlannedDate: "2026-09-15", OriginalDate: "2026-09-15",
				Note: "下一轮", CreatedAt: "2026-09-05T00:00:00Z", Status: PlanStatusOpen,
			})
			_, _, err = l.CompletePlan("PL-2", "C-OLD")
			if !IsValidation(err) || !strings.Contains(err.Error(), "已用于完成计划") {
				t.Fatalf("已完成 PL-1 的到期证书不应再用于 PL-2，得到 %v", err)
			}
			if p2 := l.findPlan("PL-2"); p2.Status != PlanStatusOpen ||
				p2.CompletedAt != "" || p2.CertificateNumber != "" {
				t.Fatalf("被拒的 PL-2 不应写入完成信息：%+v", p2)
			}
		})
	}
}

// TestCompleteWithExpiredCertKeepsOriginalCalDateGate 保住完成操作原有的日期门槛：
// 对已到期证书同样适用——校准日期恰好等于计划建立日期可以完成；早一天必须明确
// 拒绝，计划仍未完成、继续留在待办，也不留下完成时间或关联证书编号。到期不是
// 这条拒绝的理由，错误必须仍只对应“校准日期早于计划建立日期”。
func TestCompleteWithExpiredCertKeepsOriginalCalDateGate(t *testing.T) {
	// 计划建立于 2026-10-01；完成操作在更晚的 10-06，比较只以保存的建立日期为准。
	clock := &fakeClock{t: mustDate(t, "2026-10-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-20", Note: "周期校准",
	})

	clock.t = mustDate(t, "2026-10-06")
	// 已到期（截止 10-05）但校准日期早于建立日一天的证书：拒绝理由只能是
	// 既有的日期门槛，不能变成“证书到期”。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-TOO-EARLY", CalDate: "2026-09-30",
		Expiry: "2026-10-05", Method: "m", Error: 0.1, Summary: "早一天且已到期",
	})
	_, _, err := l.CompletePlan("PL-1", "C-TOO-EARLY")
	if !IsValidation(err) ||
		!strings.Contains(err.Error(), "早于") || !strings.Contains(err.Error(), "建立日期") {
		t.Fatalf("到期证书校准日期早于建立日仍应按原门槛拒绝并说明边界，得到 %v", err)
	}
	if strings.Contains(err.Error(), "到期") || strings.Contains(err.Error(), "有效期") {
		t.Fatalf("拒绝理由不应新增证书有效期规则：%v", err)
	}
	assertPlanStillOpen(t, l, "PL-1")
	if l.findCertificate("C-TOO-EARLY") == nil {
		t.Fatal("日期不符的拒绝不应删除到期证书本身")
	}

	// 校准日期恰好等于建立日期、截止日恰为完成当天（已到期）：可以完成。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-ON-DAY", CalDate: "2026-10-01",
		Expiry: "2026-10-06", Method: "m", Error: 0.1, Summary: "建立当天校准、截止日当天到期",
	})
	done, idem, err := l.CompletePlan("PL-1", "C-ON-DAY")
	if err != nil || idem {
		t.Fatalf("到期证书校准日期等于建立日期应可完成，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-ON-DAY" ||
		!strings.HasPrefix(done.CompletedAt, "2026-10-06T") {
		t.Fatalf("边界当天的到期证书完成结果异常：%+v", done)
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后应移出待办，得到 %+v", items)
	}
	// 被拒的早一天证书不能用来更换已完成计划的关联证书。
	_, _, err = l.CompletePlan("PL-1", "C-TOO-EARLY")
	if !IsValidation(err) {
		t.Fatalf("已完成计划改用另一证书应继续拒绝，得到 %v", err)
	}
	p := l.findPlan("PL-1")
	if p.Status != PlanStatusDone || p.CertificateNumber != "C-ON-DAY" ||
		p.CompletedAt != done.CompletedAt {
		t.Fatalf("拒绝更换证书后原完成信息被改动：%+v", p)
	}
}
