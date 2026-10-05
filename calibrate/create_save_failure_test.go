package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedCreatePlanSaveLeavesNoPlan 覆盖核心修复：器具与计划内容都合法，
// 但台账文件无法写入或替换时明确报保存错误——失败的新计划不占用编号，也不
// 占用该器具唯一的未结束计划名额；计划查询、待办与按器具核对只能看到此前
// 已保存的计划。恢复可写后无需重新打开台账，重新提交才建立，建立时间取本次
// 成功申请；恢复后若原计划日期已过，按本次申请的本机今天明确业务拒绝。
func TestFailedCreatePlanSaveLeavesNoPlan(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	// M-1 已有一项“已结束”的计划（用证书完成）：该器具仍应能新建计划，
	// 失败的新计划同样不能留下任何痕迹。
	mustPlan(t, l, PlanInput{Number: "P-OLD", InstrumentID: "M-1",
		Date: "2026-10-05", Note: "上一周期"})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	oldDone, _, err := l.CompletePlan("P-OLD", "C-1")
	if err != nil {
		t.Fatalf("complete P-OLD: %v", err)
	}

	in := PlanInput{Number: "P-NEW", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "年度例行校准"}

	// 提交合法新建申请时台账文件无法替换。
	clock.t = mustDate(t, "2026-10-03")
	blockSaving(t, l)
	p, err := l.CreatePlan(in)
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if p != nil {
		t.Fatalf("保存失败不能返回已建立成功的计划：%+v", p)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("保存错误应指出台账文件 %s：%v", path, err)
	}

	// 文件仍不可写时再次提交相同编号和内容：仍报保存错误，不能被上次失败
	// 留下的计划当成“编号重复”或“器具已有未结束计划”挡住。
	if p2, err := l.CreatePlan(in); err == nil || p2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v plan=%v", err, p2)
	} else if IsValidation(err) {
		t.Fatalf("再次提交不得被当成编号重复或已有计划而业务拒绝：%v", err)
	}
	// 同一器具换一个编号同样应走到保存并报保存错误：失败申请不占用该器具
	// 唯一的未结束计划名额（修复前这里会被内存中的失败计划业务拒绝）。
	other := PlanInput{Number: "P-OTHER", InstrumentID: "M-1",
		Date: "2026-10-11", Note: "另一项安排"}
	if p2, err := l.CreatePlan(other); err == nil || p2 != nil {
		t.Fatalf("同器具换编号也应报保存失败：err=%v plan=%v", err, p2)
	} else if IsValidation(err) {
		t.Fatalf("失败的新计划不得占用未结束计划名额：%v", err)
	}
	// 编号同样不被占用：别的器具用 P-NEW 申请也应走到保存而非编号重复。
	if p2, err := l.CreatePlan(PlanInput{
		Number: "P-NEW", InstrumentID: "M-2", Date: "2026-10-10", Note: "占用检查",
	}); err == nil || p2 != nil {
		t.Fatalf("别的器具用失败编号也应报保存失败：err=%v plan=%v", err, p2)
	} else if IsValidation(err) {
		t.Fatalf("失败的新计划不得占用计划编号：%v", err)
	}
	// 此前没有任何计划的器具 M-2 合法申请失败，同样只报保存错误。
	m2 := PlanInput{Number: "P-2", InstrumentID: "M-2",
		Date: "2026-10-10", Note: "失败那次的说明"}
	if p2, err := l.CreatePlan(m2); err == nil || p2 != nil {
		t.Fatalf("无计划器具的失败申请也应报保存错误：err=%v plan=%v", err, p2)
	}

	// 继续使用同一台账对象：失败计划不存在，全部入口只能看到此前已保存的计划。
	if l.findPlan("P-NEW") != nil || l.findPlan("P-OTHER") != nil || l.findPlan("P-2") != nil {
		t.Fatalf("失败的计划留在了台账中：%+v", l.data.Plans)
	}
	if len(l.data.Plans) != 1 {
		t.Fatalf("失败新建改动了计划数量：%+v", l.data.Plans)
	}
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("待办只能看到此前已保存的未结束计划：%+v", items)
	}
	if only, err := l.Todos("M-1"); err != nil || len(only) != 0 {
		t.Fatalf("M-1 待办应为空（P-OLD 已完成）：%+v err=%v", only, err)
	}
	views, err := l.Plans("M-1")
	if err != nil || len(views) != 1 || views[0].Number != "P-OLD" ||
		views[0].Status != PlanStatusDone || views[0].CertificateNumber != "C-1" {
		t.Fatalf("M-1 计划查询应只含此前已完成的 P-OLD：%+v err=%v", views, err)
	}
	if views2, err := l.Plans("M-2"); err != nil || len(views2) != 0 {
		t.Fatalf("M-2 不应出现失败计划：%+v err=%v", views2, err)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Number != "P-OLD" || r.Plans[0].Open() {
		t.Fatalf("核对中只能看到此前保存的 P-OLD（已完成）：%+v", r.Plans)
	}
	// 器具状态、证书不受失败申请影响。
	if inst := l.findInstrument("M-1"); inst.Status != StatusPending {
		t.Fatalf("失败新建改动了器具状态：%+v", inst)
	}
	if cert := l.findCertificate("C-1"); cert == nil {
		t.Fatal("失败新建后已有证书丢失")
	}

	// 恢复文件可写后不必重开台账：先做一次无关的正常状态切换并保存成功，
	// 失败的新计划不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	for _, banned := range []string{"P-NEW", "P-OTHER", "P-2", "年度例行校准", "失败那次的说明"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("失败的新计划被随后成功的写盘顺带写入文件（含 %q）：\n%s", banned, raw)
		}
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if rp := reopened.findPlan("P-NEW"); rp != nil {
		t.Fatalf("文件中不应出现失败计划：%+v", rp)
	}
	if rp := reopened.findPlan("P-OLD"); rp == nil || rp.Status != PlanStatusDone ||
		rp.CertificateNumber != "C-1" {
		t.Fatalf("此前已完成的计划信息应保持原样：%+v", rp)
	}

	// 在同一台账对象重新提交相同编号和内容才真正建立；此时仍是 10-05，
	// 计划日期 10-10 仍在未来。建立时间必须取本次成功申请的时间，
	// 而不是失败那次的 10-03。
	clock.t = mustDate(t, "2026-10-05")
	ok, err := l.CreatePlan(in)
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功建立：err=%v plan=%v", err, ok)
	}
	if ok.Number != "P-NEW" || ok.Status != PlanStatusOpen ||
		ok.PlannedDate != "2026-10-10" || ok.OriginalDate != "2026-10-10" ||
		ok.Note != "年度例行校准" {
		t.Fatalf("成功建立的计划内容异常：%+v", ok)
	}
	if ok.CreatedAt != "2026-10-05T00:00:00Z" {
		t.Fatalf("建立时间应取本次成功申请的时间，得到 %s", ok.CreatedAt)
	}
	// 新计划出现在该器具的待办与核对中；P-OLD 仍为已完成、关联证书不变。
	items, err = l.Todos("M-1")
	if err != nil || len(items) != 1 || items[0].PlanNumber != "P-NEW" ||
		items[0].Marker != TodoFuture {
		t.Fatalf("成功建立后待办应只列 P-NEW：%+v err=%v", items, err)
	}
	r, err = l.Review("M-1")
	if err != nil || len(r.Plans) != 2 {
		t.Fatalf("核对应含 P-OLD 与新建立的 P-NEW：%+v err=%v", r, err)
	}
	var donePlan *PlanView
	for i := range r.Plans {
		if r.Plans[i].Number == "P-OLD" {
			donePlan = &r.Plans[i]
		}
	}
	if donePlan == nil || donePlan.Status != PlanStatusDone ||
		donePlan.CertificateNumber != "C-1" || donePlan.CompletedAt != oldDone.CompletedAt {
		t.Fatalf("此前完成信息应保持原样：%+v", donePlan)
	}

	// 恢复后重新提交时，日期必须按本次申请的本机今天判断：时钟走到 10-11，
	// M-2 仍按原内容（日期 10-10）提交应明确业务拒绝，台账仍不新增计划。
	clock.t = mustDate(t, "2026-10-11")
	if _, err := l.CreatePlan(m2); !IsValidation(err) ||
		!strings.Contains(err.Error(), "不能早于本机今天") {
		t.Fatalf("原计划日期已过时应业务拒绝：%v", err)
	}
	if l.findPlan("P-2") != nil || len(l.data.Plans) != 2 {
		t.Fatalf("过去日期的重新提交不得新增计划：%+v", l.data.Plans)
	}
	// 换成不早于今天的日期重新提交才建立，建立时间取本次成功申请。
	ok2, err := l.CreatePlan(PlanInput{Number: "P-2", InstrumentID: "M-2",
		Date: "2026-10-20", Note: "重新提交的说明"})
	if err != nil || ok2 == nil {
		t.Fatalf("未来日期重新提交应成功：err=%v plan=%v", err, ok2)
	}
	if ok2.CreatedAt != "2026-10-11T00:00:00Z" || ok2.PlannedDate != "2026-10-20" {
		t.Fatalf("新计划应采用本次申请的时间与日期：%+v", ok2)
	}

	// 重开文件：磁盘上只有 P-OLD、P-NEW、P-2 三项，且不含失败申请的说明。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Plans) != 3 {
		t.Fatalf("重开后应只有三项计划：%+v", reopened.data.Plans)
	}
	if reopened.findPlan("P-NEW") == nil || reopened.findPlan("P-2") == nil {
		t.Fatal("重开后找不到成功建立的计划")
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "失败那次的说明") ||
		strings.Contains(string(raw), "P-OTHER") {
		t.Fatalf("失败申请的内容最终仍被写入文件：\n%s", raw)
	}
}

