package calibrate

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClock 是可手动拨动的本机时钟。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestLedger(t *testing.T, clock *fakeClock) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	return l
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(DateLayout, s)
	if err != nil {
		t.Fatalf("bad test date %s: %v", s, err)
	}
	return d
}

func TestRegisterValidation(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)

	cases := []struct {
		name string
		in   RegisterInput
	}{
		{"空白编号", RegisterInput{ID: "   ", Name: "万用表", AllowedError: 0.1}},
		{"空白名称", RegisterInput{ID: "M-1", Name: "\t", AllowedError: 0.1}},
		{"负误差", RegisterInput{ID: "M-1", Name: "万用表", AllowedError: -0.01}},
		{"NaN误差", RegisterInput{ID: "M-1", Name: "万用表", AllowedError: math.NaN()}},
		{"正无穷误差", RegisterInput{ID: "M-1", Name: "万用表", AllowedError: math.Inf(1)}},
		{"负零可以", RegisterInput{ID: "M-ok", Name: "零限", AllowedError: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.name, "可以") {
				if _, err := l.Register(tc.in); err != nil {
					t.Fatalf("零误差应被接受: %v", err)
				}
				return
			}
			if _, err := l.Register(tc.in); !IsValidation(err) {
				t.Fatalf("应返回校验错误，得到 %v", err)
			}
		})
	}

	// 新器具处于待校准状态。
	inst, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if inst.Status != StatusPending {
		t.Fatalf("新器具状态应为待校准，得到 %s", inst.Status)
	}

	// 编号重复明确拒绝，原记录不变；名称两侧空白被裁剪。
	before := len(l.Instruments())
	_, err = l.Register(RegisterInput{ID: "  M-1  ", Name: "另一个", AllowedError: 1})
	if !IsValidation(err) || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("重复编号应报校验错误，得到 %v", err)
	}
	if len(l.Instruments()) != before {
		t.Fatal("拒绝重复编号后器具数量变化")
	}
	if got := l.findInstrument("M-1"); got.Name != "万用表" {
		t.Fatalf("原记录被改动：名称=%s", got.Name)
	}

	// 无效登记不留记录：前面 4 个失败用例 + 成功 2 件。
	if len(l.Instruments()) != 2 {
		t.Fatalf("无效录入留下了记录，共 %d 件", len(l.Instruments()))
	}
}

func TestStatusTransitions(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	if l.findInstrument("M-1").Status != StatusInUse {
		t.Fatal("未切换为在用")
	}
	// 切换为在用本身不代表已经校准：没有证书时使用仍被拒绝。
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "没有校准证书") {
		t.Fatalf("在用但无证书应拒绝，得到 allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	// 停用不删除证书和使用记录：先补证书，再停用，再核对历史仍在。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "合格批次",
	})
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("retire: %v", err)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.History) != 1 || len(r.Rejections) != 1 {
		t.Fatalf("停用后证书或使用记录被删除：历史=%d 拒绝=%d", len(r.History), len(r.Rejections))
	}
	if err := l.SetStatus("missing", StatusInUse); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知器具应报 ErrNotFound，得到 %v", err)
	}
}

func TestCertificateValidation(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	base := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	}
	valid := func(mut func(*CertificateInput)) CertificateInput {
		c := base
		mut(&c)
		return c
	}

	bad := []struct {
		name string
		in   CertificateInput
	}{
		{"未知器具", valid(func(c *CertificateInput) { c.InstrumentID = "X-9" })},
		{"空白证书编号", valid(func(c *CertificateInput) { c.Number = "  " })},
		{"空白方法", valid(func(c *CertificateInput) { c.Method = "\t" })},
		{"空白摘要", valid(func(c *CertificateInput) { c.Summary = "" })},
		{"不存在的日期2月30日", valid(func(c *CertificateInput) { c.CalDate = "2026-02-30" })},
		{"不存在的月份13月", valid(func(c *CertificateInput) { c.CalDate = "2026-13-01" })},
		{"格式不符", valid(func(c *CertificateInput) { c.CalDate = "2026-9-1" })},
		{"校准日期晚于今天", valid(func(c *CertificateInput) {
			c.CalDate = "2026-10-03"
			c.Expiry = "2027-10-03"
		})},
		{"截止日等于校准日", valid(func(c *CertificateInput) { c.Expiry = "2026-09-01" })},
		{"截止日早于校准日", valid(func(c *CertificateInput) { c.Expiry = "2026-08-31" })},
		{"NaN测得误差", valid(func(c *CertificateInput) { c.Error = math.NaN() })},
		{"Inf测得误差", valid(func(c *CertificateInput) { c.Error = math.Inf(-1) })},
		{"截止日不存在", valid(func(c *CertificateInput) { c.Expiry = "2027-02-29" })},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			n := len(l.data.Certificates)
			if _, _, err := l.AddCertificate(tc.in); err == nil {
				t.Fatalf("用例 %q 应被拒绝", tc.name)
			} else if tc.name == "未知器具" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("未知器具应报 ErrNotFound，得到 %v", err)
				}
			} else if !IsValidation(err) {
				t.Fatalf("用例 %q 应返回校验错误，得到 %v", tc.name, err)
			}
			if len(l.data.Certificates) != n {
				t.Fatalf("用例 %q 留下了证书记录", tc.name)
			}
		})
	}

	// 校准日期等于本机今天是允许的。
	today := valid(func(c *CertificateInput) { c.CalDate = "2026-10-02"; c.Expiry = "2026-10-03" })
	if _, _, err := l.AddCertificate(today); err != nil {
		t.Fatalf("校准日期为今天应被接受: %v", err)
	}
}

