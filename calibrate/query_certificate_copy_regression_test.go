package calibrate

import (
	"path/filepath"
	"testing"
)

// 本文件为“证书查询”补充自动化回归保障，保护一条已有行为：
//
//	按器具取得的最近证书（LatestCertificate）以及按器具核对
//	（Review）结果里的最近证书与历史证书都只是展示副本。调用方
//	可以为显示而调整这些结果，但显示内容的修改不能绕过正式录入
//	规则，不能改写台账里的正式证书，不能改变最近证书的选择、
//	器具的校准结论与使用资格，也不会被随后正常保存的业务操作
//	带入台账文件。
//
// 标准场景固定为：器具 M-1 在用、允许误差 0.5；较早证书 C-OLD
// 合格且未到期；最近证书 C-BAD 同时超差且已到期。正式规则下
// 使用申请必须保留超差与到期两条拒绝原因，不能回退采用较早的
// C-OLD。另有器具 M-2 用来验证证书归属不被展示修改转移。
//
// AddCertificate 录入功能返回证书的副本保障见 cert_copy_test.go，
// 本文件不重复录入路径，只覆盖查询路径；同号录入、校准计划等
// 规则也不在本文件范围内。

const (
	dispOldNumber = "C-OLD"
	dispOldCal    = "2026-01-01"
	dispOldExpiry = "2027-01-01"
	dispBadNumber = "C-BAD"
	dispBadCal    = "2026-09-01"
	dispBadExpiry = "2026-09-15"
	dispToday     = "2026-10-02"
)

// setupDisplayCopyLedger 构造证书查询副本回归的标准场景并返回已打开的
// 台账：M-1 在用（允许误差 0.5），有两张证书——较早的 C-OLD 合格、
// 未到期；最近的 C-BAD 测得误差 0.8（超过 0.5，超差）且截止日
// 2026-09-15（相对时钟今天 2026-10-02 已到期）。M-2 无证书。
func setupDisplayCopyLedger(t *testing.T, clock *fakeClock) *Ledger {
	t.Helper()
	l := newTestLedger(t, clock)
	setupDisplayCopyLedgerStorage(t, l)
	return l
}

// setupDisplayCopyLedgerStorage 在已打开的台账上建立标准场景，
// 供需要自行控制 openAt 时机的测试使用。
func setupDisplayCopyLedgerStorage(t *testing.T, l *Ledger) {
	t.Helper()
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set M-1 in-use: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: dispOldNumber, CalDate: dispOldCal,
		Expiry: dispOldExpiry, Method: "规范A", Error: 0.1, Summary: "较早合格",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: dispBadNumber, CalDate: dispBadCal,
		Expiry: dispBadExpiry, Method: "规范A", Error: 0.8, Summary: "最近超差",
	})
}

// assertBadCertOfficial 逐字段断言一张证书仍是正式保存的 C-BAD。
func assertBadCertOfficial(t *testing.T, c *Certificate, where string) {
	t.Helper()
	if c == nil {
		t.Fatalf("%s：应能取到最近证书 %s", where, dispBadNumber)
	}
	if c.Number != dispBadNumber || c.InstrumentID != "M-1" ||
		c.CalDate != dispBadCal || c.Expiry != dispBadExpiry ||
		c.Method != "规范A" || c.Error != 0.8 || c.Summary != "最近超差" {
		t.Fatalf("%s：正式最近证书被展示修改改写：%+v", where, *c)
	}
}

