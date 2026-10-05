package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedPlanSaveLeavesNoPlan 覆盖核心修复：合法的新建计划申请在写盘失败时
// 明确报保存错误，计划只有真正保存成功才算建立——失败的计划不占用编号，也不占用
// 该器具唯一的未结束计划名额；计划查询、待办与按器具核对都只能看到此前已保存的
// 计划。文件仍不可写时再次提交相同编号与内容仍报保存错误（不会被当成编号重复或
// 器具已有未结束计划）。恢复可写后无需重新打开台账，先做一次正常状态切换也不
// 夹带失败的计划；重新提交才建立，建立时间取本次成功申请的时间。
func TestFailedPlanSaveLeavesNoPlan(t *testing.T) {
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
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格",
	})

	// 先成功建立并改期、再取消一项旧计划：该器具已有结束计划，失败的新计划
	// 不得改动它的取消信息与改期历史。
	mustPlan(t, l, PlanInput{Number: "P-OLD", InstrumentID: "M-1",
		Date: "2026-10-09", Note: "旧计划"})
	clock.t = mustDate(t, "2026-10-03")
	if _, err := l.ReschedulePlan("P-OLD", "2026-10-11", "排期调整"); err != nil {
		t.Fatalf("旧计划改期应成功: %v", err)
	}
	if _, err := l.CancelPlan("P-OLD", "暂停送检"); err != nil {
		t.Fatalf("旧计划取消应成功: %v", err)
	}
	savedCancel := l.findPlan("P-OLD").CancelReason
	savedChange := l.findPlan("P-OLD").Changes[0]

	// 提交合法新建申请时台账文件无法替换。
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	in := PlanInput{Number: "P-NEW", InstrumentID: "M-1",
		Date: "2026-10-20", Note: "年度例行校准"}
	p, err := l.CreatePlan(in)
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回已建立成功的计划：%+v", p)
	}
	if IsValidation(err) || IsConflict(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 文件仍不可写时再次提交相同编号与内容：仍报保存错误，不能被上次失败留下的
	// 计划挡住——既不能说成编号重复，也不能说成器具已有未结束计划。
	if p2, err := l.CreatePlan(in); err == nil || p2 != nil {
		t.Fatalf("再次提交应仍报保存错误：err=%v plan=%v", err, p2)
	} else if IsValidation(err) {
		t.Fatalf("再次提交不得被当成编号重复或已有计划而业务拒绝：%v", err)
	}

	// 继续使用同一台账对象，无需重新打开：失败的计划不可见，此前已保存的计划保持原样。
	if got := l.findPlan("P-NEW"); got != nil {
		t.Fatalf("失败的新计划不得留在台账内存中：%+v", got)
	}
	cur := l.findPlan("P-OLD")
	if cur == nil || cur.Status != PlanStatusCanceled || cur.CancelReason != savedCancel ||
		len(cur.Changes) != 1 || cur.Changes[0] != savedChange {
		t.Fatalf("此前已保存计划的取消与改期信息被改动：%+v", cur)
	}

	// 待办为空（只有一项已取消的旧计划）；按器具筛选同样查不到失败计划。
	if items, err := l.Todos(""); err != nil || len(items) != 0 {
		t.Fatalf("失败后待办应只能看到此前已保存的计划：%+v err=%v", items, err)
	}
	if items, err := l.Todos("M-1"); err != nil || len(items) != 0 {
		t.Fatalf("按器具待办不应包含失败计划：%+v err=%v", items, err)
	}
	// 计划查询与按器具核对只展示已取消的旧计划，且没有失败计划。
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Number != "P-OLD" ||
		views[0].Status != PlanStatusCanceled {
		t.Fatalf("计划查询应只看到此前已保存的计划：%+v err=%v", views, err)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Number != "P-OLD" {
		t.Fatalf("核对不应夹带失败的新计划：%+v", r.Plans)
	}

	// 器具状态、证书与使用记录均不受此次失败申请影响。
	if inst := l.findInstrument("M-1"); inst == nil || inst.Status != StatusInUse {
		t.Fatalf("器具状态被失败申请改动：%+v", inst)
	}
	if cs := l.certificatesOf("M-1"); len(cs) != 1 || cs[0].Number != "C-1" {
		t.Fatalf("证书被失败申请改动：%+v", cs)
	}
	if n := len(l.UsageRecords()); n != 0 {
		t.Fatalf("失败申请不应留下使用记录：%d", n)
	}

	// 恢复文件可写后不必重开台账：先做一次与新建计划无关、能够成功保存的正常
	// 操作（停用器具），失败的计划不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "P-NEW") || strings.Contains(string(raw), "年度例行校准") {
		t.Fatalf("失败的新计划被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.findPlan("P-NEW") != nil {
		t.Fatal("文件中不应存在失败的计划")
	}

	// 停用器具仍允许建立计划：在同一台账对象重新提交合法申请才成功，
	// 建立时间取本次成功提交，而不是失败那次。
	clock.t = mustDate(t, "2026-10-09")
	ok, err := l.CreatePlan(in)
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功建立：err=%v plan=%v", err, ok)
	}
	if ok.Number != "P-NEW" || ok.InstrumentID != "M-1" ||
		ok.PlannedDate != "2026-10-20" || ok.OriginalDate != "2026-10-20" ||
		ok.Note != "年度例行校准" || ok.Status != PlanStatusOpen {
		t.Fatalf("成功建立的计划内容不符：%+v", ok)
	}
	if ok.CreatedAt != "2026-10-09T00:00:00Z" {
		t.Fatalf("建立时间应取本次成功申请而非失败那次：%s", ok.CreatedAt)
	}
	if len(ok.Changes) != 0 || ok.CanceledAt != "" || ok.CompletedAt != "" {
		t.Fatalf("新计划不应带改期/取消/完成信息：%+v", ok)
	}

	// 成功建立后列入待办并出现在按器具核对中；先在计划日前查到“未到计划日”，
	// 时钟前进到计划日之后再查则重新判断为“逾期”。
	items, err := l.Todos("")
	if err != nil || len(items) != 1 || items[0].PlanNumber != "P-NEW" ||
		items[0].Marker != TodoFuture {
		t.Fatalf("计划日前待办应包含新计划并标记未到计划日：%+v err=%v", items, err)
	}
	clock.t = mustDate(t, "2026-10-21")
	items, err = l.Todos("")
	if err != nil || len(items) != 1 || items[0].PlanNumber != "P-NEW" ||
		items[0].Marker != TodoOverdue {
		t.Fatalf("待办标记应按查询当天重新判断：%+v err=%v", items, err)
	}
	r, err = l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 2 {
		t.Fatalf("核对应同时展示旧的已取消计划与新计划：%+v", r.Plans)
	}
	var newView *PlanView
	for i := range r.Plans {
		if r.Plans[i].Number == "P-NEW" {
			newView = &r.Plans[i]
		}
	}
	if newView == nil || !newView.Open() || newView.CreatedAt != "2026-10-09T00:00:00Z" ||
		newView.Marker != TodoOverdue {
		t.Fatalf("核对中的新计划异常：%+v", newView)
	}

	// 已保存的编号不能复用：再次用同一编号建立按业务拒绝。
	if _, err := l.CreatePlan(in); !IsValidation(err) {
		t.Fatalf("已保存编号复用应业务拒绝：%v", err)
	}

	// 重新打开文件：磁盘上同样是本次成功提交的内容，失败那次的申请时间不在其中。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-NEW")
	if rp == nil || !rp.Open() || rp.CreatedAt != "2026-10-09T00:00:00Z" {
		t.Fatalf("重开后新计划异常：%+v", rp)
	}
}