func TestPassFailBoundary(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 等于限值算合格。
	c1, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-01-01",
		Expiry: "2027-01-01", Method: "m", Error: -0.5, Summary: "s",
	})
	if err != nil {
		t.Fatalf("add limit cert: %v", err)
	}
	if !c1.Pass(0.5) {
		t.Fatal("测得误差绝对值等于限值应判合格")
	}
	// 超过限值必须显示超差，结论不可由录入者选择。
	c2, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-02-01",
		Expiry: "2027-02-01", Method: "m", Error: 0.51, Summary: "s",
	})
	if err != nil {
		t.Fatalf("add over-limit cert: %v", err)
	}
	if c2.Pass(0.5) {
		t.Fatal("超过限值必须判超差")
	}
	_ = l.SetStatus("M-1", StatusInUse)
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("最近证书超差应拒绝并明确显示超差，得到 %v", d.Reasons)
	}
}

func TestCertificateNumberIdempotencyAndConflict(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 1.0)

	in := CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "摘要",
	}
	first, _, err := l.AddCertificate(in)
	if err != nil {
		t.Fatalf("add first: %v", err)
	}
	// 再次提交同号且全部业务字段一致：返回原证书内容，不增加历史。
	again, dup, err := l.AddCertificate(in)
	if err != nil || !dup {
		t.Fatalf("幂等提交应成功并标记 duplicate，err=%v dup=%v", err, dup)
	}
	if *again != *first {
		t.Fatalf("幂等提交应返回原证书内容，首次=%+v 再次=%+v", *first, *again)
	}
	// 两次返回结果各自独立：返回的是展示副本，不共享同一指针。
	if again == first {
		t.Fatal("录入返回结果必须是独立副本，不能与台账内部存储或另一份结果共享")
	}
	if n := len(l.certificatesOf("M-1")); n != 1 {
		t.Fatalf("历史证书不应增加，得到 %d 张", n)
	}

	// 同号内容不同：报冲突且不改动已有数据（换器具也算不同）。
	conflict := in
	conflict.Error = 0.2
	if _, _, err := l.AddCertificate(conflict); !IsConflict(err) {
		t.Fatalf("同号不同内容应报冲突，得到 %v", err)
	}
	conflict2 := in
	conflict2.InstrumentID = "M-2"
	if _, _, err := l.AddCertificate(conflict2); !IsConflict(err) {
		t.Fatalf("同号挂到不同器具应报冲突，得到 %v", err)
	}
	got := l.findCertificate("C-1")
	if got.InstrumentID != "M-1" || got.Error != 0.1 {
		t.Fatalf("冲突后已有数据被改动：%+v", got)
	}

	// 证书编号在整个台账中唯一：M-2 不能再用 C-1。
	if n := len(l.data.Certificates); n != 1 {
		t.Fatalf("冲突提交不应产生记录，共 %d 张", n)
	}
}

