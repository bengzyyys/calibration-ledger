package calibrate

import (
	"testing"
)

// 本文件为“同一证书编号只对应一份已经保存的业务事实”补充回归保障：
//
//   - 同号同内容（含前后空白、不同时间提交）返回原证书，历史不增加，
//     录入时间保留第一次成功保存的值；
//   - 同号不同内容（覆盖所属器具、校准日期、截止日、方法、误差、摘要
//     任一项，含仅改一个字段、方法/摘要内部文字变化、正负号不同的误差）
//     明确报编号冲突，且不改动已有数据、不解除既有使用限制。
//
// 各项变化都使用本身合法的内容，必须报冲突而不是成功或普通输入无效。

// TestDuplicateResubmitLaterKeepsOriginalCert 首次成功录入后，在不同时间
// 再次提交相同编号和相同业务内容：返回原证书并标记重复，历史仍只有一张，
// 录入时间保留第一次成功保存的值，不能因提交时间不同而生成新证书。
func TestDuplicateResubmitLaterKeepsOriginalCert(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-01")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	}
	first, dup, err := l.AddCertificate(in)
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}

	// 次日再次提交相同内容：仍返回第一次保存的那张证书。
	clock.t = mustDate(t, "2026-10-02")
	again, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("不同时间的同号同内容提交应成功并标记重复，err=%v dup=%v", err, dup)
	}
	if *again != *first {
		t.Fatalf("重复提交应返回原证书：首次 %+v，再次 %+v", *first, *again)
	}
	if again.CreatedAt != first.CreatedAt {
		t.Fatalf("录入时间应保留第一次成功保存的值 %s，得到 %s",
			first.CreatedAt, again.CreatedAt)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("不同时间的重复提交不应生成新证书，共 %d 张", n)
	}

	// 核对视图同样只有原来那一张，录入时间不变。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.History) != 1 || r.Latest == nil ||
		r.Latest.Number != "C-1" || r.Latest.CreatedAt != first.CreatedAt {
		t.Fatalf("核对中应只有第一次保存的证书：历史=%d 最近=%+v",
			len(r.History), r.Latest)
	}

	// 从文件重开也只有一张，录入时间仍是第一次保存的值。
	reopened, err := openAt(l.Path(), clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.data.Certificates); n != 1 {
		t.Fatalf("重开后文件中应只有一张证书，共 %d 张", n)
	}
	if got := reopened.findCertificate("C-1"); got == nil || got.CreatedAt != first.CreatedAt {
		t.Fatalf("重开后录入时间被改写：%+v", got)
	}
}

// TestDuplicateMatchingTrimsOuterWhitespace 器具编号、证书编号、日期、方法
// 和摘要前后的空白沿用现有去除规则：裁剪后内容一致的同号提交是重复提交，
// 返回裁剪后保存的原证书，不增加历史。
func TestDuplicateMatchingTrimsOuterWhitespace(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 首次录入即带前后空白：保存的是裁剪后的内容。
	first, dup, err := l.AddCertificate(CertificateInput{
		InstrumentID: "  M-1  ", Number: "\tC-1 ", CalDate: " 2026-09-01",
		Expiry: "2027-09-01 ", Method: " 规范A ", Error: 0.1, Summary: " 例行校准\t",
	})
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}
	if first.InstrumentID != "M-1" || first.Number != "C-1" ||
		first.CalDate != "2026-09-01" || first.Expiry != "2027-09-01" ||
		first.Method != "规范A" || first.Summary != "例行校准" {
		t.Fatalf("保存内容应已去除前后空白：%+v", first)
	}

	// 再次提交：各字段前后空白不同，裁剪后内容一致，应判为重复。
	again, dup, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01 ",
		Expiry: " 2027-09-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	})
	if err != nil || !dup {
		t.Fatalf("裁剪后内容一致的同号提交应标记重复，err=%v dup=%v", err, dup)
	}
	if *again != *first {
		t.Fatalf("重复提交应返回裁剪后保存的原证书：首次 %+v，再次 %+v", *first, *again)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("空白差异不应产生新证书，共 %d 张", n)
	}
}

