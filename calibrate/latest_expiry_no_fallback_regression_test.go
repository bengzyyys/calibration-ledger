package calibrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“申请使用器具”补充可重复执行的回归保障，重点保护：
//
//	“最近证书到期后，不能借用较早证书继续获准使用”。
//
// 场景：器具保持在用，两张证书都按允许误差判为合格；校准日期较新的证书
// （C-NEW）先到期，较早证书（C-OLD）的有效期仍覆盖“新证截止日”申请当天。
// 即便历史中还有一张合格且未到期的旧证书，最近证书（按校准日期确定，与录入
// 先后、编号大小、有效期长短都无关）到期也必须拒绝使用，绝不回退到旧证书。
//
// 为保证日历日期变化后仍可重复执行，所有日期都从运行时的当天（每个测试开始
// 时取一次，并非写死的未来日期）派生：
//
//	newCal = 当天前 40 天    （较新校准日期）
//	oldCal = 当天前 200 天   （较早校准日期）
//	newExp = 当天前 1 天     （较新证书截止日；“当天”的前一天即截止日当天）
//	oldExp = 当天后 400 天   （较早证书有效期更长，仍覆盖全部申请当天）
//
// 于是测试用时钟只需要落在三个相对位置：截止日前两天（两张都未到期）、
// 截止日当天（新证到期、旧证未到期）以及更晚（新证持续到期、旧证仍未到期）。
// C-OLD 校准更早、有效期却更长（录入也可能更晚），这些都不能让它成为最近
// 证书或顶替已到期的 C-NEW 决定使用资格。

// dynDates 是从运行当天派生的两张证书日期与关键时钟位置。
type dynDates struct {
	anchor   time.Time // 运行当天（截止日的次日）
	before   time.Time // 新证截止日前两天：两张证书都未到期
	deadline time.Time // 新证截止日当天：新证到期，旧证仍未到期
	newCal   string
	oldCal   string
	newExp   string
	oldExp   string
}

func dynTwoCertDates(t *testing.T) dynDates {
	t.Helper()
	// 锚点不写死任何日历日期：取运行当天，保证“截止日前 / 截止日当天”的
	// 区别在任何日期运行都能被覆盖，不依赖某个固定日期恰好仍在未来。
	anchor := time.Now()
	return dynDates{
		anchor:   anchor,
		before:   anchor.AddDate(0, 0, -2),
		deadline: anchor.AddDate(0, 0, -1),
		newCal:   anchor.AddDate(0, 0, -40).Format(DateLayout),
		oldCal:   anchor.AddDate(0, 0, -200).Format(DateLayout),
		newExp:   anchor.AddDate(0, 0, -1).Format(DateLayout),
		oldExp:   anchor.AddDate(0, 0, 400).Format(DateLayout),
	}
}

// newerPassingCert 与 olderPassingCert 都判合格：测得误差绝对值 0.1/0.2，
// 均不超过器具允许误差 0.5。二者校准日期、截止日、编号都不同。
func newerPassingCert(d dynDates) CertificateInput {
	return CertificateInput{
		InstrumentID: "M-1", Number: "C-NEW", CalDate: d.newCal,
		Expiry: d.newExp, Method: "规范A", Error: 0.1, Summary: "校准日期较新的证书",
	}
}

func olderPassingCert(d dynDates) CertificateInput {
	return CertificateInput{
		InstrumentID: "M-1", Number: "C-OLD", CalDate: d.oldCal,
		Expiry: d.oldExp, Method: "规范A", Error: -0.2, Summary: "校准日期较早、有效期更长的证书",
	}
}

