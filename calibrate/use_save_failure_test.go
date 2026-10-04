package calibrate

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedUseRequestLeavesNoTrace 覆盖核心修复：使用申请在台账文件无法写入或
// 替换时明确报保存错误，不返回已提交的评估结果；本次申请的时间、允许结果与拒绝
// 原因不进入历史，也不会被此后其他成功保存的操作顺带写入。恢复可写后在同一台账
// 重新申请，只新增一条按当时情况判断的记录。
func TestFailedUseRequestLeavesNoTrace(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 先留一条成功保存的拒绝记录（待校准且无证书），作为此后对比的既有历史。
	first, err := l.RequestUse("M-1")
	if err != nil || first.Allowed {
		t.Fatalf("待校准无证书应拒绝并留痕，err=%v allowed=%v", err, first.Allowed)
	}
	wantFirst := append([]string(nil), first.Reasons...)

	// 让器具变为可用：在用 + 合格未到期证书。
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	})

	blockSaving(t, l)

	// 本可获准的申请遇到写盘失败：报保存错误，不返回已提交的评估结果。
	d, err := l.RequestUse("M-1")
	if err == nil {
		t.Fatal("台账文件无法替换时必须返回保存错误")
	}
	if d != nil {
		t.Fatalf("保存失败不能返回已提交成功的使用结果：%+v", d)
	}
	if IsValidation(err) || errors.Is(err, ErrNotFound) {
		t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
	}

	// 继续使用同一台账对象：全部使用记录与按器具核对中的被拒绝记录都只有
	// 此前成功保存的那一条，数量、内容与原因保持原样。
	recs := l.UsageRecords()
	if len(recs) != 1 || recs[0].Allowed || !sameReasons(recs[0].Reasons, wantFirst) {
		t.Fatalf("失败申请进入了全部使用记录：%+v", recs)
	}
	r, _ := l.Review("M-1")
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, wantFirst) {
		t.Fatalf("失败申请进入了核对视图的历史拒绝：%+v", r.Rejections)
	}
	// 器具登记信息与证书不因这次失败发生变化。
	if inst := l.findInstrument("M-1"); inst.Status != StatusInUse {
		t.Fatalf("失败申请不应改变器具状态：%s", inst.Status)
	}
	if latest := l.LatestCertificate("M-1"); latest == nil || latest.Number != "C-1" {
		t.Fatalf("失败申请不应改变证书：%+v", latest)
	}

	// 文件仍不可写时再次申请：仍报保存失败，历史不变。
	if _, err := l.RequestUse("M-1"); err == nil {
		t.Fatal("文件仍不可写时再次申请应仍报保存失败")
	}
	if n := len(l.UsageRecords()); n != 1 {
		t.Fatalf("再次失败申请后记录数应为 1，得到 %d", n)
	}

	// 恢复可写后先完成一次正常的状态切换：失败的申请不能被顺带写入文件。
	restoreSaving(t, l)
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("恢复后正常操作应能保存: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if recs := reopened.UsageRecords(); len(recs) != 1 ||
		recs[0].Allowed || !sameReasons(recs[0].Reasons, wantFirst) {
		t.Fatalf("失败申请被随后成功的写盘顺带写入文件：%+v", recs)
	}

	// 在同一台账重新申请：器具已停用，按新的实际情况判断并留痕，不能沿用
	// 失败那次（当时可获准）的结论；只新增这一条记录，时间取重新申请当时。
	clock.t = mustDate(t, "2026-10-03")
	d, err = l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("恢复后重新申请应能保存: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "停用") {
		t.Fatalf("重新申请应按当时状态拒绝：allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	recs = l.UsageRecords()
	if len(recs) != 2 {
		t.Fatalf("重新申请应只新增一条记录，得到 %d 条", len(recs))
	}
	if !sameReasons(recs[0].Reasons, wantFirst) ||
		!strings.HasPrefix(recs[0].RequestedAt, "2026-10-02") {
		t.Fatalf("既有记录被改写：%+v", recs[0])
	}
	second := recs[1]
	if second.Allowed || !containsReason(second.Reasons, "停用") ||
		!strings.HasPrefix(second.RequestedAt, "2026-10-03") {
		t.Fatalf("新记录应取重新申请当时的时间、结果与原因：%+v", second)
	}
}

// TestFailedRejectedUseRequestReportsSaveError 验证业务判断拒绝使用的申请
// （停用、缺少证书、最近证书超差或到期）同样只有保存成功才算提交完成：
// 即使已经得出拒绝原因，写盘失败仍报保存错误，且拒绝不进入历史。
func TestFailedRejectedUseRequestReportsSaveError(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, l *Ledger)
		reason  string
	}{
		{
			name: "停用",
			prepare: func(t *testing.T, l *Ledger) {
				mustAddCert(t, l, CertificateInput{
					InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
					Expiry: "2027-09-01", Method: "m", Error: 0, Summary: "s",
				})
				if err := l.SetStatus("M-1", StatusRetired); err != nil {
					t.Fatalf("set retired: %v", err)
				}
			},
			reason: "停用",
		},
		{
			name: "缺少证书",
			prepare: func(t *testing.T, l *Ledger) {
				if err := l.SetStatus("M-1", StatusInUse); err != nil {
					t.Fatalf("set in-use: %v", err)
				}
			},
			reason: "没有校准证书",
		},
		{
			name: "最近证书超差",
			prepare: func(t *testing.T, l *Ledger) {
				mustAddCert(t, l, CertificateInput{
					InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
					Expiry: "2027-09-01", Method: "m", Error: 5, Summary: "s",
				})
				if err := l.SetStatus("M-1", StatusInUse); err != nil {
					t.Fatalf("set in-use: %v", err)
				}
			},
			reason: "超差",
		},
		{
			name: "最近证书到期",
			prepare: func(t *testing.T, l *Ledger) {
				mustAddCert(t, l, CertificateInput{
					InstrumentID: "M-1", Number: "C-1", CalDate: "2025-01-01",
					Expiry: "2025-12-31", Method: "m", Error: 0, Summary: "s",
				})
				if err := l.SetStatus("M-1", StatusInUse); err != nil {
					t.Fatalf("set in-use: %v", err)
				}
			},
			reason: "到期",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{t: mustDate(t, "2026-10-02")}
			l := newTestLedger(t, clock)
			mustRegister(t, l, "M-1", "万用表", 0.5)
			tc.prepare(t, l)

			before, err := l.CanUse("M-1")
			if err != nil {
				t.Fatalf("can use: %v", err)
			}
			if before.Allowed || !containsReason(before.Reasons, tc.reason) {
				t.Fatalf("前置条件失效：应因%s不能使用，得到 %v", tc.reason, before.Reasons)
			}

			blockSaving(t, l)

			// 已得出拒绝原因，但记录保存失败：仍报保存错误而非留下拒绝记录。
			d, err := l.RequestUse("M-1")
			if err == nil {
				t.Fatal("写盘失败应返回保存错误")
			}
			if d != nil {
				t.Fatalf("保存失败不能返回评估结果：%+v", d)
			}
			if IsValidation(err) || errors.Is(err, ErrNotFound) {
				t.Fatalf("写盘失败应是保存错误而非业务拒绝：%v", err)
			}
			if n := len(l.data.Usage); n != 0 {
				t.Fatalf("失败的拒绝申请进入了内存台账：%d 条", n)
			}
			if recs := l.UsageRecords(); len(recs) != 0 {
				t.Fatalf("全部使用记录应仍为空：%+v", recs)
			}
			r, _ := l.Review("M-1")
			if len(r.Rejections) != 0 {
				t.Fatalf("核对中不应出现被拒绝记录：%+v", r.Rejections)
			}
		})
	}
}