// TestFailedPlanSaveAfterFinishedPlanSlot 覆盖“已有结束计划的器具”：器具只有一项
// 已完成的计划，合法新建申请写盘失败不占用其唯一的未结束计划名额；恢复后重新提交
// 可以正常建立，且不会把失败那次当成已存在的计划。
func TestFailedPlanSaveAfterFinishedPlanSlot(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0, Summary: "s",
	})
	mustPlan(t, l, PlanInput{Number: "P-DONE", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "首次"})
	if _, _, err := l.CompletePlan("P-DONE", "C-1"); err != nil {
		t.Fatalf("完成旧计划应成功: %v", err)
	}

	in := PlanInput{Number: "P-NEXT", InstrumentID: "M-1",
		Date: "2026-10-20", Note: "下一周期"}
	blockSaving(t, l)
	if p, err := l.CreatePlan(in); err == nil || p != nil {
		t.Fatalf("写盘失败应返回保存错误且无计划：err=%v p=%v", err, p)
	}
	// 失败不占用名额：仍不可见，待办为空。
	if l.findPlan("P-NEXT") != nil {
		t.Fatal("失败的计划不应占用名额")
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("失败后待办应为空：%+v", items)
	}
	// 文件仍不可写时再次提交仍是保存错误，而不是“已有未结束计划”。
	if _, err := l.CreatePlan(in); IsValidation(err) {
		t.Fatalf("再次提交不得被当成器具已有未结束计划：%v", err)
	}

	restoreSaving(t, l)
	clock.t = mustDate(t, "2026-10-09")
	ok, err := l.CreatePlan(in)
	if err != nil || ok == nil {
		t.Fatalf("恢复后应能建立新计划：err=%v plan=%v", err, ok)
	}
	if items, _ := l.Todos(""); len(items) != 1 || items[0].PlanNumber != "P-NEXT" {
		t.Fatalf("成功建立后待办应包含新计划：%+v", items)
	}
	// 旧的已完成计划与关联证书保持原样。
	done := l.findPlan("P-DONE")
	if done == nil || done.Status != PlanStatusDone || done.CertificateNumber != "C-1" {
		t.Fatalf("已完成计划被失败申请改动：%+v", done)
	}
}

