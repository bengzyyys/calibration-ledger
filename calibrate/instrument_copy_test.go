package calibrate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// checkOfficialInstrument 核对拿到的器具内容与正式登记信息逐字段一致。
func checkOfficialInstrument(t *testing.T, got *Instrument, want Instrument) {
	t.Helper()
	if got == nil {
		t.Fatalf("器具 %s 应存在", want.ID)
	}
	if got.ID != want.ID || got.Name != want.Name ||
		got.AllowedError != want.AllowedError || got.Status != want.Status ||
		got.RegisteredAt != want.RegisteredAt {
		t.Fatalf("正式登记信息被返回结果改写：应 %+v，实得 %+v", want, *got)
	}
}

// hasOutOfToleranceReason 判断拒绝原因中是否含“超差”并按指定正式限值说明。
// 台账原因用 %g 输出限值，这里按同样形式比较。
func hasOutOfToleranceReason(reasons []string, officialLimit float64) bool {
	limitText := fmt.Sprintf("%g", officialLimit)
	for _, r := range reasons {
		if strings.Contains(r, "超差") && strings.Contains(r, limitText) {
			return true
		}
	}
	return false
}

// TestReturnedInstrumentMutationCannotRewriteLedger 验证调用方整理登记成功、
// 按编号查询与列出器具返回的器具信息，不会改写台账里的正式登记：
// 改编号不能让原器具消失、释放原编号或新增另一件器具；改名称、允许误差、
// 状态和登记时间也只影响这一份展示数据，且不会被此后的正常写盘带入文件。
func TestReturnedInstrumentMutationCannotRewriteLedger(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	registered, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	mustRegister(t, l, "M-2", "温度计", 0.2)
	want := *registered

	// 调用方整理“登记成功”返回结果：编号、名称、允许误差、状态、登记时间全改。
	registered.ID = "M-COPY"
	registered.Name = "展示用名称"
	registered.AllowedError = 9.9
	registered.Status = StatusInUse
	registered.RegisteredAt = "2000-01-01T00:00:00Z"

	// 按原编号查询仍得到实际保存的登记信息。
	got, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("原器具不应消失：%v", err)
	}
	checkOfficialInstrument(t, got, want)

	// 原编号仍被占用；只在副本中出现的新编号仍报告不存在，器具数量不变。
	if _, err := l.Register(RegisterInput{ID: "M-1", Name: "另一件", AllowedError: 1}); !IsValidation(err) {
		t.Fatalf("原编号仍应被拒绝登记，得到 %v", err)
	}
	if _, err := l.Instrument("M-COPY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("副本里的新编号不应真的存在，得到 %v", err)
	}
	if list := l.Instruments(); len(list) != 2 {
		t.Fatalf("整理返回结果不应新增或减少器具，得到 %d 件", len(list))
	}

	// 按编号查询拿到副本后再整理，同样不影响正式登记与核对视图。
	fetched, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	fetched.ID = "M-QUERY"
	fetched.Name = "查询副本名称"
	fetched.AllowedError = 7.0
	fetched.Status = StatusRetired
	fetched.RegisteredAt = "2001-02-03T04:05:06Z"
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	checkOfficialInstrument(t, &r.Instrument, want)
	if _, err := l.Instrument("M-QUERY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查询副本里的新编号不应真的存在，得到 %v", err)
	}

	// 整理返回列表：改掉其中一件的全部字段，并删去另一件的条目，
	// 只影响这一份列表；正式器具的数量和内容保持不变。
	list := l.Instruments()
	for i := range list {
		if list[i].ID == "M-1" {
			list[i].ID = "M-LIST"
			list[i].Name = "列表副本名称"
			list[i].AllowedError = 3.3
			list[i].Status = StatusRetired
			list[i].RegisteredAt = "2002-03-04T05:06:07Z"
		}
	}
	list = list[:1] // 调用方从自己的列表里删去 M-2 条目
	if len(list) != 1 {
		t.Fatalf("测试前提失效：删条目前列表应已含 2 件")
	}
	fresh := l.Instruments()
	if len(fresh) != 2 {
		t.Fatalf("删去列表条目不应减少正式器具，得到 %d 件", len(fresh))
	}
	checkOfficialInstrument(t, &fresh[0], want) // 已按编号排序，M-1 在前
	other, err := l.Instrument("M-2")
	if err != nil {
		t.Fatalf("被删条目对应的正式器具不应消失：%v", err)
	}
	if other.Name != "温度计" || other.AllowedError != 0.2 || other.Status != StatusPending {
		t.Fatalf("被删条目对应的正式登记被改动：%+v", other)
	}
	if _, err := l.Instrument("M-LIST"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("列表副本里的新编号不应真的存在，得到 %v", err)
	}

	// 一次正常保存（状态切换）之后重开同一台账，读到的仍是正式登记信息。
	if err := l.SetStatus("M-2", StatusInUse); err != nil {
		t.Fatalf("set status: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopenedList := reopened.Instruments()
	if len(reopenedList) != 2 {
		t.Fatalf("重开后正式器具数量应为 2，得到 %d", len(reopenedList))
	}
	got, err = reopened.Instrument("M-1")
	if err != nil {
		t.Fatalf("重开后原器具应仍在：%v", err)
	}
	checkOfficialInstrument(t, got, want)
	if _, err := reopened.Instrument("M-COPY"); !errors.Is(err, ErrNotFound) {
		t.Fatal("重开后副本里的新编号不应出现")
	}
}

// TestReturnedInstrumentAllowedErrorMutationKeepsOutOfTolerance 验证一件在用
// 器具最近证书未到期但测得误差超登记限值时，调大返回结果的允许误差：
// 核对仍显示超差，申请使用仍被拒绝，原因仍按正式限值说明，证书内容不变。
func TestReturnedInstrumentAllowedErrorMutationKeepsOutOfTolerance(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "比对法", Error: 0.8, Summary: "例行校准",
	})

	inst, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	inst.AllowedError = 5.0

	// 核对仍按正式限值 0.5 判定超差。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Latest == nil || r.Latest.Number != "C-1" || r.Latest.Error != 0.8 || r.Latest.Pass {
		t.Fatalf("核对应仍按正式限值显示超差：%+v", r.Latest)
	}
	if r.CanUse || !hasOutOfToleranceReason(r.Reasons, 0.5) {
		t.Fatalf("核对应仍因超差拒绝并按正式限值 0.5 说明，得到 %v", r.Reasons)
	}

	// 只评估与正式申请使用都仍被拒绝，原因仍写正式限值 0.5。
	d, err := l.CanUse("M-1")
	if err != nil || d.Allowed || !hasOutOfToleranceReason(d.Reasons, 0.5) {
		t.Fatalf("调大副本限值不应恢复使用资格，err=%v allowed=%v reasons=%v",
			err, d.Allowed, d.Reasons)
	}
	d2, err := l.RequestUse("M-1")
	if err != nil || d2.Allowed || !hasOutOfToleranceReason(d2.Reasons, 0.5) {
		t.Fatalf("申请使用仍应被拒绝并按正式限值说明，err=%v allowed=%v reasons=%v",
			err, d2.Allowed, d2.Reasons)
	}

	// 整理核对结果里的允许误差同样无效。
	r.Instrument.AllowedError = 10
	d3, err := l.CanUse("M-1")
	if err != nil || d3.Allowed {
		t.Fatalf("核对副本限值被调大不应影响使用判断，err=%v allowed=%v", err, d3.Allowed)
	}

	// 正式限值与证书内容、归属不变。
	got, err := l.Instrument("M-1")
	if err != nil || got.AllowedError != 0.5 {
		t.Fatalf("正式允许误差被改写：%+v err=%v", got, err)
	}
	latest := l.LatestCertificate("M-1")
	if latest == nil || latest.Number != "C-1" || latest.Error != 0.8 ||
		latest.Expiry != "2027-09-01" || latest.InstrumentID != "M-1" {
		t.Fatalf("证书内容与归属不应改变：%+v", latest)
	}
}

