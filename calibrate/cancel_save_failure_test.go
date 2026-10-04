package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedCancelSaveLeavesNoChange 覆盖核心修复：已通过业务校验的取消在
// 写盘失败时明确报保存错误，计划仍为取消前的未完成状态——当前计划日期、最初
// 计划日期、说明与改期历史保持原样，取消时间和原因不留下本次申请的内容；
// 待办仍列出该计划，按器具核对也展示尚未取消的计划。恢复可写后重新提交才生效，
// 取消时间和原因取本次成功提交而非失败那次。
func TestFailedCancelSaveLeavesNoChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})

	// 一条成功的改期留痕：10-10 → 10-20，失败取消不得改动它。
	clock.t = mustDate(t, "2026-10-03")
	if _, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突"); err != nil {
		t.Fatalf("改期应成功: %v", err)
	}
	savedChange := l.findPlan("P-1").Changes[0]

	// 提交合法取消时台账文件无法替换。
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	p, err := l.CancelPlan("P-1", "暂停送检")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回成功的计划结果：%+v", p)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 文件仍不可写时再次提交：仍报保存错误，不能因为上一次失败就按“已结束”拒绝。
	if p2, err := l.CancelPlan("P-1", "暂停送检"); err == nil || p2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v plan=%v", err, p2)
	} else if IsValidation(err) {
		t.Fatalf("再次提交不得被当成已结束而业务拒绝：%v", err)
	}

	// 继续使用同一台账对象，无需重新打开：计划仍为取消前的未完成状态，
	// 编号、所属器具、当前与最初计划日期、说明、改期历史都保持原样，
	// 取消时间和原因不能留下本次申请的内容。
	cur := l.findPlan("P-1")
	if cur == nil {
		t.Fatal("计划丢失")
	}
	if !cur.Open() || cur.Status != PlanStatusOpen {
		t.Fatalf("失败取消后计划不应结束：%+v", cur)
	}
	if cur.PlannedDate != "2026-10-20" || cur.OriginalDate != "2026-10-10" ||
		cur.Note != "周期校准" || cur.Number != "P-1" || cur.InstrumentID != "M-1" {
		t.Fatalf("失败取消改动了计划基本信息：%+v", cur)
	}
	if cur.CanceledAt != "" || cur.CancelReason != "" {
		t.Fatalf("失败取消留下了取消时间或原因：%+v", cur)
	}
	if len(cur.Changes) != 1 || cur.Changes[0] != savedChange {
		t.Fatalf("失败取消改动了改期历史：%+v", cur.Changes)
	}

	// 待办仍能找到这项计划，并按查询当天判断标记：10-20 当天为“今天需校准”。
	clock.t = mustDate(t, "2026-10-20")
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-20" || items[0].Marker != TodoToday {
		t.Fatalf("待办应仍包含未取消的计划并标记今天需校准：%+v", items)
	}
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].Marker != TodoToday {
		t.Fatalf("按器具查待办异常：%+v err=%v", only, err)
	}

	// 按器具核对与计划查询同样展示尚未取消的计划。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusOpen ||
		r.Plans[0].CanceledAt != "" || r.Plans[0].CancelReason != "" ||
		r.Plans[0].Marker != TodoToday {
		t.Fatalf("核对应展示尚未取消的计划：%+v", r.Plans)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Status != PlanStatusOpen ||
		views[0].CanceledAt != "" {
		t.Fatalf("计划查询应与取消前一致：%+v err=%v", views, err)
	}

	// 恢复文件可写后不必重开台账：先做一次与取消无关、能够成功保存的正常操作
	// （顺带停用器具），失败的取消内容不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "暂停送检") || strings.Contains(string(raw), "已取消") {
		t.Fatalf("失败取消被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if rp := reopened.findPlan("P-1"); rp == nil || !rp.Open() {
		t.Fatalf("文件中的计划应停留在取消前的未完成状态：%+v", rp)
	}

	// 停用器具的未结束计划仍允许取消：在同一台账对象重新提交合法申请才成功，
	// 取消时间和原因取这次成功提交，而不是失败那次。
	clock.t = mustDate(t, "2026-10-21")
	ok, err := l.CancelPlan("P-1", "实验室长期停用")
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功：err=%v plan=%v", err, ok)
	}
	if ok.Status != PlanStatusCanceled ||
		ok.CanceledAt != "2026-10-21T00:00:00Z" || ok.CancelReason != "实验室长期停用" {
		t.Fatalf("成功后取消时间与原因应取本次提交：%+v", ok)
	}
	if ok.PlannedDate != "2026-10-20" || ok.OriginalDate != "2026-10-10" ||
		len(ok.Changes) != 1 || ok.Changes[0] != savedChange {
		t.Fatalf("取消不应改动原计划与改期历史：%+v", ok)
	}
	inst := l.findInstrument("M-1")
	if inst == nil || inst.Status != StatusRetired {
		t.Fatalf("取消不应改变器具状态：%+v", inst)
	}

	// 成功取消后不再列入待办；核对展示取消时间和原因；不能再次取消或改期。
	if items, err := l.Todos(""); err != nil || len(items) != 0 {
		t.Fatalf("成功取消后待办应为空：%+v err=%v", items, err)
	}
	r, err = l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusCanceled ||
		r.Plans[0].CanceledAt != "2026-10-21T00:00:00Z" ||
		r.Plans[0].CancelReason != "实验室长期停用" {
		t.Fatalf("核对应展示取消时间和原因：%+v", r.Plans)
	}
	if _, err := l.CancelPlan("P-1", "再次取消"); !IsValidation(err) {
		t.Fatalf("已取消计划不能再次取消：%v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-11-01", "已取消还想改期"); !IsValidation(err) {
		t.Fatalf("已取消计划不能改期：%v", err)
	}

	// 重新打开文件：磁盘上同样是本次成功提交的内容，失败那次的原因不在其中。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-1")
	if rp == nil || rp.Status != PlanStatusCanceled ||
		rp.CanceledAt != "2026-10-21T00:00:00Z" || rp.CancelReason != "实验室长期停用" {
		t.Fatalf("重开后取消状态异常：%+v", rp)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "暂停送检") {
		t.Fatalf("失败申请的原因被写入文件：\n%s", raw)
	}
}