// TestConflictReportedForEachBusinessField 同号内容的比较必须覆盖所属器具、
// 校准日期、有效期截止日、校准方法、测得误差和摘要：只改其中任一项（各项
// 新内容本身都合法）都必须明确报编号冲突，而不是成功或普通输入无效；
// 冲突后原证书全部内容、历史数量和最近证书选择与提交前一致。
func TestConflictReportedForEachBusinessField(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	base := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	}
	saved, dup, err := l.AddCertificate(base)
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}

	cases := []struct {
		name string
		mut  func(*CertificateInput)
	}{
		// 换到另一件已经登记的器具。
		{"所属器具", func(c *CertificateInput) { c.InstrumentID = "M-2" }},
		// 换成真实且不晚于今天的校准日期。
		{"校准日期", func(c *CertificateInput) { c.CalDate = "2026-09-02" }},
		// 换成真实且晚于校准日期的截止日。
		{"有效期截止日", func(c *CertificateInput) { c.Expiry = "2027-10-01" }},
		// 换成非空方法。
		{"校准方法", func(c *CertificateInput) { c.Method = "规范B" }},
		// 换成有限误差。
		{"测得误差", func(c *CertificateInput) { c.Error = -0.2 }},
		// 换成非空摘要。
		{"摘要", func(c *CertificateInput) { c.Summary = "周期校准" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, _, err := l.AddCertificate(in); !IsConflict(err) {
				t.Fatalf("只改%s应明确报编号冲突，得到 %v", tc.name, err)
			} else if IsValidation(err) {
				t.Fatalf("只改%s的合法内容不应报成普通输入无效：%v", tc.name, err)
			}
		})
	}

	// 全部冲突之后：原证书内容逐项不变，历史数量与最近证书选择不变。
	got := l.findCertificate("C-1")
	if got == nil || *got != *saved {
		t.Fatalf("冲突后原证书被改动：保存时 %+v，现在 %+v", *saved, got)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("冲突提交不应产生新证书，共 %d 张", n)
	}
	if latest := l.LatestCertificate("M-1"); latest == nil || *latest != *saved {
		t.Fatalf("最近证书选择被冲突提交改变：%+v", latest)
	}
	// 目标器具不能多出这张同号证书。
	if n := len(l.certificatesOf("M-2")); n != 0 {
		t.Fatalf("冲突提交不应把同号证书挂到 M-2，该器具有 %d 张证书", n)
	}
}

// TestConflictOnInternalTextChange 方法或摘要内部的文字发生变化（含内部
// 空白差异），不能借去除前后空白把它当成相同内容：必须报冲突且不改动
// 已有数据。
func TestConflictOnInternalTextChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	base := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
	}
	mustAddCert(t, l, base)

	cases := []struct {
		name string
		mut  func(*CertificateInput)
	}{
		{"方法内部加空格", func(c *CertificateInput) { c.Method = "规范 A" }},
		{"方法内部多空格", func(c *CertificateInput) { c.Method = "规范  A" }},
		{"摘要内部加空格", func(c *CertificateInput) { c.Summary = "例行 校准" }},
		{"摘要文字变化", func(c *CertificateInput) { c.Summary = "例行校准。" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mut(&in)
			if _, _, err := l.AddCertificate(in); !IsConflict(err) {
				t.Fatalf("%s应报编号冲突，得到 %v", tc.name, err)
			}
		})
	}
	got := l.findCertificate("C-1")
	if got.Method != "规范A" || got.Summary != "例行校准" {
		t.Fatalf("内部文字冲突后原证书被改动：%+v", got)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("内部文字冲突不应产生新证书，共 %d 张", n)
	}
}

// TestConflictSignedErrorSameMagnitude 正负误差即使绝对值相同、合格结论
// 相同，数值不同也属于内容冲突，不能借结论一致把它当成重复提交。
func TestConflictSignedErrorSameMagnitude(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	base := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: -0.4, Summary: "例行校准",
	}
	saved, dup, err := l.AddCertificate(base)
	if err != nil || dup {
		t.Fatalf("首次录入应成功且非重复，err=%v dup=%v", err, dup)
	}
	if !saved.Pass(0.5) {
		t.Fatal("用例前提：-0.4 在允许误差 0.5 下应合格")
	}

	// +0.4 同样合格，但数值不同：必须报冲突。
	flip := base
	flip.Error = 0.4
	if _, _, err := l.AddCertificate(flip); !IsConflict(err) {
		t.Fatalf("绝对值相同、符号不同的误差应报冲突，得到 %v", err)
	}
	if got := l.findCertificate("C-1"); got.Error != -0.4 {
		t.Fatalf("冲突后原证书误差被改写：%g", got.Error)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("符号冲突不应产生新证书，共 %d 张", n)
	}
}

