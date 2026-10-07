package calibrate

import (
	"path/filepath"
	"testing"
)

// 本文件的回归测试保护“查询返回的证书只是展示副本，不能改写正式台账”：
// 覆盖按器具取得最近证书（LatestCertificate）与按器具核对结果（Review）
// 中的最近证书和历史证书；录入功能返回证书的检查见 cert_copy_test.go。

// twoCertLedger 搭好一件在用器具：较早的合格证书 C-OLD 与最近的
// 超差且已到期证书 C-NEW（今天 2026-10-02，截止日 2026-10-01）。
func twoCertLedger(t *testing.T, clock *fakeClock) (*Ledger, string) {
	t.Helper()
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
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: "2026-01-01",
		Expiry: "2030-01-01", Method: "规范A", Error: 0.1, Summary: "较早合格",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-01",
		Expiry: "2026-10-01", Method: "规范B", Error: 0.8, Summary: "最近超差",
	})
	return l, path
}

// wantOfficialNewCert 断言证书内容仍是正式保存的 C-NEW。
func wantOfficialNewCert(t *testing.T, c *Certificate) {
	t.Helper()
	if c == nil || c.Number != "C-NEW" || c.InstrumentID != "M-1" ||
		c.CalDate != "2026-09-01" || c.Expiry != "2026-10-01" ||
		c.Method != "规范B" || c.Error != 0.8 || c.Summary != "最近超差" {
		t.Fatalf("正式证书被展示副本改写：%+v", c)
	}
}

// TestLatestCertificateQueryCopyCannotRewriteOfficial 验证整理 LatestCertificate
// 的返回结果（误差改小、截止日改到未来、改编号、改归属、改校准日期等）不会
// 改写正式台账：再次查询与正式申请使用仍依据原来保存的最近证书，保留超差与
// 到期的拒绝原因，不回退到较早的合格证书；展示改动也不会被随后正常保存的
// 业务操作带入台账文件。
func TestLatestCertificateQueryCopyCannotRewriteOfficial(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l, path := twoCertLedger(t, clock)

	latest := l.LatestCertificate("M-1")
	if latest == nil || latest.Number != "C-NEW" {
		t.Fatalf("最近证书应为 C-NEW，得到 %+v", latest)
	}
	// 查询本身不产生使用申请记录。
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("查询不应留下使用记录，得到 %d 条", len(recs))
	}

	// 调用方整理展示副本：测得误差改小、截止日改到未来，改证书编号、
	// 所属器具、校准日期、方法与摘要。
	latest.Error = 0.1
	latest.Expiry = "2031-01-01"
	latest.Number = "C-伪造"
	latest.InstrumentID = "M-2"
	latest.CalDate = "2026-10-01"
	latest.Method = "展示用方法"
	latest.Summary = "展示用摘要"

	// 把核对结果中的展示结论也改成合格、未到期。
	r0, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r0.Latest == nil || r0.Latest.Pass || !r0.Latest.Expired {
		t.Fatalf("核对前提失效：最近证书应超差且已到期，得到 %+v", r0.Latest)
	}
	r0.Latest.Error = 0.1
	r0.Latest.Expiry = "2031-01-01"
	r0.Latest.Pass = true
	r0.Latest.Expired = false

	// 再次查询仍返回正式保存的内容。
	wantOfficialNewCert(t, l.LatestCertificate("M-1"))

	// 再次核对仍按正式最近证书给出结论与全部拒绝原因。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse {
		t.Fatal("展示副本被改成合格未到期后，核对仍应判不能使用")
	}
	if r.Latest == nil || r.Latest.Number != "C-NEW" || r.Latest.Error != 0.8 ||
		r.Latest.Pass || !r.Latest.Expired {
		t.Fatalf("核对中的最近证书被展示副本改写：%+v", r.Latest)
	}
	if !containsReason(r.Reasons, "超差") || !containsReason(r.Reasons, "到期") {
		t.Fatalf("核对原因应保留超差与到期，得到 %v", r.Reasons)
	}
	// 历史数量与先后顺序不变：最近在前，较早合格证书只在历史中。
	if len(r.History) != 2 || r.History[0].Number != "C-NEW" || r.History[1].Number != "C-OLD" {
		t.Fatalf("历史数量或顺序被展示副本改变：%+v", r.History)
	}
	// 归属不变：被“转移”到 M-2 的证书不存在。
	if got := l.LatestCertificate("M-2"); got != nil {
		t.Fatalf("展示副本改写的归属不应转移证书：%+v", got)
	}
	// 编号占用不变：原编号仍被占用，同号不同内容仍报冲突。
	conflict := CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: "2026-09-01",
		Expiry: "2026-10-01", Method: "规范B", Error: 0.2, Summary: "最近超差",
	}
	if _, _, err := l.AddCertificate(conflict); !IsConflict(err) {
		t.Fatalf("原编号同号不同内容仍应报冲突，得到 %v", err)
	}

	// 正式申请使用仍被拒绝并保留全部原因，不回退到较早的合格证书。
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("最近证书超差且到期应拒绝使用，err=%v allowed=%v", err, d.Allowed)
	}
	if !containsReason(d.Reasons, "超差") || !containsReason(d.Reasons, "到期") {
		t.Fatalf("拒绝原因应保留超差与到期，得到 %v", d.Reasons)
	}
	if d.Latest == nil || d.Latest.Number != "C-NEW" {
		t.Fatalf("使用判断应依据正式最近证书，得到 %+v", d.Latest)
	}

	// 展示改动不得被随后正常保存的业务操作带入台账：触发一次正常写盘后
	// 从文件重开，正式证书、历史顺序与使用留痕都保持原样。
	if err := l.SetStatus("M-2", StatusInUse); err != nil {
		t.Fatalf("set M-2 in-use: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	wantOfficialNewCert(t, reopened.LatestCertificate("M-1"))
	rr, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("reopened review: %v", err)
	}
	if len(rr.History) != 2 || rr.History[0].Number != "C-NEW" || rr.History[1].Number != "C-OLD" {
		t.Fatalf("写盘后历史被展示改动污染：%+v", rr.History)
	}
	recs := reopened.UsageRecords()
	if len(recs) != 1 || recs[0].Allowed {
		t.Fatalf("应只有正式申请留下的一条拒绝记录，得到 %+v", recs)
	}
}

