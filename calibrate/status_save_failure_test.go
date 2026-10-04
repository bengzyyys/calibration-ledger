package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedStatusSwitchLeavesLastSavedStatus 覆盖核心修复：目标状态合法且与
// 最后成功保存的状态不同时，写盘失败必须明确报保存错误，器具仍保持最后成功
// 保存的状态——同一台账对象随后的列出、按器具核对、待办与使用判断都按保留
// 下来的状态判断；失败状态不会被其他成功写盘顺带写入，恢复后重新提交才生效。
func TestFailedStatusSwitchLeavesLastSavedStatus(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格且未到期",
	})
	if _, err := l.CreatePlan(PlanInput{Number: "P-1", InstrumentID: "M-1",
		Date: "2026-10-10", Note: "周期校准"}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set retired: %v", err)
	}

	// 停用期间申请使用被拒绝并留痕：原因在申请时冻结，之后的切换（含失败
	// 切换）都不能改写它。
	rec, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if rec.Allowed || !containsReason(rec.Reasons, "停用") {
		t.Fatalf("停用器具应被拒绝并记录停用原因：%v", rec.Reasons)
	}
	frozen := append([]string(nil), rec.Reasons...)

	// 合格且未到期证书的停用器具切换为在用时，台账文件无法替换。
	blockSaving(t, l)
	if err := l.SetStatus("M-1", StatusInUse); err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	} else if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 继续使用同一台账对象，无需重新打开：器具仍是最后成功保存的停用状态。
	if got := l.findInstrument("M-1"); got == nil || got.Status != StatusRetired {
		t.Fatalf("失败切换改动了内存中的器具状态：%+v", got)
	}
	if listed := l.Instruments(); len(listed) != 1 || listed[0].Status != StatusRetired {
		t.Fatalf("列出器具应仍显示停用：%+v", listed)
	}

	// 不能仅凭证书合格就获准使用：原因仍包含停用，最近证书仍是 C-1。
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "停用") {
		t.Fatalf("失败的在用切换不得解除停用限制：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	if d.Latest == nil || d.Latest.Number != "C-1" {
		t.Fatalf("最近证书判断异常：%+v", d.Latest)
	}

	// 按器具核对同样按保留下来的停用状态判断，历史拒绝原因保持冻结内容。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Instrument.Status != StatusRetired || r.CanUse ||
		!containsReason(r.Reasons, "停用") || r.Latest == nil || r.Latest.Number != "C-1" {
		t.Fatalf("核对结果应与失败前一致：status=%s canUse=%v reasons=%v latest=%v",
			r.Instrument.Status, r.CanUse, r.Reasons, r.Latest)
	}
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, frozen) {
		t.Fatalf("过去拒绝使用的冻结原因被此次切换改写：%+v", r.Rejections)
	}

	// 待办中该器具的状态列也仍是停用。
	items, err := l.Todos("")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(items) != 1 || items[0].Status != StatusRetired {
		t.Fatalf("待办应仍按停用状态列出该器具：%+v", items)
	}

	// 文件仍不可保存时再次提交相同目标状态：继续报保存错误，不能把上一次
	// 未保存的在用当成已生效而直接返回成功。
	if err := l.SetStatus("M-1", StatusInUse); err == nil {
		t.Fatal("再次提交失败操作的目标状态应仍报保存错误")
	}
	if got := l.findInstrument("M-1"); got.Status != StatusRetired {
		t.Fatalf("再次失败后状态应为停用：%s", got.Status)
	}

	// 文件不可写时，非法状态与未知器具仍按业务校验拒绝，不进行写盘、不改状态。
	if err := l.SetStatus("M-1", Status("报废")); !IsValidation(err) {
		t.Fatalf("非法状态应报业务校验错误：%v", err)
	}
	if err := l.SetStatus("NOPE", StatusInUse); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知器具应报 ErrNotFound：%v", err)
	}
	if got := l.findInstrument("M-1"); got.Status != StatusRetired {
		t.Fatalf("业务拒绝后状态被改动：%s", got.Status)
	}

	// 恢复可写后不必重开台账：先完成另一项正常保存（补录另一日期的证书），
	// 先前失败的在用状态不能被顺带写入文件。
	restoreSaving(t, l)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-09-02",
		Expiry: "2027-09-02", Method: "规范A", Error: 0.1, Summary: "正常保存",
	})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), `"status": "在用"`) {
		t.Fatalf("失败的在用状态被随后成功的写盘顺带写入文件：\n%s", raw)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.findInstrument("M-1"); got == nil || got.Status != StatusRetired {
		t.Fatalf("文件中的器具应停留在停用状态：%+v", got)
	}

	// 在同一台账对象重新提交切换为在用：只有这次保存成功，状态才在查询和
	// 使用判断中生效（合格未到期证书此时才足以获准使用）。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("恢复后重新提交应成功保存: %v", err)
	}
	if got := l.findInstrument("M-1"); got.Status != StatusInUse {
		t.Fatalf("成功切换后状态应为在用：%s", got.Status)
	}
	d, _ = l.CanUse("M-1")
	if !d.Allowed || d.Latest == nil || d.Latest.Number != "C-2" {
		t.Fatalf("保存成功后应按在用 + 最近合格证书获准使用：%+v", d)
	}
	r, _ = l.Review("M-1")
	if !r.CanUse || r.Instrument.Status != StatusInUse {
		t.Fatalf("核对应显示在用且可以使用：status=%s canUse=%v", r.Instrument.Status, r.CanUse)
	}
	// 停用期间的历史拒绝仍可查，冻结原因不随成功切换重写。
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, frozen) {
		t.Fatalf("成功切换后历史拒绝原因被改写：%+v", r.Rejections)
	}
	reopened, _ = openAt(path, clock.now)
	if got := reopened.findInstrument("M-1"); got.Status != StatusInUse {
		t.Fatalf("重开台账后应读到成功切换的在用状态：%s", got.Status)
	}

	// 请求状态本来就等于最后成功保存的状态时直接成功，不额外要求写盘。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("重复提交当前状态应直接成功：%v", err)
	}
}

