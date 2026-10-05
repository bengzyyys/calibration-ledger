package calibrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“申请使用器具”补充回归保障，重点保护一条现有规则：
//
//	最近证书（只按校准日期确定，与录入先后、有效期长短无关）到期后，
//	不能借用历史中仍合格、仍未到期的较早证书继续获准使用。
//
// 场景固定为：器具保持在用；两张证书测得误差都在允许误差内（均合格）；
// 校准日期较新的证书先到期，较早证书的有效期更长，仍覆盖申请当天。
// 关键边界是较新证书的截止日当天——截止日当天即到期——这一天申请必须
// 因“最近证书到期”被拒绝，而不能回退到旧证书，也不能按谁的有效期更长
// 决定谁是最近证书。
//
// 全部日期由可手动拨动的假时钟给出：截止日前一天与截止日当天的区别只
// 来自时钟推进，不依赖任何固定日期恰好仍在未来，日历日期如何变化都能
// 重复执行。

const (
	newerNumber = "C-NEW"
	newerCal    = "2026-09-20"
	newerExpiry = "2026-10-20"
	olderNumber = "C-OLD"
	olderCal    = "2026-09-10"
	olderExpiry = "2027-09-10"
)

// setupTwoPassingCerts 构造本回归的标准场景：
//   - 器具 M-1 在用，允许误差 0.5；
//   - 较新证书 C-NEW：校准日期 2026-09-20，截止日 2026-10-20（先到期），
//     测得误差 -0.1，合格；
//   - 较早证书 C-OLD：校准日期 2026-09-10，截止日 2027-09-10（更长，
//     在整个场景覆盖的日期内始终未到期），测得误差 0.4，合格。
//
// backfill 为 true 时模拟“较早证书在较新证书之后补录”：先在 09-20 录入
// C-NEW，时钟拨到 09-25 再录入 C-OLD；为 false 时按校准日期先后录入。
// 两种录入顺序得到的最近证书、使用资格与历史顺序必须完全一致。
func setupTwoPassingCerts(t *testing.T, clock *fakeClock, backfill bool) *Ledger {
	t.Helper()
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("切换在用失败: %v", err)
	}

	newer := CertificateInput{
		InstrumentID: "M-1", Number: newerNumber, CalDate: newerCal,
		Expiry: newerExpiry, Method: "规范A", Error: -0.1, Summary: "较新一次校准",
	}
	older := CertificateInput{
		InstrumentID: "M-1", Number: olderNumber, CalDate: olderCal,
		Expiry: olderExpiry, Method: "规范A", Error: 0.4, Summary: "较早一次校准",
	}
	if backfill {
		clock.t = mustDate(t, "2026-09-20")
		mustAddCert(t, l, newer)
		// 录入更晚（09-25 才补录）、有效期更长，都不能让 C-OLD 成为最近证书。
		clock.t = mustDate(t, "2026-09-25")
		mustAddCert(t, l, older)
	} else {
		clock.t = mustDate(t, "2026-09-15")
		mustAddCert(t, l, older)
		clock.t = mustDate(t, "2026-09-20")
		mustAddCert(t, l, newer)
	}
	return l
}

// assertLatestNewer 核对最近证书始终是校准日期较新的 C-NEW：与录入先后
// （CreatedAt）及有效期长短都无关。
func assertLatestNewer(t *testing.T, l *Ledger) {
	t.Helper()
	latest := l.LatestCertificate("M-1")
	if latest == nil {
		t.Fatal("应有最近证书")
	}
	if latest.Number != newerNumber {
		t.Fatalf("最近证书必须按校准日期取较新的 %s，不能按有效期或录入先后，得到 %s",
			newerNumber, latest.Number)
	}
	if latest.CalDate != newerCal || latest.Expiry != newerExpiry {
		t.Fatalf("最近证书日期字段异常：%+v", latest)
	}
}

