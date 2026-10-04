package calibrate

import (
	"path/filepath"
	"testing"
)

// TestReturnedCertificateCannotRewriteOfficial 验证调用方整理 AddCertificate
// 的返回结果不能改写台账里的正式证书：误差、截止日、编号、所属器具、校准日期、
// 方法与摘要的修改都只影响调用方自己那份展示数据。
func TestReturnedCertificateCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	_ = l.SetStatus("M-1", StatusInUse)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.8, Summary: "实测批次",
	}
	ret, dup, err := l.AddCertificate(in)
	if err != nil || dup {
		t.Fatalf("首次录入应为新证书：err=%v dup=%v", err, dup)
	}
	want := Certificate{
		Number:       "C-1",
		InstrumentID: "M-1",
		CalDate:      "2026-09-01",
		Expiry:       "2027-09-01",
		Method:       "规范A",
		Error:        0.8,
		Summary:      "实测批次",
		CreatedAt:    ret.CreatedAt,
	}

	// 把返回结果中的误差改成合格值：再次核对仍显示原来的 0.8 和超差，
	// 申请使用仍被拒绝。
	ret.Error = 0.1
	if latest := l.LatestCertificate("M-1"); latest == nil || latest.Error != 0.8 ||
		latest.Number != "C-1" {
		t.Fatalf("正式证书被返回结果改写：%+v", latest)
	}
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("改返回误差不得解除超差限制：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	if d.Latest == nil || d.Latest.Error != 0.8 || d.Latest.Pass {
		t.Fatalf("核对视图应表达正式证书的 0.8 与超差：%+v", d.Latest)
	}
	used, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if used.Allowed {
		t.Fatal("正式证书超差时申请使用必须被拒绝")
	}

	// 修改返回结果中的编号、所属器具、校准日期、方法和摘要：正式证书仍保留
	// 实际录入内容，不发生归属转移、编号释放或最近证书选择变化。
	ret.Number = "C-MOVED"
	ret.InstrumentID = "M-2"
	ret.CalDate = "2026-01-01"
	ret.Method = "被改过的方法"
	ret.Summary = "被改过的摘要"
	if got := l.findCertificate("C-1"); got == nil || *got != want {
		t.Fatalf("原编号证书被改写或丢失：%+v", got)
	}
	if l.findCertificate("C-MOVED") != nil {
		t.Fatal("修改返回结果的编号不能占用或产生新编号")
	}
	if n := len(l.certificatesOf("M-2")); n != 0 {
		t.Fatalf("证书归属不能被返回结果转移，M-2 出现证书 %d 张", n)
	}
	if latest := l.LatestCertificate("M-1"); latest == nil || *latest != want {
		t.Fatalf("最近证书选择或内容被返回结果改变：%+v", latest)
	}

	// 正式编号仍被占用、同日位置仍被占用：同号不同内容报冲突，同日换号被拒绝。
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.2, Summary: "实测批次",
	}); !IsConflict(err) {
		t.Fatalf("改返回结果不能释放原编号，同号不同内容仍应报冲突：%v", err)
	}
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-01",
		Expiry: "2027-09-02", Method: "m", Error: 0.1, Summary: "s",
	}); !isSameDayRejection(err) {
		t.Fatalf("改返回校准日期不能释放同日位置，得到 %v", err)
	}

	// 其他正常业务操作触发保存后，展示结果里的修改不得进入证书历史。
	_ = l.SetStatus("M-2", StatusInUse)
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.data.Certificates); n != 1 {
		t.Fatalf("历史中只能保留正式录入的一张证书，得到 %d 张", n)
	}
	if got := reopened.findCertificate("C-1"); got == nil || *got != want {
		t.Fatalf("写盘重开后正式证书被污染：%+v", got)
	}
	if reopened.findCertificate("C-MOVED") != nil {
		t.Fatal("展示用编号被写进了台账文件")
	}
}

// TestReturnedCertificateExpiryCannotRestoreUsage 验证正式证书已到期时，
// 把返回结果的截止日改成未来不能恢复使用资格。
func TestReturnedCertificateExpiryCannotRestoreUsage(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)

	ret, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2025-01-01",
		Expiry: "2026-10-01", Method: "m", Error: 0, Summary: "s",
	})
	if err != nil {
		t.Fatalf("add cert: %v", err)
	}
	// 调用方把自己那份返回结果的截止日改到未来。
	ret.Expiry = "2030-01-01"

	d, _ := l.CanUse("M-1")
	if d.Allowed || !containsReason(d.Reasons, "到期") {
		t.Fatalf("改返回截止日不能恢复使用资格：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	if d.Latest == nil || !d.Latest.Expired || d.Latest.Expiry != "2026-10-01" {
		t.Fatalf("核对视图应保留正式截止日与到期判定：%+v", d.Latest)
	}
	r, _ := l.Review("M-1")
	if r.CanUse || r.Latest == nil || !r.Latest.Expired || r.Latest.Expiry != "2026-10-01" {
		t.Fatalf("按器具核对仍应表达正式到期证书：%+v", r.Latest)
	}
}