// TestCancelValidationRejectedWhileSavingBlocked 区分文件读写错误与业务拒绝：
// 文件不可写时，计划不存在、已结束或取消原因为空白仍按既有方式拒绝（校验错误
// 或 ErrNotFound），且不进行写盘尝试、不改动任何已有记录。
func TestCancelValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	// 先建立并成功取消 P-2，构造一项已结束计划；再建立未结束的 P-1。
	mustPlan(t, l, PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-10-11", Note: "备用"})
	if _, err := l.CancelPlan("P-2", "不再需要"); err != nil {
		t.Fatalf("取消 P-2 应成功: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	blockSaving(t, l)
	t.Run("空白原因", func(t *testing.T) {
		if p, err := l.CancelPlan("P-1", "  "); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("空白原因应业务拒绝：err=%v plan=%v", err, p)
		}
	})
	t.Run("未知计划", func(t *testing.T) {
		if p, err := l.CancelPlan("NOPE", "x"); err == nil || p != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("未知计划应报 ErrNotFound：err=%v plan=%v", err, p)
		}
	})
	t.Run("已结束计划", func(t *testing.T) {
		if p, err := l.CancelPlan("P-2", "再次取消"); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("已结束计划应业务拒绝：err=%v plan=%v", err, p)
		}
	})

	got := l.findPlan("P-1")
	if !got.Open() || got.CanceledAt != "" || got.CancelReason != "" ||
		got.PlannedDate != "2026-10-10" {
		t.Fatalf("业务拒绝后计划被改动：%+v", got)
	}
	ended := l.findPlan("P-2")
	if ended.Status != PlanStatusCanceled || ended.CancelReason != "不再需要" {
		t.Fatalf("已结束计划的记录被改动：%+v", ended)
	}
}