// TestReturnedInstrumentStatusMutationCannotChangeEligibility 验证把返回结果
// 的状态改成在用不能解除停用限制；原本允许使用的器具也不会因为副本被调低
// 限值或改成停用而失去资格。证书内容与归属不随这些整理改变。
func TestReturnedInstrumentStatusMutationCannotChangeEligibility(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	passingCert := func(id, number string, errV float64) CertificateInput {
		return CertificateInput{
			InstrumentID: id, Number: number, CalDate: "2026-09-01",
			Expiry: "2027-09-01", Method: "比对法", Error: errV, Summary: "例行校准",
		}
	}

	// 停用器具：持有合格且未到期的证书。
	mustRegister(t, l, "M-RET", "停用表", 0.5)
	mustAddCert(t, l, passingCert("M-RET", "C-RET", 0.1))
	_ = l.SetStatus("M-RET", StatusRetired)
	retired, err := l.Instrument("M-RET")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	retired.Status = StatusInUse
	retired.ID = "M-FAKE"

	d, err := l.CanUse("M-RET")
	if err != nil {
		t.Fatalf("can use: %v", err)
	}
	if d.Allowed || !containsReason(d.Reasons, "停用") {
		t.Fatalf("停用限制不应被副本状态解除，allowed=%v reasons=%v", d.Allowed, d.Reasons)
	}
	d2, err := l.RequestUse("M-RET")
	if err != nil || d2.Allowed || !containsReason(d2.Reasons, "停用") {
		t.Fatalf("停用器具申请使用仍应被拒绝，err=%v allowed=%v reasons=%v",
			err, d2.Allowed, d2.Reasons)
	}
	// 证书归属不随副本编号改变而转移。
	if latest := l.LatestCertificate("M-RET"); latest == nil || latest.Number != "C-RET" {
		t.Fatalf("停用器具的证书不应丢失或被转移：%+v", latest)
	}
	if latest := l.LatestCertificate("M-FAKE"); latest != nil {
		t.Fatalf("证书不应随副本编号转移到新编号：%+v", latest)
	}

	// 在用器具：最近证书合格且未到期，原本允许使用。
	mustRegister(t, l, "M-OK", "在用表", 0.5)
	mustAddCert(t, l, passingCert("M-OK", "C-OK", 0.1))
	_ = l.SetStatus("M-OK", StatusInUse)

	lowLimit, err := l.Instrument("M-OK")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	lowLimit.AllowedError = 0 // 正式限值 0.5 下合格的 0.1 在副本限值下变成超差。
	d, err = l.CanUse("M-OK")
	if err != nil || !d.Allowed || len(d.Reasons) != 0 {
		t.Fatalf("调低副本限值不应使在用器具失去资格，err=%v allowed=%v reasons=%v",
			err, d.Allowed, d.Reasons)
	}

	retiredCopy, err := l.Instrument("M-OK")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	retiredCopy.Status = StatusRetired
	d, err = l.CanUse("M-OK")
	if err != nil || !d.Allowed {
		t.Fatalf("把副本改成停用不应限制正式器具，err=%v allowed=%v reasons=%v",
			err, d.Allowed, d.Reasons)
	}
	pendingCopy, err := l.Instrument("M-OK")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	pendingCopy.Status = StatusPending
	d, err = l.CanUse("M-OK")
	if err != nil || !d.Allowed {
		t.Fatalf("把副本改成待校准不应限制正式器具，err=%v allowed=%v", err, d.Allowed)
	}

	// 正式状态、限值与证书保持原样。
	got, err := l.Instrument("M-OK")
	if err != nil || got.Status != StatusInUse || got.AllowedError != 0.5 {
		t.Fatalf("正式登记被副本整理改动：%+v err=%v", got, err)
	}
	if latest := l.LatestCertificate("M-OK"); latest == nil || latest.Number != "C-OK" ||
		latest.Error != 0.1 || latest.InstrumentID != "M-OK" {
		t.Fatalf("在用器具的证书内容与归属改变：%+v", latest)
	}
}