// assertHistoryOnExpiryDay 核对截止日当天的历史证书视图：按校准日期由近到远
// 列出，且两张证书各自保留独立结论与截止日——较新证书已到期不把旧证书带成
// 到期，旧证书未到期也不把较新证书的到期误说成超差。
func assertHistoryOnExpiryDay(t *testing.T, r *InstrumentReview) {
	t.Helper()
	if len(r.History) != 2 {
		t.Fatalf("历史应同时保留两张证书，得到 %d 张", len(r.History))
	}
	first := r.History[0]
	second := r.History[1]
	if first.Number != newerNumber || second.Number != olderNumber {
		t.Fatalf("历史必须按校准日期由近到远为 %s、%s，得到 %s、%s",
			newerNumber, olderNumber, first.Number, second.Number)
	}
	// 两张证书测得误差绝对值都不超过允许误差 0.5（0.4 同样在内）；
	// 到期与否不改变合格结论：新证书到期不等于超差。
	if !first.Pass {
		t.Fatalf("较新证书 %s 应仍按允许误差判为合格，到期不应被说成超差：%+v",
			newerNumber, first)
	}
	if !second.Pass {
		t.Fatalf("较早证书 %s 应判为合格：%+v", olderNumber, second)
	}
	if first.Expiry != newerExpiry || second.Expiry != olderExpiry {
		t.Fatalf("两张证书应各自保留截止日：%s / %s", first.Expiry, second.Expiry)
	}
	if !first.Expired {
		t.Fatalf("较新证书 %s 在其截止日当天起应显示已到期：%+v", newerNumber, first)
	}
	if second.Expired {
		t.Fatalf("较早证书 %s 有效期更长，应显示未到期，不能被新证书带成到期：%+v",
			olderNumber, second)
	}
}

// TestNewerPassingCertExpiryDeniesWithoutFallingBackToOlder 覆盖主场景：
// 两张证书都合格，较新证书先到期。截止日前一天申请依据较新证书获准；
// 到截止日当天申请必须因较新（最近）证书到期而拒绝，即使较早证书合格且
// 有效期仍覆盖当天，也不能回退旧结论。
func TestNewerPassingCertExpiryDeniesWithoutFallingBackToOlder(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-09-20")}
	l := setupTwoPassingCerts(t, clock, false)

	// 截止日前一天（10-19）：最近证书 C-NEW 合格且未到期，申请应依据它获准。
	clock.t = mustDate(t, "2026-10-19")
	before, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use before expiry: %v", err)
	}
	if !before.Allowed || len(before.Reasons) != 0 {
		t.Fatalf("较新证书截止日前应依据它获准，得到 allowed=%v reasons=%v",
			before.Allowed, before.Reasons)
	}
	if before.Latest == nil || before.Latest.Number != newerNumber ||
		!before.Latest.Pass || before.Latest.Expired {
		t.Fatalf("截止日前最近证书应为合格未到期的 %s：%+v", newerNumber, before.Latest)
	}
	allowed, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("截止日前申请失败: %v", err)
	}
	if !allowed.Allowed {
		t.Fatalf("截止日前申请应获准，得到 %v", allowed.Reasons)
	}
	if !strings.HasPrefix(allowed.evaluatedAt.Format(time.RFC3339), "2026-10-19T") {
		t.Fatalf("获准记录时间应属于申请当天 2026-10-19，得到 %s",
			allowed.evaluatedAt.Format(time.RFC3339))
	}

	// 只推进时钟到截止日当天（10-20），不改动任何数据。
	clock.t = mustDate(t, "2026-10-20")
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("截止日当天申请失败: %v", err)
	}
	if d.Allowed {
		t.Fatalf("最近证书截止日当天应判到期并拒绝，不能借用仍有效的 %s，得到 reasons=%v",
			olderNumber, d.Reasons)
	}
	// 拒绝原因必须指出真正到期的最近证书编号及其截止日，而不是旧证书，
	// 也不能出现超差（两张证书都合格）。
	if !containsReason(d.Reasons, newerNumber) ||
		!containsReason(d.Reasons, newerExpiry) ||
		!containsReason(d.Reasons, "到期") {
		t.Fatalf("拒绝原因应指出最近证书 %s 于 %s 到期，得到 %v",
			newerNumber, newerExpiry, d.Reasons)
	}
	if containsReason(d.Reasons, olderNumber) {
		t.Fatalf("不得回退或指责仍合格未到期的较早证书 %s：%v", olderNumber, d.Reasons)
	}
	if containsReason(d.Reasons, "超差") {
		t.Fatalf("两张证书都合格，到期拒绝不应混入超差原因：%v", d.Reasons)
	}
	if !strings.HasPrefix(d.evaluatedAt.Format(time.RFC3339), "2026-10-20T") {
		t.Fatalf("拒绝记录应保留申请当天 2026-10-20 的时间，得到 %s",
			d.evaluatedAt.Format(time.RFC3339))
	}

	// 截止日后一天仍是拒绝：日期推进不会让旧证书“接管”。
	clock.t = mustDate(t, "2026-10-21")
	after, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use after expiry: %v", err)
	}
	if after.Allowed || !containsReason(after.Reasons, newerNumber) {
		t.Fatalf("截止日后仍应因最近证书 %s 到期而拒绝，得到 %v", newerNumber, after.Reasons)
	}
	assertLatestNewer(t, l)
}

