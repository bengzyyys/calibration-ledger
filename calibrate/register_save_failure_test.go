package calibrate

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedRegisterSaveLeavesNoInstrument 覆盖核心修复：登记内容合法但台账文件
// 无法写入或替换时明确报保存错误——失败的新器具不出现在列表、不占用编号，
// 不能被安排校准或申请使用，也不会被随后其他成功保存的操作顺带写入。恢复可写后
// 无需重新打开台账，重新提交且保存成功才新增一件待校准器具，登记时间、名称与
// 允许误差取本次成功提交；此前已保存器具的状态、证书、计划与使用记录不受影响。
func TestFailedRegisterSaveLeavesNoInstrument(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	// M-1 上准备好证书、状态、使用留痕与校准计划，失败登记不得影响它们。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	allowedUse, err := l.RequestUse("M-1")
	if err != nil || !allowedUse.Allowed {
		t.Fatalf("M-1 应可获准使用：err=%v decision=%v", err, allowedUse)
	}
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"})

	// 失败申请：10-03 提交，名称与误差是这份内容；恢复后将用不同内容重新提交，
	// 证明成功提交不会沿用失败申请。
	clock.t = mustDate(t, "2026-10-03")
	failed := RegisterInput{ID: "M-NEW", Name: "失败名称", AllowedError: 0.25}
	blockSaving(t, l)
	inst, err := l.Register(failed)
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if inst != nil {
		t.Fatalf("保存失败不能返回已登记的器具：%+v", inst)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("保存错误应指出台账文件 %s：%v", path, err)
	}

	// 文件仍不可写时再次提交同号合法内容：仍报保存错误，不能被内存中残留的
	// 失败器具当成“编号重复”业务拒绝。
	if inst2, err := l.Register(failed); err == nil || inst2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v inst=%v", err, inst2)
	} else if IsValidation(err) {
		t.Fatalf("再次提交不得被当成编号重复而业务拒绝：%v", err)
	}

	// 文件不可写时，原本不合法的申请仍报原有业务或参数错误，不触碰写盘。
	bad := []RegisterInput{
		{ID: "   ", Name: "x", AllowedError: 1},
		{ID: "M-X", Name: "\t", AllowedError: 1},
		{ID: "M-X", Name: "x", AllowedError: -0.01},
		{ID: "M-X", Name: "x", AllowedError: math.NaN()},
		{ID: "M-X", Name: "x", AllowedError: math.Inf(1)},
		// 已经保存的编号不能覆盖：即使写盘被挡住，也按业务校验拒绝。
		{ID: " M-1 ", Name: "覆盖者", AllowedError: 9},
	}
	for i, in := range bad {
		if got, err := l.Register(in); err == nil || got != nil {
			t.Fatalf("非法用例 %d 应被拒绝且不返回器具：err=%v inst=%v", i, err, got)
		} else if !IsValidation(err) {
			t.Fatalf("非法用例 %d 应报业务校验错误而非文件错误：%v", i, err)
		}
	}
	// 零误差的合法登记仍会走到写盘并报保存错误（不是业务拒绝）。
	if got, err := l.Register(RegisterInput{ID: "M-ZERO", Name: "零限", AllowedError: 0}); err == nil || got != nil {
		t.Fatalf("零误差合法申请应走到保存并报保存错误：err=%v inst=%v", err, got)
	} else if IsValidation(err) {
		t.Fatalf("零误差是合法登记，不应被业务拒绝：%v", err)
	}

	// 继续使用同一台账对象（无需重开）：各入口都看不到失败器具，列表与登记前一致。
	if l.findInstrument("M-NEW") != nil || l.findInstrument("M-ZERO") != nil {
		t.Fatalf("失败器具留在了台账中：%+v", l.data.Instruments)
	}
	if listed := l.Instruments(); len(listed) != 2 ||
		listed[0].ID != "M-1" || listed[1].ID != "M-2" {
		t.Fatalf("列表应与登记前一致（只有 M-1、M-2）：%+v", listed)
	}
	if _, err := l.Review("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按失败编号核对应报告器具不存在：%v", err)
	}
	if _, err := l.CanUse("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败器具不能参与使用资格判断：%v", err)
	}
	if _, err := l.RequestUse("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败器具不能申请使用：%v", err)
	}
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-NEW", Number: "C-X", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0, Summary: "s",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不能给失败器具录入证书：%v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-X", InstrumentID: "M-NEW",
		Date: "2026-10-10", Note: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不能为失败器具安排校准计划：%v", err)
	}

	// 此前已保存器具的状态、证书、计划与使用记录保持原样。
	if got := l.findInstrument("M-1"); got.Status != StatusInUse ||
		got.Name != "万用表" || got.AllowedError != 0.5 {
		t.Fatalf("失败登记改动了原有器具：%+v", got)
	}
	if c := l.findCertificate("C-1"); c == nil {
		t.Fatal("原有证书丢失")
	}
	if p := l.findPlan("P-1"); p == nil {
		t.Fatal("原有计划丢失")
	}
	if recs := l.UsageRecords(); len(recs) != 1 || recs[0].InstrumentID != "M-1" {
		t.Fatalf("原有使用记录被改动：%+v", recs)
	}

	// 恢复可写后不必重开台账：先对原有器具完成一次正常的状态切换，失败器具
	// 不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "M-NEW") || strings.Contains(string(raw), "M-ZERO") ||
		strings.Contains(string(raw), "失败名称") {
		t.Fatalf("失败器具被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.findInstrument("M-NEW") != nil {
		t.Fatalf("文件中不应出现失败器具：%+v", reopened.data.Instruments)
	}

	// 在同一台账对象重新提交该编号：时间推进到 10-05，名称与允许误差也改用新
	// 内容。只有这次保存成功才新增器具，登记时间、名称与误差取本次提交。
	clock.t = mustDate(t, "2026-10-05")
	success := RegisterInput{ID: " M-NEW ", Name: " 成功名称 ", AllowedError: 0.75}
	ok, err := l.Register(success)
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功登记：err=%v inst=%v", err, ok)
	}
	if ok.ID != "M-NEW" || ok.Name != "成功名称" || ok.AllowedError != 0.75 {
		t.Fatalf("成功登记的编号/名称/误差应取本次提交并裁剪空白：%+v", ok)
	}
	if ok.Status != StatusPending {
		t.Fatalf("新登记器具应为待校准状态：%s", ok.Status)
	}
	if ok.RegisteredAt != "2026-10-05T00:00:00Z" {
		t.Fatalf("登记时间应取本次成功提交的时间，得到 %s", ok.RegisteredAt)
	}
	if got := l.findInstrument("M-NEW"); got == nil ||
		got.RegisteredAt != "2026-10-05T00:00:00Z" ||
		got.Name != "成功名称" || got.AllowedError != 0.75 {
		t.Fatalf("台账内的新器具应采用本次成功提交的内容：%+v", got)
	}

	// 成功后再次登记同号仍按现有规则拒绝；原有器具与记录仍保持原样。
	if _, err := l.Register(failed); !IsValidation(err) ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("成功登记后同号应按编号唯一拒绝：%v", err)
	}
	if len(l.Instruments()) != 3 {
		t.Fatalf("重复登记不应新增器具：%+v", l.data.Instruments)
	}
	if got := l.findInstrument("M-1"); got.Status != StatusRetired {
		t.Fatalf("原有器具状态被改动：%+v", got)
	}
	if l.findPlan("P-1") == nil || l.findCertificate("C-1") == nil {
		t.Fatal("原有证书或计划丢失")
	}

	// 重开文件：磁盘上只有 M-1、M-2、M-NEW（本次成功内容），不含失败申请。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Instruments) != 3 {
		t.Fatalf("重开后应有三件器具：%+v", reopened.data.Instruments)
	}
	got := reopened.findInstrument("M-NEW")
	if got == nil || got.Name != "成功名称" || got.AllowedError != 0.75 ||
		got.RegisteredAt != "2026-10-05T00:00:00Z" || got.Status != StatusPending {
		t.Fatalf("文件中的新器具内容异常：%+v", got)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "失败名称") {
		t.Fatalf("失败申请的内容最终仍被写入文件：\n%s", raw)
	}
}

