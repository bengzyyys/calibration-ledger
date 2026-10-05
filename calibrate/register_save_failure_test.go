package calibrate

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedRegisterLeavesNoInstrument 覆盖核心修复：登记内容合法但台账文件
// 无法写入或替换时，明确报保存错误——失败的器具不进入列表、不占用编号，
// 按该编号核对报告不存在，也不能安排校准或申请使用；失败器具不会被此后其他
// 成功保存的操作顺带写入。恢复可写后无需重新打开台账，重新提交才正式登记，
// 登记时间、名称与允许误差取本次成功提交。
func TestFailedRegisterLeavesNoInstrument(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格",
	})
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if _, err := l.RequestUse("M-1"); err != nil {
		t.Fatalf("request use: %v", err)
	}

	in := RegisterInput{ID: "M-NEW", Name: "  示波器  ", AllowedError: 1.5}

	// 提交合法登记时台账文件无法替换。
	clock.t = mustDate(t, "2026-10-03")
	blockSaving(t, l)
	inst, err := l.Register(in)
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

	// 文件仍不可写时再次提交同号：仍报保存错误，不能被上次失败留下的器具
	// 当成“编号重复”业务拒绝。
	if inst2, err := l.Register(in); err == nil || inst2 != nil {
		t.Fatalf("再次提交应仍报保存失败：err=%v inst=%v", err, inst2)
	} else if IsValidation(err) {
		t.Fatalf("再次提交不得被当成编号重复而业务拒绝：%v", err)
	}

	// 继续使用同一台账对象：失败器具不存在，全部入口只能看到此前已保存的器具。
	if l.findInstrument("M-NEW") != nil {
		t.Fatalf("失败的器具留在了台账中：%+v", l.data.Instruments)
	}
	if listed := l.Instruments(); len(listed) != 1 || listed[0].ID != "M-1" {
		t.Fatalf("列出器具应与登记前一致：%+v", listed)
	}
	if _, err := l.Review("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按失败编号核对应报告器具不存在：%v", err)
	}
	if _, err := l.CanUse("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按失败编号判断使用应报告器具不存在：%v", err)
	}
	if _, err := l.RequestUse("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("按失败编号申请使用应报告器具不存在且不留痕：%v", err)
	}
	if _, err := l.CreatePlan(PlanInput{Number: "P-NEW", InstrumentID: "M-NEW",
		Date: "2026-10-10", Note: "安排"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不能给失败器具安排校准计划：%v", err)
	}
	if len(l.data.Usage) != 1 {
		t.Fatalf("失败登记不得改动历史使用记录：%+v", l.data.Usage)
	}

	// 文件不可写时，原本不合法的申请仍报告原有业务或参数错误，不触碰写盘：
	// 空白编号/名称、负数或非有限允许误差、已保存编号重复；零误差合法，
	// 应走到保存并报保存错误。
	invalid := []struct {
		name string
		in   RegisterInput
	}{
		{"空白编号", RegisterInput{ID: "  ", Name: "x", AllowedError: 0.5}},
		{"空白名称", RegisterInput{ID: "M-8", Name: " ", AllowedError: 0.5}},
		{"负数误差", RegisterInput{ID: "M-8", Name: "x", AllowedError: -0.1}},
		{"非有限误差", RegisterInput{ID: "M-8", Name: "x", AllowedError: math.NaN()}},
		{"编号重复", RegisterInput{ID: "M-1", Name: "x", AllowedError: 0.5}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := l.Register(tc.in); !IsValidation(err) {
				t.Fatalf("用例 %q 应是业务校验错误而非文件错误：%v", tc.name, err)
			}
		})
	}
	if _, err := l.Register(RegisterInput{ID: "M-ZERO", Name: "零误差表", AllowedError: 0}); err == nil {
		t.Fatal("零误差合法，文件不可写时应报保存错误")
	} else if IsValidation(err) {
		t.Fatalf("零误差可登记，不应被业务拒绝：%v", err)
	}
	if l.findInstrument("M-ZERO") != nil {
		t.Fatal("零误差登记保存失败后同样不得留下器具")
	}

	// 已有器具的状态、证书、计划与历史使用记录不受失败登记影响。
	if got := l.findInstrument("M-1"); got.Status != StatusPending {
		t.Fatalf("失败登记改动了已有器具状态：%+v", got)
	}
	if cert := l.findCertificate("C-1"); cert == nil {
		t.Fatal("失败登记后已有证书丢失")
	}
	if p := l.findPlan("P-1"); p == nil || !p.Open() {
		t.Fatalf("失败登记后已有计划异常：%+v", p)
	}

	// 恢复可写后不必重开台账：先对原有器具完成一次正常的状态切换并保存
	// 成功，失败登记的器具不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	for _, banned := range []string{"M-NEW", "M-ZERO", "示波器", "零误差表"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("失败登记的器具被随后成功的写盘顺带写入文件（含 %q）：\n%s", banned, raw)
		}
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.findInstrument("M-NEW") != nil {
		t.Fatal("文件中不应出现失败登记的器具")
	}
	if _, err := l.Review("M-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("正常保存后按失败编号核对仍应报告不存在：%v", err)
	}

	// 在同一台账对象重新提交该编号才真正登记；名称与允许误差取本次提交的
	// 内容（两侧空白按现有方式去除），登记时间取本次成功提交的时间，
	// 而不是失败那次的 2026-10-03。
	clock.t = mustDate(t, "2026-10-05")
	ok, err := l.Register(RegisterInput{ID: "M-NEW", Name: "  数字示波器  ", AllowedError: 2.0})
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功登记：err=%v inst=%v", err, ok)
	}
	if ok.ID != "M-NEW" || ok.Name != "数字示波器" || ok.AllowedError != 2.0 ||
		ok.Status != StatusPending {
		t.Fatalf("成功登记的器具内容异常：%+v", ok)
	}
	if ok.RegisteredAt != "2026-10-05T00:00:00Z" {
		t.Fatalf("登记时间应取本次成功提交的时间，得到 %s", ok.RegisteredAt)
	}
	if listed := l.Instruments(); len(listed) != 2 || listed[1].ID != "M-NEW" {
		t.Fatalf("成功登记后列表应含新器具：%+v", listed)
	}
	r, err := l.Review("M-NEW")
	if err != nil || r.Instrument.Name != "数字示波器" || r.Instrument.Status != StatusPending {
		t.Fatalf("成功登记后核对应能看到新器具：%+v err=%v", r, err)
	}

	// 成功登记后再次提交同号，才按现有规则以编号重复拒绝。
	if _, err := l.Register(in); !IsValidation(err) ||
		!strings.Contains(err.Error(), "已存在") {
		t.Fatalf("成功保存后同号应业务拒绝：%v", err)
	}

	// 重开文件：磁盘上只有 M-1 与 M-NEW 两件器具，内容以成功提交为准。
	reopened, err = openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Instruments) != 2 {
		t.Fatalf("重开后应只有两件器具：%+v", reopened.data.Instruments)
	}
	saved := reopened.findInstrument("M-NEW")
	if saved == nil || saved.Name != "数字示波器" || saved.AllowedError != 2.0 ||
		saved.RegisteredAt != "2026-10-05T00:00:00Z" {
		t.Fatalf("重开后新器具内容异常：%+v", saved)
	}
}

