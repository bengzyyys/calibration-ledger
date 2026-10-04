package calibrate

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestFailedUseRequestLeavesNoRecord 覆盖核心修复：器具编号存在且评估完成
// （无论允许还是拒绝）后，台账文件无法写入或替换时必须明确返回保存错误，
// 不返回已提交成功的使用结果；本次失败的申请时间、允许结果与拒绝原因都
// 不进入历史，也不会被此后其他成功保存的操作顺带写入。
func TestFailedUseRequestLeavesNoRecord(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格且未到期",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	// 先留一条成功保存的获准记录，作为失败申请不得改动的既有历史。
	first, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if !first.Allowed {
		t.Fatalf("在用 + 合格未到期证书应获准：%v", first.Reasons)
	}
	before := l.UsageRecords()
	if len(before) != 1 || !before[0].Allowed {
		t.Fatalf("既有历史应只有一条获准记录：%+v", before)
	}

	// 台账文件无法替换：获准申请保存失败，必须报保存错误而非返回成功结果。
	blockSaving(t, l)
	d, err := l.RequestUse("M-1")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if d != nil {
		t.Fatalf("保存失败不能返回已提交成功的使用结果：%+v", d)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 同一台账随后的查询只能看到此前成功保存的一条记录。
	if got := l.UsageRecords(); !sameUsage(got, before) {
		t.Fatalf("失败申请进入了使用记录：%+v", got)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Rejections) != 0 {
		t.Fatalf("失败申请不应出现在被拒绝记录中：%+v", r.Rejections)
	}

	// 文件仍不可写时再次申请：继续报保存错误，历史仍不增加。
	if _, err := l.RequestUse("M-1"); err == nil {
		t.Fatal("文件仍不可写时再次申请应仍报保存错误")
	}
	if got := l.UsageRecords(); !sameUsage(got, before) {
		t.Fatalf("再次失败后历史被改动：%+v", got)
	}

	// 恢复可写后先完成另一项正常保存（切换状态再切回之外的既有操作）：
	// 这里用停用切换验证失败的申请不会被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set retired: %v", err)
	}
	raw, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	var count int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"requested_at"`) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("失败的申请被随后成功的写盘顺带写入文件（应有 1 条记录）：\n%s", raw)
	}
	reopened, err := openAt(l.Path(), clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.UsageRecords(); !sameUsage(got, before) {
		t.Fatalf("文件中的使用记录应与失败前一致：%+v", got)
	}

	// 恢复可写后重新申请：器具已停用，应按新的实际情况拒绝并只新增这一条
	// 记录，不能沿用失败那次的获准结论。
	d, err = l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("恢复后重新申请应成功保存: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "停用") {
		t.Fatalf("重新申请应按当前停用状态拒绝：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	got := l.UsageRecords()
	if len(got) != 2 {
		t.Fatalf("恢复后重新申请应只新增一条记录，得到 %d 条：%+v", len(got), got)
	}
	if !got[0].Allowed || got[1].Allowed || !containsReason(got[1].Reasons, "停用") {
		t.Fatalf("新增记录应取重新申请当时的判断：%+v", got)
	}
}

// TestFailedUseRequestOnRejectionReportsSaveError 验证器具处于停用、缺少证书、
// 最近证书超差或到期时，即使已经得出拒绝原因，只要记录没有保存成功，本次
// 操作仍报告保存失败，且拒绝原因不进入历史。
func TestFailedUseRequestOnRejectionReportsSaveError(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}

	setup := func(t *testing.T, mutate func(l *Ledger)) *Ledger {
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		mutate(l)
		return l
	}

	cases := []struct {
		name   string
		mutate func(l *Ledger)
		reason string
	}{
		{"停用", func(l *Ledger) {
			mustAddCert(t, l, CertificateInput{
				InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
				Expiry: "2027-09-01", Method: "m", Error: 0.1, Summary: "s",
			})
			if err := l.SetStatus("M-1", StatusRetired); err != nil {
				t.Fatalf("set retired: %v", err)
			}
		}, "停用"},
		{"缺少证书", func(l *Ledger) {
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("set in-use: %v", err)
			}
		}, "没有校准证书"},
		{"最近证书超差", func(l *Ledger) {
			mustAddCert(t, l, CertificateInput{
				InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
				Expiry: "2027-09-01", Method: "m", Error: 0.9, Summary: "s",
			})
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("set in-use: %v", err)
			}
		}, "超差"},
		{"最近证书到期", func(l *Ledger) {
			mustAddCert(t, l, CertificateInput{
				InstrumentID: "M-1", Number: "C-1", CalDate: "2025-01-01",
				Expiry: "2025-12-31", Method: "m", Error: 0.1, Summary: "s",
			})
			if err := l.SetStatus("M-1", StatusInUse); err != nil {
				t.Fatalf("set in-use: %v", err)
			}
		}, "到期"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := setup(t, tc.mutate)

			// 先确认业务上确实会被拒绝（保存正常时留痕）。
			d, err := l.CanUse("M-1")
			if err != nil {
				t.Fatalf("can use: %v", err)
			}
			if d.Allowed || !containsReason(d.Reasons, tc.reason) {
				t.Fatalf("前置条件应被拒绝且原因含 %q：allowed=%v reasons=%v",
					tc.reason, d.Allowed, d.Reasons)
			}

			blockSaving(t, l)
			d, err = l.RequestUse("M-1")
			if err == nil {
				t.Fatal("已得出拒绝原因但写盘失败时仍必须返回保存错误")
			}
			if d != nil {
				t.Fatalf("保存失败不能返回已留痕的拒绝结果：%+v", d)
			}
			if IsValidation(err) || errors.Is(err, ErrNotFound) {
				t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
			}

			// 失败的拒绝原因不进入历史：使用记录与核对中的被拒绝记录都为空。
			if got := l.UsageRecords(); len(got) != 0 {
				t.Fatalf("失败的拒绝申请进入了使用记录：%+v", got)
			}
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review: %v", err)
			}
			if len(r.Rejections) != 0 {
				t.Fatalf("失败的拒绝申请进入了被拒绝记录：%+v", r.Rejections)
			}

			// 恢复可写后重新申请：保存成功，拒绝原因此时才留痕。
			restoreSaving(t, l)
			d, err = l.RequestUse("M-1")
			if err != nil {
				t.Fatalf("恢复后重新申请应成功保存: %v", err)
			}
			if d.Allowed || !containsReason(d.Reasons, tc.reason) {
				t.Fatalf("重新申请应拒绝且原因含 %q：allowed=%v reasons=%v",
					tc.reason, d.Allowed, d.Reasons)
			}
			r, _ = l.Review("M-1")
			if len(r.Rejections) != 1 || !containsReason(r.Rejections[0].Reasons, tc.reason) {
				t.Fatalf("恢复后应只有本次重新申请的一条拒绝记录：%+v", r.Rejections)
			}
		})
	}
}

// TestFailedUseRequestKeepsLedgerData 验证失败的使用申请不改变器具登记信息、
// 证书与校准计划，也不会被随后的正常保存顺带写入。
func TestFailedUseRequestKeepsLedgerData(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格且未到期",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"}); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	blockSaving(t, l)
	if _, err := l.RequestUse("M-1"); err == nil {
		t.Fatal("写盘失败应返回保存错误")
	}

	// 器具、证书、计划均不因这次失败发生变化。
	inst := l.findInstrument("M-1")
	if inst == nil || inst.Status != StatusInUse || inst.Name != "万用表" || inst.AllowedError != 0.5 {
		t.Fatalf("失败申请改动了器具登记信息：%+v", inst)
	}
	if latest := l.LatestCertificate("M-1"); latest == nil || latest.Number != "C-1" {
		t.Fatalf("失败申请改动了证书：%+v", latest)
	}
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("失败申请改动了校准计划：%+v", items)
	}

	// 恢复后先做一项既有保存操作（改期），失败的申请不能被顺带写入。
	restoreSaving(t, l)
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突"); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	reopened, err := openAt(l.Path(), clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.UsageRecords(); len(got) != 0 {
		t.Fatalf("失败的申请被随后成功的写盘顺带写入文件：%+v", got)
	}
}

// TestUnknownInstrumentUseRequestStillFails 确认未知器具编号仍明确报错且不新增
// 使用记录，单纯查询能否使用与按器具核对也不产生申请记录。
func TestUnknownInstrumentUseRequestStillFails(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	if _, err := l.RequestUse("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知编号应报 ErrNotFound：%v", err)
	}
	if _, err := l.CanUse("M-1"); err != nil {
		t.Fatalf("can use: %v", err)
	}
	if _, err := l.Review("M-1"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if got := l.UsageRecords(); len(got) != 0 {
		t.Fatalf("未知编号申请与查询不应产生使用记录：%+v", got)
	}
}

// sameUsage 比较两份使用记录的时间、结果与原因是否完全一致。
func sameUsage(a, b []UsageRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].InstrumentID != b[i].InstrumentID ||
			a[i].RequestedAt != b[i].RequestedAt ||
			a[i].Allowed != b[i].Allowed ||
			!sameReasons(a[i].Reasons, b[i].Reasons) {
			return false
		}
	}
	return true
}
