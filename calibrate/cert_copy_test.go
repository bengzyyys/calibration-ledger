package calibrate

import (
	"path/filepath"
	"testing"
)

// TestReturnedCertificateCannotRewriteOfficial 验证调用方整理 AddCertificate
// 返回的证书（首次录入与同号重复两条路径）不会改写台账里的正式证书：
// 误差、截止日、编号、所属器具、校准日期、方法和摘要都以实际保存的内容为准。
func TestReturnedCertificateCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2026-10-10", Method: "比对法", Error: 0.8, Summary: "超差",
	}
	cert, dup, err := l.AddCertificate(in)
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}

	// 调用方整理返回结果：改误差、截止日、编号、所属器具、校准日期、方法和摘要。
	cert.Error = 0.1
	cert.Expiry = "2030-01-01"
	cert.Number = "C-伪造"
	cert.InstrumentID = "M-2"
	cert.CalDate = "2026-10-01"
	cert.Method = "展示用方法"
	cert.Summary = "展示用摘要"

	// 再次核对仍显示原来的 0.8 和超差，申请使用仍被拒绝。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Latest == nil || r.Latest.Number != "C-1" || r.Latest.Error != 0.8 ||
		r.Latest.Expiry != "2026-10-10" || r.Latest.Pass {
		t.Fatalf("最近证书被返回结果改写：%+v", r.Latest)
	}
	if r.CanUse {
		t.Fatal("超差证书被改成合格后申请使用仍应被拒绝")
	}
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("超差器具申请使用应被拒绝，err=%v allowed=%v", err, d.Allowed)
	}

	// 编号占用不变：被“释放”的 C-1 仍被占用，伪造编号不存在。
	conflict := in
	conflict.Error = 0.2
	if _, _, err := l.AddCertificate(conflict); !IsConflict(err) {
		t.Fatalf("同号不同内容仍应报冲突，得到 %v", err)
	}
	if got := l.LatestCertificate("M-1"); got == nil || got.Number != "C-1" {
		t.Fatalf("最近证书的选择被返回结果改变：%+v", got)
	}
	// 证书归属不变：M-2 不应出现被转移过来的证书。
	if got := l.LatestCertificate("M-2"); got != nil {
		t.Fatalf("返回结果改写的归属不应转移证书：%+v", got)
	}
	// 同日限制仍按实际校准日期判断：M-1 在 2026-09-01 已有证书。
	other := in
	other.Number = "C-2"
	if _, _, err := l.AddCertificate(other); !IsValidation(err) {
		t.Fatalf("同器具同日不同编号仍应拒绝，得到 %v", err)
	}

	// 台账因其他正常业务操作保存后，从文件重开仍是正式内容。
	_ = l.SetStatus("M-2", StatusInUse)
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.LatestCertificate("M-1")
	if got == nil || got.Number != "C-1" || got.Error != 0.8 ||
		got.Expiry != "2026-10-10" || got.Method != "比对法" || got.Summary != "超差" {
		t.Fatalf("展示副本的修改被带入证书历史：%+v", got)
	}
}

// TestReturnedCertificateExpiryMutationKeepsRejection 验证正式证书已到期时，
// 把返回结果的截止日改到未来也不能恢复使用资格。
func TestReturnedCertificateExpiryMutationKeepsRejection(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)

	cert, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2026-10-01", Method: "m", Error: 0.1, Summary: "s",
	})
	if err != nil {
		t.Fatalf("add cert: %v", err)
	}
	cert.Expiry = "2030-01-01"

	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed {
		t.Fatal("正式证书已到期，改写返回结果的截止日不应恢复使用资格")
	}
	if !containsReason(d.Reasons, "到期") {
		t.Fatalf("拒绝原因应包含到期，得到 %v", d.Reasons)
	}
}

// TestDuplicateReturnedCertificateIsIndependentCopy 验证同号同内容再次提交
// 返回的已有证书同样是独立副本：两份返回结果互不影响，也不影响正式证书；
// 按原始内容再次提交仍标记为重复并返回原业务内容与最初录入时间。
func TestDuplicateReturnedCertificateIsIndependentCopy(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.2, Summary: "s",
	}
	first, dup, err := l.AddCertificate(in)
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}
	second, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("同号同内容再次提交应标记为重复，err=%v dup=%v", err, dup)
	}

	// 两份返回结果各自独立：整理第一份不影响第二份，也不影响正式证书。
	first.Error = 0.9
	first.Summary = "第一份自己整理的内容"
	if second.Error != 0.2 || second.Summary != "s" {
		t.Fatalf("整理第一份返回结果影响了第二份：%+v", second)
	}
	if got := l.LatestCertificate("M-1"); got == nil || got.Error != 0.2 {
		t.Fatalf("整理返回结果改写了正式证书：%+v", got)
	}

	// 整理第二份也不影响此前已取得的最近证书与核对结果。
	latest := l.LatestCertificate("M-1")
	review, _ := l.Review("M-1")
	second.Number = "C-伪造"
	second.InstrumentID = "M-9"
	if latest.Number != "C-1" || review.Latest.Number != "C-1" {
		t.Fatal("整理重复返回结果影响了此前已取得的最近证书或核对结果")
	}

	// 按原始内容再次提交仍标记为重复，返回原业务内容和最初录入时间，
	// 历史中只保留一张。
	clock.t = mustDate(t, "2026-10-03")
	third, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("整理过返回数据后按原始内容提交仍应标记为重复，err=%v dup=%v", err, dup)
	}
	if third.Error != 0.2 || third.Summary != "s" || third.CreatedAt != first.CreatedAt {
		t.Fatalf("重复提交应返回原业务内容与最初录入时间：%+v", third)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("历史中应只保留一张证书，得到 %d 张", n)
	}

	// 同号但内容不同仍报冲突，不因此前整理过返回数据而接受覆盖。
	different := in
	different.Error = 0.1
	if _, _, err := l.AddCertificate(different); !IsConflict(err) {
		t.Fatalf("同号不同内容仍应报冲突，得到 %v", err)
	}
}