func TestLatestByCalDateAndSameDayRule(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	cert := func(number, cal string, errV float64) CertificateInput {
		return CertificateInput{
			InstrumentID: "M-1", Number: number, CalDate: cal,
			Expiry: "2030-01-01", Method: "m", Error: errV, Summary: "s",
		}
	}
	// 先录入较晚（合格）证书，再补录更早的超差证书：补录只增加历史，
	// 最近证书仍按校准日期取较晚的一张。
	mustAddCert(t, l, cert("C-NEW", "2026-09-01", 0.1))
	mustAddCert(t, l, cert("C-OLD", "2026-01-01", 5))
	latest := l.LatestCertificate("M-1")
	if latest.Number != "C-NEW" {
		t.Fatalf("最近证书应按校准日期确定，得到 %s", latest.Number)
	}
	if n := len(l.certificatesOf("M-1")); n != 2 {
		t.Fatalf("补录较早证书应增加历史，共 %d 张", n)
	}

	// 同一器具同一天不接受两张不同编号的证书，拒绝时保留原记录。
	n := len(l.data.Certificates)
	_, _, err := l.AddCertificate(cert("C-DUP", "2026-09-01", 0.2))
	if !IsValidation(err) || !strings.Contains(err.Error(), "同一天") {
		t.Fatalf("同日不同号应被拒绝，得到 %v", err)
	}
	if len(l.data.Certificates) != n || l.findCertificate("C-DUP") != nil {
		t.Fatal("同日拒绝不应留下任何记录")
	}

	// 最近证书超差或到期时，不得回退到更早的合格证书。
	clock2 := &fakeClock{t: mustDate(t, "2026-10-02")}
	l2 := newTestLedger(t, clock2)
	mustRegister(t, l2, "M-1", "万用表", 0.5)
	mustAddCert(t, l2, cert("C-BAD", "2026-09-01", 5))    // 最近，超差
	mustAddCert(t, l2, cert("C-GOOD", "2026-01-01", 0.0)) // 更早，合格
	_ = l2.SetStatus("M-1", StatusInUse)
	d, _ := l2.RequestUse("M-1")
	if d.Allowed || !containsReason(d.Reasons, "超差") {
		t.Fatalf("最近证书超差不得回退到更早的合格证书，得到 %v", d.Reasons)
	}
}

func TestExpiryBoundaryAndReevaluation(t *testing.T) {
	// 证书截止日为 2026-10-02：当天起即到期；每次查询都按当前日期重新判断。
	clock := &fakeClock{t: mustDate(t, "2026-10-01")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-01-01",
		Expiry: "2026-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	_ = l.SetStatus("M-1", StatusInUse)

	d, _ := l.RequestUse("M-1")
	if !d.Allowed {
		t.Fatalf("截止日前一天应允许使用，得到 %v", d.Reasons)
	}

	// 到了截止日当天：不修改任何数据，仅时钟前进，再次申请即被拒绝。
	clock.t = mustDate(t, "2026-10-02")
	d, _ = l.RequestUse("M-1")
	if d.Allowed || !containsReason(d.Reasons, "到期") {
		t.Fatalf("截止日当天应判到期并拒绝，得到 %v", d.Reasons)
	}

	// 重新打开同一台账，按打开时的本机日期判断，仍是到期。
	reopened, err := openAt(path, func() time.Time { return mustDate(t, "2026-10-03") })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review after reopen: %v", err)
	}
	if r.CanUse || r.Latest == nil || !r.Latest.Expired {
		t.Fatalf("重开台账后应按新日期判到期，canUse=%v", r.CanUse)
	}
}

func TestUseReasonsAllListedAndRecorded(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "示波器", 0.5)

	// 待校准 + 无证书：两个原因都要列出，且留痕。
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("待校准无证书应拒绝，err=%v", err)
	}
	if !containsReason(d.Reasons, "待校准") || !containsReason(d.Reasons, "没有校准证书") {
		t.Fatalf("应列出全部适用原因，得到 %v", d.Reasons)
	}

	// 未知编号明确报错且不新增记录。
	before := len(l.UsageRecords())
	if _, err := l.RequestUse("NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知编号应报 ErrNotFound，得到 %v", err)
	}
	if len(l.UsageRecords()) != before {
		t.Fatal("未知编号的使用申请不应留痕")
	}
	if _, err := l.RequestUse("   "); !IsValidation(err) {
		t.Fatalf("空白编号应报校验错误，得到 %v", err)
	}

	// 停用 + 到期 + 超差同时存在时，原因全部列出。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-2", CalDate: "2025-01-01",
		Expiry: "2025-12-31", Method: "m", Error: 0.9, Summary: "s",
	})
	_ = l.SetStatus("M-2", StatusRetired)
	d, _ = l.RequestUse("M-2")
	if d.Allowed {
		t.Fatal("应拒绝")
	}
	for _, want := range []string{"停用", "超差", "到期"} {
		if !containsReason(d.Reasons, want) {
			t.Fatalf("原因缺少 %q：%v", want, d.Reasons)
		}
	}

	// 获准使用同样留痕。
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0, Summary: "s",
	})
	d, _ = l.RequestUse("M-1")
	if !d.Allowed {
		t.Fatalf("在用+最近证书合格+未到期应获准，得到 %v", d.Reasons)
	}
	// 假时钟下多条记录时间相同，直接找到 M-1 的最新一条核对留痕。
	var allowedRec *UsageRecord
	recs := l.UsageRecords()
	for i := range recs {
		if recs[i].InstrumentID == "M-1" && recs[i].Allowed {
			allowedRec = &recs[i]
		}
	}
	if allowedRec == nil || allowedRec.RequestedAt == "" {
		t.Fatalf("允许记录未正确保存：%+v", recs)
	}
}

