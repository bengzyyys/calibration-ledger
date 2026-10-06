package calibrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“录入证书”的同号判定补充自动化回归保障，守住一条业务事实：
//
//	同一台账中，同一个证书编号只能对应一份已经保存的证书。
//
// 必须同时保护两个方向的既有行为，任何一侧被放宽或收紧都算回归：
//
//  1. 同号且全部业务内容一致：成功返回原证书并标明重复，历史仍只有原来那
//     一张，录入时间保留第一次成功保存的值；后一次提交发生在更晚的时间也
//     不能据此生成新证书。器具编号、证书编号、日期、方法、摘要前后的空白
//     沿用现有去除规则；数字写法不同但测得误差是同一个数值，仍是同一份
//     业务内容。方法或摘要内部的文字变化不能借去除空白被当成相同内容。
//  2. 同号但任一字段不同：明确报告编号冲突（ConflictError），而不是成功、
//     普通输入无效或保存失败，且绝不能用新内容悄悄替换原证书。比较必须
//     覆盖所属器具、校准日期、有效期截止日、校准方法、测得误差和摘要；
//     正负误差即使绝对值相同、合格结论相同，数值不同仍是冲突。
//
// 此外还要守住冲突提交没有任何副作用：冲突后按器具核对看到的原证书全部
// 内容、历史数量与最近证书选择都与提交前一致；把同号证书挂到另一件器具
// 时，目标器具不能多出这张证书；原证书超差时把误差改成合格值重交、原
// 证书已到期时把截止日改成未来重交，都不能解除或恢复使用资格。

// sameNumberBaseInput 是同号判定回归的标准证书内容：校准日期早于假时钟的
// 本机今天，截止日晚于校准日期，误差有限，方法与摘要非空。
func sameNumberBaseInput() CertificateInput {
	return CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	}
}

// setupSameNumberLedger 准备一件已登记器具 M-1（允许误差 0.5）和另一件
// 已登记器具 M-2（用于“换到另一件器具”的合法变化），时钟拨到给定时刻。
func setupSameNumberLedger(t *testing.T, now time.Time) (*fakeClock, *Ledger) {
	t.Helper()
	clock := &fakeClock{t: now}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	return clock, l
}

// assertStoredCertIntact 核对冲突提交后台账中仍只有原证书这一张，且全部
// 内容（含录入时间）与首次保存时完全一致，所属器具的历史数量与最近证书
// 选择也不变。
func assertStoredCertIntact(t *testing.T, l *Ledger, want Certificate) {
	t.Helper()
	stored := l.findCertificate(want.Number)
	if stored == nil {
		t.Fatalf("冲突提交后证书 %s 消失", want.Number)
	}
	if *stored != want {
		t.Fatalf("冲突提交不得改动原证书：保存=%+v 期望=%+v", *stored, want)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("全台账应仍只有 1 张证书，得到 %d 张：%+v", n, l.data.Certificates)
	}
	if n := len(l.certificatesOf(want.InstrumentID)); n != 1 {
		t.Fatalf("原器具 %s 的历史应仍只有 1 张证书，得到 %d 张",
			want.InstrumentID, n)
	}
	latest := l.LatestCertificate(want.InstrumentID)
	if latest == nil || *latest != want {
		t.Fatalf("最近证书应仍是原证书 %s：%+v", want.Number, latest)
	}
}

