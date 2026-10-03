package calibrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockSaving 让台账文件无法被原子替换：把台账文件改成同名目录后，
// save() 的 Rename 会稳定失败（即使以 root 运行也无法用文件覆盖目录）。
// 已打开的台账对象仍可在内存中继续查询；restoreSaving 恢复可写，
// 全程不需要关闭并重新打开台账。
func blockSaving(t *testing.T, l *Ledger) {
	t.Helper()
	if err := os.Remove(l.path); err != nil {
		t.Fatalf("移除台账文件以构造写盘失败: %v", err)
	}
	if err := os.Mkdir(l.path, 0o755); err != nil {
		t.Fatalf("以同名目录挡住台账替换: %v", err)
	}
}

func restoreSaving(t *testing.T, l *Ledger) {
	t.Helper()
	// 挡路的是空目录（save 的临时文件已被 defer 清理），移除后下一次
	// Rename 即可把临时文件改成台账文件。
	if err := os.Remove(l.path); err != nil {
		t.Fatalf("移除挡路目录以恢复可写: %v", err)
	}
}

// TestFailedCertificateSaveLeavesNoConclusion 覆盖核心修复：已通过业务校验的
// 新证书在写盘失败时明确报保存错误，台账状态与录入前完全一致。
func TestFailedCertificateSaveLeavesNoConclusion(t *testing.T) {
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

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	}

	blockSaving(t, l)

	cert, dup, err := l.AddCertificate(in)
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if cert != nil || dup {
		t.Fatalf("保存失败不能返回已录入或重复证书：cert=%v dup=%v", cert, dup)
	}
	if IsValidation(err) || IsConflict(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 继续使用同一台账对象：历史、最近证书与使用资格都与录入前一致。
	if n := len(l.data.Certificates); n != 0 {
		t.Fatalf("失败证书进入了内存台账：%d 张", n)
	}
	if l.findCertificate("C-1") != nil {
		t.Fatal("失败证书仍能按编号找到")
	}
	if n := len(l.certificatesOf("M-1")); n != 0 {
		t.Fatalf("失败证书进入了器具历史：%d 张", n)
	}
	if latest := l.LatestCertificate("M-1"); latest != nil {
		t.Fatalf("无证书器具在失败录入后出现最近证书：%+v", latest)
	}
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "没有校准证书") {
		t.Fatalf("失败的合格证书不得解除无证书限制：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	r, _ := l.Review("M-1")
	if r.CanUse || r.Latest != nil || len(r.History) != 0 {
		t.Fatalf("核对结果应与录入前一致：canUse=%v latest=%v history=%d",
			r.CanUse, r.Latest, len(r.History))
	}

	// 文件仍不可保存时，再次提交同号同内容应再次报保存失败，而不是以重复
	// 证书为由跳过保存返回成功。
	if _, dup, err := l.AddCertificate(in); err == nil || dup {
		t.Fatalf("再次提交应仍报保存失败且不是重复证书：err=%v dup=%v", err, dup)
	}

	// 失败的证书不占用当天位置：同日换个编号必须走到写盘（再次保存失败），
	// 而不能被“同一器具同一天已有证书”的校验挡住。
	sameDay := in
	sameDay.Number = "C-2"
	if _, _, err := l.AddCertificate(sameDay); err == nil || IsValidation(err) {
		t.Fatalf("失败证书不得占用同日位置，同日新证书应再次保存失败而非业务拒绝：%v", err)
	}
	if l.findCertificate("C-2") != nil || len(l.data.Certificates) != 0 {
		t.Fatal("同日重试的失败证书也不能留下记录")
	}

	// 恢复文件可写后不必重开台账：先做一件与该证书无关、能够成功保存的正常
	// 操作，失败的证书不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Certificates) != 0 || reopened.findCertificate("C-1") != nil {
		t.Fatalf("失败证书被随后成功的写盘顺带写入文件：%+v", reopened.data.Certificates)
	}

	// 在同一台账对象上重新提交同号同内容：作为新证书正常录入并进入历史。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	cert, dup, err = l.AddCertificate(in)
	if err != nil || dup || cert == nil {
		t.Fatalf("恢复后重新提交应作为新证书录入：err=%v dup=%v cert=%v", err, dup, cert)
	}
	if cert.Number != "C-1" {
		t.Fatalf("返回的新证书编号异常：%s", cert.Number)
	}
	if n := len(l.certificatesOf("M-1")); n != 1 {
		t.Fatalf("新证书应进入历史，得到 %d 张", n)
	}
	// 真正保存成功后才参与最近证书与使用资格判断。
	d, _ = l.CanUse("M-1")
	if !d.Allowed || d.Latest == nil || d.Latest.Number != "C-1" {
		t.Fatalf("保存成功后应解除无证书限制：allowed=%v latest=%v", d.Allowed, d.Latest)
	}
	reopened, _ = openAt(path, clock.now)
	if reopened.findCertificate("C-1") == nil || len(reopened.data.Certificates) != 1 {
		t.Fatal("保存成功的证书应已进入台账文件")
	}

	// 真实保存成功后，该编号与同日位置才被占用：同日另一编号现在按现有规则拒绝。
	if _, _, err := l.AddCertificate(sameDay); !IsValidation(err) ||
		!strings.Contains(err.Error(), "同一天") {
		t.Fatalf("成功录入后同日位置才应被占用，得到 %v", err)
	}
}