// assertLatestIsNewExpired 核对“最近证书是已到期的 C-NEW，旧证书仍合格未到期”：
// 最近证书与历史第一张都必须是校准日期较新的 C-NEW，且两张证书各自保留
// 合格结论与截止日——不能把旧证书也显示成到期，也不能把新证书的到期误说成超差。
func assertLatestIsNewExpired(t *testing.T, r *InstrumentReview, d dynDates) {
	t.Helper()
	if r.CanUse {
		t.Fatalf("最近证书到期时不应可使用，得到 reasons=%v", r.Reasons)
	}
	if !containsReason(r.Reasons, "到期") ||
		!containsReason(r.Reasons, "C-NEW") || !containsReason(r.Reasons, d.newExp) {
		t.Fatalf("拒绝原因应点名真正到期的最近证书 C-NEW 及其截止日 %s，得到 %v",
			d.newExp, r.Reasons)
	}
	if containsReason(r.Reasons, "C-OLD") {
		t.Fatalf("不应回退或牵连仍合格未到期的旧证书 C-OLD：%v", r.Reasons)
	}
	if containsReason(r.Reasons, "超差") {
		t.Fatalf("两张证书都合格，不能把到期误说成超差：%v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Number != "C-NEW" {
		t.Fatalf("最近证书必须仍是校准日期较新的 C-NEW，得到 %+v", r.Latest)
	}
	if !r.Latest.Pass {
		t.Fatalf("最近证书 C-NEW 按允许误差仍判合格，到期不等于超差：%+v", r.Latest)
	}
	if !r.Latest.Expired || r.Latest.Expiry != d.newExp || r.Latest.CalDate != d.newCal {
		t.Fatalf("最近证书 C-NEW 应显示已到期并保留原截止日/校准日：%+v", r.Latest)
	}
	if len(r.History) != 2 {
		t.Fatalf("历史应同时保留两张证书，得到 %d 张", len(r.History))
	}
	// 历史按校准日期由近到远列出：C-NEW 在前、C-OLD 在后。
	n, o := r.History[0], r.History[1]
	if n.Number != "C-NEW" || !n.Pass || !n.Expired || n.Expiry != d.newExp {
		t.Fatalf("历史第一张应为已到期但合格的较新证书 C-NEW，得到 %+v", n)
	}
	if o.Number != "C-OLD" || !o.Pass || o.Expired || o.Expiry != d.oldExp {
		t.Fatalf("历史第二张应为合格且未到期的较早证书 C-OLD（不能被显示成到期），得到 %+v", o)
	}
}

// TestLatestExpiredCertBlocksUseWithoutFallingBackToOlderCert 覆盖主场景：
// 两张合格证书共存，较新证书先到期、旧证书有效期仍覆盖申请当天。截止日前申请
// 依据较新证书获准；截止日当天必须因该最近证书到期而拒绝，不能因为历史里还有
// 合格未到期的旧证书而沿用旧结论；继续推进日历，结论保持拒绝。
func TestLatestExpiredCertBlocksUseWithoutFallingBackToOlderCert(t *testing.T) {
	d := dynTwoCertDates(t)
	clock := &fakeClock{t: d.before}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	// 先录较新证书，再补较早但有效期更长的证书；两张均合格。
	mustAddCert(t, l, newerPassingCert(d))
	mustAddCert(t, l, olderPassingCert(d))

	// 截止日前：最近证书是校准日期较新的 C-NEW，两张都未到期，申请获准。
	can, err := l.CanUse("M-1")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if !can.Allowed || len(can.Reasons) != 0 {
		t.Fatalf("截止日前最近证书合格未到期应获准，得到 allowed=%v reasons=%v",
			can.Allowed, can.Reasons)
	}
	if can.Latest == nil || can.Latest.Number != "C-NEW" || !can.Latest.Pass || can.Latest.Expired {
		t.Fatalf("截止日前最近证书应为合格未到期的 C-NEW：%+v", can.Latest)
	}
	allowed, err := l.RequestUse("M-1")
	if err != nil || !allowed.Allowed {
		t.Fatalf("截止日前申请应获准并留痕，err=%v allowed=%v", err, allowed)
	}
	rBefore, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review before: %v", err)
	}
	if !rBefore.CanUse || len(rBefore.History) != 2 ||
		rBefore.History[0].Number != "C-NEW" || rBefore.History[1].Number != "C-OLD" {
		t.Fatalf("截止日前历史应按校准日期由近到远为 C-NEW、C-OLD 且可用：%+v",
			rBefore.History)
	}
	if rBefore.History[0].Expired || rBefore.History[1].Expired {
		t.Fatalf("截止日前两张证书都应未到期：%+v", rBefore.History)
	}

	// 只把时钟拨到较新证书截止日当天（不改任何数据）：申请必须因最近证书
	// C-NEW 到期被拒绝，即使 C-OLD 合格且未到期也不能回退借用。
	clock.t = d.deadline
	rejected, err := l.RequestUse("M-1")
	if err != nil {
		t.Fatalf("request use on deadline: %v", err)
	}
	if rejected.Allowed ||
		!containsReason(rejected.Reasons, "到期") ||
		!containsReason(rejected.Reasons, "C-NEW") ||
		!containsReason(rejected.Reasons, d.newExp) ||
		!containsReason(rejected.Reasons, "截止日当天即到期") {
		t.Fatalf("截止日当天应因最近证书 C-NEW 到期拒绝，得到 allowed=%v reasons=%v",
			rejected.Allowed, rejected.Reasons)
	}
	if containsReason(rejected.Reasons, "C-OLD") || containsReason(rejected.Reasons, "超差") {
		t.Fatalf("拒绝原因既不能借用旧证书也不能误报超差：%v", rejected.Reasons)
	}

	rDeadline, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review deadline: %v", err)
	}
	assertLatestIsNewExpired(t, rDeadline, d)
	// 截止日前的获准申请不是拒绝记录；当天的拒绝应恰好 1 条并冻结到期原因。
	if len(rDeadline.Rejections) != 1 {
		t.Fatalf("应只看到截止日当天这 1 条拒绝，得到 %d 条", len(rDeadline.Rejections))
	}
	rej := rDeadline.Rejections[0]
	if !strings.HasPrefix(rej.RequestedAt, d.deadline.Format(DateLayout)) {
		t.Fatalf("拒绝记录应保留截止日当天的申请时间 %s，得到 %s",
			d.deadline.Format(DateLayout), rej.RequestedAt)
	}
	if !containsReason(rej.Reasons, "C-NEW") || !containsReason(rej.Reasons, d.newExp) {
		t.Fatalf("拒绝留痕应冻结当时的到期原因（最近证书 C-NEW 与截止日）：%+v", rej)
	}

	// 日历继续前进：较新证书持续到期，旧证书仍未到期，结论保持拒绝。
	clock.t = d.anchor.AddDate(0, 0, 30)
	rLater, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review later: %v", err)
	}
	assertLatestIsNewExpired(t, rLater, d)
	canLater, _ := l.CanUse("M-1")
	if canLater.Allowed {
		t.Fatalf("最近证书到期后不能因旧证书仍有效而获准：%v", canLater.Reasons)
	}

	// 使用记录共两条：先获准（空原因）、后拒绝（到期原因冻结），先后与内容
	// 都不因日历推进或补录而改变。
	recs := l.UsageRecords()
	if len(recs) != 2 {
		t.Fatalf("应保留截止日前获准与截止日拒绝两条申请，得到 %d 条", len(recs))
	}
	if !recs[0].Allowed || len(recs[0].Reasons) != 0 ||
		!strings.HasPrefix(recs[0].RequestedAt, d.before.Format(DateLayout)) {
		t.Fatalf("第一条应为截止日前的获准记录并保留当时时间，得到 %+v", recs[0])
	}
	if recs[1].Allowed || !containsReason(recs[1].Reasons, "C-NEW") {
		t.Fatalf("第二条应为到期拒绝记录，得到 %+v", recs[1])
	}
}