// TestCreatePlanValidationRejectedWhileSavingBlocked 区分文件读写错误与业务
// 拒绝：文件不可写时，编号重复、器具已有未结束计划、日期或说明不符合要求、
// 未知器具等仍按原有方式拒绝（校验错误或 ErrNotFound），不触碰写盘、不留计划。
func TestCreatePlanValidationRejectedWhileSavingBlocked(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期"})

	blockSaving(t, l)
	cases := []struct {
		name     string
		in       PlanInput
		notFound bool
	}{
		{"编号重复", PlanInput{Number: "P-1", InstrumentID: "M-2", Date: "2026-10-10", Note: "复用"}, false},
		{"器具已有未结束计划", PlanInput{Number: "P-2", InstrumentID: "M-1", Date: "2026-10-10", Note: "二"}, false},
		{"空白编号", PlanInput{Number: "  ", InstrumentID: "M-2", Date: "2026-10-10", Note: "x"}, false},
		{"空白说明", PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-10-10", Note: ""}, false},
		{"日期早于今天", PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-10-01", Note: "x"}, false},
		{"不存在的日期", PlanInput{Number: "P-3", InstrumentID: "M-2", Date: "2026-02-30", Note: "x"}, false},
		{"未知器具", PlanInput{Number: "P-3", InstrumentID: "X-9", Date: "2026-10-10", Note: "x"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.CreatePlan(tc.in)
			if err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			}
			if got != nil {
				t.Fatalf("业务拒绝不能返回计划结果：%+v", got)
			}
			if tc.notFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("用例 %q 应报 ErrNotFound：%v", tc.name, err)
				}
			} else if !IsValidation(err) {
				t.Fatalf("用例 %q 应是业务校验错误而非文件错误：%v", tc.name, err)
			}
		})
	}

	// 业务拒绝不写盘、不留计划：台账仍只有此前保存的 P-1。
	if len(l.data.Plans) != 1 || l.findPlan("P-1") == nil {
		t.Fatalf("业务拒绝后计划被改动：%+v", l.data.Plans)
	}
	items, err := l.Todos("")
	if err != nil || len(items) != 1 || items[0].PlanNumber != "P-1" {
		t.Fatalf("待办应仍只含此前保存的 P-1：%+v err=%v", items, err)
	}
}