// TestFailedSwitchFromInUseDoesNotPrematurelyRestrict 验证原先在用的器具切换
// 为停用或待校准失败时，不能提前按新状态限制使用；证书本身到期或缺失导致的
// 限制仍按既有规则判断（不会因失败切换多出状态原因）。
func TestFailedSwitchFromInUseDoesNotPrematurelyRestrict(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.1, Summary: "s",
	})
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	blockSaving(t, l)
	for _, target := range []Status{StatusRetired, StatusPending} {
		if err := l.SetStatus("M-1", target); err == nil {
			t.Fatalf("切换为%s时写盘失败应返回保存错误", target)
		}
		if got := l.findInstrument("M-1"); got.Status != StatusInUse {
			t.Fatalf("失败切换为%s后应仍在用，得到 %s", target, got.Status)
		}
		d, err := l.CanUse("M-1")
		if err != nil {
			t.Fatalf("can use: %v", err)
		}
		if !d.Allowed || len(d.Reasons) != 0 {
			t.Fatalf("失败切换为%s不得提前限制使用：allowed=%v reasons=%v",
				target, d.Allowed, d.Reasons)
		}
		r, _ := l.Review("M-1")
		if !r.CanUse || r.Instrument.Status != StatusInUse {
			t.Fatalf("核对应仍显示在用且可以使用：%+v", r.Instrument)
		}
	}

	// 恢复后重新提交停用：保存成功才真正受限。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后停用应成功: %v", err)
	}
	d, _ := l.CanUse("M-1")
	if d.Allowed || !containsReason(d.Reasons, "停用") {
		t.Fatalf("成功停用后应按停用限制使用：%v", d.Reasons)
	}
}

// TestFailedStatusSwitchKeepsCertificateBasedRestrictions 验证证书本身到期、
// 超差或缺失导致的使用限制，在状态切换保存失败时仍按既有规则判断。
func TestFailedStatusSwitchKeepsCertificateBasedRestrictions(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}

	// 在用 + 到期证书：切换停用失败后仍因到期被拒，原因里不能出现停用。
	lExp := newTestLedger(t, clock)
	mustRegister(t, lExp, "M-EXP", "万用表", 0.5)
	mustAddCert(t, lExp, CertificateInput{
		InstrumentID: "M-EXP", Number: "C-OLD", CalDate: "2025-01-01",
		Expiry: "2025-12-31", Method: "m", Error: 0.1, Summary: "s",
	})
	if err := lExp.SetStatus("M-EXP", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	blockSaving(t, lExp)
	if err := lExp.SetStatus("M-EXP", StatusRetired); err == nil {
		t.Fatal("写盘失败应返回保存错误")
	}
	d, _ := lExp.CanUse("M-EXP")
	if d.Allowed || !containsReason(d.Reasons, "到期") || containsReason(d.Reasons, "停用") {
		t.Fatalf("失败停用不得替换证书到期原因：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}

	// 在用 + 无证书：切换待校准失败后仍只因无证书被拒，不能出现待校准原因。
	lNone := newTestLedger(t, clock)
	mustRegister(t, lNone, "M-NONE", "万用表", 0.5)
	if err := lNone.SetStatus("M-NONE", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	blockSaving(t, lNone)
	if err := lNone.SetStatus("M-NONE", StatusPending); err == nil {
		t.Fatal("写盘失败应返回保存错误")
	}
	d, _ = lNone.CanUse("M-NONE")
	if d.Allowed || !containsReason(d.Reasons, "没有校准证书") ||
		containsReason(d.Reasons, "待校准") {
		t.Fatalf("失败的待校准切换不得增加状态原因：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
}