// assertOfficialQueryState 从全部查询/判断入口断言标准场景未被改变：
// 最近证书仍是 C-BAD、核对结论仍拒绝且原因完整（超差+到期，不回退
// C-OLD）、历史仍为 C-BAD、C-OLD 两张。
func assertOfficialQueryState(t *testing.T, l *Ledger) {
	t.Helper()
	assertBadCertOfficial(t, l.LatestCertificate("M-1"), "再次取得最近证书")

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse {
		t.Fatalf("最近证书超差且到期时不应允许使用，得到 reasons=%v", r.Reasons)
	}
	if !containsReason(r.Reasons, "超差") ||
		!containsReason(r.Reasons, "到期") ||
		!containsReason(r.Reasons, dispBadNumber) ||
		!containsReason(r.Reasons, dispBadExpiry) {
		t.Fatalf("应保留最近证书 %s 的超差与到期原因，得到 %v", dispBadNumber, r.Reasons)
	}
	if containsReason(r.Reasons, dispOldNumber) {
		t.Fatalf("不得回退采用较早证书 %s：%v", dispOldNumber, r.Reasons)
	}
	if r.Latest == nil {
		t.Fatal("核对结果应包含最近证书")
	}
	assertBadCertOfficial(t, &r.Latest.Certificate, "核对中的最近证书")
	if r.Latest.Pass || !r.Latest.Expired {
		t.Fatalf("最近证书应显示超差且已到期，得到 pass=%v expired=%v",
			r.Latest.Pass, r.Latest.Expired)
	}
	if len(r.History) != 2 {
		t.Fatalf("历史证书数量应保持 2 张，得到 %d 张", len(r.History))
	}
	h0, h1 := r.History[0], r.History[1]
	if h0.Number != dispBadNumber || h0.Pass || !h0.Expired ||
		h0.InstrumentID != "M-1" || h0.CalDate != dispBadCal || h0.Error != 0.8 {
		t.Fatalf("历史第一张应为正式的超差已到期 %s：%+v", dispBadNumber, h0)
	}
	if h1.Number != dispOldNumber || !h1.Pass || h1.Expired ||
		h1.InstrumentID != "M-1" || h1.CalDate != dispOldCal || h1.Error != 0.1 {
		t.Fatalf("历史第二张应为正式的合格未到期 %s：%+v", dispOldNumber, h1)
	}
}

// assertOfficialCertsPersisted 重开台账文件，确认正式证书内容、归属、
// 数量与历史先后都保持原样，展示用的伪造字段没有被写盘带入。
func assertOfficialCertsPersisted(t *testing.T, path string, clock *fakeClock) {
	t.Helper()
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Certificates) != 2 {
		t.Fatalf("正式证书应仍为 2 张，得到 %d 张：%+v",
			len(reopened.data.Certificates), reopened.data.Certificates)
	}
	assertBadCertOfficial(t, reopened.LatestCertificate("M-1"), "重开后取得最近证书")
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("reopen review: %v", err)
	}
	if len(r.History) != 2 ||
		r.History[0].Number != dispBadNumber || r.History[1].Number != dispOldNumber {
		t.Fatalf("重开后历史数量与先后顺序应保持原样：%+v", r.History)
	}
	if got := reopened.LatestCertificate("M-2"); got != nil {
		t.Fatalf("证书归属不应转移到 M-2：%+v", got)
	}
	for _, c := range reopened.data.Certificates {
		if c.Number != dispBadNumber && c.Number != dispOldNumber {
			t.Fatalf("台账文件中出现展示用伪造证书：%+v", c)
		}
		if c.InstrumentID != "M-1" {
			t.Fatalf("证书 %s 归属被展示修改改写：%+v", c.Number, c)
		}
	}
}

