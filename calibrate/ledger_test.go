package calibrate

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 固定“今天”，让日期相关断言可重复。
var testToday = time.Date(2026, 10, 2, 9, 30, 0, 0, time.Local)

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatalf("打开台账失败: %v", err)
	}
	l.now = func() time.Time { return testToday }
	return l
}

func registerInstrument(t *testing.T, l *Ledger, id, name string, allowed float64) {
	t.Helper()
	if err := l.RegisterInstrument(id, name, allowed); err != nil {
		t.Fatalf("登记器具失败: %v", err)
	}
}

func addCert(t *testing.T, l *Ledger, c Certificate) *Certificate {
	t.Helper()
	cert, created, err := l.AddCertificate(c)
	if err != nil {
		t.Fatalf("录入证书失败: %v", err)
	}
	if !created {
		t.Fatalf("期望新建证书，但返回了已有证书")
	}
	return cert
}

// ---------- 登记 ----------

func TestRegisterInstrumentOK(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	inst := l.Instrument("I001")
	if inst == nil {
		t.Fatal("器具未找到")
	}
	if inst.Status != StatusPending {
		t.Fatalf("新器具状态应为待校准，实际为 %q", inst.Status)
	}
	if inst.Name != "卡尺" || inst.AllowedError != 0.05 {
		t.Fatalf("登记信息不正确: %+v", inst)
	}
}

func TestRegisterTrimsWhitespace(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "  I002  ", "  千分尺  ", 0.01)
	if l.Instrument("I002") == nil {
		t.Fatal("编号首尾空白未被规整")
	}
}

func TestRegisterBlankRejected(t *testing.T) {
	l := newTestLedger(t)
	for _, tc := range []struct{ id, name string }{
		{"", "卡尺"},
		{"   ", "卡尺"},
		{"I001", ""},
		{"I001", "  \t "},
	} {
		err := l.RegisterInstrument(tc.id, tc.name, 0.05)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("id=%q name=%q 期望 ErrInvalidInput，实际 %v", tc.id, tc.name, err)
		}
	}
	if len(l.Instruments) != 0 {
		t.Fatal("无效登记不应留下任何器具")
	}
}

func TestRegisterInvalidErrorRejected(t *testing.T) {
	l := newTestLedger(t)
	for _, v := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		err := l.RegisterInstrument("I001", "卡尺", v)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("允许误差 %v 期望 ErrInvalidInput，实际 %v", v, err)
		}
	}
	if len(l.Instruments) != 0 {
		t.Fatal("无效登记不应留下任何器具")
	}
}

func TestRegisterDuplicateRejectedUnchanged(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	err := l.RegisterInstrument("I001", "另一把尺子", 0.02)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重复编号期望 ErrConflict，实际 %v", err)
	}
	inst := l.Instrument("I001")
	if inst.Name != "卡尺" || inst.AllowedError != 0.05 {
		t.Fatalf("重复编号不应改变原记录: %+v", inst)
	}
	if len(l.Instruments) != 1 {
		t.Fatal("重复编号不应新增记录")
	}
}

// ---------- 状态切换 ----------

func TestSetStatusTransitions(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	for _, st := range []Status{StatusActive, StatusInactive, StatusPending, StatusActive} {
		if err := l.SetStatus("I001", st); err != nil {
			t.Fatalf("切换到 %s 失败: %v", st, err)
		}
		if got := l.Instrument("I001").Status; got != st {
			t.Fatalf("期望状态 %s，实际 %s", st, got)
		}
	}
}

func TestSetStatusUnknownInstrument(t *testing.T) {
	l := newTestLedger(t)
	if err := l.SetStatus("NOPE", StatusActive); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}

func TestSetStatusInvalid(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	if err := l.SetStatus("I001", Status("报废")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("期望 ErrInvalidInput，实际 %v", err)
	}
	if l.Instrument("I001").Status != StatusPending {
		t.Fatal("无效状态不应改变原状态")
	}
}

// ---------- 证书录入 ----------