// TestBackfillOlderCertAfterLatestExpiredKeepsRejection 守住录入先后无关：
// 较新证书先录入并已到期、当天的拒绝也已保存之后，才补录校准较早、录入更晚、
// 有效期更长的合格证书。补录只增加历史：最近证书仍是已到期的 C-NEW，新的
// 申请仍因 C-NEW 到期被拒；旧证书不会顶替，也不会重写此前已保存的拒绝原因。
func TestBackfillOlderCertAfterLatestExpiredKeepsRejection(t *testing.T) {
	d := dynTwoCertDates(t)
	clock := &fakeClock{t: d.before}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	// 先只有校准日期较新的证书。
	mustAddCert(t, l, newerPassingCert(d))

	// 截止日前获准并保存。
	allowed, err := l.RequestUse("M-1")
	if err != nil || !allowed.Allowed {
		t.Fatalf("截止日前应获准，err=%v allowed=%v", err, allowed)
	}
	allowedAt := allowed.evaluatedAt.Format(time.RFC3339)

	// 到较新证书截止日当天：拒绝并保存。
	clock.t = d.deadline
	onDeadline, err := l.RequestUse("M-1")
	if err != nil || onDeadline.Allowed || !containsReason(onDeadline.Reasons, "C-NEW") {
		t.Fatalf("截止日当天应因 C-NEW 到期拒绝，err=%v d=%+v", err, onDeadline)
	}
	frozenReason := append([]string(nil), onDeadline.Reasons...)

	// 在更晚的日期（较新证书已到期之后）补录校准更早、录入更晚、有效期更长
	// 的合格旧证书。
	clock.t = d.anchor.AddDate(0, 0, 30)
	mustAddCert(t, l, olderPassingCert(d))
	newCert := l.findCertificate("C-NEW")
	oldCert := l.findCertificate("C-OLD")
	if newCert == nil || oldCert == nil {
		t.Fatal("两张证书都应在历史中")
	}
	if oldCert.CreatedAt <= newCert.CreatedAt {
		t.Fatalf("旧证书补录时间应更晚：C-OLD=%s C-NEW=%s", oldCert.CreatedAt, newCert.CreatedAt)
	}
	if oldCert.CalDate >= newCert.CalDate {
		t.Fatalf("旧证书校准日期应更早：C-OLD=%s C-NEW=%s", oldCert.CalDate, newCert.CalDate)
	}
	if oldCert.Expiry <= newCert.Expiry {
		t.Fatalf("旧证书有效期应更长：C-OLD=%s C-NEW=%s", oldCert.Expiry, newCert.Expiry)
	}

	// 最近证书只按校准日期取：录入更晚、有效期更长都不能让 C-OLD 顶替 C-NEW。
	if latest := l.LatestCertificate("M-1"); latest == nil || latest.Number != "C-NEW" {
		t.Fatalf("补录后最近证书仍应是校准日期较新的 C-NEW，得到 %+v", latest)
	}
	can, _ := l.CanUse("M-1")
	if can.Allowed || !containsReason(can.Reasons, "C-NEW") ||
		!containsReason(can.Reasons, d.newExp) || containsReason(can.Reasons, "C-OLD") {
		t.Fatalf("补录旧证书后仍应只因 C-NEW 到期拒绝，得到 allowed=%v reasons=%v",
			can.Allowed, can.Reasons)
	}

	// 再申请一次：仍是到期拒绝并新增留痕；原因仍只点名 C-NEW。
	again, err := l.RequestUse("M-1")
	if err != nil || again.Allowed || !containsReason(again.Reasons, "C-NEW") ||
		containsReason(again.Reasons, "C-OLD") {
		t.Fatalf("补录后申请仍应因 C-NEW 到期拒绝，err=%v d=%+v", err, again)
	}

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	assertLatestIsNewExpired(t, r, d)
	// 两条拒绝（截止日当天、补录后）原因内容一致；补录没有重写第一条历史，
	// 截止日前的获准记录也没有被改成拒绝。
	if len(r.Rejections) != 2 {
		t.Fatalf("应有截止日与补录后两条拒绝，得到 %d 条", len(r.Rejections))
	}
	if !sameReasons(r.Rejections[0].Reasons, frozenReason) {
		t.Fatalf("补录旧证书重写了此前已保存的拒绝原因：保存=%v 现在=%v",
			frozenReason, r.Rejections[0].Reasons)
	}
	if !sameReasons(r.Rejections[1].Reasons, frozenReason) {
		t.Fatalf("补录后的拒绝原因应与截止日当天一致，得到 %v", r.Rejections[1].Reasons)
	}
	recs := l.UsageRecords()
	if len(recs) != 3 {
		t.Fatalf("记录次序应为 获准/拒绝/拒绝，得到 %d 条：%+v", len(recs), recs)
	}
	if !recs[0].Allowed {
		t.Fatalf("第一条应为截止日前获准，得到 %+v", recs[0])
	}
	if recs[1].Allowed || recs[2].Allowed {
		t.Fatalf("后两条都应为到期拒绝，得到 %+v", recs)
	}
	if recs[0].RequestedAt != allowedAt || len(recs[0].Reasons) != 0 {
		t.Fatalf("截止日前已保存的获准记录不应被到期或补录改写：%+v", recs[0])
	}
	if !sameReasons(recs[1].Reasons, frozenReason) || !sameReasons(recs[2].Reasons, frozenReason) {
		t.Fatalf("两条拒绝都应保留相同的冻结到期原因：%+v", recs)
	}
}

