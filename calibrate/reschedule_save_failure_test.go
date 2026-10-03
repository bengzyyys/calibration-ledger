package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedRescheduleSaveLeavesNoChange 覆盖核心修复：已通过业务校验的改期在
// 写盘失败时明确报保存错误，计划日期、改期历史、待办标记与改期前完全一致；
// 恢复可写后重新提交才生效，且只记录从最后成功保存的日期出发的一条改期。
func TestFailedRescheduleSaveLeavesNoChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})

	// 第一条成功改期：10-10 → 10-20。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("首次改期应成功: %v", err)
	}
	savedFirst := first.Changes[0]

	// 提交合法的第二次改期 10-20 → 11-05 时台账文件无法替换。
	clock.t = mustDate(t, "2026-10-04")
	blockSaving(t, l)
	p, err := l.ReschedulePlan("P-1", "2026-11-05", "实验室改造延期")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回成功的计划结果：%+v", p)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 新日期等于当前日期的改期同样只有保存成功才能留下记录。
	if p2, err := l.ReschedulePlan("P-1", "2026-10-20", "同日重申"); err == nil || p2 != nil {
		t.Fatalf("同日改期写盘失败也应报保存错误且不返回计划：err=%v plan=%v", err, p2)
	}

	// 文件仍不可保存时再次提交 11-05：仍报保存失败，而不是以任何方式成功。
	if p2, err := l.ReschedulePlan("P-1", "2026-11-05", "实验室改造延期"); err == nil || p2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v plan=%v", err, p2)
	}

	// 继续使用同一台账对象，无需重新打开：当前日期仍是最后成功保存的 10-20，
	// 编号、所属器具、最初日期、说明和未结束状态都保留。
	cur := l.findPlan("P-1")
	if cur == nil {
		t.Fatal("计划丢失")
	}
	if cur.PlannedDate != "2026-10-20" || cur.OriginalDate != "2026-10-10" {
		t.Fatalf("失败改期改动了计划日期：%+v", cur)
	}
	if cur.Number != "P-1" || cur.InstrumentID != "M-1" ||
		cur.Note != "周期校准" || !cur.Open() {
		t.Fatalf("失败改期改动了计划基本信息或状态：%+v", cur)
	}
	// 失败申请的原因与时间不进入改期历史；第一条记录的前后日期、时间、原因及次序不变。
	if len(cur.Changes) != 1 {
		t.Fatalf("失败改期进入了历史，共 %d 条：%+v", len(cur.Changes), cur.Changes)
	}
	if cur.Changes[0] != savedFirst {
		t.Fatalf("已成功保存的改期记录被改动：原 %+v 现 %+v", savedFirst, cur.Changes[0])
	}

	// 待办标记按查询当天与最后成功保存的日期（10-20）判断：10-20 当天应为
	// “今天需校准”；若误用了失败的 11-05，则会错误地显示“未到计划日”。
	clock.t = mustDate(t, "2026-10-20")
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].PlanNumber != "P-1" ||
		items[0].PlannedDate != "2026-10-20" || items[0].Marker != TodoToday {
		t.Fatalf("待办应仍按 10-20 标记为今天需校准：%+v", items)
	}
	only, err := l.Todos("M-1")
	if err != nil || len(only) != 1 || only[0].Marker != TodoToday {
		t.Fatalf("按器具查待办异常：%+v err=%v", only, err)
	}

	// 按器具核对与查询该器具计划同样使用最后成功保存的日期。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].PlannedDate != "2026-10-20" ||
		r.Plans[0].Marker != TodoToday || len(r.Plans[0].Changes) != 1 {
		t.Fatalf("核对结果应与失败前一致：%+v", r.Plans)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].PlannedDate != "2026-10-20" ||
		len(views[0].Changes) != 1 {
		t.Fatalf("计划查询应与失败前一致：%+v err=%v", views, err)
	}

	// 恢复文件可写后不必重开台账：先做一次与改期无关、能够成功保存的正常操作
	// （顺带停用器具），失败的改期不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "2026-11-05") || strings.Contains(string(raw), "实验室改造延期") {
		t.Fatalf("失败改期被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp := reopened.findPlan("P-1")
	if rp == nil || rp.PlannedDate != "2026-10-20" || len(rp.Changes) != 1 {
		t.Fatalf("文件中的计划应停留在最后成功保存的状态：%+v", rp)
	}

	// 停用器具的计划仍可改期：在同一台账对象重新提交合法改期到 11-08。
	// 当前日期才改为 11-08，且只新增一条 10-20 → 11-08，时间取本次成功操作；
	// 历史保留第一条，任何位置都不能出现 11-05。
	clock.t = mustDate(t, "2026-10-21")
	ok, err := l.ReschedulePlan("P-1", "2026-11-08", "改造完成重新排期")
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功：err=%v plan=%v", err, ok)
	}
	if ok.PlannedDate != "2026-11-08" || ok.Status != PlanStatusOpen {
		t.Fatalf("成功后当前计划日期才应改变：%+v", ok)
	}
	inst := l.findInstrument("M-1")
	if inst == nil || inst.Status != StatusRetired {
		t.Fatalf("改期不应改变器具状态：%+v", inst)
	}
	if len(ok.Changes) != 2 {
		t.Fatalf("应只有两条改期记录，得到 %d：%+v", len(ok.Changes), ok.Changes)
	}
	if ok.Changes[0] != savedFirst {
		t.Fatalf("第一条改期记录不能变化：原 %+v 现 %+v", savedFirst, ok.Changes[0])
	}
	second := ok.Changes[1]
	wantSecond := PlanChange{
		From:      "2026-10-20",
		To:        "2026-11-08",
		Reason:    "改造完成重新排期",
		ChangedAt: "2026-10-21T00:00:00Z",
	}
	if second != wantSecond {
		t.Fatalf("新增记录应为最后成功日期 10-20 到 11-08、采用本次操作时间：%+v", second)
	}

	// 重新打开文件：磁盘上同样只有两条记录且无 11-05。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rp = reopened.findPlan("P-1")
	if rp == nil || rp.PlannedDate != "2026-11-08" || len(rp.Changes) != 2 {
		t.Fatalf("重开后计划状态异常：%+v", rp)
	}
	if rp.Changes[0] != savedFirst || rp.Changes[1] != wantSecond {
		t.Fatalf("重开后改期历史异常：%+v", rp.Changes)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "2026-11-05") {
		t.Fatalf("失败日期 11-05 被写入文件：\n%s", raw)
	}
}