// TestLatestCertificateQueryIsDisplayCopy 验证按器具取得的最近证书是
// 独立展示副本：把查询结果中的测得误差改小、截止日改到未来、结论相关
// 字段以及证书编号、所属器具、校准日期、方法、摘要全部改写，再次查询
// 与正式申请使用仍依据原来保存的 C-BAD，保留超差、到期两条拒绝原因，
// 不回退较早证书；编号占用、同日限制与证书归属也都按正式数据判断。
func TestLatestCertificateQueryIsDisplayCopy(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	setupDisplayCopyLedgerStorage(t, l)

	latest := l.LatestCertificate("M-1")
	assertBadCertOfficial(t, latest, "首次取得最近证书")

	// 调用方为展示而全面整理这份查询结果。
	latest.Error = 0.0
	latest.Expiry = "2030-01-01"
	latest.Number = "C-展示伪造"
	latest.InstrumentID = "M-2"
	latest.CalDate = "2026-10-01"
	latest.Method = "展示用方法"
	latest.Summary = "展示用摘要"
	latest.CreatedAt = "2099-01-01T00:00:00Z"

	// 只读评估仍按正式的 C-BAD 判定超差、到期；返回的评估视图也是
	// 正式内容，整理它同样不影响随后的正式申请。
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "超差") ||
		!containsReason(d.Reasons, "到期") ||
		!containsReason(d.Reasons, dispBadNumber) {
		t.Fatalf("改小查询结果误差、改远截止日不应恢复使用资格：%+v", d)
	}
	if d.Latest != nil {
		d.Latest.Error = 0
		d.Latest.Expired = false
		d.Latest.Pass = true
		d.Latest.Number = "C-评估伪造"
	}

	// 正式申请使用：仍被拒绝并按正式原因留痕。
	used, err := l.RequestUse("M-1")
	if err != nil || used.Allowed {
		t.Fatalf("正式申请应依据 C-BAD 拒绝，err=%v allowed=%v", err, used.Allowed)
	}
	if !containsReason(used.Reasons, "超差") ||
		!containsReason(used.Reasons, "到期") ||
		containsReason(used.Reasons, dispOldNumber) {
		t.Fatalf("正式申请应保留 C-BAD 的超差与到期原因且不回退旧证书：%v", used.Reasons)
	}
	assertOfficialQueryState(t, l)

	// 留痕按正式原因保存，调用方整理申请返回结果不改写历史。
	used.Reasons = []string{"展示用原因"}
	recs := l.UsageRecords()
	if len(recs) != 1 || recs[0].Allowed ||
		!containsReason(recs[0].Reasons, "超差") ||
		!containsReason(recs[0].Reasons, "到期") {
		t.Fatalf("使用留痕应保留正式拒绝原因：%+v", recs)
	}

	// 正式编号仍占用：同号不同内容报冲突；伪造编号不存在、未进入台账。
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: dispBadNumber, CalDate: dispBadCal,
		Expiry: dispBadExpiry, Method: "规范A", Error: 0.1, Summary: "冲突",
	}); !IsConflict(err) {
		t.Fatalf("正式编号 %s 仍应占用，同号不同内容报冲突，得到 %v", dispBadNumber, err)
	}
	if l.findCertificate("C-展示伪造") != nil || l.findCertificate("C-评估伪造") != nil {
		t.Fatal("展示伪造的证书编号不应进入台账")
	}
	// 同日限制按正式校准日期判断：M-1 在 2026-09-01 已有 C-BAD。
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-SAME-DAY", CalDate: dispBadCal,
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "同日",
	}); !IsValidation(err) {
		t.Fatalf("同日不同编号仍应按正式校准日期拒绝，得到 %v", err)
	}
	// 归属不转移：M-2 仍无证书。
	if got := l.LatestCertificate("M-2"); got != nil {
		t.Fatalf("把展示副本所属器具改成 M-2 不应转移正式证书：%+v", got)
	}

	// 随后一次正常保存的业务操作不得把展示改动带入台账文件。
	if err := l.SetStatus("M-2", StatusInUse); err != nil {
		t.Fatalf("set M-2 in-use: %v", err)
	}
	assertOfficialCertsPersisted(t, path, clock)
}