// TestSameNumberSameContentReturnsOriginalAndKeepsFirstSaveTime 覆盖幂等主
// 场景：首次录入成功后，把时钟拨到另一个更晚的日历日期再提交相同编号与
// 相同业务内容，必须成功返回原证书、标明重复，不产生新证书、不刷新录入
// 时间。
func TestSameNumberSameContentReturnsOriginalAndKeepsFirstSaveTime(t *testing.T) {
	firstClock := mustDate(t, "2026-09-01").Add(9 * time.Hour)
	clock, l := setupSameNumberLedger(t, firstClock)

	in := sameNumberBaseInput()
	first, dup, err := l.AddCertificate(in)
	if err != nil || dup || first == nil {
		t.Fatalf("首次录入应作为新证书成功：err=%v dup=%v cert=%v", err, dup, first)
	}
	if !strings.HasPrefix(first.CreatedAt, "2026-09-01T09:00") {
		t.Fatalf("首次录入时间应取第一次成功保存的时刻，得到 %s", first.CreatedAt)
	}

	// 后一次提交发生在不同时间（晚了一个多月）：内容相同只能返回原证书。
	clock.t = mustDate(t, "2026-10-02").Add(15*time.Hour + 30*time.Minute)
	again, isDup, err := l.AddCertificate(in)
	if err != nil || !isDup || again == nil {
		t.Fatalf("更晚时刻同号同内容应成功并标明重复：err=%v dup=%v cert=%v",
			err, isDup, again)
	}
	if *again != *first {
		t.Fatalf("重复提交应返回原证书全部内容：首次 %+v，再次 %+v", *first, *again)
	}

	// 历史仍只有原来那一张；录入时间保留第一次成功保存的值。
	assertStoredCertIntact(t, l, *first)
	stored := l.findCertificate("C-1")
	if stored.CreatedAt != first.CreatedAt {
		t.Fatalf("录入时间必须保留首次保存值 %s，得到 %s",
			first.CreatedAt, stored.CreatedAt)
	}
	if strings.HasPrefix(stored.CreatedAt, "2026-10-02") {
		t.Fatalf("更晚的重复提交不能刷新录入时间，得到 %s", stored.CreatedAt)
	}
}

// TestSameNumberTrimsSurroundingWhitespaceButKeepsInternalText 保护“相同
// 内容”的归一化边界：器具编号、证书编号、日期、方法、摘要前后的空白沿用
// 现有 TrimSpace 规则后参与比较，测得误差只看数值（1e-1 与 0.1 是同一个
// float64）；但方法、摘要内部发生的文字（空白）变化不能被去除空白抹平，
// 必须报冲突。
func TestSameNumberTrimsSurroundingWhitespaceButKeepsInternalText(t *testing.T) {
	_, l := setupSameNumberLedger(t, mustDate(t, "2026-09-01"))
	in := sameNumberBaseInput()
	first, _, err := l.AddCertificate(in)
	if err != nil {
		t.Fatalf("首次录入失败: %v", err)
	}

	// 所有字段前后加空白（含制表符、换行），误差换一种数字写法：归一化后
	// 与首次内容完全一致，必须幂等返回原证书。
	padded := CertificateInput{
		InstrumentID: "  M-1  ", Number: "\tC-1\t", CalDate: " 2026-09-01 ",
		Expiry: "  2027-09-01  ", Method: "  规范A  ", Error: 1e-1, Summary: "摘要\n",
	}
	again, dup, err := l.AddCertificate(padded)
	if err != nil || !dup {
		t.Fatalf("前后空白被去除后内容一致应返回原证书：err=%v dup=%v", err, dup)
	}
	if *again != *first {
		t.Fatalf("空白归一化后应返回原证书：首次 %+v，再次 %+v", *first, *again)
	}
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("幂等提交不应增加证书，得到 %d 张", n)
	}

	// 方法内部插入空白：是不同的方法文字，不能借去除空白当成相同内容。
	internalMethod := in
	internalMethod.Method = "规范 A"
	if _, _, err := l.AddCertificate(internalMethod); !IsConflict(err) {
		t.Fatalf("方法内部文字变化应报编号冲突，得到 %v", err)
	}
	if got := l.findCertificate("C-1"); got.Method != "规范A" {
		t.Fatalf("冲突后原方法被改动：%s", got.Method)
	}

	// 摘要内部插入空白同理：是不同的摘要内容。
	internalSummary := in
	internalSummary.Summary = "摘 要"
	if _, _, err := l.AddCertificate(internalSummary); !IsConflict(err) {
		t.Fatalf("摘要内部文字变化应报编号冲突，得到 %v", err)
	}
	if got := l.findCertificate("C-1"); got.Summary != "摘要" {
		t.Fatalf("冲突后原摘要被改动：%s", got.Summary)
	}
	assertStoredCertIntact(t, l, *first)
}

