package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedCancelSaveLeavesPlanOpen 覆盖核心修复：已通过业务校验的取消在
// 写盘失败时明确报保存错误，计划状态、取消时间与原因、计划日期、最初计划
// 日期、说明、改期历史与待办标记与取消前完全一致；恢复可写后重新提交才
// 真正取消，取消时间取本次成功操作而非失败时刻。
func TestFailedCancelSaveLeavesPlanOpen(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})

	// 先成功改期一次 10-10 → 10-20：失败的取消不能改动计划日期与改期历史。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("前置改期应成功: %v", err)
	}
	savedChange := first.Changes[0]

	// 合法取消在台账文件无法替换时写盘失败。
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	failedReason := "暂停送检"
	p, err := l.CancelPlan("P-1", failedReason)
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回成功取消的计划结果：%+v", p)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 文件仍不可保存时再次提交取消申请：仍报保存失败，而不是因上一次失败
	// 把计划当成已结束而业务拒绝。
	if p2, err := l.CancelPlan("P-1", failedReason); err == nil || p2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v plan=%v", err, p2)
	}

	// 继续使用同一台账对象，无需重新打开：计划仍为未完成，取消时间与原因
	// 保持空白；当前计划日期、最初计划日期、说明与已有改期记录都不改变。
	cur := l.findPlan("P-1")
	if cur == nil {
		t.Fatal("计划丢失")
	}
	if !cur.Open() || cur.CanceledAt != "" || cur.CancelReason != "" {
		t.Fatalf("失败取消改动了取消信息：%+v", cur)
	}
	if cur.PlannedDate != "2026-10-20" || cur.OriginalDate != "2026-10-10" {
		t.Fatalf("失败取消改动了计划日期：%+v", cur)
	}
	if cur.Number != "P-1" || cur.InstrumentID != "M-1" ||
		cur.Note != "周期校准" || cur.Status != PlanStatusOpen {
		t.Fatalf("失败取消改动了计划基本信息：%+v", cur)
	}
	if len(cur.Changes) != 1 || cur.Changes[0] != savedChange {
		t.Fatalf("失败取消改动了改期历史：%+v", cur.Changes)
	}
	// 失败申请的时间与原因不能出现在任何位置（这里直接检查内存中的整个计划）。
	if cur.CanceledAt != "" || strings.Contains(cur.CancelReason, failedReason) {
		t.Fatalf("失败申请的时间或原因留了下来：%+v", cur)
	}

	// 计划仍在待办中，日期标记按最后成功保存的计划日期与查询当天判断：
	// 10-20 查询时 10-20 应为“今天需校准”；若误标取消，待办会直接为空。
	clock.t = mustDate(t, "2026-10-20")
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-20" || items[0].Marker != TodoToday {
		t.Fatalf("失败取消后计划应仍按 10-20 列入待办并标今天需校准：%+v", items)
	}
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].Marker != TodoToday {
		t.Fatalf("按器具查待办异常：%+v err=%v", only, err)
	}

	// 按器具核对与计划查询同样显示未完成、无取消时间与原因。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusOpen ||
		r.Plans[0].Marker != TodoToday ||
		r.Plans[0].CanceledAt != "" || r.Plans[0].CancelReason != "" ||
		len(r.Plans[0].Changes) != 1 {
		t.Fatalf("核对结果应与失败前一致：%+v", r.Plans)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Status != PlanStatusOpen ||
		views[0].CanceledAt != "" || views[0].CancelReason != "" {
		t.Fatalf("计划查询应与失败前一致：%+v err=%v", views, err)
	}

	// 恢复文件可写后不必重开台账：先做一次与取消无关、能够成功保存的正常
	// 操作（停用器具），失败的取消信息不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "canceled_at") ||
		strings.Contains(string(raw), `"status": "已取消"`) ||
		strings.Contains(string(raw), failedReason) ||
		strings.Contains(string(raw), "2026-10-04T00:00:00Z") {
		t.Fatalf("失败取消被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-1")
	if rp == nil || !rp.Open() || rp.CanceledAt != "" || rp.CancelReason != "" {
		t.Fatalf("文件中的计划应停留在取消前状态：%+v", rp)
	}

	// 停用器具的未结束计划仍允许取消：在同一台账对象重新提交合法申请。
	// 计划从待办移除，取消时间取本次成功操作（10-21）而非失败时刻 10-04，
	// 原计划日期与改期历史保留可查；器具状态不被取消改变。
	clock.t = mustDate(t, "2026-10-21")
	done, err := l.CancelPlan("P-1", failedReason)
	if err != nil || done == nil {
		t.Fatalf("恢复后重新提交应成功取消：err=%v plan=%v", err, done)
	}
	if done.Status != PlanStatusCanceled || done.CancelReason != failedReason {
		t.Fatalf("成功后取消信息异常：%+v", done)
	}
	if done.CanceledAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("取消时间应取本次成功操作而非失败时刻 10-04：%s", done.CanceledAt)
	}
	if done.PlannedDate != "2026-10-20" || done.OriginalDate != "2026-10-10" ||
		len(done.Changes) != 1 || done.Changes[0] != savedChange {
		t.Fatalf("取消后原计划与改期历史应保留：%+v", done)
	}
	inst := l.findInstrument("M-1")
	if inst == nil || inst.Status != StatusRetired {
		t.Fatalf("取消不应改变器具状态：%+v", inst)
	}
	if items, err := l.Todos(""); err != nil || len(items) != 0 {
		t.Fatalf("真正取消后应移出待办：%+v err=%v", items, err)
	}
	r, _ = l.Review("M-1")
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusCanceled ||
		r.Plans[0].CancelReason != failedReason ||
		r.Plans[0].CanceledAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("核对结果应显示本次成功取消的信息：%+v", r.Plans)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if !strings.Contains(string(raw), "2026-10-21T00:00:00Z") ||
		strings.Contains(string(raw), "2026-10-04T00:00:00Z") {
		t.Fatalf("文件应只含成功取消时间、不含失败时刻：\n%s", raw)
	}
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp = reopened.findPlan("P-1")
	if rp == nil || rp.Status != PlanStatusCanceled ||
		rp.CancelReason != failedReason ||
		rp.CanceledAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("重开后取消信息异常：%+v", rp)
	}

	// 真正保存成功后：计划编号不能复用，不能再改期、再次取消或用证书完成。
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-11-01", Note: "复用编号"}); !IsValidation(err) {
		t.Fatalf("已取消计划编号不能复用，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-11-01", "x"); !IsValidation(err) {
		t.Fatalf("已取消计划不能改期，得到 %v", err)
	}
	if _, err := l.CancelPlan("P-1", "再次取消"); !IsValidation(err) {
		t.Fatalf("已取消计划不能再次取消，得到 %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-21",
		Expiry: "2027-10-21", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, _, err := l.CompletePlan("P-1", "C-1"); !IsValidation(err) {
		t.Fatalf("已取消计划不能用证书完成，得到 %v", err)
	}
}

// TestCancelValidationRejectedWhileSavingBlocked 区分文件读写错误与业务拒绝：
// 文件不可写时，空白原因、未知计划、已结束计划仍按现有方式拒绝（不进行写盘
// 尝试、不改动任何记录），不能被保存错误替代。
func TestCancelValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-2", Date: "2026-11-10", Note: "二"})
	// P-2 先在可写时成功取消，作为已结束计划。
	if _, err := l.CancelPlan("P-2", "停产"); err != nil {
		t.Fatalf("前置取消 P-2 应成功: %v", err)
	}

	blockSaving(t, l)
	cases := []struct {
		name   string
		number string
		reason string
		notVal bool
	}{
		{"空白原因", "P-1", "  ", false},
		{"未知计划", "NOPE", "x", true},
		{"已取消计划", "P-2", "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := l.CancelPlan(tc.number, tc.reason)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if p != nil {
				t.Fatalf("业务拒绝不能返回计划结果：%+v", p)
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
	if !got.Open() || got.CanceledAt != "" || got.CancelReason != "" {
		t.Fatalf("业务拒绝后 P-1 被改动：%+v", got)
	}
	// 恢复后 P-1 仍可正常取消。
	restoreSaving(t, l)
	if p, err := l.CancelPlan("P-1", "暂停送检"); err != nil || p == nil ||
		p.Status != PlanStatusCanceled {
		t.Fatalf("恢复后 P-1 应能正常取消：err=%v plan=%v", err, p)
	}
}