// TestReviewLatestDisplayMutationKeepsOfficialRejection 验证核对结果里的
// 最近证书是展示副本：即使把展示结论改成合格、未到期，把误差改小、
// 截止日改到未来并改写编号/器具/校准日期，再次核对与正式申请仍按正式
// 保存的 C-BAD 拒绝，历史中的同一证书与较早证书也不受影响。
func TestReviewLatestDisplayMutationKeepsOfficialRejection(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	setupDisplayCopyLedgerStorage(t, l)

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	// 调用方把核对里的最近证书按“想看的样子”全面整理。
	r.Latest.Error = 0.0
	r.Latest.Expiry = "2030-12-31"
	r.Latest.Pass = true
	r.Latest.Expired = false
	r.Latest.Number = "C-核对伪造"
	r.Latest.InstrumentID = "M-2"
	r.Latest.CalDate = "2026-10-01"
	r.Latest.Method = "展示方法"
	r.Latest.Summary = "展示摘要"
	r.CanUse = true
	r.Reasons = []string{}

	// 同一份结果里历史中的同一张证书不受最近证书整理的影响。
	if h := r.History[0]; h.Number != dispBadNumber || h.Pass || !h.Expired || h.Error != 0.8 {
		t.Fatalf("整理核对最近证书影响了同一结果历史中的同一张证书：%+v", h)
	}
	if h := r.History[1]; h.Number != dispOldNumber || !h.Pass || h.Expired {
		t.Fatalf("整理核对最近证书影响了较早证书：%+v", h)
	}

	// 正式申请仍被拒绝并留痕，再次查询仍是正式结论。
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("正式申请不应被展示结论改写为获准，err=%v allowed=%v", err, d.Allowed)
	}
	if !containsReason(d.Reasons, "超差") || !containsReason(d.Reasons, "到期") {
		t.Fatalf("正式拒绝原因应完整保留：%v", d.Reasons)
	}
	assertOfficialQueryState(t, l)

	// 正常保存（状态切换）后重开：展示伪造内容没有进入台账文件。
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("set M-2 retired: %v", err)
	}
	assertOfficialCertsPersisted(t, path, clock)
}

// TestReviewHistoryMutationsCannotRewriteOfficialHistory 验证核对历史
// 列表是展示副本：修改历史项的证书编号、所属器具、校准日期等字段，
// 以及调换先后、删去项目或追加伪造项目，都不能改变正式证书的内容、
// 归属、历史数量和先后顺序；再次查询恢复原样，正常保存也不带入台账。
func TestReviewHistoryMutationsCannotRewriteOfficialHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	setupDisplayCopyLedgerStorage(t, l)

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	// 修改第一张历史项的全部字段。
	r.History[0].Number = "C-历史伪造"
	r.History[0].InstrumentID = "M-2"
	r.History[0].CalDate = "2030-01-01"
	r.History[0].Expiry = "2031-01-01"
	r.History[0].Error = 0
	r.History[0].Method = "展示方法"
	r.History[0].Summary = "展示摘要"
	r.History[0].Pass = true
	r.History[0].Expired = false
	// 调换先后、删去项目、追加伪造项目。
	r.History[0], r.History[1] = r.History[1], r.History[0]
	r.History = r.History[:1]
	r.History = append(r.History, CertificateView{
		Certificate: Certificate{
			Number: "C-追加伪造", InstrumentID: "M-2",
			CalDate: "2099-01-01", Expiry: "2099-12-31", Error: 0,
		},
		Pass: true, Expired: false,
	})

	// 正式存储的数量、内容、归属与顺序不变。
	if n := len(l.data.Certificates); n != 2 {
		t.Fatalf("整理历史列表不应改变正式证书数量，得到 %d", n)
	}
	assertBadCertOfficial(t, l.LatestCertificate("M-1"), "整理历史后取得最近证书")
	if got := l.LatestCertificate("M-2"); got != nil {
		t.Fatalf("历史项归属修改不应把证书转移给 M-2：%+v", got)
	}

	// 再次核对：历史恢复为 C-BAD、C-OLD 两张，伪造项不存在，
	// 被删去的较早证书仍在，被调换的顺序恢复。
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("second review: %v", err)
	}
	if len(r2.History) != 2 ||
		r2.History[0].Number != dispBadNumber || r2.History[1].Number != dispOldNumber {
		t.Fatalf("再次核对历史数量与顺序应恢复正式状态：%+v", r2.History)
	}
	if r2.History[0].InstrumentID != "M-1" || r2.History[0].CalDate != dispBadCal ||
		r2.History[0].Error != 0.8 || r2.History[0].Pass || !r2.History[0].Expired {
		t.Fatalf("再次核对第一张应是正式的超差到期 C-BAD：%+v", r2.History[0])
	}
	if r2.History[1].InstrumentID != "M-1" || r2.History[1].CalDate != dispOldCal ||
		r2.History[1].Error != 0.1 || !r2.History[1].Pass || r2.History[1].Expired {
		t.Fatalf("再次核对第二张应是正式的合格未到期 C-OLD：%+v", r2.History[1])
	}

	// 正常保存后重开，展示增删改没有一项进入台账文件。
	if err := l.SetStatus("M-2", StatusInUse); err != nil {
		t.Fatalf("set M-2 in-use: %v", err)
	}
	assertOfficialCertsPersisted(t, path, clock)
}