// TestSameNumberEachFieldChangeAloneIsConflictNotRejection 逐字段保护比较
// 范围：所属器具、校准日期、有效期截止日、校准方法、测得误差、摘要中的
// 任意一项变化（每项都换成本身合法的内容），都必须明确报告编号冲突，而
// 不是成功或普通输入无效；原证书全部内容、历史数量与最近证书不变，挂到
// 另一件器具时目标器具也不能多出这张同号证书。
func TestSameNumberEachFieldChangeAloneIsConflictNotRejection(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*CertificateInput)
	}{
		{"所属器具换成另一件已登记器具", func(c *CertificateInput) { c.InstrumentID = "M-2" }},
		{"校准日期换成真实且不晚于今天的日期", func(c *CertificateInput) { c.CalDate = "2026-08-15" }},
		{"有效期截止日换成更晚的真实日期", func(c *CertificateInput) { c.Expiry = "2028-09-01" }},
		{"校准方法换成另一个非空方法", func(c *CertificateInput) { c.Method = "规范B" }},
		{"测得误差换成另一个有限数值", func(c *CertificateInput) { c.Error = 0.2 }},
		{"摘要换成另一段非空摘要", func(c *CertificateInput) { c.Summary = "另一份摘要" }},
		{"方法内部空白变化", func(c *CertificateInput) { c.Method = "规范 A" }},
		{"摘要内部空白变化", func(c *CertificateInput) { c.Summary = "摘 要" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, l := setupSameNumberLedger(t, mustDate(t, "2026-10-02"))
			base := sameNumberBaseInput()
			first, _, err := l.AddCertificate(base)
			if err != nil {
				t.Fatalf("首次录入失败: %v", err)
			}

			changed := base
			tc.mut(&changed)
			cert, dup, err := l.AddCertificate(changed)
			if err == nil {
				t.Fatalf("单字段变化必须被拒绝，却返回了证书 %+v（dup=%v）", cert, dup)
			}
			if !IsConflict(err) {
				t.Fatalf("合法的单字段变化应报编号冲突，而不是普通输入无效或其他错误：%v", err)
			}
			if IsValidation(err) {
				t.Fatalf("编号冲突不能被降级成普通输入无效：%v", err)
			}
			if cert != nil || dup {
				t.Fatalf("冲突时不得返回成功或重复证书：cert=%v dup=%v", cert, dup)
			}
			if !strings.Contains(err.Error(), "C-1") ||
				!strings.Contains(err.Error(), "内容不同") {
				t.Fatalf("冲突信息应指出编号与内容不同，得到 %q", err.Error())
			}

			// 原证书全部内容、历史数量与最近证书保持提交前一致。
			assertStoredCertIntact(t, l, *first)
			// 同号证书不能出现在目标器具 M-2 名下（即使本次提交挂到了 M-2）。
			if n := len(l.certificatesOf("M-2")); n != 0 {
				t.Fatalf("目标器具 M-2 不能多出这张同号证书，得到 %d 张", n)
			}
			if latest := l.LatestCertificate("M-2"); latest != nil {
				t.Fatalf("目标器具 M-2 不应出现最近证书：%+v", latest)
			}
		})
	}
}

// TestSameNumberRepeatedConflictsNeverAccumulate 模拟调用方拿着同号内容
// 连续尝试多种合法变化：每一次都报冲突，任何一次都不能把新内容写进台账，
// 多轮冲突之后全台账仍只有首次保存的那一张。
func TestSameNumberRepeatedConflictsNeverAccumulate(t *testing.T) {
	_, l := setupSameNumberLedger(t, mustDate(t, "2026-10-02"))
	base := sameNumberBaseInput()
	first, _, err := l.AddCertificate(base)
	if err != nil {
		t.Fatalf("首次录入失败: %v", err)
	}

	mutations := []func(*CertificateInput){
		func(c *CertificateInput) { c.InstrumentID = "M-2" },
		func(c *CertificateInput) { c.CalDate = "2026-08-15" },
		func(c *CertificateInput) { c.Expiry = "2028-09-01" },
		func(c *CertificateInput) { c.Method = "规范B" },
		func(c *CertificateInput) { c.Error = 0.2 },
		func(c *CertificateInput) { c.Summary = "另一份摘要" },
	}
	for i, mut := range mutations {
		changed := base
		mut(&changed)
		if _, _, err := l.AddCertificate(changed); !IsConflict(err) {
			t.Fatalf("第 %d 次变化应报冲突，得到 %v", i+1, err)
		}
	}
	assertStoredCertIntact(t, l, *first)
	if n := len(l.certificatesOf("M-2")); n != 0 {
		t.Fatalf("多轮冲突后目标器具 M-2 仍不应有证书，得到 %d 张", n)
	}
}