// TestLatestCertificatePicksNewerCalDateRegardlessOfValidityAndOrder 直接守住
// “最近证书只按校准日期决定”：无论旧证书先录还是后录、有效期更长还是编号
// 大小如何，只要 C-NEW 校准日期更新，它就是最近证书；截止日当天的到期判断
// 也只挂在 C-NEW 上。
func TestLatestCertificatePicksNewerCalDateRegardlessOfValidityAndOrder(t *testing.T) {
	d := dynTwoCertDates(t)

	for _, newerFirst := range []bool{true, false} {
		name := "较新证书先录入"
		if !newerFirst {
			name = "较新证书后录入"
		}
		t.Run(name, func(t *testing.T) {
			clock := &fakeClock{t: d.before}
			l := newTestLedger(t, clock)
			mustRegister(t, l, "M-1", "万用表", 0.5)
			_ = l.SetStatus("M-1", StatusInUse)
			if newerFirst {
				mustAddCert(t, l, newerPassingCert(d))
				mustAddCert(t, l, olderPassingCert(d))
			} else {
				mustAddCert(t, l, olderPassingCert(d))
				mustAddCert(t, l, newerPassingCert(d))
			}

			latest := l.LatestCertificate("M-1")
			if latest == nil || latest.Number != "C-NEW" {
				t.Fatalf("最近证书应只按校准日期取 C-NEW（录入先后无关），得到 %+v", latest)
			}
			cs := l.certificatesOf("M-1")
			if len(cs) != 2 || cs[0].Number != "C-NEW" || cs[1].Number != "C-OLD" {
				t.Fatalf("历史应始终按校准日期由近到远为 C-NEW、C-OLD：%+v", cs)
			}
			// 有效期更长的是 C-OLD，但这不能改变最近证书；截止日前两者都未到期。
			can, _ := l.CanUse("M-1")
			if !can.Allowed || can.Latest == nil || can.Latest.Number != "C-NEW" {
				t.Fatalf("截止日前应依据 C-NEW 获准，得到 %+v", can)
			}

			clock.t = d.deadline
			can, _ = l.CanUse("M-1")
			if can.Allowed || !containsReason(can.Reasons, "C-NEW") ||
				containsReason(can.Reasons, "C-OLD") {
				t.Fatalf("截止日当天应只按最近证书 C-NEW 判到期，得到 %v", can.Reasons)
			}
		})
	}
}