// TestFailedRegisterOnBrandNewLedgerLeavesEmptyLedger 覆盖首次登记新台账时
// 写盘失败的情形：失败后台账仍是空台账，查询列表为空、按编号核对报不存在；
// 恢复后重新提交才创建文件并新增器具。
func TestFailedRegisterOnBrandNewLedgerLeavesEmptyLedger(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// 新台账尚无文件：直接用同名目录挡住原子替换（无需先移除台账文件）。
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("以同名目录挡住台账创建: %v", err)
	}
	in := RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5}
	if got, err := l.Register(in); err == nil || got != nil {
		t.Fatalf("首次登记写盘失败应返回保存错误：err=%v inst=%v", err, got)
	} else if IsValidation(err) || !strings.Contains(err.Error(), path) {
		t.Fatalf("应为指出台账路径的保存错误：%v", err)
	}

	// 失败后继续查询同一台账：与登记前一致——空列表、编号不存在。
	if listed := l.Instruments(); len(listed) != 0 {
		t.Fatalf("失败后列表应为空：%+v", listed)
	}
	if _, err := l.Review("M-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败编号核对应报不存在：%v", err)
	}
	if _, err := l.RequestUse("M-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败编号不能申请使用：%v", err)
	}
	// 文件仍不可写时再次提交合法内容继续报保存错误，不能变成编号重复。
	if _, err := l.Register(in); IsValidation(err) {
		t.Fatalf("再次提交不得被当成编号重复：%v", err)
	}
	// 不合法内容仍按业务校验拒绝。
	if _, err := l.Register(RegisterInput{ID: " ", Name: "x", AllowedError: 1}); !IsValidation(err) {
		t.Fatalf("空白编号应报业务校验错误：%v", err)
	}

	// 恢复可写：无需重开，时钟前进后重新提交才真正创建台账文件。
	restoreSaving(t, l)
	clock.t = mustDate(t, "2026-10-05")
	ok, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0})
	if err != nil || ok == nil {
		t.Fatalf("恢复后零误差登记应成功：err=%v inst=%v", err, ok)
	}
	if ok.RegisteredAt != "2026-10-05T00:00:00Z" || ok.AllowedError != 0 ||
		ok.Status != StatusPending {
		t.Fatalf("成功登记内容异常：%+v", ok)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Instruments) != 1 || reopened.findInstrument("M-1") == nil {
		t.Fatalf("文件中应有一件成功登记的器具：%+v", reopened.data.Instruments)
	}
}