// TestSeparatelyFetchedInstrumentCopiesAreIndependent 验证分别取得的两份
// 器具结果互不影响：整理其中一份不能改变已拿到的另一份，也不回写正式记录；
// 两次列出的清单同样彼此独立。
func TestSeparatelyFetchedInstrumentCopiesAreIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	first, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	want := *first
	second, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}

	// 整理登记返回结果，不改变稍后按编号取得的另一份与正式记录。
	first.ID = "M-A"
	first.Name = "第一份名称"
	first.AllowedError = 1.1
	first.Status = StatusRetired
	first.RegisteredAt = "2000-01-01T00:00:00Z"
	checkOfficialInstrument(t, second, want)
	if got, err := l.Instrument("M-1"); err != nil || got.Name != want.Name {
		t.Fatalf("整理第一份结果改写了正式登记：%+v err=%v", got, err)
	}

	// 整理第二份，不改变第一份已保留的内容与正式记录。
	second.ID = "M-B"
	second.Name = "第二份名称"
	second.AllowedError = 2.2
	second.Status = StatusInUse
	second.RegisteredAt = "2001-02-03T04:05:06Z"
	if first.ID != "M-A" || first.Name != "第一份名称" || first.AllowedError != 1.1 ||
		first.Status != StatusRetired || first.RegisteredAt != "2000-01-01T00:00:00Z" {
		t.Fatalf("整理第二份结果影响了第一份：%+v", *first)
	}
	checkOfficialInstrument(t, mustGetInstrument(t, l, "M-1"), want)

	// 两次列出的清单互不影响，也不影响正式登记。
	listA := l.Instruments()
	listB := l.Instruments()
	listA[0].ID = "M-LIST-A"
	listA[0].Name = "清单A名称"
	listB[0].ID = "M-LIST-B"
	listB = listB[:0] // 删光清单 B 的条目
	if len(listB) != 0 || listA[0].ID != "M-LIST-A" {
		t.Fatal("测试前提失效：清单自身的整理没有生效")
	}
	fresh := l.Instruments()
	if len(fresh) != 1 {
		t.Fatalf("整理返回清单不应改变正式器具数量，得到 %d 件", len(fresh))
	}
	checkOfficialInstrument(t, &fresh[0], want)
}