// TestReviewOfExpiredLatestDoesNotRecordUsage 核对只查询：在最近证书已到期的
// 情形下反复核对（含跨过若干天、重开文件后核对），证书与使用记录都必须保持
// 不变，不能把一次查询变成新的使用申请，也不能改写台账文件。
func TestReviewOfExpiredLatestDoesNotRecordUsage(t *testing.T) {
	d := dynTwoCertDates(t)
	clock := &fakeClock{t: d.before}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, newerPassingCert(d))
	mustAddCert(t, l, olderPassingCert(d))

	clock.t = d.deadline
	rawBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, at := range []time.Time{d.deadline, d.anchor, d.anchor.AddDate(0, 0, 10)} {
		clock.t = at
		r, err := l.Review("M-1")
		if err != nil {
			t.Fatalf("review at %s: %v", at.Format(DateLayout), err)
		}
		assertLatestIsNewExpired(t, r, d)
		if len(r.Rejections) != 0 {
			t.Fatalf("只核对不应产生任何拒绝记录，得到 %d 条", len(r.Rejections))
		}
		// CanUse / UsageRecords 同样是只读入口。
		if _, err := l.CanUse("M-1"); err != nil {
			t.Fatalf("can use: %v", err)
		}
		if recs := l.UsageRecords(); len(recs) != 0 {
			t.Fatalf("查询不应新增使用申请，得到 %d 条", len(recs))
		}
	}
	rawAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger after: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatal("只读核对不应改写台账文件")
	}

	// 重新打开同一文件核对：仍无任何使用记录，两张证书结论与截止日不变。
	reopened, err := openAt(path, func() time.Time { return d.anchor.AddDate(0, 0, 20) })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if recs := reopened.UsageRecords(); len(recs) != 0 {
		t.Fatalf("重开后仍不应有查询产生的使用记录，得到 %d 条", len(recs))
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review reopened: %v", err)
	}
	assertLatestIsNewExpired(t, r, d)
}