func validCert(instrument, number string) Certificate {
	return Certificate{
		InstrumentID:  instrument,
		Number:        number,
		CalDate:       "2026-09-01",
		ExpiryDate:    "2027-09-01",
		Method:        "外校",
		MeasuredError: 0.01,
		Summary:       "零点示值误差",
	}
}

func TestAddCertificateOK(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	cert := addCert(t, l, validCert("I001", "C001"))
	if cert.Number != "C001" {
		t.Fatalf("证书编号不正确: %+v", cert)
	}
	if l.LatestCertificate("I001") == nil {
		t.Fatal("最近证书不应为空")
	}
}

func TestAddCertificateBlankFields(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	base := validCert("I001", "C001")
	for _, mutate := range []func(*Certificate){
		func(c *Certificate) { c.Number = "" },
		func(c *Certificate) { c.Number = "   " },
		func(c *Certificate) { c.Method = "" },
		func(c *Certificate) { c.Method = "  " },
		func(c *Certificate) { c.Summary = "" },
		func(c *Certificate) { c.Summary = "\t" },
		func(c *Certificate) { c.InstrumentID = "" },
	} {
		c := base
		mutate(&c)
		_, _, err := l.AddCertificate(c)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("期望 ErrInvalidInput，实际 %v", err)
		}
	}
	if len(l.Certificates) != 0 {
		t.Fatal("无效录入不应留下证书")
	}
}

func TestAddCertificateBadDates(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	base := validCert("I001", "C001")
	for _, tc := range []struct {
		cal, exp string
	}{
		{"2026-13-01", "2027-01-01"}, // 月份不存在
		{"2026-02-30", "2027-01-01"}, // 日期不存在
		{"2026-10-02", "2027-02-29"}, // 2027 非闰年
		{"2026-9-1", "2027-9-1"},     // 非 YYYY-MM-DD
		{"2026-10-03", "2027-10-03"}, // 校准日晚于今天
		{"2026-09-01", "2026-09-01"}, // 截止日等于校准日
		{"2026-09-01", "2026-08-31"}, // 截止日早于校准日
	} {
		c := base
		c.CalDate = tc.cal
		c.ExpiryDate = tc.exp
		_, _, err := l.AddCertificate(c)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("cal=%s exp=%s 期望 ErrInvalidInput，实际 %v", tc.cal, tc.exp, err)
		}
	}
	if len(l.Certificates) != 0 {
		t.Fatal("无效日期不应留下证书")
	}
}

func TestAddCertificateLeapDayValid(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2024-02-29" // 闰年存在的日期
	c.ExpiryDate = "2025-02-28"
	if _, _, err := l.AddCertificate(c); err != nil {
		t.Fatalf("闰年 2 月 29 日应有效: %v", err)
	}
}

func TestAddCertificateNonFiniteMeasured(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		c := validCert("I001", "C001")
		c.MeasuredError = v
		_, _, err := l.AddCertificate(c)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("测得误差 %v 期望 ErrInvalidInput，实际 %v", v, err)
		}
	}
}