// TestFailedQualifiedCertificateCannotLiftRestriction 验证最近证书超差或到期
// 的器具，不会因一张保存失败的合格（且更近、未到期）证书而被解除限制。
func TestFailedQualifiedCertificateCannotLiftRestriction(t *testing.T) {
	cases := []struct {
		name   string
		bad    CertificateInput
		good   CertificateInput
		reason string
	}{
		{
			name:   "最近证书超差",
			bad:    CertificateInput{InstrumentID: "M-1", Number: "C-BAD", CalDate: "2026-09-01", Expiry: "2027-09-01", Method: "m", Error: 5, Summary: "s"},
			good:   CertificateInput{InstrumentID: "M-1", Number: "C-GOOD", CalDate: "2026-09-15", Expiry: "2027-09-15", Method: "m", Error: 0, Summary: "s"},
			reason: "超差",
		},
		{
			name:   "最近证书到期",
			bad:    CertificateInput{InstrumentID: "M-1", Number: "C-OLD", CalDate: "2025-01-01", Expiry: "2025-12-31", Method: "m", Error: 0, Summary: "s"},
			good:   CertificateInput{InstrumentID: "M-1", Number: "C-GOOD", CalDate: "2026-09-15", Expiry: "2027-09-15", Method: "m", Error: 0, Summary: "s"},
			reason: "到期",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{t: mustDate(t, "2026-10-02")}
			path := filepath.Join(t.TempDir(), "ledger.json")
			l, err := openAt(path, clock.now)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			mustRegister(t, l, "M-1", "万用表", 0.5)
			mustAddCert(t, l, tc.bad)
			_ = l.SetStatus("M-1", StatusInUse)

			before, _ := l.CanUse("M-1")
			if before.Allowed || !containsReason(before.Reasons, tc.reason) {
				t.Fatalf("前置条件失效：应因%s不能使用，得到 %v", tc.reason, before.Reasons)
			}

			blockSaving(t, l)
			if _, _, err := l.AddCertificate(tc.good); err == nil {
				t.Fatal("写盘失败应返回保存错误")
			}

			// 同一对象核对：限制不被解除，最近证书仍是旧的那张，历史不增加。
			d, _ := l.CanUse("M-1")
			if d.Allowed || !containsReason(d.Reasons, tc.reason) {
				t.Fatalf("失败的合格证书不得解除%s限制：allowed=%v reasons=%v",
					tc.reason, d.Allowed, d.Reasons)
			}
			r, _ := l.Review("M-1")
			if r.CanUse || r.Latest == nil || r.Latest.Number != tc.bad.Number {
				t.Fatalf("最近证书不应变化：canUse=%v latest=%v", r.CanUse, r.Latest)
			}
			if len(r.History) != 1 || r.History[0].Number != tc.bad.Number {
				t.Fatalf("失败证书不得进入历史：%+v", r.History)
			}
		})
	}
}