// TestReviewLatestAndHistoryInitiallyConsistentButIndependent 验证同一次
// 核对中最近证书与历史里对应的那张证书最初显示完全一致，但它们是两份
// 独立展示：修改其中一处只影响被修改的那份展示，另一处以及正式台账
// 都不变化。
func TestReviewLatestAndHistoryInitiallyConsistentButIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	l := setupDisplayCopyLedger(t, clock)

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.History) != 2 {
		t.Fatalf("应有 2 张历史证书，得到 %d", len(r.History))
	}
	// 同一次核对：最近证书与历史第一张最初必须逐字段一致。
	hv := r.History[0]
	lv := r.Latest
	if lv.Number != hv.Number || lv.InstrumentID != hv.InstrumentID ||
		lv.CalDate != hv.CalDate || lv.Expiry != hv.Expiry ||
		lv.Method != hv.Method || lv.Summary != hv.Summary ||
		lv.Error != hv.Error || lv.CreatedAt != hv.CreatedAt ||
		lv.Pass != hv.Pass || lv.Expired != hv.Expired {
		t.Fatalf("同一次核对中最近证书与历史对应项最初应一致：latest=%+v history=%+v",
			lv, hv)
	}

	// 只改最近证书这份展示：历史中同一张证书保持原样。
	r.Latest.Number = "C-只改最近"
	r.Latest.Error = 0
	r.Latest.Pass = true
	r.Latest.Expired = false
	r.Latest.Expiry = "2030-01-01"
	if h := r.History[0]; h.Number != dispBadNumber || h.Error != 0.8 ||
		h.Pass || !h.Expired || h.Expiry != dispBadExpiry {
		t.Fatalf("修改最近证书展示影响了历史中的同一张证书：%+v", h)
	}

	// 只改历史中的对应项：最近证书这份展示保留自己的修改，互不影响。
	r.History[0].Number = "C-只改历史"
	r.History[0].Error = 0.1
	r.History[0].Pass = true
	if r.Latest.Number != "C-只改最近" || r.Latest.Error != 0 ||
		!r.Latest.Pass || r.Latest.Expired {
		t.Fatalf("修改历史项反过来影响了最近证书展示：%+v", r.Latest)
	}
	// 改动较早历史项也不波及其余展示。
	r.History[1].Number = "C-旧证伪造"
	r.History[1].Expired = true
	if r.Latest.Number != "C-只改最近" {
		t.Fatal("修改较早历史项影响了最近证书展示")
	}

	// 正式台账与再次查询都不受任何一处展示修改影响。
	assertOfficialQueryState(t, l)
}