// TestExpiredLatestHistoryFrozenAndAllowedRecordPreservedAcrossReopen 验证
// 持久化与历史冻结：截止日前获准、截止日当天拒绝都保存后，把日历拨到更晚并
// 重新打开同一台账——已保存的拒绝保留申请时间与当时的到期原因，已获准的记录
// 仍是获准（不因子日后最近证书到期而被改成拒绝）；补录旧证书也不重写历史。
func TestExpiredLatestHistoryFrozenAndAllowedRecordPreservedAcrossReopen(t *testing.T) {
	d := dynTwoCertDates(t)
	clock := &fakeClock{t: d.before}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, newerPassingCert(d))

	allowed, err := l.RequestUse("M-1")
	if err != nil || !allowed.Allowed {
		t.Fatalf("截止日前应获准，err=%v allowed=%v", err, allowed)
	}
	clock.t = d.deadline
	rejected, err := l.RequestUse("M-1")
	if err != nil || rejected.Allowed || !containsReason(rejected.Reasons, d.newExp) {
		t.Fatalf("截止日当天应因 C-NEW 到期拒绝，err=%v d=%+v", err, rejected)
	}
	frozen := append([]string(nil), rejected.Reasons...)

	// 在更晚日期重开同一台账（无需重新打开即可操作的同一对象结论一致，
	// 这里额外验证文件持久化）：当前不可用，但历史结论不被重算。
	reopened, err := openAt(path, func() time.Time { return d.anchor.AddDate(0, 0, 90) })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	recs := reopened.UsageRecords()
	if len(recs) != 2 {
		t.Fatalf("应保留两条历史申请，得到 %d", len(recs))
	}
	if !recs[0].Allowed || len(recs[0].Reasons) != 0 ||
		!strings.HasPrefix(recs[0].RequestedAt, d.before.Format(DateLayout)) {
		t.Fatalf("截止日前已获准并保存的申请不应被后来的到期改成拒绝：%+v", recs[0])
	}
	if recs[1].Allowed || !sameReasons(recs[1].Reasons, frozen) ||
		!strings.HasPrefix(recs[1].RequestedAt, d.deadline.Format(DateLayout)) {
		t.Fatalf("拒绝记录应保留申请时间与当时的到期原因：%+v", recs[1])
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse || !containsReason(r.Reasons, "C-NEW") {
		t.Fatalf("重开后当前仍应因最近证书到期不可用：%v", r.Reasons)
	}
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, frozen) {
		t.Fatalf("核对中的历史拒绝原因应冻结：保存=%v 现在=%+v", frozen, r.Rejections)
	}

	// 在重开的台账上补录较早、有效期更长的合格证书：当前仍拒绝，且两条历史
	// （一条获准、一条拒绝）的结果与原因都不被重写。
	reopened.now = func() time.Time { return d.anchor.AddDate(0, 0, 95) }
	if _, _, err := reopened.AddCertificate(olderPassingCert(d)); err != nil {
		t.Fatalf("backfill older cert: %v", err)
	}
	if latest := reopened.LatestCertificate("M-1"); latest == nil || latest.Number != "C-NEW" {
		t.Fatalf("补录后最近证书仍应是 C-NEW，得到 %+v", latest)
	}
	recs = reopened.UsageRecords()
	if len(recs) != 2 ||
		!recs[0].Allowed || recs[1].Allowed ||
		!sameReasons(recs[1].Reasons, frozen) {
		t.Fatalf("补录旧证书重写了历史申请：%+v", recs)
	}
}