// TestTwoCertificateReturnsAreIndependent 验证同一张证书先后取得的两份录入
// 返回结果各自独立：修改其中一份不能让另一份跟着变化，也不能影响此前已取得的
// 最近证书或核对结果。
func TestTwoCertificateReturnsAreIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.8, Summary: "摘要",
	}
	first, _, err := l.AddCertificate(in)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	// 在取得第二份返回结果之前先取一份最近证书与核对结果。
	snapshot := l.LatestCertificate("M-1")
	wantSnap := *snapshot
	reviewBefore, _ := l.Review("M-1")
	wantHistory := reviewBefore.History[0].Certificate

	second, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("同号同内容再次提交应标记 duplicate：err=%v dup=%v", err, dup)
	}
	if *second != *first {
		t.Fatalf("两份返回结果应表达相同的业务内容与最初录入时间：%+v / %+v",
			*first, *second)
	}
	if second == first {
		t.Fatal("两份返回结果必须各自独立，不能共享同一指针")
	}

	// 修改第一份：第二份、此前取得的快照与核对结果都不跟着变。
	first.Error = 0.1
	first.Expiry = "2030-01-01"
	if second.Error != 0.8 || second.Expiry != "2027-09-01" {
		t.Fatalf("整理第一份返回结果影响了第二份：%+v", second)
	}
	if *snapshot != wantSnap {
		t.Fatal("整理第一份返回结果影响了此前取得的最近证书")
	}
	if reviewBefore.History[0].Certificate != wantHistory {
		t.Fatal("整理第一份返回结果影响了此前取得的核对结果")
	}

	// 修改第二份：第一份与台账内正式证书都不跟着变。
	second.Number = "C-2"
	second.Summary = "第二份自己的摘要"
	if first.Number != "C-1" || first.Summary != "摘要" {
		t.Fatalf("整理第二份返回结果影响了第一份：%+v", first)
	}
	got := l.findCertificate("C-1")
	if got == nil || got.Error != 0.8 || got.Expiry != "2027-09-01" ||
		got.Summary != "摘要" {
		t.Fatalf("整理返回结果改写了台账内正式证书：%+v", got)
	}
	if l.findCertificate("C-2") != nil {
		t.Fatal("第二份返回结果改编号不能在台账中产生新证书")
	}
	if n := len(l.certificatesOf("M-1")); n != 1 {
		t.Fatalf("历史中只能保留一张证书，得到 %d 张", n)
	}
}

// TestMutatedReturnKeepsDuplicateAndConflictRules 验证即使调用方整理过返回
// 数据，原始内容再次提交仍标记为重复并返回原业务内容和最初录入时间；同号不同
// 内容仍报冲突，不能被当成覆盖接受。
func TestMutatedReturnKeepsDuplicateAndConflictRules(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.8, Summary: "摘要",
	}
	first, _, err := l.AddCertificate(in)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	createdAt := first.CreatedAt

	// 调用方把第一份返回结果改得面目全非。
	first.Number = "C-FREE"
	first.InstrumentID = "M-2"
	first.CalDate = "2026-01-01"
	first.Expiry = "2030-01-01"
	first.Method = "别的方法"
	first.Error = 0
	first.Summary = "别的摘要"

	// 按原始内容再次提交同号证书：仍标记为重复，返回原业务内容和最初录入时间。
	again, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("原始内容再次提交仍应标记重复：err=%v dup=%v", err, dup)
	}
	if again.Number != "C-1" || again.InstrumentID != "M-1" ||
		again.CalDate != "2026-09-01" || again.Expiry != "2027-09-01" ||
		again.Method != "规范A" || again.Error != 0.8 || again.Summary != "摘要" {
		t.Fatalf("重复提交应返回原业务内容，得到 %+v", again)
	}
	if again.CreatedAt != createdAt {
		t.Fatalf("重复提交应返回最初录入时间 %s，得到 %s", createdAt, again.CreatedAt)
	}
	if n := len(l.certificatesOf("M-1")); n != 1 {
		t.Fatalf("历史中只应保留一张证书，得到 %d 张", n)
	}

	// 正式提交同号但内容不同的证书：仍报冲突，不能因为整理过返回数据就覆盖。
	diff := in
	diff.Error = 0.2
	if _, _, err := l.AddCertificate(diff); !IsConflict(err) {
		t.Fatalf("同号不同内容仍应报冲突，得到 %v", err)
	}
	got := l.findCertificate("C-1")
	if got == nil || got.Error != 0.8 {
		t.Fatalf("冲突提交后正式证书被覆盖：%+v", got)
	}

	// 返回结果里用过的“新编号”并未被占用：另一日期/器具可以正常录入它。
	free, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-2", Number: "C-FREE", CalDate: "2026-09-02",
		Expiry: "2027-09-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if err != nil {
		t.Fatalf("仅在返回结果中出现的编号不应被占用：%v", err)
	}
	if free.Number != "C-FREE" {
		t.Fatalf("新录入证书内容异常：%+v", free)
	}
}

// isSameDayRejection 判定 err 是否为“同一器具同一天不能再登记”的校验拒绝。
func isSameDayRejection(err error) bool {
	return IsValidation(err) && containsReason([]string{err.Error()}, "同一天")
}