func TestAddCertificateUnknownInstrument(t *testing.T) {
	l := newTestLedger(t)
	_, _, err := l.AddCertificate(validCert("NOPE", "C001"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
	if len(l.Certificates) != 0 {
		t.Fatal("未知器具不应留下证书")
	}
}

func TestAddCertificateNumberIdempotent(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	first := addCert(t, l, c)
	// 同号且业务字段完全一致：返回原证书，不增加历史。
	second, created, err := l.AddCertificate(c)
	if err != nil {
		t.Fatalf("同号同内容应幂等返回: %v", err)
	}
	if created {
		t.Fatal("幂等提交不应标记为新建")
	}
	if second != first {
		t.Fatal("幂等提交应返回同一份证书")
	}
	if len(l.Certificates) != 1 {
		t.Fatalf("幂等提交不应增加历史，当前 %d 份", len(l.Certificates))
	}
}

func TestAddCertificateNumberConflict(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	registerInstrument(t, l, "I002", "千分尺", 0.01)
	addCert(t, l, validCert("I001", "C001"))
	for _, mutate := range []func(*Certificate){
		func(c *Certificate) { c.InstrumentID = "I002" },
		func(c *Certificate) { c.CalDate = "2026-08-01" },
		func(c *Certificate) { c.ExpiryDate = "2027-08-01" },
		func(c *Certificate) { c.Method = "内校" },
		func(c *Certificate) { c.MeasuredError = 0.02 },
		func(c *Certificate) { c.Summary = "不同摘要" },
	} {
		c := validCert("I001", "C001")
		mutate(&c)
		_, _, err := l.AddCertificate(c)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("期望 ErrConflict，实际 %v", err)
		}
	}
	if len(l.Certificates) != 1 {
		t.Fatal("冲突提交不应改动已有证书")
	}
	// 原证书内容不变。
	got := l.LatestCertificate("I001")
	if got.CalDate != "2026-09-01" || got.MeasuredError != 0.01 {
		t.Fatalf("已有证书被改动: %+v", got)
	}
}

func TestAddCertificateSameDayDifferentNumberRejected(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	addCert(t, l, validCert("I001", "C001"))
	c := validCert("I001", "C002") // 同校准日、不同编号
	_, _, err := l.AddCertificate(c)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("同器具同日不同编号期望 ErrConflict，实际 %v", err)
	}
	if len(l.Certificates) != 1 || l.Certificates[0].Number != "C001" {
		t.Fatal("拒绝时应保留原证书")
	}
}

func TestAddCertificateSameDaySameNumberIdempotent(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	addCert(t, l, validCert("I001", "C001"))
	cert, created, err := l.AddCertificate(validCert("I001", "C001"))
	if err != nil || created || cert.Number != "C001" {
		t.Fatalf("同日同号应幂等: created=%v err=%v", created, err)
	}
}

// ---------- 最近证书按校准日期 ----------

func TestLatestCertificateByCalDateNotEntryOrder(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	// 先录较早的，再录较晚的。
	older := validCert("I001", "C-OLD")
	older.CalDate = "2026-01-01"
	older.ExpiryDate = "2027-01-01"
	addCert(t, l, older)
	newer := validCert("I001", "C-NEW")
	newer.CalDate = "2026-09-01"
	newer.ExpiryDate = "2027-09-01"
	addCert(t, l, newer)
	if got := l.LatestCertificate("I001"); got.Number != "C-NEW" {
		t.Fatalf("最近证书应按校准日期取 C-NEW，实际 %s", got.Number)
	}

	// 补录更早的证书只增加历史，不改变最近证书。
	backfill := validCert("I001", "C-BACK")
	backfill.CalDate = "2025-01-01"
	backfill.ExpiryDate = "2026-01-01"
	addCert(t, l, backfill)
	if got := l.LatestCertificate("I001"); got.Number != "C-NEW" {
		t.Fatalf("补录较早证书不应改变最近证书，实际 %s", got.Number)
	}
	if len(l.Certificates) != 3 {
		t.Fatalf("补录应增加历史，当前 %d 份", len(l.Certificates))
	}
}

func TestLatestCertificateNoFallbackToQualified(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	// 较早的合格证书。
	old := validCert("I001", "C-OK")
	old.CalDate = "2025-01-01"
	old.ExpiryDate = "2026-01-01"
	old.MeasuredError = 0.01
	addCert(t, l, old)
	// 最近的超差证书。
	bad := validCert("I001", "C-BAD")
	bad.CalDate = "2026-09-01"
	bad.ExpiryDate = "2027-09-01"
	bad.MeasuredError = 0.10
	addCert(t, l, bad)

	l.SetStatus("I001", StatusActive)
	approved, reasons, err := l.ApplyUse("I001")
	if err != nil {
		t.Fatalf("申请失败: %v", err)
	}
	if approved {
		t.Fatal("最近证书超差时不应批准，即使更早证书合格")
	}
	if !contains(reasons, "最近证书超差") {
		t.Fatalf("原因应包含超差，实际 %v", reasons)
	}
}

// ---------- 合格判定 ----------