// TestReviewLatestAndHistoryCopiesAreIndependent 验证查询结果彼此独立：
// 同一次核对中最近证书与历史里对应的那张最初显示一致，但修改其中一处只
// 影响被修改的那份展示；调换、删去或追加历史列表项目不改变正式台账的
// 历史数量与顺序；分别取得的最近证书与多份核对结果也互不影响。
func TestReviewLatestAndHistoryCopiesAreIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l, _ := twoCertLedger(t, clock)

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Latest == nil || len(r.History) != 2 {
		t.Fatalf("核对前提失效：应有最近证书与两张历史，得到 %+v", r)
	}
	// 同一张证书在最近证书与历史中最初显示一致。
	if r.Latest.Certificate != r.History[0].Certificate ||
		r.Latest.Pass != r.History[0].Pass || r.Latest.Expired != r.History[0].Expired {
		t.Fatalf("最近证书与历史对应项最初应一致：latest=%+v history=%+v",
			r.Latest, r.History[0])
	}

	// 修改最近证书展示，只影响这一份。
	r.Latest.Error = 0.01
	r.Latest.Pass = true
	r.Latest.Expired = false
	r.Latest.Number = "C-展示"
	if r.History[0].Number != "C-NEW" || r.History[0].Error != 0.8 ||
		r.History[0].Pass || !r.History[0].Expired {
		t.Fatalf("整理最近证书展示影响了历史中的对应证书：%+v", r.History[0])
	}

	// 修改历史里对应的那张，也不影响最近证书展示。
	r.History[0].Summary = "历史展示摘要"
	r.History[0].Error = 0.02
	if r.Latest.Summary != "最近超差" || r.Latest.Error != 0.01 {
		t.Fatalf("整理历史展示影响了最近证书展示：%+v", r.Latest)
	}

	// 调换、删去、追加历史列表项目。
	r.History[0], r.History[1] = r.History[1], r.History[0]
	r.History = append(r.History[:1], r.History[0])
	if len(r.History) != 2 {
		t.Fatal("测试前提失效：对历史列表的整理没有生效")
	}

	// 分别取得的最近证书与另一份核对结果不受上述整理影响。
	latest := l.LatestCertificate("M-1")
	wantOfficialNewCert(t, latest)
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review again: %v", err)
	}
	if len(r2.History) != 2 || r2.History[0].Number != "C-NEW" || r2.History[1].Number != "C-OLD" {
		t.Fatalf("正式历史数量或顺序被展示整理改变：%+v", r2.History)
	}
	if r2.Latest == nil || r2.Latest.Number != "C-NEW" || r2.Latest.Error != 0.8 {
		t.Fatalf("新核对中的最近证书被此前的展示整理改变：%+v", r2.Latest)
	}

	// 反向也成立：整理新取得的结果不影响此前已取得的展示与正式台账。
	r2.Latest.Error = 0.03
	r2.History[1].Number = "C-涂改"
	wantOfficialNewCert(t, latest)
	if r.Latest.Error != 0.01 {
		t.Fatal("整理新核对结果影响了此前已取得的核对展示")
	}
	r3, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review third: %v", err)
	}
	if len(r3.History) != 2 || r3.History[0].Number != "C-NEW" ||
		r3.History[1].Number != "C-OLD" || r3.History[1].Error != 0.1 {
		t.Fatalf("正式历史被任何展示整理改变：%+v", r3.History)
	}
}