// TestOfficialStatusChangeVisibleWhileStaleCopiesKept 验证通过正常状态切换
// 成功修改正式状态后，新查询显示真实的新状态，而此前取得的器具副本仍保留
// 获取时的内容；副本上的整理也不会在此后一次正常保存中被带入台账文件。
func TestOfficialStatusChangeVisibleWhileStaleCopiesKept(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	officialAtRegister := mustGetInstrument(t, l, "M-1")

	snapshot, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	snapshotReview, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if snapshot.Status != StatusPending || snapshotReview.Instrument.Status != StatusPending {
		t.Fatal("测试前提失效：新器具应为待校准")
	}

	// 走正常状态切换功能把正式状态改为停用。
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err := l.Instrument("M-1")
	if err != nil || got.Status != StatusRetired {
		t.Fatalf("新查询应显示真实的新状态停用，得到 %+v err=%v", got, err)
	}
	r, err := l.Review("M-1")
	if err != nil || r.Instrument.Status != StatusRetired {
		t.Fatalf("新核对应显示真实的新状态停用，得到 %+v err=%v", r.Instrument, err)
	}

	// 此前取得的副本仍保留获取时的内容。
	if snapshot.Status != StatusPending {
		t.Fatalf("旧副本应保留获取时的待校准状态，得到 %s", snapshot.Status)
	}
	if snapshotReview.Instrument.Status != StatusPending {
		t.Fatalf("旧核对副本应保留获取时的待校准状态，得到 %s", snapshotReview.Instrument.Status)
	}

	// 整理旧副本的全部字段，再触发一次无关的正常保存并重开台账：
	// 文件里只能读到正式登记信息与真实状态。
	snapshot.ID = "M-STALE"
	snapshot.Name = "旧副本名称"
	snapshot.AllowedError = 8.8
	snapshot.RegisteredAt = "2000-01-01T00:00:00Z"
	snapshotReview.Instrument.Name = "旧核对名称"
	snapshotReview.Instrument.AllowedError = 6.6
	mustRegister(t, l, "M-2", "温度计", 0.2)

	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err = reopened.Instrument("M-1")
	if err != nil {
		t.Fatalf("reopen instrument: %v", err)
	}
	if got.Status != StatusRetired || got.Name != "万用表" ||
		got.AllowedError != 0.5 || got.RegisteredAt != officialAtRegister.RegisteredAt {
		t.Fatalf("重开后应读到正式登记信息与真实状态，得到 %+v", *got)
	}
	if _, err := reopened.Instrument("M-STALE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("旧副本编号不应进入台账，得到 %v", err)
	}
	if list := reopened.Instruments(); len(list) != 2 {
		t.Fatalf("重开后正式器具数量应为 2，得到 %d", len(list))
	}
}

// mustGetInstrument 取器具，失败即终止测试。
func mustGetInstrument(t *testing.T, l *Ledger, id string) *Instrument {
	t.Helper()
	got, err := l.Instrument(id)
	if err != nil {
		t.Fatalf("instrument %s: %v", id, err)
	}
	return got
}