// TestSameNumberSignedErrorDifferenceIsConflictEvenWhenVerdictSame 专门保护
// 测得误差的符号：原证书误差 -0.4（在允许误差 0.5 内，合格），重交 +0.4，
// 两者绝对值相同、合格结论也相同，但数值不同，仍属于内容冲突，原误差的
// 符号必须保留；只有逐字节相同的 -0.4 重交才是重复提交。
func TestSameNumberSignedErrorDifferenceIsConflictEvenWhenVerdictSame(t *testing.T) {
	_, l := setupSameNumberLedger(t, mustDate(t, "2026-10-02"))
	in := sameNumberBaseInput()
	in.Error = -0.4
	first, _, err := l.AddCertificate(in)
	if err != nil {
		t.Fatalf("首次录入失败: %v", err)
	}
	if !first.Pass(0.5) {
		t.Fatalf("前置条件失效：-0.4 应在允许误差 0.5 内判合格")
	}

	// 绝对值相同、结论相同的反号误差：仍是冲突，不能替换原证书。
	flipped := in
	flipped.Error = 0.4
	cert, dup, err := l.AddCertificate(flipped)
	if !IsConflict(err) || cert != nil || dup {
		t.Fatalf("反号误差应报冲突且不返回证书：err=%v cert=%v dup=%v", err, cert, dup)
	}
	if got := l.findCertificate("C-1"); got.Error != -0.4 {
		t.Fatalf("原误差符号必须保留为 -0.4，得到 %g", got.Error)
	}

	// 控制对照：逐字节相同的 -0.4 重交仍是幂等重复。
	again, isDup, err := l.AddCertificate(in)
	if err != nil || !isDup || again.Error != -0.4 {
		t.Fatalf("相同数值重交应幂等返回原证书：err=%v dup=%v cert=%+v", err, isDup, again)
	}
	assertStoredCertIntact(t, l, *first)
}

// TestSameNumberConflictUntouchedAfterReopen 保护“冲突不写盘”跨进程成立：
// 首次证书保存进文件后，连续发起多次同号内容不同的提交（全部被冲突拒绝），
// 用同一时刻重新打开台账文件，原证书全部内容、历史数量与最近证书选择仍与
// 首次保存一致，目标器具也没有多出证书。
func TestSameNumberConflictUntouchedAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)
	base := sameNumberBaseInput()
	first, _, err := l.AddCertificate(base)
	if err != nil {
		t.Fatalf("首次录入失败: %v", err)
	}

	mutated := []CertificateInput{
		func() CertificateInput { c := base; c.InstrumentID = "M-2"; return c }(),
		func() CertificateInput { c := base; c.CalDate = "2026-08-15"; return c }(),
		func() CertificateInput { c := base; c.Expiry = "2028-09-01"; return c }(),
		func() CertificateInput { c := base; c.Method = "规范B"; return c }(),
		func() CertificateInput { c := base; c.Error = 0.2; return c }(),
		func() CertificateInput { c := base; c.Summary = "另一份摘要"; return c }(),
	}
	for i, in := range mutated {
		if _, _, err := l.AddCertificate(in); !IsConflict(err) {
			t.Fatalf("第 %d 次变化应报冲突，得到 %v", i+1, err)
		}
	}

	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.data.Certificates); n != 1 {
		t.Fatalf("重开后应仍只有 1 张证书，得到 %d 张：%+v", n, reopened.data.Certificates)
	}
	stored := reopened.findCertificate("C-1")
	if stored == nil || *stored != *first {
		t.Fatalf("重开后原证书内容被改动：stored=%+v want=%+v", stored, first)
	}
	if n := len(reopened.certificatesOf("M-1")); n != 1 {
		t.Fatalf("重开后原器具历史应仍为 1 张，得到 %d", n)
	}
	if n := len(reopened.certificatesOf("M-2")); n != 0 {
		t.Fatalf("重开后目标器具 M-2 不应出现同号证书，得到 %d 张", n)
	}
	latest := reopened.LatestCertificate("M-1")
	if latest == nil || latest.Number != "C-1" {
		t.Fatalf("重开后最近证书应仍是 C-1：%+v", latest)
	}
}

