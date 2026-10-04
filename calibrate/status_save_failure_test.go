package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedStatusSaveKeepsLastSavedStatus 覆盖核心修复：目标状态合法且与当前
// 状态不同的切换在写盘失败时明确报保存错误，台账保持最后成功保存的状态；
// 同一台账对象随后的列表、核对与使用判断都按保留状态执行。
func TestFailedStatusSaveKeepsLastSavedStatus(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	// 合格且未到期的最近证书：之后能否使用的差异只能来自状态。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	})
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set retired: %v", err)
	}

	blockSaving(t, l)

	// 停用 → 在用：保存失败必须报文件读写错误，且状态仍是停用。
	if err := l.SetStatus("M-1", StatusInUse); err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	} else if IsValidation(err) || IsConflict(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	if got := l.findInstrument("M-1").Status; got != StatusRetired {
		t.Fatalf("保存失败后内存状态应保持停用，得到 %s", got)
	}
	for _, in := range l.Instruments() {
		if in.ID == "M-1" && in.Status != StatusRetired {
			t.Fatalf("列表应按保留状态展示，得到 %s", in.Status)
		}
	}
	// 证书合格且未到期，但状态仍是停用：不能使用的原因仍包含停用。
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed {
		t.Fatal("保存失败后不能仅凭证书合格就获准使用")
	}
	joined := strings.Join(d.Reasons, "；")
	if !strings.Contains(joined, "停用") {
		t.Fatalf("不能使用的原因应仍包含停用：%v", d.Reasons)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Instrument.Status != StatusRetired || r.CanUse {
		t.Fatalf("核对应按保留状态判断：status=%s canUse=%v", r.Instrument.Status, r.CanUse)
	}

	// 文件仍不可写时再次提交同一目标状态：仍报保存错误，不能当成已生效。
	if err := l.SetStatus("M-1", StatusInUse); err == nil {
		t.Fatal("文件仍不可写时再次提交同一目标状态不能返回成功")
	}

	// 恢复可写后先完成另一项正常保存：先前失败的状态不能被顺带写入。
	restoreSaving(t, l)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-09-02",
		Expiry: "2027-09-02", Method: "规范A", Error: 0.1, Summary: "摘要",
	})
	if got := l.findInstrument("M-1").Status; got != StatusRetired {
		t.Fatalf("其他操作成功保存不能顺带写入失败状态，得到 %s", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if !strings.Contains(string(raw), string(StatusRetired)) ||
		strings.Contains(string(raw), string(StatusInUse)) {
		t.Fatalf("文件中应保持停用状态：%s", raw)
	}

	// 无需重新打开台账：再次成功提交切换，目标状态才生效。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("恢复可写后重新提交应成功：%v", err)
	}
	if got := l.findInstrument("M-1").Status; got != StatusInUse {
		t.Fatalf("成功保存后状态应为在用，得到 %s", got)
	}
	d2, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if !d2.Allowed {
		t.Fatalf("在用 + 合格未到期证书应允许使用：%v", d2.Reasons)
	}

	// 成功保存后再次打开同一文件，能读到成功切换的状态。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.findInstrument("M-1").Status; got != StatusInUse {
		t.Fatalf("重新打开应读到在用，得到 %s", got)
	}
}

// TestFailedRetireSaveDoesNotRestrictUse 覆盖反向场景：在用器具切换为停用或
// 待校准失败时，不能提前按新状态限制使用；证书本身导致的限制仍按既有规则判断。
func TestFailedRetireSaveDoesNotRestrictUse(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	blockSaving(t, l)

	for _, target := range []Status{StatusRetired, StatusPending} {
		if err := l.SetStatus("M-1", target); err == nil {
			t.Fatalf("切换为%s时写盘失败必须返回保存错误", target)
		}
		if got := l.findInstrument("M-1").Status; got != StatusInUse {
			t.Fatalf("切换为%s失败后状态应保持在用，得到 %s", target, got)
		}
		// 不能提前按新状态限制使用：证书合格未到期且仍在用，应允许。
		d, err := l.CanUse("M-1")
		if err != nil {
			t.Fatalf("can use: %v", err)
		}
		if !d.Allowed {
			t.Fatalf("切换为%s失败不能提前限制使用：%v", target, d.Reasons)
		}
	}
	restoreSaving(t, l)
}

// TestFailedStatusSaveKeepsCertBasedRestrictions 确认保存失败只回滚状态：
// 证书本身超差、到期或缺失导致的限制仍按既有规则判断。
func TestFailedStatusSaveKeepsCertBasedRestrictions(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	// 超差证书：即使在用也不能使用。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.9, Summary: "摘要",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	blockSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err == nil {
		t.Fatal("写盘失败必须返回保存错误")
	}
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed {
		t.Fatal("超差证书仍应限制使用")
	}
	joined := strings.Join(d.Reasons, "；")
	if !strings.Contains(joined, "超差") || strings.Contains(joined, "停用") {
		t.Fatalf("限制原因应来自超差证书而非失败的停用：%v", d.Reasons)
	}
	restoreSaving(t, l)
}

// TestSameStatusStillSucceedsWithoutSave 确认既有行为保留：请求的状态本来就
// 等于最后成功保存的状态时直接返回成功，即使此刻台账文件无法写入。
func TestSameStatusStillSucceedsWithoutSave(t *testing.T) {
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

	blockSaving(t, l)
	defer restoreSaving(t, l)

	// 目标状态等于当前状态：直接成功，不要求写盘。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("相同状态应直接返回成功：%v", err)
	}
}

// TestFailedStatusSaveDoesNotRewriteHistory 确认状态切换（含失败切换）不修改
// 证书、使用记录：停用后的历史拒绝原因保持申请当时冻结的内容。
func TestFailedStatusSaveDoesNotRewriteHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	// 待校准状态下申请使用被拒绝，原因冻结留痕。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed {
		t.Fatal("待校准且无证书应被拒绝")
	}
	frozen := strings.Join(d.Reasons, "；")

	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	blockSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err == nil {
		t.Fatal("写盘失败必须返回保存错误")
	}
	restoreSaving(t, l)

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Rejections) != 1 {
		t.Fatalf("历史拒绝记录应保持一条：%d", len(r.Rejections))
	}
	if got := strings.Join(r.Rejections[0].Reasons, "；"); got != frozen {
		t.Fatalf("历史拒绝原因不应随切换重写：%q != %q", got, frozen)
	}
	if n := len(l.data.Certificates); n != 0 {
		t.Fatalf("状态切换不应改动证书：%d 张", n)
	}
}