// TestReviewOnExpiryDayShowsNewerExpiredOlderStillValid 核对截止日当天的
// 按器具核对结果：最近证书是较新的 C-NEW 且已到期；历史按校准日期由近到远，
// 同时能看出新证书已到期、旧证书尚未到期；当前不可使用且原因只指向新证书。
// 两种录入顺序（先后录入 / 事后补录）结果一致。核对是只读操作：连续核对
// 不新增使用记录、不改动两张证书与器具状态。
func TestReviewOnExpiryDayShowsNewerExpiredOlderStillValid(t *testing.T) {
	for _, backfill := range []bool{false, true} {
		name := "按校准日期先后录入"
		if backfill {
			name = "较早证书事后补录"
		}
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{t: mustDate(t, "2026-09-20")}
			l := setupTwoPassingCerts(t, clock, backfill)
			assertLatestNewer(t, l)

			clock.t = mustDate(t, "2026-10-19")
			rBefore, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review before expiry: %v", err)
			}
			if !rBefore.CanUse || rBefore.Latest == nil || rBefore.Latest.Expired {
				t.Fatalf("截止日前核对应可使用且最近证书未到期：canUse=%v latest=%+v",
					rBefore.CanUse, rBefore.Latest)
			}

			// 截止日当天核对：两次连续核对都必须得到同一结论，且不产生使用记录。
			clock.t = mustDate(t, "2026-10-20")
			usageBefore := len(l.UsageRecords())
			r, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("review on expiry day: %v", err)
			}
			r2, err := l.Review("M-1")
			if err != nil {
				t.Fatalf("second review: %v", err)
			}
			if got := len(l.UsageRecords()); got != usageBefore {
				t.Fatalf("只读核对不应新增使用记录：%d -> %d", usageBefore, got)
			}
			for _, rv := range []*InstrumentReview{r, r2} {
				if rv.CanUse {
					t.Fatalf("截止日当天应不可使用，得到 %v", rv.Reasons)
				}
				if !containsReason(rv.Reasons, newerNumber) ||
					!containsReason(rv.Reasons, newerExpiry) {
					t.Fatalf("不可使用原因应指向 %s 及其截止日，得到 %v",
						newerNumber, rv.Reasons)
				}
				if containsReason(rv.Reasons, olderNumber) ||
					containsReason(rv.Reasons, "超差") {
					t.Fatalf("原因不应涉及旧证书或超差：%v", rv.Reasons)
				}
				if rv.Latest == nil || rv.Latest.Number != newerNumber ||
					!rv.Latest.Pass || !rv.Latest.Expired {
					t.Fatalf("最近证书应为已到期但仍合格的 %s：%+v", newerNumber, rv.Latest)
				}
				assertHistoryOnExpiryDay(t, rv)
				if rv.Instrument.Status != StatusInUse {
					t.Fatalf("核对不应改动器具状态，得到 %s", rv.Instrument.Status)
				}
			}
		})
	}
}