func TestQualifiedBoundary(t *testing.T) {
	// 等于限值算合格。
	if !Qualified(0.05, 0.05) {
		t.Fatal("|测得误差| 等于允许误差应算合格")
	}
	if !Qualified(0.05, -0.05) {
		t.Fatal("|测得误差| 等于允许误差应算合格（负值取绝对值）")
	}
	if Qualified(0.05, 0.0500001) {
		t.Fatal("超过限值应算超差")
	}
	if Qualified(0.05, -0.06) {
		t.Fatal("超过限值应算超差（负值取绝对值）")
	}
}

func TestQualifiedIsSystemComputed(t *testing.T) {
	// 录入者无法选择结论：测得误差超限即超差，与证书字段无关。
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.MeasuredError = 0.06
	addCert(t, l, c)
	l.SetStatus("I001", StatusActive)
	approved, reasons, _ := l.ApplyUse("I001")
	if approved || !contains(reasons, "最近证书超差") {
		t.Fatalf("超差必须由系统判定，approved=%v reasons=%v", approved, reasons)
	}
}

// ---------- 到期判定 ----------

func TestExpiredBoundary(t *testing.T) {
	// 截止日当天起即到期。
	if !Expired("2026-10-02", testToday) {
		t.Fatal("截止日当天应算到期")
	}
	if Expired("2026-10-03", testToday) {
		t.Fatal("截止日次日不应算到期")
	}
	if !Expired("2026-10-01", testToday) {
		t.Fatal("截止日早于今天应算到期")
	}
}

func TestExpiryRejudgedByCurrentDate(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2026-10-05"
	addCert(t, l, c)
	l.SetStatus("I001", StatusActive)

	// 今天 2026-10-02：未到期，批准。
	approved, _, _ := l.ApplyUse("I001")
	if !approved {
		t.Fatal("到期前应批准使用")
	}

	// 把“今天”推进到 2026-10-05（截止日当天）：到期，拒绝。
	l.now = func() time.Time { return time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local) }
	approved, reasons, _ := l.ApplyUse("I001")
	if approved {
		t.Fatal("截止日当天起应拒绝")
	}
	if !contains(reasons, "最近证书已到期") {
		t.Fatalf("原因应包含到期，实际 %v", reasons)
	}
}

// ---------- 使用申请 ----------

func setupUsableInstrument(t *testing.T, l *Ledger) {
	t.Helper()
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2027-09-01"
	addCert(t, l, c)
	l.SetStatus("I001", StatusActive)
}

func TestApplyUseApproved(t *testing.T) {
	l := newTestLedger(t)
	setupUsableInstrument(t, l)
	approved, reasons, err := l.ApplyUse("I001")
	if err != nil {
		t.Fatalf("申请失败: %v", err)
	}
	if !approved {
		t.Fatalf("应批准使用，原因: %v", reasons)
	}
	if len(reasons) != 0 {
		t.Fatalf("批准时不应有原因，实际 %v", reasons)
	}
}

func TestApplyUseRejectedWithAllReasons(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	// 停用 + 无证书：两个原因都应列出。
	l.SetStatus("I001", StatusInactive)
	approved, reasons, err := l.ApplyUse("I001")
	if err != nil {
		t.Fatalf("申请失败: %v", err)
	}
	if approved {
		t.Fatal("停用且无证书应拒绝")
	}
	if !contains(reasons, "器具已停用") || !contains(reasons, "没有校准证书") {
		t.Fatalf("应列出全部适用原因，实际 %v", reasons)
	}
}

func TestApplyUsePendingRejected(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2027-09-01"
	addCert(t, l, c)
	// 保持待校准状态。
	approved, reasons, _ := l.ApplyUse("I001")
	if approved || !contains(reasons, "器具处于待校准状态") {
		t.Fatalf("待校准应拒绝，approved=%v reasons=%v", approved, reasons)
	}
}

func TestApplyUseExpiredRejected(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2026-10-01" // 今天 10-02 已到期
	addCert(t, l, c)
	l.SetStatus("I001", StatusActive)
	approved, reasons, _ := l.ApplyUse("I001")
	if approved || !contains(reasons, "最近证书已到期") {
		t.Fatalf("到期应拒绝，approved=%v reasons=%v", approved, reasons)
	}
}