// TestConflictCannotLiftUsageRestriction 冲突提交不能成为改写原证书、
// 解除使用限制的后门：原证书超差时把误差改成合格值重交仍被拒绝且限制
// 不变；原证书已到期时把截止日改到未来重交仍被拒绝且资格不恢复。
func TestConflictCannotLiftUsageRestriction(t *testing.T) {
	t.Run("超差原证书", func(t *testing.T) {
		clock := &fakeClock{t: mustDate(t, "2026-10-02")}
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		if err := l.SetStatus("M-1", StatusInUse); err != nil {
			t.Fatalf("set in-use: %v", err)
		}
		bad := CertificateInput{
			InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
			Expiry: "2027-09-01", Method: "规范A", Error: 0.8, Summary: "超差批次",
		}
		mustAddCert(t, l, bad)
		if d, err := l.CanUse("M-1"); err != nil || d.Allowed ||
			!containsReason(d.Reasons, "超差") {
			t.Fatalf("前提：超差器具应不可用且原因含超差，err=%v allowed=%v reasons=%v",
				err, d.Allowed, d.Reasons)
		}

		// 把误差改成合格值重交同号证书：报冲突，不解除限制。
		fix := bad
		fix.Error = 0.1
		if _, _, err := l.AddCertificate(fix); !IsConflict(err) {
			t.Fatalf("改成合格误差重交应报冲突，得到 %v", err)
		}
		d, err := l.CanUse("M-1")
		if err != nil || d.Allowed || !containsReason(d.Reasons, "超差") {
			t.Fatalf("冲突重交不应解除超差限制，err=%v allowed=%v reasons=%v",
				err, d.Allowed, d.Reasons)
		}
		r, err := l.Review("M-1")
		if err != nil {
			t.Fatalf("review: %v", err)
		}
		if len(r.History) != 1 || r.Latest == nil || r.Latest.Error != 0.8 || r.Latest.Pass {
			t.Fatalf("原超差证书不应被改写：历史=%d 最近=%+v", len(r.History), r.Latest)
		}
	})

	t.Run("已到期原证书", func(t *testing.T) {
		clock := &fakeClock{t: mustDate(t, "2026-10-02")}
		l := newTestLedger(t, clock)
		mustRegister(t, l, "M-1", "万用表", 0.5)
		if err := l.SetStatus("M-1", StatusInUse); err != nil {
			t.Fatalf("set in-use: %v", err)
		}
		expired := CertificateInput{
			InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
			Expiry: "2026-10-01", Method: "规范A", Error: 0.1, Summary: "例行校准",
		}
		mustAddCert(t, l, expired)
		if d, err := l.CanUse("M-1"); err != nil || d.Allowed ||
			!containsReason(d.Reasons, "到期") {
			t.Fatalf("前提：证书已到期应不可用且原因含到期，err=%v allowed=%v reasons=%v",
				err, d.Allowed, d.Reasons)
		}

		// 把截止日改到未来重交同号证书：报冲突，不恢复使用资格。
		renew := expired
		renew.Expiry = "2027-09-01"
		if _, _, err := l.AddCertificate(renew); !IsConflict(err) {
			t.Fatalf("改成未来截止日重交应报冲突，得到 %v", err)
		}
		d, err := l.CanUse("M-1")
		if err != nil || d.Allowed || !containsReason(d.Reasons, "到期") {
			t.Fatalf("冲突重交不应恢复使用资格，err=%v allowed=%v reasons=%v",
				err, d.Allowed, d.Reasons)
		}
		r, err := l.Review("M-1")
		if err != nil {
			t.Fatalf("review: %v", err)
		}
		if len(r.History) != 1 || r.Latest == nil ||
			r.Latest.Expiry != "2026-10-01" || !r.Latest.Expired {
			t.Fatalf("原到期证书不应被改写：历史=%d 最近=%+v", len(r.History), r.Latest)
		}
	})
}