// TestBackfilledOlderLongerCertCannotReplaceExpiringNewer 明确保护补录场景：
// 较新证书先登记并先到期；较早证书在更晚日期补录、有效期更长。补录只增加
// 历史：最近证书仍是校准日期较新的一张；较新证书到期后申请仍被拒绝，
// 且这次拒绝在时钟继续前进后仍可查到、原因不被重写。
func TestBackfilledOlderLongerCertCannotReplaceExpiringNewer(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-09-20")}
	l := setupTwoPassingCerts(t, clock, true)
	assertLatestNewer(t, l)

	// 较新证书截止日当天产生一次因到期的拒绝（此时旧证书已补录在历史中）。
	clock.t = mustDate(t, "2026-10-20")
	d, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request on expiry day: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, newerNumber) ||
		!containsReason(d.Reasons, newerExpiry) ||
		containsReason(d.Reasons, olderNumber) {
		t.Fatalf("补录旧证书后，截止日当天仍应仅因 %s 到期拒绝，得到 allowed=%v reasons=%v",
			newerNumber, d.Allowed, d.Reasons)
	}

	// 时钟继续前进后再次核对：拒绝记录仍在，原因保持申请当时冻结的内容；
	// 旧证书仍未到期，新证书仍到期，最近证书不变。
	clock.t = mustDate(t, "2026-11-01")
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review later: %v", err)
	}
	if r.Latest == nil || r.Latest.Number != newerNumber || !r.Latest.Expired {
		t.Fatalf("最近证书仍应是已到期的 %s：%+v", newerNumber, r.Latest)
	}
	assertHistoryOnExpiryDay(t, r)
	if len(r.Rejections) != 1 {
		t.Fatalf("应能查到截止日当天那 1 次拒绝，得到 %d 条", len(r.Rejections))
	}
	rej := r.Rejections[0]
	if rej.Allowed {
		t.Fatal("留痕应保持拒绝结果")
	}
	if !strings.HasPrefix(rej.RequestedAt, "2026-10-20T") {
		t.Fatalf("拒绝记录应保留申请时间 2026-10-20，得到 %s", rej.RequestedAt)
	}
	if !containsReason(rej.Reasons, newerNumber) ||
		!containsReason(rej.Reasons, newerExpiry) ||
		containsReason(rej.Reasons, olderNumber) {
		t.Fatalf("拒绝原因应冻结为 %s 到期，补录旧证书不能重写：%v",
			newerNumber, rej.Reasons)
	}
}