// TestSeparatelyFetchedCertificateResultsIndependent 验证分别取得的证书
// 结果彼此独立：两份最近证书与两份核对结果互不影响——整理其中一份，
// 其他已取得的结果不变化；各自的增删改也不回写正式台账。
func TestSeparatelyFetchedCertificateResultsIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	setupDisplayCopyLedgerStorage(t, l)

	latest1 := l.LatestCertificate("M-1")
	latest2 := l.LatestCertificate("M-1")
	r1, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review 1: %v", err)
	}
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review 2: %v", err)
	}

	// 整理第一份最近证书：第二份最近证书、两份核对都保持正式内容。
	latest1.Number = "C-第一份"
	latest1.Error = 0
	latest1.InstrumentID = "M-2"
	latest1.Expiry = "2030-01-01"
	assertBadCertOfficial(t, latest2, "第二份最近证书")
	for _, rv := range []*InstrumentReview{r1, r2} {
		assertBadCertOfficial(t, &rv.Latest.Certificate, "另一份核对的最近证书")
		if len(rv.History) != 2 || rv.History[0].Number != dispBadNumber ||
			rv.History[1].Number != dispOldNumber {
			t.Fatalf("另一份核对历史被第一份最近证书整理影响：%+v", rv.History)
		}
	}

	// 整理第一份核对（最近证书字段、历史调换/删改/追加、结论）：
	// 第二份核对与第二份最近证书都保持原样。
	r1.Latest.Number = "C-核对一"
	r1.Latest.Pass = true
	r1.Latest.Expired = false
	r1.Latest.Error = 0
	r1.CanUse = true
	r1.History[0].Number = "C-核对一历史"
	r1.History[0], r1.History[1] = r1.History[1], r1.History[0]
	r1.History = r1.History[:1]
	r1.History = append(r1.History, CertificateView{
		Certificate: Certificate{Number: "C-核对一追加", InstrumentID: "M-2"},
	})
	assertBadCertOfficial(t, latest2, "整理核对一后的第二份最近证书")
	assertBadCertOfficial(t, &r2.Latest.Certificate, "第二份核对的最近证书")
	if r2.Latest.Pass || !r2.Latest.Expired || r2.CanUse {
		t.Fatalf("第二份核对结论被第一份整理影响：canUse=%v latest=%+v",
			r2.CanUse, r2.Latest)
	}
	if len(r2.History) != 2 || r2.History[0].Number != dispBadNumber ||
		r2.History[1].Number != dispOldNumber ||
		r2.History[0].Pass || !r2.History[0].Expired {
		t.Fatalf("第二份核对历史被第一份整理影响：%+v", r2.History)
	}

	// 正式台账、再次查询以及正常保存后的文件内容都保持正式状态。
	assertOfficialQueryState(t, l)
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("set M-2 retired: %v", err)
	}
	assertOfficialCertsPersisted(t, path, clock)
}