func TestReviewFreezesHistoricalReasons(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 待校准且无证书时申请，被拒绝。
	_, _ = l.RequestUse("M-1")
	r, _ := l.Review("M-1")
	if len(r.Rejections) != 1 {
		t.Fatalf("应看到 1 条拒绝记录，得到 %d", len(r.Rejections))
	}
	frozen := append([]string(nil), r.Rejections[0].Reasons...)

	// 之后补录合格证书并切换为在用：当前可以使用，但历史拒绝原因不变。
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0, Summary: "s",
	})
	r, _ = l.Review("M-1")
	if !r.CanUse {
		t.Fatalf("补录合格证书并在用后应可使用，得到 %v", r.Reasons)
	}
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, frozen) {
		t.Fatalf("历史拒绝原因被改写：保存=%v 现在=%v", frozen, r.Rejections[0].Reasons)
	}
	if r.Latest == nil || r.Latest.Number != "C-1" {
		t.Fatal("核对视图应包含最近证书")
	}
	if len(r.History) != 1 {
		t.Fatalf("核对视图应包含全部历史证书，得到 %d", len(r.History))
	}
}

func TestPersistenceAndLedgerIsolation(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.json")
	pathB := filepath.Join(dir, "b.json")
	clock := func() time.Time { return mustDate(t, "2026-10-02") }

	la, err := openAt(pathA, clock)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	mustRegister(t, la, "ONLY-A", "台账A器具", 0.1)
	lb, err := openAt(pathB, clock)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	mustRegister(t, lb, "ONLY-B", "台账B器具", 0.2)

	// 退出后再次打开同一台账仍能继续查询和操作。
	reopenedA, err := openAt(pathA, clock)
	if err != nil {
		t.Fatalf("reopen A: %v", err)
	}
	if reopenedA.findInstrument("ONLY-A") == nil {
		t.Fatal("台账A重开后丢失登记数据")
	}
	if reopenedA.findInstrument("ONLY-B") != nil {
		t.Fatal("不同台账的数据混入了 A")
	}
	if _, err := reopenedA.Review("ONLY-B"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("台账 A 中不应能查到 B 的器具，得到 %v", err)
	}
	reopenedB, _ := openAt(pathB, clock)
	if reopenedB.findInstrument("ONLY-A") != nil || reopenedB.findInstrument("ONLY-B") == nil {
		t.Fatal("台账B数据被 A 污染")
	}

	// 在重开的台账 A 上继续操作并再次持久化。
	if err := reopenedA.SetStatus("ONLY-A", StatusRetired); err != nil {
		t.Fatalf("在重开的台账上操作失败: %v", err)
	}
	again, _ := openAt(pathA, clock)
	if again.findInstrument("ONLY-A").Status != StatusRetired {
		t.Fatal("重开后所做的操作没有保存")
	}
}

func TestInvalidWritesLeaveNoHalfRecords(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 制造一连串失败操作，然后重开文件，确认没有半份记录。
	_, _ = l.Register(RegisterInput{ID: " ", Name: "x", AllowedError: 1})
	_, _ = l.Register(RegisterInput{ID: "M-1", Name: "x", AllowedError: 1})
	_, _, _ = l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-X", CalDate: "not-a-date",
		Expiry: "2027-01-01", Method: "m", Error: 0, Summary: "s",
	})
	_, _, _ = l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-X", CalDate: "2026-01-01",
		Expiry: "2026-01-01", Method: "m", Error: 0, Summary: "s",
	})
	_, _ = l.RequestUse("GHOST")

	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.Instruments()) != 1 {
		t.Fatalf("重开后器具数量异常：%d", len(reopened.Instruments()))
	}
	if len(reopened.data.Certificates) != 0 || len(reopened.data.Usage) != 0 {
		t.Fatalf("重开后发现半份记录：证书 %d 使用 %d",
			len(reopened.data.Certificates), len(reopened.data.Usage))
	}
}

func mustRegister(t *testing.T, l *Ledger, id, name string, allowed float64) {
	t.Helper()
	if _, err := l.Register(RegisterInput{ID: id, Name: name, AllowedError: allowed}); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func mustAddCert(t *testing.T, l *Ledger, in CertificateInput) {
	t.Helper()
	if _, _, err := l.AddCertificate(in); err != nil {
		t.Fatalf("add cert %s: %v", in.Number, err)
	}
}

func containsReason(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

func sameReasons(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
