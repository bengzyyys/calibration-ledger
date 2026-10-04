package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedCompleteSaveLeavesPlanOpen 覆盖核心修复：已通过业务校验的完成在
// 写盘失败时明确报保存错误，计划状态、完成时间、关联证书编号、计划日期、
// 改期历史与待办标记与完成前完全一致；恢复可写后重新提交才真正完成，
// 完成时间取本次成功操作而非失败时刻。
func TestFailedCompleteSaveLeavesPlanOpen(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})

	// 先成功改期一次 10-10 → 10-15：失败的完成不能改动计划日期与改期历史。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-15", "实验室排期冲突")
	if err != nil {
		t.Fatalf("前置改期应成功: %v", err)
	}
	savedChange := first.Changes[0]

	// 合法完成在台账文件无法替换时写盘失败。
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	p, idem, err := l.CompletePlan("P-1", "C-1")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil || idem {
		t.Fatalf("保存失败不能返回成功的计划结果或标记重复完成：plan=%v idem=%v", p, idem)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 文件仍不可保存时再次提交同一计划同一证书：仍报保存失败，
	// 而不是跳过保存返回“此前已完成”的成功结果。
	if p2, idem2, err := l.CompletePlan("P-1", "C-1"); err == nil || p2 != nil || idem2 {
		t.Fatalf("再次提交应仍报保存失败且不是重复完成：err=%v plan=%v idem=%v",
			err, p2, idem2)
	}

	// 继续使用同一台账对象：计划仍为未完成，完成时间与关联证书编号保持
	// 操作前的值；计划日期、改期历史也不改变。
	cur := l.findPlan("P-1")
	if cur == nil {
		t.Fatal("计划丢失")
	}
	if !cur.Open() || cur.CompletedAt != "" || cur.CertificateNumber != "" {
		t.Fatalf("失败完成改动了完成信息：%+v", cur)
	}
	if cur.PlannedDate != "2026-10-15" || cur.OriginalDate != "2026-10-10" {
		t.Fatalf("失败完成改动了计划日期：%+v", cur)
	}
	if len(cur.Changes) != 1 || cur.Changes[0] != savedChange {
		t.Fatalf("失败完成改动了改期历史：%+v", cur.Changes)
	}

	// 计划仍在待办中，日期标记按最后成功保存的计划日期与查询当天判断：
	// 10-20 查询时 10-15 应为“逾期”；若误标完成，待办会直接为空。
	clock.t = mustDate(t, "2026-10-20")
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-15" || items[0].Marker != TodoOverdue {
		t.Fatalf("失败完成后计划应仍按 10-15 列入待办并标逾期：%+v", items)
	}
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].Marker != TodoOverdue {
		t.Fatalf("按器具核对待办异常：%+v err=%v", only, err)
	}

	// 按器具核对与计划查询同样显示未完成、无关联证书与完成时间。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusOpen ||
		r.Plans[0].Marker != TodoOverdue ||
		r.Plans[0].CompletedAt != "" || r.Plans[0].CertificateNumber != "" ||
		len(r.Plans[0].Changes) != 1 {
		t.Fatalf("核对结果应与失败前一致：%+v", r.Plans)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Status != PlanStatusOpen ||
		views[0].CompletedAt != "" || views[0].CertificateNumber != "" {
		t.Fatalf("计划查询应与失败前一致：%+v err=%v", views, err)
	}

	// 恢复文件可写后不必重开台账：先做一次与完成无关、能够成功保存的正常
	// 操作（停用器具），失败的完成信息不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "completed_at") ||
		strings.Contains(string(raw), `"status": "已完成"`) ||
		strings.Contains(string(raw), "2026-10-04T00:00:00Z") {
		t.Fatalf("失败完成被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-1")
	if rp == nil || !rp.Open() || rp.CompletedAt != "" || rp.CertificateNumber != "" {
		t.Fatalf("文件中的计划应停留在完成前状态：%+v", rp)
	}

	// 在同一台账对象重新提交同一计划同一证书：这是一次全新完成，而非幂等
	// 返回；计划从待办移除，完成时间取本次成功操作（10-21）而非失败时刻。
	clock.t = mustDate(t, "2026-10-21")
	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem || done == nil {
		t.Fatalf("恢复后重新提交应真正完成：err=%v idem=%v plan=%v", err, idem, done)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-1" {
		t.Fatalf("成功后完成信息异常：%+v", done)
	}
	if done.CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("完成时间应取本次成功操作而非失败时刻 10-04：%s", done.CompletedAt)
	}
	if items, err := l.Todos(""); err != nil || len(items) != 0 {
		t.Fatalf("真正完成后应移出待办：%+v err=%v", items, err)
	}
	r, _ = l.Review("M-1")
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-1" ||
		r.Plans[0].CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("核对结果应显示本次成功完成的信息：%+v", r.Plans)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if !strings.Contains(string(raw), "2026-10-21T00:00:00Z") ||
		strings.Contains(string(raw), "2026-10-04T00:00:00Z") {
		t.Fatalf("文件应只含成功完成时间、不含失败时刻：\n%s", raw)
	}
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp = reopened.findPlan("P-1")
	if rp == nil || rp.Status != PlanStatusDone || rp.CertificateNumber != "C-1" ||
		rp.CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("重开后完成信息异常：%+v", rp)
	}

	// 真正保存成功后，同证书重复完成同一计划才幂等返回原结果，时钟前进也
	// 不刷新完成时间。
	clock.t = mustDate(t, "2026-10-25")
	again, idem2, err := l.CompletePlan("P-1", "C-1")
	if err != nil || !idem2 {
		t.Fatalf("成功后同证书重复完成应幂等：err=%v idem=%v", err, idem2)
	}
	if again.CompletedAt != done.CompletedAt {
		t.Fatalf("重复完成刷新了完成时间：原 %s 现 %s", done.CompletedAt, again.CompletedAt)
	}
}