// TestLegalNewerCertificateUpdatesNewQueriesButFetchedCopiesStayFrozen
// 验证正式台账合法变化与展示副本冻结两不相扰：合法录入校准日期更晚的
// 证书后，新查询选中新证书并按其内容重新给出合格、可用的结论；此前
// 已经取得的结果（含调用方自己的展示整理）仍保留查询时的证书内容与
// 拒绝结论，不随台账更新而悄悄变化。
func TestLegalNewerCertificateUpdatesNewQueriesButFetchedCopiesStayFrozen(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	// 较早合格证书 + 较近但超差（未到期）的证书：当前最近为超差证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: dispOldNumber, CalDate: dispOldCal,
		Expiry: dispOldExpiry, Method: "规范A", Error: 0.1, Summary: "较早合格",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: dispBadNumber, CalDate: dispBadCal,
		Expiry: "2027-09-15", Method: "规范A", Error: 0.8, Summary: "较近超差",
	})

	// 录入新证书前先取得各类结果：它们定格在“最近为超差证书、不可用”。
	latestBefore := l.LatestCertificate("M-1")
	reviewBefore, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review before: %v", err)
	}
	canBefore, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use before: %v", err)
	}
	if latestBefore.Number != dispBadNumber || reviewBefore.CanUse ||
		canBefore.Allowed || !containsReason(canBefore.Reasons, "超差") {
		t.Fatalf("测试前提失效：新证书录入前应按 %s 超差拒绝", dispBadNumber)
	}
	// 调用方在旧结果上做展示整理。
	latestBefore.Number = "C-旧结果伪造"
	latestBefore.Error = 0
	reviewBefore.Latest.Pass = true
	reviewBefore.Latest.Number = "C-旧核对伪造"
	reviewBefore.CanUse = true
	canBefore.Latest.Number = "C-旧评估伪造"

	// 合法录入校准日期更晚、合格、未到期的新证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "规范A", Error: 0.1, Summary: "最新合格",
	})

	// 新查询：最近证书切换为 C-NEW，按它重新给出合格、未到期、可用结论。
	assertLatest := func(c *Certificate, where string) {
		t.Helper()
		if c == nil || c.Number != "C-NEW" || c.CalDate != "2026-10-01" ||
			c.Expiry != "2027-10-01" || c.Error != 0.1 {
			t.Fatalf("%s：新查询应选中新证书 C-NEW：%+v", where, c)
		}
	}
	assertLatest(l.LatestCertificate("M-1"), "录入后取得最近证书")
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review after: %v", err)
	}
	if !r.CanUse || len(r.Reasons) != 0 || r.Latest == nil ||
		!r.Latest.Pass || r.Latest.Expired {
		t.Fatalf("新查询应按 C-NEW 判定合格可用，得到 canUse=%v reasons=%v latest=%+v",
			r.CanUse, r.Reasons, r.Latest)
	}
	assertLatest(&r.Latest.Certificate, "录入后核对的最近证书")
	if len(r.History) != 3 {
		t.Fatalf("新查询历史应含 3 张证书，得到 %d", len(r.History))
	}
	wantOrder := []string{"C-NEW", dispBadNumber, dispOldNumber}
	for i, want := range wantOrder {
		if r.History[i].Number != want {
			t.Fatalf("新查询历史顺序应为 %v，得到 %q 在位置 %d：%+v",
				wantOrder, r.History[i].Number, i, r.History)
		}
	}
	// 历史结论按各自内容：C-NEW、C-OLD 合格，C-BAD 仍超差（但未到期）。
	if r.History[0].Pass != true || r.History[1].Pass != false ||
		r.History[2].Pass != true {
		t.Fatalf("各历史证书应保留各自结论：%+v", r.History)
	}
	// 正式申请按新证书获准并留痕。
	d, err := l.RequestUse("M-1")
	if err != nil || !d.Allowed {
		t.Fatalf("录入合格新证书后正式申请应获准，err=%v decision=%+v", err, d)
	}
	if recs := l.UsageRecords(); len(recs) != 1 || !recs[0].Allowed {
		t.Fatalf("查询不留痕，应只有这 1 条获准记录：%+v", recs)
	}

	// 此前已取得的结果保留查询时内容与自身展示整理，不随台账更新变化。
	if latestBefore.Number != "C-旧结果伪造" || latestBefore.Error != 0 {
		t.Fatalf("旧最近证书结果随正式台账更新变化：%+v", latestBefore)
	}
	if reviewBefore.Latest.Number != "C-旧核对伪造" ||
		reviewBefore.Latest.Pass != true || reviewBefore.CanUse != true {
		t.Fatalf("旧核对结果随正式台账更新变化：%+v", reviewBefore)
	}
	if len(reviewBefore.History) != 2 {
		t.Fatalf("旧核对历史应定格在查询时的 2 张，得到 %d", len(reviewBefore.History))
	}
	if canBefore.Latest.Number != "C-旧评估伪造" || canBefore.Allowed {
		t.Fatalf("旧评估结果随正式台账更新变化：%+v", canBefore)
	}

	// 重开台账：新证书与历史顺序正式落盘，旧结果的展示整理不在文件里。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Certificates) != 3 {
		t.Fatalf("正式证书应为 3 张，得到 %d", len(reopened.data.Certificates))
	}
	rr, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("reopen review: %v", err)
	}
	if !rr.CanUse || rr.Latest == nil || rr.Latest.Number != "C-NEW" {
		t.Fatalf("重开后应按 C-NEW 判定可用：%+v", rr)
	}
	for i, want := range wantOrder {
		if rr.History[i].Number != want {
			t.Fatalf("重开后历史顺序应为 %v：%+v", wantOrder, rr.History)
		}
	}
}