func TestApplyUseUnknownInstrument(t *testing.T) {
	l := newTestLedger(t)
	before := len(l.Usage)
	_, _, err := l.ApplyUse("NOPE")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
	if len(l.Usage) != before {
		t.Fatal("未知编号不应新增申请记录")
	}
}

func TestApplyUseSavesRecord(t *testing.T) {
	l := newTestLedger(t)
	setupUsableInstrument(t, l)
	before := l.now()
	approved, _, _ := l.ApplyUse("I001")
	if !approved {
		t.Fatal("应批准")
	}
	res, err := l.Check("I001")
	if err != nil {
		t.Fatalf("核对失败: %v", err)
	}
	if len(res.UsageRecords) != 1 {
		t.Fatalf("应保存 1 条申请记录，实际 %d", len(res.UsageRecords))
	}
	rec := res.UsageRecords[0]
	if !rec.Approved || rec.InstrumentID != "I001" {
		t.Fatalf("记录内容不正确: %+v", rec)
	}
	if rec.Time.Before(before.Add(-time.Second)) {
		t.Fatalf("申请时间不正确: %v", rec.Time)
	}
}

func TestRejectionReasonsAreFrozen(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	l.SetStatus("I001", StatusInactive)
	// 第一次申请：停用 + 无证书。
	_, reasons1, _ := l.ApplyUse("I001")
	if !contains(reasons1, "器具已停用") || !contains(reasons1, "没有校准证书") {
		t.Fatalf("初次拒绝原因不正确: %v", reasons1)
	}
	// 随后补录证书并切换为在用。
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2027-09-01"
	addCert(t, l, c)
	l.SetStatus("I001", StatusActive)

	res, err := l.Check("I001")
	if err != nil {
		t.Fatalf("核对失败: %v", err)
	}
	if len(res.UsageRecords) != 1 {
		t.Fatalf("应有 1 条历史记录，实际 %d", len(res.UsageRecords))
	}
	rec := res.UsageRecords[0]
	if rec.Approved {
		t.Fatal("历史拒绝记录不应被改写为批准")
	}
	if !contains(rec.Reasons, "器具已停用") || !contains(rec.Reasons, "没有校准证书") {
		t.Fatalf("历史拒绝原因应保持不变，实际 %v", rec.Reasons)
	}
}

// ---------- 核对 ----------

func TestCheckShowsAllInfo(t *testing.T) {
	l := newTestLedger(t)
	registerInstrument(t, l, "I001", "卡尺", 0.05)
	old := validCert("I001", "C-OLD")
	old.CalDate = "2026-01-01"
	old.ExpiryDate = "2027-01-01"
	addCert(t, l, old)
	newer := validCert("I001", "C-NEW")
	newer.CalDate = "2026-09-01"
	newer.ExpiryDate = "2027-09-01"
	addCert(t, l, newer)
	l.SetStatus("I001", StatusActive)
	l.ApplyUse("I001")

	res, err := l.Check("I001")
	if err != nil {
		t.Fatalf("核对失败: %v", err)
	}
	if !res.Approved {
		t.Fatalf("当前应可使用，原因: %v", res.Reasons)
	}
	if res.LatestCertificate == nil || res.LatestCertificate.Number != "C-NEW" {
		t.Fatalf("最近证书应为 C-NEW")
	}
	if len(res.Certificates) != 2 {
		t.Fatalf("应列出全部 2 份历史证书，实际 %d", len(res.Certificates))
	}
	if res.Certificates[0].Number != "C-OLD" || res.Certificates[1].Number != "C-NEW" {
		t.Fatalf("历史证书应按校准日期排序: %v", res.Certificates)
	}
	if len(res.UsageRecords) != 1 || !res.UsageRecords[0].Approved {
		t.Fatalf("使用记录不正确: %+v", res.UsageRecords)
	}
}

func TestCheckUnknownInstrument(t *testing.T) {
	l := newTestLedger(t)
	if _, err := l.Check("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，实际 %v", err)
	}
}

// ---------- 持久化 ----------

func TestPersistenceReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")

	l1, err := Open(path)
	if err != nil {
		t.Fatalf("打开台账失败: %v", err)
	}
	l1.now = func() time.Time { return testToday }
	registerInstrument(t, l1, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2027-09-01"
	addCert(t, l1, c)
	l1.SetStatus("I001", StatusActive)
	if _, _, err := l1.ApplyUse("I001"); err != nil {
		t.Fatalf("申请失败: %v", err)
	}

	// 重新打开同一台账。
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("重新打开台账失败: %v", err)
	}
	l2.now = func() time.Time { return testToday }
	res, err := l2.Check("I001")
	if err != nil {
		t.Fatalf("核对失败: %v", err)
	}
	if !res.Approved {
		t.Fatalf("重开后应仍可使用，原因: %v", res.Reasons)
	}
	if res.LatestCertificate == nil || res.LatestCertificate.Number != "C001" {
		t.Fatal("重开后最近证书丢失")
	}
	if len(res.UsageRecords) != 1 {
		t.Fatalf("重开后使用记录丢失，实际 %d 条", len(res.UsageRecords))
	}
	if res.Instrument.Status != StatusActive {
		t.Fatalf("重开后状态应为在用，实际 %s", res.Instrument.Status)
	}
}

func TestPersistenceReopenRejudgesByOpenDate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")

	l1, _ := Open(path)
	l1.now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local) }
	registerInstrument(t, l1, "I001", "卡尺", 0.05)
	c := validCert("I001", "C001")
	c.CalDate = "2026-09-01"
	c.ExpiryDate = "2026-10-05"
	addCert(t, l1, c)
	l1.SetStatus("I001", StatusActive)

	// 10-02 打开：未到期，可批准。
	l2, _ := Open(path)
	l2.now = func() time.Time { return testToday }
	if approved, _, _ := l2.ApplyUse("I001"); !approved {
		t.Fatal("到期前应批准")
	}

	// 10-05 打开：截止日当天，到期拒绝。
	l3, _ := Open(path)
	l3.now = func() time.Time { return time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local) }
	approved, reasons, _ := l3.ApplyUse("I001")
	if approved {
		t.Fatal("重开后按打开日期应判定到期")
	}
	if !contains(reasons, "最近证书已到期") {
		t.Fatalf("原因应包含到期，实际 %v", reasons)
	}
}

func TestDifferentLedgersAreIsolated(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(filepath.Join(dir, "a.json"))
	b, _ := Open(filepath.Join(dir, "b.json"))
	a.now = func() time.Time { return testToday }
	b.now = func() time.Time { return testToday }

	registerInstrument(t, a, "I001", "卡尺", 0.05)
	if a.Instrument("I001") == nil {
		t.Fatal("台账 a 应有 I001")
	}
	if b.Instrument("I001") != nil {
		t.Fatal("台账 b 不应看到台账 a 的器具")
	}

	registerInstrument(t, b, "I001", "另一把尺子", 0.02)
	if a.Instrument("I001").Name != "卡尺" {
		t.Fatal("台账数据应互不混入")
	}
}

func TestInvalidInputLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	l, _ := Open(path)
	l.now = func() time.Time { return testToday }
	registerInstrument(t, l, "I001", "卡尺", 0.05)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件失败: %v", err)
	}
	// 一连串无效操作。
	l.RegisterInstrument("", "X", 0.05)
	l.RegisterInstrument("I002", "X", -1)
	l.SetStatus("NOPE", StatusActive)
	l.AddCertificate(validCert("NOPE", "C001"))
	l.AddCertificate(func() Certificate { c := validCert("I001", "C001"); c.MeasuredError = math.NaN(); return c }())
	l.ApplyUse("NOPE")

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账文件失败: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("无效操作不应改动台账文件")
	}
}

func TestOpenCorruptedFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(path, []byte("{不是合法JSON"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("损坏的台账文件应返回错误")
	}
}

func TestOpenEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatalf("空文件应按空台账打开: %v", err)
	}
	if len(l.Instruments) != 0 {
		t.Fatal("空台账不应有器具")
	}
}

// ---------- 辅助 ----------

func contains(list []string, want string) bool {
	for _, v := range list {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