// TestRescheduleValidationRejectedWhileSavingBlocked 区分文件读写错误与业务
// 拒绝：文件不可写时，日期或原因不符合要求仍按业务校验拒绝（返回校验错误），
// 且不进行写盘尝试、不改动任何记录。
func TestRescheduleValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	blockSaving(t, l)
	bad := []struct {
		name    string
		date    string
		reason  string
		num     string
		wantVal bool
	}{
		{"空白原因", "2026-11-05", "  ", "P-1", true},
		{"早于操作当天", "2026-10-01", "提前", "P-1", true},
		{"不存在的日期", "2026-11-31", "x", "P-1", true},
		{"未知计划", "2026-11-05", "x", "NOPE", false},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			p, err := l.ReschedulePlan(tc.num, tc.date, tc.reason)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if p != nil {
				t.Fatalf("业务拒绝不能返回计划结果：%+v", p)
			}
			if tc.wantVal && !IsValidation(err) {
				t.Fatalf("用例 %q 应是业务校验错误而非文件错误：%v", tc.name, err)
			}
			if !tc.wantVal && !errors.Is(err, ErrNotFound) {
				t.Fatalf("未知计划应报 ErrNotFound：%v", err)
			}
		})
	}
	got := l.findPlan("P-1")
	if got.PlannedDate != "2026-10-10" || len(got.Changes) != 0 || !got.Open() {
		t.Fatalf("业务拒绝后计划被改动：%+v", got)
	}
}