// TestFailedFirstRegisterOnNewLedger 覆盖首次登记新台账的场景：台账还没有
// 任何器具、文件尚未创建时，合法登记遇到文件无法写入同样只报保存错误，
// 列表保持为空；恢复可写后重新提交才建立台账并登记成功。
func TestFailedFirstRegisterOnNewLedger(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// 新台账还没有文件：直接以同名目录挡住原子替换。
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("以同名目录挡住台账替换: %v", err)
	}
	inst, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	if err == nil || inst != nil {
		t.Fatalf("首次登记写盘失败应报保存错误：err=%v inst=%v", err, inst)
	}
	if IsValidation(err) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("保存错误应指出台账文件 %s：%v", path, err)
	}
	if listed := l.Instruments(); len(listed) != 0 {
		t.Fatalf("失败后列表应保持为空：%+v", listed)
	}
	// 再次提交同号仍报保存错误，不能变成编号重复。
	if _, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5}); err == nil ||
		IsValidation(err) {
		t.Fatalf("再次提交应仍报保存错误：%v", err)
	}

	// 恢复可写后无需重新打开台账，重新提交才正式登记。
	restoreSaving(t, l)
	clock.t = mustDate(t, "2026-10-04")
	ok, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	if err != nil || ok == nil {
		t.Fatalf("恢复后重新提交应成功登记：err=%v inst=%v", err, ok)
	}
	if ok.RegisteredAt != "2026-10-04T00:00:00Z" {
		t.Fatalf("登记时间应取本次成功提交的时间，得到 %s", ok.RegisteredAt)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Instruments) != 1 || reopened.findInstrument("M-1") == nil {
		t.Fatalf("重开后应只有成功登记的 M-1：%+v", reopened.data.Instruments)
	}
}
