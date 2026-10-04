package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// completeSetup 准备一件器具、一项未结束计划和一张符合完成条件的证书。
func completeSetup(t *testing.T, l *Ledger) {
	t.Helper()
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-04",
		Expiry: "2027-10-04", Method: "m", Error: 0.1, Summary: "s",
	})
}

// TestFailedCompleteSaveLeavesPlanOpen 覆盖核心修复：已通过业务校验的完成在
// 写盘失败时明确报保存错误，不返回成功的计划结果；同一台账对象随后看到的计划、
// 待办、核对与计划查询都与操作前一致，失败的完成时间和证书不留下、不占证书、
// 不被顺带写盘；恢复可写后重新提交才真正完成，完成时间取本次成功操作。
func TestFailedCompleteSaveLeavesPlanOpen(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-04")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	completeSetup(t, l)

	// 提交合法完成时台账文件无法替换。
	blockSaving(t, l)
	p, idem, err := l.CompletePlan("P-1", "C-1")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil || idem {
		t.Fatalf("保存失败不能返回已完成计划或标记重复完成：plan=%v idem=%v", p, idem)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 文件仍不可写时再次提交同一计划和同一证书：仍报保存失败，而不是跳过保存
	// 返回“此前已完成”的成功结果。
	if p2, idem2, err := l.CompletePlan("P-1", "C-1"); err == nil || p2 != nil || idem2 {
		t.Fatalf("再次提交应仍报保存失败且不是重复完成：err=%v plan=%v idem=%v",
			err, p2, idem2)
	}

	// 同一台账对象无需重开：计划基本信息、计划日期、改期记录和未结束状态不变。
	cur := l.findPlan("P-1")
	if cur == nil {
		t.Fatal("计划丢失")
	}
	if cur.Status != PlanStatusOpen {
		t.Fatalf("失败的完成把计划标成 %s：%+v", cur.Status, cur)
	}
	if cur.CompletedAt != "" || cur.CertificateNumber != "" {
		t.Fatalf("失败的完成写入了完成时间或关联证书：%+v", cur)
	}
	if cur.PlannedDate != "2026-10-10" || cur.OriginalDate != "2026-10-10" ||
		cur.Number != "P-1" || cur.InstrumentID != "M-1" || cur.Note != "周期校准" {
		t.Fatalf("失败的完成改动了计划基本信息：%+v", cur)
	}
	if len(cur.Changes) != 0 {
		t.Fatalf("失败的完成改动了改期记录：%+v", cur.Changes)
	}

	// 待办：全量与按器具筛选都仍能看到该计划，标记按当前日期（10-04）判断。
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-10" || items[0].Marker != TodoFuture {
		t.Fatalf("失败后计划不应从待办消失且标记不变：%+v", items)
	}
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].Marker != TodoFuture {
		t.Fatalf("按器具查待办异常：%+v err=%v", only, err)
	}

	// 按器具核对与计划查询：计划仍未完成、无完成信息、有待办标记。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusOpen ||
		r.Plans[0].CompletedAt != "" || r.Plans[0].CertificateNumber != "" ||
		r.Plans[0].Marker != TodoFuture {
		t.Fatalf("核对结果应与失败前一致：%+v", r.Plans)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Status != PlanStatusOpen ||
		views[0].CompletedAt != "" || views[0].CertificateNumber != "" {
		t.Fatalf("计划查询应与失败前一致：%+v err=%v", views, err)
	}

	// 证书未被算作已用于完成任何计划。
	for i := range l.data.Plans {
		if l.data.Plans[i].CertificateNumber == "C-1" &&
			l.data.Plans[i].Status == PlanStatusDone {
			t.Fatalf("失败的完成占用了证书 C-1：%+v", l.data.Plans[i])
		}
	}

	// 阻断期间台账路径被同名目录占据，无法读取文件；磁盘内容在恢复后通过重开
	// 同一台账核对（见下）。此处继续确认：阻断期间任何成功保存都未发生。

	// 恢复文件可写后不必重开台账：先做一次与完成无关、能够成功保存的正常操作
	// （顺带停用器具），失败的完成信息不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), PlanStatusDone) ||
		strings.Contains(string(raw), "completed_at") ||
		strings.Contains(string(raw), `"certificate_number"`) {
		t.Fatalf("失败的完成被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-1")
	if rp == nil || rp.Status != PlanStatusOpen ||
		rp.CompletedAt != "" || rp.CertificateNumber != "" {
		t.Fatalf("文件中的计划应停留在最后成功保存的未完成状态：%+v", rp)
	}

	// 停用器具的计划仍可完成：时钟拨到 10-21，同一台账对象重新提交才真正完成。
	// 完成时间必须是本次成功操作的时间（10-21），不能沿用失败时刻（10-04）。
	clock.t = mustDate(t, "2026-10-21")
	done, idem, err := l.CompletePlan("P-1", "C-1")
	if err != nil || idem || done == nil {
		t.Fatalf("恢复后重新提交应真正成功：err=%v idem=%v plan=%v", err, idem, done)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-1" {
		t.Fatalf("成功后计划字段异常：%+v", done)
	}
	if done.CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("完成时间应取本次成功操作 10-21，得到 %s", done.CompletedAt)
	}
	if done.PlannedDate != "2026-10-10" || done.OriginalDate != "2026-10-10" ||
		len(done.Changes) != 0 {
		t.Fatalf("成功完成不应改动计划日期或改期历史：%+v", done)
	}

	// 成功后：待办移除该计划，核对显示关联证书与本次完成时间。
	if items, err := l.Todos(""); err != nil || len(items) != 0 {
		t.Fatalf("成功后计划应从待办移除：items=%+v err=%v", items, err)
	}
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review after complete: %v", err)
	}
	if len(r2.Plans) != 1 || r2.Plans[0].Status != PlanStatusDone ||
		r2.Plans[0].CertificateNumber != "C-1" ||
		r2.Plans[0].CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("核对应显示关联证书与本次完成时间：%+v", r2.Plans)
	}

	// 磁盘文件同步为本次成功结果，且不残留失败时刻 10-04。
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if !strings.Contains(string(raw), PlanStatusDone) ||
		!strings.Contains(string(raw), "2026-10-21T00:00:00Z") {
		t.Fatalf("成功的完成应已写入文件：\n%s", raw)
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

	// 真正保存成功后，再用同一证书提交：返回原结果、保留原完成时间（幂等）。
	clock.t = mustDate(t, "2026-11-01")
	again, idem2, err := l.CompletePlan("P-1", "C-1")
	if err != nil || !idem2 || again == nil {
		t.Fatalf("真正成功后同证书重复完成应幂等返回原结果：err=%v idem=%v", err, idem2)
	}
	if again.CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("重复完成不能刷新完成时间，得到 %s", again.CompletedAt)
	}
}