// TestCompleteValidationRejectedWhileSavingBlocked 区分文件读写错误与业务
// 拒绝：文件不可写时，不符合完成条件的请求仍按业务校验拒绝（不进行写盘
// 尝试、不改动任何记录），不能被保存错误替代。
func TestCompleteValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustRegister(t, l, "M-3", "信号源", 0.2)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	// 早于计划建立日期（10-02）的证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "m", Error: 0.1, Summary: "s",
	})
	// 他器具证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-OTHER", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	// C-OTHER 已真正用于完成 M-2 的 P-2；M-2 另有未结束的 P-3。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-10-11", Note: "二"})
	if _, _, err := l.CompletePlan("P-2", "C-OTHER"); err != nil {
		t.Fatalf("C-OTHER 完成 P-2 应成功: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-11-11", Note: "三"})
	// 已取消的计划（建在尚无未结束计划的 M-3 上；完成已取消计划在检查
	// 证书归属前即被拒绝）。
	mustPlan(t, l, PlanInput{Number: "P-X", InstrumentID: "M-3", Date: "2026-12-12", Note: "x"})
	if _, err := l.CancelPlan("P-X", "暂停送检"); err != nil {
		t.Fatalf("cancel P-X: %v", err)
	}

	blockSaving(t, l)
	cases := []struct {
		name   string
		plan   string
		cert   string
		notVal bool
	}{
		{"他器具证书", "P-1", "C-OTHER", false},
		{"证书早于计划建立日期", "P-1", "C-OLD", false},
		{"证书已用于完成其他计划", "P-3", "C-OTHER", false},
		{"完成已取消计划", "P-X", "C-1", false},
		{"未知证书", "P-1", "C-NO", true},
		{"未知计划", "P-NO", "C-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, idem, err := l.CompletePlan(tc.plan, tc.cert)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if p != nil || idem {
				t.Fatalf("业务拒绝不能返回计划结果或标记重复：%+v", p)
			}
			if tc.notVal {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("用例 %q 应报 ErrNotFound：%v", tc.name, err)
				}
			} else if !IsValidation(err) {
				t.Fatalf("用例 %q 应是业务校验错误而非文件错误：%v", tc.name, err)
			}
		})
	}

	got := l.findPlan("P-1")
	if !got.Open() || got.CompletedAt != "" || got.CertificateNumber != "" {
		t.Fatalf("业务拒绝后 P-1 被改动：%+v", got)
	}
	// 业务拒绝没有触碰写盘：恢复后同一台账中 C-OTHER 仍只关联 P-2，
	// P-3 保持未完成（业务拒绝不产生任何暂存改动）。
	restoreSaving(t, l)
	if rp := l.findPlan("P-3"); rp == nil || !rp.Open() || rp.CertificateNumber != "" {
		t.Fatalf("P-3 应仍未完成：%+v", rp)
	}
	if px := l.findPlan("P-X"); px == nil || px.Status != PlanStatusCanceled {
		t.Fatalf("P-X 应保持已取消：%+v", px)
	}
	// 恢复可写后 P-1 仍可用同一证书正常完成。
	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem || done == nil || done.Status != PlanStatusDone {
		t.Fatalf("恢复后 P-1 应能正常完成：err=%v idem=%v plan=%v", err, idem, done)
	}
}

// TestFailedCompleteWithOutOfToleranceCertGrantsNoQualification 验证超差证书
// 的失败完成同样不留任何结论；恢复后它能完成计划，但器具不会因此获得使用
// 资格（仍按最近证书超差判定）。
func TestFailedCompleteWithOutOfToleranceCertGrantsNoQualification(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "一"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-BAD", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 5, Summary: "超差",
	})

	blockSaving(t, l)
	if p, _, err := l.CompletePlan("P-1", "C-BAD"); err == nil || p != nil {
		t.Fatalf("超差证书的失败完成也应报保存错误且不返回计划：err=%v plan=%v", err, p)
	}
	if got := l.findPlan("P-1"); !got.Open() || got.CertificateNumber != "" {
		t.Fatalf("失败后计划应仍未完成：%+v", got)
	}

	restoreSaving(t, l)
	done, idem, err := l.CompletePlan("P-1", "C-BAD")
	if err != nil || idem || done == nil || done.Status != PlanStatusDone {
		t.Fatalf("恢复后超差证书应能真正完成计划：err=%v idem=%v plan=%v", err, idem, done)
	}
	// 计划完成不改变使用资格：在用 + 最近证书超差仍被拒绝。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("超差证书完成计划不应授予使用资格：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
}