// TestSameNumberConflictCannotLiftFailOrExpiryRestriction 保护冲突提交不能
// 改变使用资格：原证书超差时，把同号证书的误差改成合格值重交不能解除超差
// 限制；原证书已到期时，把截止日改成未来日期重交不能恢复使用资格。两次
// 重交内容本身都合法，结果只能是编号冲突。
func TestSameNumberConflictCannotLiftFailOrExpiryRestriction(t *testing.T) {
	t.Run("超差证书改成合格值重交不能解除限制", func(t *testing.T) {
		_, l := setupSameNumberLedger(t, mustDate(t, "2026-10-02"))
		if err := l.SetStatus("M-1", StatusInUse); err != nil {
			t.Fatalf("切换在用: %v", err)
		}
		bad := sameNumberBaseInput()
		bad.Error = 0.8 // 绝对值超过 0.5：超差，但截止日仍在未来。
		first, _, err := l.AddCertificate(bad)
		if err != nil {
			t.Fatalf("超差证书录入失败: %v", err)
		}
		before, _ := l.CanUse("M-1")
		if before.Allowed || !containsReason(before.Reasons, "超差") {
			t.Fatalf("前置条件失效：应因超差被限制，得到 %v", before.Reasons)
		}

		// 用同号、合格误差重交：内容本身合法，只能报冲突。
		good := bad
		good.Error = 0.1
		cert, dup, err := l.AddCertificate(good)
		if !IsConflict(err) || cert != nil || dup {
			t.Fatalf("合格值重交超差证书应报冲突：err=%v cert=%v dup=%v", err, cert, dup)
		}

		// 限制不被解除：最近证书仍是误差 0.8 的原证书，申请使用仍只因超差被拒。
		after, _ := l.CanUse("M-1")
		if after.Allowed || !containsReason(after.Reasons, "超差") {
			t.Fatalf("冲突重交不能解除超差限制：allowed=%v reasons=%v",
				after.Allowed, after.Reasons)
		}
		if containsReason(after.Reasons, "到期") {
			t.Fatalf("未来截止日的证书不应混入到期原因：%v", after.Reasons)
		}
		rec, err := l.RequestUse("M-1")
		if err != nil || rec.Allowed || !containsReason(rec.Reasons, "超差") {
			t.Fatalf("正式申请仍应因原超差证书被拒：err=%v rec=%+v", err, rec)
		}
		assertStoredCertIntact(t, l, *first)
	})

	t.Run("到期证书改成未来截止日重交不能恢复资格", func(t *testing.T) {
		_, l := setupSameNumberLedger(t, mustDate(t, "2026-10-02"))
		if err := l.SetStatus("M-1", StatusInUse); err != nil {
			t.Fatalf("切换在用: %v", err)
		}
		expired := sameNumberBaseInput()
		expired.CalDate = "2025-01-01"
		expired.Expiry = "2025-12-31" // 早于本机今天 2026-10-02，已到期。
		expired.Error = 0.1           // 误差合格：限制只应来自到期。
		first, _, err := l.AddCertificate(expired)
		if err != nil {
			t.Fatalf("到期证书录入失败: %v", err)
		}
		before, _ := l.CanUse("M-1")
		if before.Allowed || !containsReason(before.Reasons, "到期") {
			t.Fatalf("前置条件失效：应因到期被限制，得到 %v", before.Reasons)
		}

		// 用同号、未来截止日（仍晚于校准日期，本身合法）重交：只能报冲突。
		updated := expired
		updated.Expiry = "2028-01-01"
		cert, dup, err := l.AddCertificate(updated)
		if !IsConflict(err) || cert != nil || dup {
			t.Fatalf("未来截止日重交到期证书应报冲突：err=%v cert=%v dup=%v", err, cert, dup)
		}

		// 使用资格不恢复：最近证书仍是已到期的原证书，申请使用仍只因到期被拒。
		after, _ := l.CanUse("M-1")
		if after.Allowed || !containsReason(after.Reasons, "到期") {
			t.Fatalf("冲突重交不能恢复使用资格：allowed=%v reasons=%v",
				after.Allowed, after.Reasons)
		}
		if containsReason(after.Reasons, "超差") {
			t.Fatalf("合格证书不应混入超差原因：%v", after.Reasons)
		}
		if after.Latest == nil || after.Latest.Expiry != "2025-12-31" || !after.Latest.Pass {
			t.Fatalf("最近证书应仍是合格但已到期的原证书：%+v", after.Latest)
		}
		rec, err := l.RequestUse("M-1")
		if err != nil || rec.Allowed || !containsReason(rec.Reasons, "到期") {
			t.Fatalf("正式申请仍应因原证书到期被拒：err=%v rec=%+v", err, rec)
		}
		assertStoredCertIntact(t, l, *first)
	})
}