// TestCompleteValidationRejectedWhileSavingBlocked 区分文件读写错误与业务拒绝：
// 文件不可写时，不符合完成条件的请求仍按业务拒绝处理，不进行写盘尝试、不改动
// 任何记录，不能被保存错误替代。
func TestCompleteValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-04")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-04",
		Expiry: "2027-10-04", Method: "m", Error: 0.1, Summary: "s",
	})
	// 早于计划建立日期（10-04）的证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "m", Error: 0.1, Summary: "s",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-OTHER", CalDate: "2026-10-04",
		Expiry: "2027-10-04", Method: "m", Error: 0.1, Summary: "s",
	})
	// M-1 先有一项已用 C-1 完成的旧计划，再建未结束的 P-1：C-1 因而已被占用。
	mustPlan(t, l, PlanInput{Number: "P-OLD", InstrumentID: "M-1",
		Date: "2026-10-05", Note: "旧"})
	if _, _, err := l.CompletePlan("P-OLD", "C-1"); err != nil {
		t.Fatalf("前置完成 P-OLD 失败: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "一"})
	// M-2 的计划 P-2 已用 C-OTHER 完成；另有一项已取消计划 P-3。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2",
		Date: "2026-10-10", Note: "二"})
	if _, _, err := l.CompletePlan("P-2", "C-OTHER"); err != nil {
		t.Fatalf("前置完成 P-2 失败: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-3", InstrumentID: "M-2",
		Date: "2026-10-11", Note: "三"})
	if _, err := l.CancelPlan("P-3", "暂停送检"); err != nil {
		t.Fatalf("前置取消 P-3 失败: %v", err)
	}

	blockSaving(t, l)
	bad := []struct {
		name     string
		plan     string
		cert     string
		notFound bool
		wantSub  string
	}{
		{"证书已用于完成其他计划", "P-1", "C-1", false, "已用于完成计划"},
		{"校准日期早于建立日期", "P-1", "C-OLD", false, "建立日期"},
		{"证书属于别的器具", "P-1", "C-OTHER", false, "属于器具"},
		{"完成已取消计划", "P-3", "C-OTHER", false, "已取消"},
		{"证书编号不存在", "P-1", "C-NO", true, ""},
		{"计划编号不存在", "P-NO", "C-1", true, ""},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			p, idem, err := l.CompletePlan(tc.plan, tc.cert)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if p != nil || idem {
				t.Fatalf("业务拒绝不能返回计划结果或标记幂等：plan=%v idem=%v", p, idem)
			}
			if tc.notFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("用例 %q 应报 ErrNotFound：%v", tc.name, err)
				}
			} else {
				if !IsValidation(err) {
					t.Fatalf("用例 %q 应是业务校验错误而非文件错误：%v", tc.name, err)
				}
				if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
					t.Fatalf("用例 %q 错误信息应含 %q：%v", tc.name, tc.wantSub, err)
				}
			}
		})
	}

	// 业务拒绝不改动任何记录：P-1 仍未完成、无完成信息。
	got := l.findPlan("P-1")
	if got.Status != PlanStatusOpen || got.CompletedAt != "" || got.CertificateNumber != "" {
		t.Fatalf("业务拒绝后计划被改动：%+v", got)
	}
	// 待办仍列出 P-1（P-3 已取消、P-OLD/P-2 已完成）。
	items, err := l.Todos("")
	if err != nil || len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("业务拒绝后待办异常：%+v err=%v", items, err)
	}
}

