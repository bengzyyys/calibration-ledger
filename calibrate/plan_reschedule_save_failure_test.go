package calibrate

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestFailedRescheduleTakesNoEffect 覆盖核心修复：合法改期在台账文件无法
// 写入或替换时只报保存错误，当前计划日期、改期历史、待办标记与文件内容都
// 保持最后成功保存的状态；恢复可写后重新提交才生效，新记录从最后成功保存
// 的日期起算，失败申请的日期、原因与时间彻底消失。
func TestFailedRescheduleTakesNoEffect(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "年度校准"})

	// 第一次改期保存成功：10-10 → 10-20。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("首次改期应成功: %v", err)
	}
	firstAt := first.Changes[0].ChangedAt

	// 挡住台账文件的原子替换。
	blockSaving(t, l)

	// 新日期等于当前日期的合法改期同样只有保存成功才能留痕：操作当天
	// 正好是当前计划日 10-20，保存失败后不得多出一条记录。
	clock.t = mustDate(t, "2026-10-20")
	if p, err := l.ReschedulePlan("P-1", "2026-10-20", "同日重申"); err == nil || p != nil {
		t.Fatalf("同日改期写盘失败应返回保存错误且无计划结果：p=%v err=%v", p, err)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 时钟走到 11-01：提交合法新日期 11-05，保存失败。
	clock.t = mustDate(t, "2026-11-01")
	failedAt := clock.t.Format(time.RFC3339)
	p, err := l.ReschedulePlan("P-1", "2026-11-05", "车间占用")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回成功的计划结果：%+v", p)
	}
	if IsValidation(err) || IsConflict(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 无需重新打开台账：内存中的计划仍是最后成功保存的状态。
	got := l.findPlan("P-1")
	if got.PlannedDate != "2026-10-20" || got.OriginalDate != "2026-10-10" {
		t.Fatalf("失败改期改动了当前或最初计划日期：%+v", got)
	}
	if got.Number != "P-1" || got.InstrumentID != "M-1" || got.Note != "年度校准" || !got.Open() {
		t.Fatalf("计划编号、器具、说明或未结束状态被改动：%+v", got)
	}
	if len(got.Changes) != 1 {
		t.Fatalf("失败改期进入了历史，共 %d 条", len(got.Changes))
	}
	c0 := got.Changes[0]
	if c0.From != "2026-10-10" || c0.To != "2026-10-20" ||
		c0.Reason != "实验室排期冲突" || c0.ChangedAt != firstAt {
		t.Fatalf("已成功保存的改期记录前后日期、时间、原因或次序被改动：%+v", c0)
	}
	for _, c := range got.Changes {
		if c.To == "2026-11-05" || c.Reason == "车间占用" || c.ChangedAt == failedAt {
			t.Fatalf("失败申请的日期、原因或时间进入了改期历史：%+v", c)
		}
	}

	// 按器具核对、查询该器具计划、校准待办都以最后成功保存的 10-20 为准：
	// 11-01 查询时 10-20 已逾期，不能按失败提交的 11-05 显示“未到计划日”。
	r, _ := l.Review("M-1")
	if len(r.Plans) != 1 || r.Plans[0].PlannedDate != "2026-10-20" ||
		len(r.Plans[0].Changes) != 1 || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("核对结果应保持最后成功保存的计划与逾期标记：%+v", r.Plans)
	}
	plans, err := l.Plans("M-1")
	if err != nil || len(plans) != 1 || plans[0].PlannedDate != "2026-10-20" ||
		plans[0].Marker != TodoOverdue {
		t.Fatalf("器具计划查询应使用最后成功保存的日期：%+v err=%v", plans, err)
	}
	items, err := l.Todos("M-1")
	if err != nil || len(items) != 1 || items[0].PlannedDate != "2026-10-20" ||
		items[0].Marker != TodoOverdue {
		t.Fatalf("待办应按最后成功保存的日期标逾期，不能按失败的 11-05 变化：%+v err=%v", items, err)
	}

	// 文件仍不可保存时，业务校验仍先于写盘：过去日期、空白原因按业务拒绝
	// （退出语义上的校验错误），且同样不留记录。
	if _, err := l.ReschedulePlan("P-1", "2026-10-01", "过去"); !IsValidation(err) {
		t.Fatalf("早于操作当天的日期应业务拒绝，得到 %v", err)
	}
	if _, err := l.ReschedulePlan("P-1", "2026-11-05", "  "); !IsValidation(err) {
		t.Fatalf("空白原因应业务拒绝，得到 %v", err)
	}
	if n := len(l.findPlan("P-1").Changes); n != 1 {
		t.Fatalf("业务拒绝后历史数量变化：%d", n)
	}
	// 文件仍不可保存时再次提交 11-05：仍报保存失败，不会以任何方式成功。
	if p, err := l.ReschedulePlan("P-1", "2026-11-05", "车间占用"); err == nil || p != nil {
		t.Fatalf("再次提交应仍报保存失败：p=%v err=%v", p, err)
	}

	// 恢复可写后无需重开台账：先做一件与改期无关、能够成功保存的正常操作
	// （停用器具），失败的改期不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	disk := reopened.findPlan("P-1")
	if disk == nil || disk.PlannedDate != "2026-10-20" || len(disk.Changes) != 1 {
		t.Fatalf("失败改期被随后成功的写盘顺带写入文件：%+v", disk)
	}
	if disk.Changes[0].To == "2026-11-05" || disk.Changes[0].Reason == "车间占用" {
		t.Fatalf("文件中出现失败改期：%+v", disk.Changes)
	}
	if reopened.findInstrument("M-1").Status != StatusRetired {
		t.Fatal("正常操作自身的保存应当生效")
	}

	// 在同一台账重新提交合法改期到 11-08：当前日期才改变，且只新增一条
	// 10-20 → 11-08，时间采用这次成功操作的时间；历史中不得出现 11-05。
	clock.t = mustDate(t, "2026-11-02")
	successAt := clock.t.Format(time.RFC3339)
	ok2, err := l.ReschedulePlan("P-1", "2026-11-08", "重新排期")
	if err != nil {
		t.Fatalf("恢复后重新提交应成功: %v", err)
	}
	if ok2.PlannedDate != "2026-11-08" || len(ok2.Changes) != 2 {
		t.Fatalf("成功改期后日期与历史数量异常：%+v", ok2)
	}
	wantChanges := []PlanChange{
		{From: "2026-10-10", To: "2026-10-20", ChangedAt: firstAt, Reason: "实验室排期冲突"},
		{From: "2026-10-20", To: "2026-11-08", ChangedAt: successAt, Reason: "重新排期"},
	}
	for i, want := range wantChanges {
		if ok2.Changes[i] != want {
			t.Fatalf("第 %d 条改期记录异常：got=%+v want=%+v", i, ok2.Changes[i], want)
		}
	}
	for _, c := range ok2.Changes {
		if c.To == "2026-11-05" || c.From == "2026-11-05" {
			t.Fatalf("历史中不得出现失败时提交的 11-05：%+v", ok2.Changes)
		}
	}
	// 停用器具的计划仍可改期，器具状态不因改期改变。
	if l.findInstrument("M-1").Status != StatusRetired {
		t.Fatal("改期不应改变器具状态")
	}
	// 成功改期后待办标记按新日期判断：11-02 查 11-08 为未到计划日。
	items, _ = l.Todos("M-1")
	if len(items) != 1 || items[0].PlannedDate != "2026-11-08" || items[0].Marker != TodoFuture {
		t.Fatalf("成功改期后待办应按新日期标记：%+v", items)
	}

	// 文件中最终也恰好是两条历史，且不含 11-05。
	reopened, _ = openAt(path, clock.now)
	final := reopened.findPlan("P-1")
	if final == nil || final.PlannedDate != "2026-11-08" || len(final.Changes) != 2 {
		t.Fatalf("落盘后的最终计划异常：%+v", final)
	}
	if final.Changes[0] != wantChanges[0] || final.Changes[1] != wantChanges[1] {
		t.Fatalf("落盘改期历史与成功操作不一致：%+v", final.Changes)
	}
}