// TestNewerCertRefreshesQueriesButKeepsEarlierResults 验证合法录入校准日期
// 更晚的证书后，新的查询选中新证书并按其内容重新给出结论与使用资格；
// 先前已经取得的结果仍保留查询时的证书内容与结论，不随正式台账更新而改变。
func TestNewerCertRefreshesQueriesButKeepsEarlierResults(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "规范A", Error: 0.8, Summary: "超差",
	})

	// 录入新证书前取得的结果：最近证书超差，不能使用。
	latestBefore := l.LatestCertificate("M-1")
	reviewBefore, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	decisionBefore, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if latestBefore == nil || latestBefore.Number != "C-1" || reviewBefore.CanUse ||
		decisionBefore.Allowed {
		t.Fatal("测试前提失效：录入新证书前应依据超差的 C-1 拒绝使用")
	}

	// 合法录入校准日期更晚的合格证书。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-2", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "规范B", Error: 0.1, Summary: "合格",
	})

	// 新的查询选中新证书，并按其内容重新给出结论与使用资格。
	latestNow := l.LatestCertificate("M-1")
	if latestNow == nil || latestNow.Number != "C-2" || latestNow.Error != 0.1 {
		t.Fatalf("新查询应选中新证书 C-2，得到 %+v", latestNow)
	}
	reviewNow, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review now: %v", err)
	}
	if !reviewNow.CanUse || reviewNow.Latest == nil ||
		reviewNow.Latest.Number != "C-2" || !reviewNow.Latest.Pass {
		t.Fatalf("新核对应按 C-2 重新给出结论并允许使用：%+v", reviewNow.Latest)
	}
	if len(reviewNow.History) != 2 || reviewNow.History[0].Number != "C-2" ||
		reviewNow.History[1].Number != "C-1" {
		t.Fatalf("新核对的历史应包含两张且新证书在前：%+v", reviewNow.History)
	}
	d, err := l.RequestUse("M-1")
	if err != nil || !d.Allowed {
		t.Fatalf("录入合格新证书后正式申请应获准，err=%v reasons=%v", err, d.Reasons)
	}

	// 先前取得的结果仍保留查询时的证书内容与结论。
	if latestBefore.Number != "C-1" || latestBefore.Error != 0.8 {
		t.Fatalf("此前取得的最近证书被台账更新改变：%+v", latestBefore)
	}
	if reviewBefore.CanUse || reviewBefore.Latest == nil ||
		reviewBefore.Latest.Number != "C-1" || reviewBefore.Latest.Pass {
		t.Fatalf("此前取得的核对结果被台账更新改变：%+v", reviewBefore.Latest)
	}
	if len(reviewBefore.History) != 1 || reviewBefore.History[0].Number != "C-1" {
		t.Fatalf("此前取得的历史被台账更新改变：%+v", reviewBefore.History)
	}
	if decisionBefore.Allowed || decisionBefore.Latest == nil ||
		decisionBefore.Latest.Number != "C-1" {
		t.Fatalf("此前取得的使用评估被台账更新改变：%+v", decisionBefore.Latest)
	}
}

// TestNoCertificateInstrumentQueriesStayEmpty 验证没有证书的已登记器具
// 仍显示无最近证书、无证书历史，并因缺少证书不能使用；查询不制造占位
// 证书，也不产生使用申请记录。
func TestNoCertificateInstrumentQueriesStayEmpty(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	path := l.Path()
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}

	if got := l.LatestCertificate("M-1"); got != nil {
		t.Fatalf("无证书器具的最近证书应为空，得到 %+v", got)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Latest != nil {
		t.Fatalf("无证书器具的核对不应制造占位最近证书：%+v", r.Latest)
	}
	if r.History != nil {
		t.Fatalf("无证书器具的证书历史应保持空结果表示，得到 %+v", r.History)
	}
	if r.CanUse || !containsReason(r.Reasons, "没有校准证书") {
		t.Fatalf("无证书器具应因缺少证书不能使用，得到 canUse=%v reasons=%v",
			r.CanUse, r.Reasons)
	}
	d, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || d.Latest != nil || !containsReason(d.Reasons, "没有校准证书") {
		t.Fatalf("无证书器具的评估不应制造占位证书：allowed=%v latest=%+v",
			d.Allowed, d.Latest)
	}

	// 查询与空结果展示不产生使用申请记录，台账内也没有占位证书。
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("查询不应留下使用记录，得到 %d 条", len(recs))
	}
	if n := len(l.data.Certificates); n != 0 {
		t.Fatalf("查询不应制造占位证书，台账内有 %d 张", n)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.data.Certificates); n != 0 {
		t.Fatalf("重开后台账内仍不应有占位证书，得到 %d 张", n)
	}
}