// TestFailedCompleteWithOutOfToleranceCertRetried 验证超差证书保存失败时同样
// 不完成计划、不改变器具使用资格；恢复后用该超差证书成功完成，器具仍不能获得
// 使用资格。
func TestFailedCompleteWithOutOfToleranceCertRetried(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-04")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "一"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-BAD", CalDate: "2026-10-04",
		Expiry: "2027-10-04", Method: "m", Error: 5, Summary: "超差",
	})

	// 前置：超差使器具不可使用。
	d, _ := l.CanUse("M-1")
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("前置条件失效：应因超差不可用，得到 %v", d.Reasons)
	}

	blockSaving(t, l)
	if p, _, err := l.CompletePlan("P-1", "C-BAD"); err == nil || p != nil {
		t.Fatalf("超差证书完成写盘失败也应报保存错误且不返回计划：err=%v plan=%v", err, p)
	}
	if got := l.findPlan("P-1"); !got.Open() || got.CertificateNumber != "" {
		t.Fatalf("失败后计划不应被完成：%+v", got)
	}
	d2, _ := l.CanUse("M-1")
	if d2.Allowed || !containsReason(d2.Reasons, "超差") {
		t.Fatalf("失败的完成不得改变使用资格：allowed=%v reasons=%v", d2.Allowed, d2.Reasons)
	}

	// 恢复后用同一超差证书完成：计划完成，但器具仍不可使用。
	restoreSaving(t, l)
	clock.t = mustDate(t, "2026-10-21")
	done, _, err := l.CompletePlan("P-1", "C-BAD")
	if err != nil || done == nil || done.Status != PlanStatusDone {
		t.Fatalf("恢复后超差证书应能完成计划：err=%v plan=%v", err, done)
	}
	if done.CompletedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("完成时间应取本次成功操作，得到 %s", done.CompletedAt)
	}
	d3, _ := l.CanUse("M-1")
	if d3.Allowed || !containsReason(d3.Reasons, "超差") {
		t.Fatalf("超差证书完成计划不应赋予使用资格：allowed=%v reasons=%v", d3.Allowed, d3.Reasons)
	}
}