// TestAllowedRequestBeforeExpiryStaysAllowedAfterExpiry 保护历史冻结的另一面：
// 截止日前已经获准并保存的申请保留原结果，之后最近证书到期不会把它改成拒绝。
// 到期后的新申请另行拒绝，新旧记录各自保留申请当时的结论。
func TestAllowedRequestBeforeExpiryStaysAllowedAfterExpiry(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-09-20")}
	l := setupTwoPassingCerts(t, clock, true)

	// 截止日前一天获准并保存。
	clock.t = mustDate(t, "2026-10-19")
	ok, err := l.RequestUse("M-1")
	if err != nil || !ok.Allowed {
		t.Fatalf("截止日前应获准，err=%v reasons=%v", err, ok.Reasons)
	}

	// 截止日当天再次申请被拒绝。
	clock.t = mustDate(t, "2026-10-20")
	bad, err := l.RequestUse("M-1")
	if err != nil || bad == nil || bad.Allowed {
		t.Fatalf("截止日当天应拒绝，err=%v decision=%+v", err, bad)
	}

	recs := l.UsageRecords()
	if len(recs) != 2 {
		t.Fatalf("应保留两次申请，得到 %d 条", len(recs))
	}
	// 按实际时刻排序：获准在前、拒绝在后；两条记录的结论各自冻结。
	gotAllowed, gotRejected := recs[0], recs[1]
	if !gotAllowed.Allowed || len(gotAllowed.Reasons) != 0 {
		t.Fatalf("较早的获准记录不应被后来的到期改成拒绝：%+v", gotAllowed)
	}
	if !strings.HasPrefix(gotAllowed.RequestedAt, "2026-10-19T") {
		t.Fatalf("获准记录时间应保留为 2026-10-19，得到 %s", gotAllowed.RequestedAt)
	}
	if gotRejected.Allowed ||
		!containsReason(gotRejected.Reasons, newerNumber) ||
		!containsReason(gotRejected.Reasons, newerExpiry) {
		t.Fatalf("较晚的拒绝记录应保留 %s 到期原因：%+v", newerNumber, gotRejected)
	}

	// Review 只汇总被拒绝记录；获准记录不出现在拒绝列表里，拒绝列表内容
	// 不被后续操作改写。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Rejections) != 1 || r.Rejections[0].Allowed {
		t.Fatalf("核对中只应有那 1 条拒绝，得到 %+v", r.Rejections)
	}
}

// TestExpiryBoundarySurvivesReopen 保护跨进程持久化：截止日前的获准与
// 截止日当天的拒绝都保存进台账文件后，用较晚日期重新打开同一文件，
// 最近证书仍是较新证书且已到期、旧证书未到期，历史记录原样可查；
// 结论不依赖内存中的假时钟状态。
func TestExpiryBoundarySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	clock := &fakeClock{t: mustDate(t, "2026-09-20")}
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: newerNumber, CalDate: newerCal,
		Expiry: newerExpiry, Method: "规范A", Error: -0.1, Summary: "较新一次校准",
	})
	clock.t = mustDate(t, "2026-09-25")
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: olderNumber, CalDate: olderCal,
		Expiry: olderExpiry, Method: "规范A", Error: 0.4, Summary: "补录的较早校准",
	})

	clock.t = mustDate(t, "2026-10-19")
	if ok, err := l.RequestUse("M-1"); err != nil || !ok.Allowed {
		t.Fatalf("截止日前获准写入失败: %v", err)
	}
	clock.t = mustDate(t, "2026-10-20")
	if bad, err := l.RequestUse("M-1"); err != nil || bad.Allowed {
		t.Fatalf("截止日当天拒绝写入失败: %v", err)
	}

	// 用晚得多的日期重新打开：较新证书到期、旧证书仍未到期，结论不变。
	reopened, err := openAt(path, func() time.Time { return mustDate(t, "2027-01-05") })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review after reopen: %v", err)
	}
	if r.CanUse {
		t.Fatalf("重开后仍应因最近证书到期而不可使用：%v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Number != newerNumber || !r.Latest.Expired || !r.Latest.Pass {
		t.Fatalf("重开后最近证书应是已到期仍合格的 %s：%+v", newerNumber, r.Latest)
	}
	if len(r.History) != 2 {
		t.Fatalf("重开后历史应有两张证书，得到 %d", len(r.History))
	}
	old := r.History[1]
	if old.Number != olderNumber || old.Expired || !old.Pass {
		t.Fatalf("重开后旧证书应仍合格未到期：%+v", old)
	}
	recs := reopened.UsageRecords()
	if len(recs) != 2 {
		t.Fatalf("重开后两次申请记录应原样保留，得到 %+v", recs)
	}
	if !recs[0].Allowed {
		t.Fatal("重开后截止日前的获准记录被改写")
	}
	if recs[1].Allowed ||
		!containsReason(recs[1].Reasons, newerNumber) ||
		!containsReason(recs[1].Reasons, newerExpiry) {
		t.Fatalf("重开后拒绝记录应保留 %s 到期原因：%+v", newerNumber, recs[1])
	}
}