// TestNoCertificateInstrumentKeepsEmptyResultsAndDeniesUse 验证没有证书的
// 已登记器具仍表示为“无最近证书、无证书历史”，并因缺少证书不能使用：
// 不为统一展示制造占位证书；调用方在自己的核对结果里伪造占位证书或
// 追加历史，不改变再次查询的空结果。查询与调整展示不产生使用申请
// 记录，只有正式申请使用才留痕。
func TestNoCertificateInstrumentKeepsEmptyResultsAndDeniesUse(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, dispToday)}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	// M-2 有证书，用来确认 M-1 的空结果不会把别的器具证书拿来占位。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-FOR-M2", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.1, Summary: "M-2 的证书",
	})

	if got := l.LatestCertificate("M-1"); got != nil {
		t.Fatalf("无证书器具应无最近证书（nil），得到 %+v", got)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Latest != nil {
		t.Fatalf("核对结果应无最近证书，得到 %+v", r.Latest)
	}
	if r.History != nil {
		t.Fatalf("无证书时历史应保持既有空表示 nil，得到 %+v", r.History)
	}
	if r.CanUse || !containsReason(r.Reasons, "没有校准证书") {
		t.Fatalf("无证书器具应因缺少证书不能使用，得到 canUse=%v reasons=%v",
			r.CanUse, r.Reasons)
	}

	// 调用方在自己那份展示结果里伪造占位最近证书、追加历史、改成可用。
	r.Latest = &CertificateView{
		Certificate: Certificate{Number: "C-占位", InstrumentID: "M-1"},
		Pass:        true, Expired: false,
	}
	r.History = append(r.History, CertificateView{
		Certificate: Certificate{Number: "C-占位历史", InstrumentID: "M-1"},
	})
	r.CanUse = true
	r.Reasons = []string{}

	// 多次查询与评估都不留痕。
	if _, err := l.CanUse("M-1"); err != nil {
		t.Fatalf("can use: %v", err)
	}
	if _, err := l.Review("M-1"); err != nil {
		t.Fatalf("second review: %v", err)
	}
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("查询与调整展示不应产生使用申请记录，得到 %+v", recs)
	}

	// 再次查询仍是无最近证书、无历史、缺证书不能使用，没有占位证书。
	if got := l.LatestCertificate("M-1"); got != nil {
		t.Fatalf("伪造占位不应进入再次查询，得到 %+v", got)
	}
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review after mutation: %v", err)
	}
	if r2.Latest != nil || r2.History != nil || r2.CanUse ||
		!containsReason(r2.Reasons, "没有校准证书") {
		t.Fatalf("再次查询应保持无证书的空结果与拒绝：%+v", r2)
	}

	// 只有正式申请使用才按原有规则拒绝并留痕。
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed || !containsReason(d.Reasons, "没有校准证书") {
		t.Fatalf("正式申请应因缺少证书被拒绝，err=%v decision=%+v", err, d)
	}
	if recs := l.UsageRecords(); len(recs) != 1 || recs[0].Allowed ||
		!containsReason(recs[0].Reasons, "没有校准证书") {
		t.Fatalf("应只有正式申请留下的 1 条缺证书拒绝：%+v", recs)
	}
	if rr, _ := l.Review("M-1"); len(rr.Rejections) != 1 {
		t.Fatalf("核对中应只看到正式申请留下的 1 条拒绝，得到 %d", len(rr.Rejections))
	}

	// 正常保存后重开：M-1 仍无证书，占位内容不在文件中；M-2 证书归属不变。
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("set M-2 retired: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Certificates) != 1 ||
		reopened.data.Certificates[0].Number != "C-FOR-M2" ||
		reopened.data.Certificates[0].InstrumentID != "M-2" {
		t.Fatalf("重开后应只有属于 M-2 的 1 张正式证书：%+v", reopened.data.Certificates)
	}
	if got := reopened.LatestCertificate("M-1"); got != nil {
		t.Fatalf("重开后 M-1 仍应无最近证书：%+v", got)
	}
}