// TestPlanValidationRejectedWhileSavingBlocked 区分文件读写错误与业务拒绝：文件不可写
// 时，未通过业务检查的申请仍按原有方式拒绝（校验错误或 ErrNotFound），不能被混成
// 保存失败，且不改动任何已有记录。恢复后若原计划日期已过去，重新提交也按业务
// 规则拒绝，台账仍不新增计划。
func TestPlanValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	// M-1 已有一项成功保存的未结束计划，占用其名额与编号 P-1。
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期"})

	blockSaving(t, l)
	t.Run("空白说明", func(t *testing.T) {
		if p, err := l.CreatePlan(PlanInput{Number: "P-X", InstrumentID: "M-1",
			Date: "2026-10-20", Note: "  "}); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("空白说明应业务拒绝：err=%v plan=%v", err, p)
		}
	})
	t.Run("日期早于今天", func(t *testing.T) {
		if p, err := l.CreatePlan(PlanInput{Number: "P-X", InstrumentID: "M-2",
			Date: "2026-10-01", Note: "过期"}); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("过去日期应业务拒绝：err=%v plan=%v", err, p)
		}
	})
	t.Run("编号已保存", func(t *testing.T) {
		if p, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-2",
			Date: "2026-10-20", Note: "复用编号"}); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("编号重复应业务拒绝：err=%v plan=%v", err, p)
		}
	})
	t.Run("器具有未结束计划", func(t *testing.T) {
		if p, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-1",
			Date: "2026-10-20", Note: "再建一项"}); err == nil || p != nil || !IsValidation(err) {
			t.Fatalf("占用名额应业务拒绝：err=%v plan=%v", err, p)
		}
	})
	t.Run("未知器具", func(t *testing.T) {
		if p, err := l.CreatePlan(PlanInput{Number: "P-X", InstrumentID: "NOPE",
			Date: "2026-10-20", Note: "x"}); err == nil || p != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("未知器具应报 ErrNotFound：err=%v plan=%v", err, p)
		}
	})

	// 业务拒绝后台账不变：只有此前保存的 P-1，M-2 仍无计划。
	if l.findPlan("P-X") != nil || l.findPlan("P-2") != nil {
		t.Fatal("业务拒绝留下了计划记录")
	}
	if items, _ := l.Todos(""); len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("业务拒绝后待办异常：%+v", items)
	}

	// 一份在不可写期间合法、但写盘失败的申请：恢复后时钟前进到原计划日期之后，
	// 重新提交必须按本次申请的本机今天重新判断日期并明确拒绝，台账仍不新增计划。
	restoreSaving(t, l)
	// 先完成一次正常保存以重建台账文件，再重新挡住写盘。
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	in := PlanInput{Number: "P-LATE", InstrumentID: "M-2",
		Date: "2026-10-05", Note: "原计划日"}
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	if _, err := l.CreatePlan(in); err == nil {
		t.Fatal("写盘失败应返回保存错误")
	}
	restoreSaving(t, l)
	clock.t = mustDate(t, "2026-10-06")
	if p, err := l.CreatePlan(in); err == nil || p != nil || !IsValidation(err) {
		t.Fatalf("原计划日期已过去应明确业务拒绝：err=%v plan=%v", err, p)
	}
	if l.findPlan("P-LATE") != nil {
		t.Fatal("过期日期拒绝后不应新增计划")
	}
	if items, _ := l.Todos("M-2"); len(items) != 0 {
		t.Fatalf("M-2 不应有待办：%+v", items)
	}
	// 把日期改到未来后同一编号与器具即可正常建立，停用器具的计划名额同样不受限制。
	ok, err := l.CreatePlan(PlanInput{Number: "P-LATE", InstrumentID: "M-2",
		Date: "2026-10-06", Note: "原计划日"})
	if err != nil || ok == nil || ok.CreatedAt != "2026-10-06T00:00:00Z" {
		t.Fatalf("改到未来日期后应能建立，建立时间取本次申请：err=%v plan=%v", err, ok)
	}
}
