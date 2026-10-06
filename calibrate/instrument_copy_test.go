package calibrate

import (
	"errors"
	"path/filepath"
	"testing"
)

// 本文件为“器具登记信息返回结果”的既有行为提供自动化回归保障：
// 登记成功、按编号查询、列出器具与按器具核对返回的器具信息都是独立展示副本，
// 调用方对副本（编号、名称、允许误差、状态、登记时间）的整理不回写台账，
// 不释放或占用编号、不改变器具数量，也不影响使用资格判断与证书归属。
// 范围只涉及器具信息，不扩展到证书、使用原因或计划结果的副本测试。

// 先按正式登记信息断言一件器具，测试辅助。
func assertOfficialInstrument(t *testing.T, l *Ledger, id, name string, allowed float64, status Status, registeredAt string) {
	t.Helper()
	got, err := l.Instrument(id)
	if err != nil {
		t.Fatalf("按原编号 %s 查询应仍存在：%v", id, err)
	}
	if got.ID != id || got.Name != name || got.AllowedError != allowed ||
		got.Status != status || got.RegisteredAt != registeredAt {
		t.Fatalf("正式登记信息与保存内容不符：%+v", got)
	}
}

// TestReturnedRegisterResultCannotRewriteLedger 对应核心场景：登记成功返回的
// 器具副本被全面整理（改编号、名称、允许误差、状态、登记时间）后，按原编号
// 查询、列出器具与核对仍得到实际保存的登记信息；原编号不释放、新编号不占用、
// 器具数量不增加；随后一次正常写盘也不把展示内容带入台账文件。
func TestReturnedRegisterResultCannotRewriteLedger(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-2", "示波器", 1.0)

	registered, err := l.Register(RegisterInput{ID: "M-1", Name: " 万用表 ", AllowedError: 0.5})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if registered.ID != "M-1" || registered.Name != "万用表" ||
		registered.AllowedError != 0.5 || registered.Status != StatusPending {
		t.Fatalf("登记返回结果内容异常：%+v", registered)
	}
	savedAt := registered.RegisteredAt
	if savedAt == "" {
		t.Fatal("登记时间不应为空")
	}

	// 调用方整理返回结果：编号改成尚不存在的编号，并改动全部其他字段。
	registered.ID = "M-NEW"
	registered.Name = "展示用名称"
	registered.AllowedError = 9.9
	registered.Status = StatusInUse
	registered.RegisteredAt = "2099-01-01T00:00:00Z"

	// 原器具没有消失，查询、列出、核对都仍是实际保存的登记信息。
	assertOfficialInstrument(t, l, "M-1", "万用表", 0.5, StatusPending, savedAt)
	list := l.Instruments()
	if len(list) != 2 {
		t.Fatalf("正式器具数量应保持 2 件，得到 %d 件：%+v", len(list), list)
	}
	var listed *Instrument
	for i := range list {
		if list[i].ID == "M-1" {
			listed = &list[i]
		}
	}
	if listed == nil || listed.Name != "万用表" || listed.AllowedError != 0.5 ||
		listed.Status != StatusPending || listed.RegisteredAt != savedAt {
		t.Fatalf("列出器具被返回结果改写：%+v", list)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.Instrument.ID != "M-1" || r.Instrument.Name != "万用表" ||
		r.Instrument.AllowedError != 0.5 || r.Instrument.Status != StatusPending ||
		r.Instrument.RegisteredAt != savedAt {
		t.Fatalf("核对中的登记信息被返回结果改写：%+v", r.Instrument)
	}

	// 原编号未释放：再登记原编号仍被拒绝；副本中的新编号不存在、未占用。
	if _, err := l.Register(RegisterInput{ID: "M-1", Name: "另一件", AllowedError: 0.1}); !IsValidation(err) {
		t.Fatalf("原编号仍应占用、重复登记被拒绝，得到 %v", err)
	}
	if _, err := l.Instrument("M-NEW"); err == nil {
		t.Fatal("只在副本中出现的新编号不应能查到")
	} else if !errors.Is(err, ErrNotFound) {
		t.Fatalf("新编号查询应报告不存在，得到 %v", err)
	}
	// 新编号未被占用：用它登记应成功，台账变为 3 件，原器具仍在。
	mustRegister(t, l, "M-NEW", "真新器具", 0.2)
	if len(l.Instruments()) != 3 {
		t.Fatalf("副本改写未新增器具，正式登记 M-NEW 后应为 3 件，得到 %d 件", len(l.Instruments()))
	}
	assertOfficialInstrument(t, l, "M-1", "万用表", 0.5, StatusPending, savedAt)

	// 正常状态切换保存后重开同一台账，读到的仍是正式登记信息与真实状态。
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set status: %v", err)
	}
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.Instruments()) != 3 {
		t.Fatalf("重开后器具数量应为 3，得到 %d", len(reopened.Instruments()))
	}
	got, err := reopened.Instrument("M-1")
	if err != nil {
		t.Fatalf("reopen instrument: %v", err)
	}
	if got.Name != "万用表" || got.AllowedError != 0.5 || got.Status != StatusRetired ||
		got.RegisteredAt != savedAt {
		t.Fatalf("展示内容被写盘带入台账：%+v", got)
	}
}

// TestReturnedInstrumentMutationCannotRaiseLimitOrLiftRetirement 验证器具信息
// 副本对使用资格判断没有影响：
//   - 在用器具证书未到期但测得误差超过登记限值时，把副本允许误差调大，
//     核对仍显示超差、申请使用仍被拒绝，原因仍按正式限值说明；
//   - 停用器具即使有合格且未到期的证书，把副本状态改成在用也不能解除停用；
//   - 原本允许使用的器具，不会因为副本被调低限值或改成停用而失去资格。
func TestReturnedInstrumentMutationCannotRaiseLimitOrLiftRetirement(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)

	// M-1：在用，登记限值 0.5；证书测得 0.8，未到期——正式数据为超差。
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.8, Summary: "超差批次",
	})
	over, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	over.AllowedError = 100

	// 分别从按编号查询、列出与核对三个入口取得副本并调大限值。
	if list := l.Instruments(); len(list) != 1 {
		t.Fatalf("应只有一件器具，得到 %d", len(list))
	} else {
		list[0].AllowedError = 100
	}
	rv, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	rv.Instrument.AllowedError = 100
	if rv.Latest == nil || rv.Latest.Pass {
		t.Fatalf("核对仍应按正式限值判定超差，得到 %+v", rv.Latest)
	}
	if rv.CanUse || !containsReason(rv.Reasons, "超差") ||
		!containsReason(rv.Reasons, "0.5") {
		t.Fatalf("调大副本限值不应解除超差限制，原因应按正式限值 0.5 说明，得到 %v", rv.Reasons)
	}
	d, err := l.CanUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("调大副本限值后申请使用仍应被拒绝，err=%v allowed=%v", err, d.Allowed)
	}
	if !containsReason(d.Reasons, "超差") || !containsReason(d.Reasons, "0.5") {
		t.Fatalf("拒绝原因应仍按正式限值说明，得到 %v", d.Reasons)
	}
	d2, err := l.RequestUse("M-1")
	if err != nil || d2.Allowed {
		t.Fatalf("申请使用应被拒绝并留痕，err=%v allowed=%v", err, d2.Allowed)
	}
	if !containsReason(d2.Reasons, "超差") {
		t.Fatalf("留痕的拒绝原因应按正式限值显示超差，得到 %v", d2.Reasons)
	}

	// M-2：停用，但有合格且未到期的证书——正式数据为停用限制。
	mustRegister(t, l, "M-2", "温度计", 0.5)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-2", Number: "C-2", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.1, Summary: "合格",
	})
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("retire M-2: %v", err)
	}
	retired, err := l.Instrument("M-2")
	if err != nil {
		t.Fatalf("instrument M-2: %v", err)
	}
	retired.Status = StatusInUse
	rv2, _ := l.Review("M-2")
	rv2.Instrument.Status = StatusInUse
	if rv2.CanUse || !containsReason(rv2.Reasons, "停用") {
		t.Fatalf("把副本状态改成在用不应解除停用限制，得到 canUse=%v reasons=%v",
			rv2.CanUse, rv2.Reasons)
	}
	d3, err := l.RequestUse("M-2")
	if err != nil || d3.Allowed || !containsReason(d3.Reasons, "停用") {
		t.Fatalf("停用器具申请使用仍应被拒绝且原因含停用，err=%v decision=%+v", err, d3)
	}

	// M-3：在用、证书合格且未到期——正式数据允许使用；调低副本限值或改成
	// 停用都不能让它失去资格。
	mustRegister(t, l, "M-3", "压力表", 1.0)
	_ = l.SetStatus("M-3", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-3", Number: "C-3", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0.8, Summary: "合格",
	})
	ok, err := l.Instrument("M-3")
	if err != nil {
		t.Fatalf("instrument M-3: %v", err)
	}
	ok.AllowedError = 0 // 正式限值 1.0 下 0.8 合格，副本限值 0 下应为超差。
	ok.Status = StatusRetired
	rv3, _ := l.Review("M-3")
	rv3.Instrument.AllowedError = 0
	rv3.Instrument.Status = StatusRetired
	if !rv3.CanUse || rv3.Latest == nil || !rv3.Latest.Pass {
		t.Fatalf("原本允许使用的器具不应因副本整理失去资格：canUse=%v latest=%+v",
			rv3.CanUse, rv3.Latest)
	}
	d4, err := l.RequestUse("M-3")
	if err != nil || !d4.Allowed {
		t.Fatalf("正式合格在用的器具申请应获准，err=%v decision=%+v", err, d4)
	}

	// 证书内容与归属不因这些整理改变。
	for _, want := range []struct {
		id, number string
		errV       float64
	}{
		{"M-1", "C-1", 0.8},
		{"M-2", "C-2", 0.1},
		{"M-3", "C-3", 0.8},
	} {
		c := l.LatestCertificate(want.id)
		if c == nil || c.Number != want.number || c.Error != want.errV ||
			c.InstrumentID != want.id {
			t.Fatalf("证书内容或归属被器具副本整理改变：%+v", c)
		}
	}
	// 正式登记的允许误差与状态保持原值。
	assertOfficialInstrument(t, l, "M-1", "万用表", 0.5, StatusInUse, l.findInstrument("M-1").RegisteredAt)
	assertOfficialInstrument(t, l, "M-2", "温度计", 0.5, StatusRetired, l.findInstrument("M-2").RegisteredAt)
	assertOfficialInstrument(t, l, "M-3", "压力表", 1.0, StatusInUse, l.findInstrument("M-3").RegisteredAt)
}

// TestReturnedInstrumentListCopyIsIndependent 验证列出器具返回的是独立副本：
// 改其中器具的字段、删去列表条目，只影响这一份列表；正式器具的数量和内容、
// 编号占用以及此后重新列出的结果都不变。
func TestReturnedInstrumentListCopyIsIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.2)
	savedAt1 := l.findInstrument("M-1").RegisteredAt

	list := l.Instruments()
	if len(list) != 2 || list[0].ID != "M-1" || list[1].ID != "M-2" {
		t.Fatalf("列表应按编号排序包含两件器具：%+v", list)
	}
	// 修改条目的全部展示字段并删去第二条。
	list[0].ID = "M-FAKE"
	list[0].Name = "列表里改的名"
	list[0].AllowedError = 50
	list[0].Status = StatusRetired
	list[0].RegisteredAt = "2099-01-01T00:00:00Z"
	list = append(list[:0], list[0]) // 只保留被篡改的那一条，相当于删去 M-2

	if len(l.Instruments()) != 2 {
		t.Fatalf("删改返回列表不应改变正式器具数量，重新列出得到 %d 件", len(l.Instruments()))
	}
	assertOfficialInstrument(t, l, "M-1", "万用表", 0.5, StatusPending, savedAt1)
	if _, err := l.Instrument("M-2"); err != nil {
		t.Fatalf("从列表删去 M-2 不应删除正式器具：%v", err)
	}
	if _, err := l.Instrument("M-FAKE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("列表中的伪造编号不应进入台账，得到 %v", err)
	}
	// 被“删除”的 M-2 编号仍占用，被“改名”的 M-1 编号也仍占用。
	if _, err := l.Register(RegisterInput{ID: "M-2", Name: "重复", AllowedError: 1}); !IsValidation(err) {
		t.Fatalf("M-2 编号仍应占用，得到 %v", err)
	}
	if _, err := l.Register(RegisterInput{ID: "M-1", Name: "重复", AllowedError: 1}); !IsValidation(err) {
		t.Fatalf("M-1 编号仍应占用，得到 %v", err)
	}
}

// TestSeparatelyFetchedInstrumentResultsIndependent 验证分别取得的两份器具
// 结果互不影响：整理登记返回、两次按编号查询、两次列出与两次核对中的器具
// 信息，其他已取得的副本和台账正式记录都不变化。
func TestSeparatelyFetchedInstrumentResultsIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	registered, _ := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5})
	savedAt := registered.RegisteredAt
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set status: %v", err)
	}

	q1, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument 1: %v", err)
	}
	q2, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument 2: %v", err)
	}
	list1 := l.Instruments()
	list2 := l.Instruments()
	r1, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review 1: %v", err)
	}
	r2, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review 2: %v", err)
	}

	// 整理登记返回结果：其他全部副本与正式记录保持登记时的内容。
	registered.ID = "M-FAKE"
	registered.Name = "登记结果改名"
	registered.AllowedError = 9
	registered.Status = StatusRetired
	registered.RegisteredAt = "2099-01-01T00:00:00Z"
	for _, got := range []*Instrument{q1, q2, &list1[0], &list2[0], &r1.Instrument, &r2.Instrument} {
		if got.ID != "M-1" || got.Name != "万用表" || got.AllowedError != 0.5 ||
			got.Status != StatusInUse || got.RegisteredAt != savedAt {
			t.Fatalf("整理登记返回结果影响了其他已取得副本：%+v", got)
		}
	}

	// 整理第一份按编号查询：第二份查询、两份列表、两份核对都不变。
	q1.Name = "第一份查询改名"
	q1.AllowedError = 8
	q1.Status = StatusRetired
	q1.ID = "M-Q1"
	for _, got := range []*Instrument{q2, &list1[0], &list2[0], &r1.Instrument, &r2.Instrument} {
		if got.Name != "万用表" || got.AllowedError != 0.5 ||
			got.Status != StatusInUse || got.ID != "M-1" {
			t.Fatalf("整理第一份查询影响了其他已取得副本：%+v", got)
		}
	}

	// 整理第一份列表与第一份核对：彼此独立，也不影响第二份与正式记录。
	list1[0].Name = "第一份列表改名"
	list1 = list1[:0]
	r1.Instrument.Name = "第一份核对改名"
	r1.Instrument.AllowedError = 7
	if list2[0].Name != "万用表" || len(list2) != 1 {
		t.Fatalf("整理第一份列表影响了第二份列表：%+v", list2)
	}
	if r2.Instrument.Name != "万用表" || r2.Instrument.AllowedError != 0.5 {
		t.Fatalf("整理第一份核对影响了第二份核对：%+v", r2.Instrument)
	}
	if q2.Name != "万用表" || q2.Status != StatusInUse {
		t.Fatal("整理列表或核对影响了此前取得的查询副本")
	}
	assertOfficialInstrument(t, l, "M-1", "万用表", 0.5, StatusInUse, savedAt)
}

// TestRealStatusChangeUpdatesNewQueriesButFetchedCopiesStayFrozen 验证正式状态
// 切换成功后，新查询显示真实的新状态，而此前取得的器具副本仍保留获取时的
// 内容；展示数据的修改不随这次正常保存进入台账文件。
func TestRealStatusChangeUpdatesNewQueriesButFetchedCopiesStayFrozen(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustRegister(t, l, "M-2", "温度计", 0.2)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set M-1 in-use: %v", err)
	}
	if err := l.SetStatus("M-2", StatusInUse); err != nil {
		t.Fatalf("set M-2 in-use: %v", err)
	}

	// 在正式切换为停用之前取得各入口的副本（此时两件器具都在用）。
	beforeQ, _ := l.Instrument("M-1")
	beforeList := l.Instruments()
	beforeReview, _ := l.Review("M-1")
	beforeRegistered, _ := l.Register(RegisterInput{ID: "M-3", Name: "压力表", AllowedError: 1.0})

	// 调用方先在副本上做展示整理，再执行正式状态切换。
	beforeQ.AllowedError = 9
	beforeList[0].Name = "列表展示名"
	beforeReview.Instrument.AllowedError = 8
	beforeRegistered.Status = StatusInUse // M-3 正式为待校准，副本伪造成在用

	// 通过正常状态切换功能把 M-1、M-2 正式改为停用（正常写盘）。
	if err := l.SetStatus("M-1", StatusRetired); err != nil {
		t.Fatalf("set M-1 retired: %v", err)
	}
	if err := l.SetStatus("M-2", StatusRetired); err != nil {
		t.Fatalf("set M-2 retired: %v", err)
	}

	// 新查询显示真实的新状态与正式限值/名称。
	afterQ, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument after: %v", err)
	}
	if afterQ.Status != StatusRetired || afterQ.AllowedError != 0.5 || afterQ.Name != "万用表" {
		t.Fatalf("正式切换后新查询应显示真实状态与登记信息：%+v", afterQ)
	}
	afterList := l.Instruments()
	var afterListed *Instrument
	for i := range afterList {
		if afterList[i].ID == "M-1" {
			afterListed = &afterList[i]
		}
	}
	if afterListed == nil || afterListed.Status != StatusRetired || afterListed.Name != "万用表" {
		t.Fatalf("正式切换后列出结果应显示新状态与正式名称：%+v", afterList)
	}
	afterReview, _ := l.Review("M-1")
	if afterReview.Instrument.Status != StatusRetired ||
		afterReview.CanUse || !containsReason(afterReview.Reasons, "停用") {
		t.Fatalf("正式停用后核对应显示停用并拒绝使用：%+v", afterReview)
	}
	// M-3 未经正式切换仍为待校准，副本伪造成在用不影响它，也不影响 M-2。
	if got, _ := l.Instrument("M-3"); got.Status != StatusPending {
		t.Fatalf("M-3 应仍为待校准，得到 %+v", got)
	}
	if got, _ := l.Instrument("M-2"); got.Status != StatusRetired {
		t.Fatalf("M-2 新查询应显示停用，得到 %+v", got)
	}

	// 此前取得的副本保留获取时的内容与调用方自己的整理，不随正式变化更新。
	if beforeQ.Status != StatusInUse || beforeQ.AllowedError != 9 || beforeQ.Name != "万用表" {
		t.Fatalf("旧查询副本应保留获取时的在用状态与自身整理：%+v", beforeQ)
	}
	var oldListed *Instrument
	for i := range beforeList {
		if beforeList[i].ID == "M-1" {
			oldListed = &beforeList[i]
		}
	}
	if oldListed == nil || oldListed.Status != StatusInUse || oldListed.Name != "列表展示名" {
		t.Fatalf("旧列表副本应保留获取时状态与自身整理：%+v", beforeList)
	}
	if beforeReview.Instrument.Status != StatusInUse || beforeReview.Instrument.AllowedError != 8 {
		t.Fatalf("旧核对副本应保留获取时的在用状态与自身整理：%+v", beforeReview.Instrument)
	}
	if beforeRegistered.Status != StatusInUse {
		t.Fatalf("登记返回副本被后续正式状态切换带动变化：%+v", beforeRegistered)
	}

	// 重新打开同一台账：正式登记信息与真实状态保留，展示数据未进入文件。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	g1, _ := reopened.Instrument("M-1")
	if g1.Name != "万用表" || g1.AllowedError != 0.5 || g1.Status != StatusRetired {
		t.Fatalf("重开后 M-1 应为正式内容与停用状态：%+v", g1)
	}
	g2, _ := reopened.Instrument("M-2")
	if g2.Name != "温度计" || g2.AllowedError != 0.2 || g2.Status != StatusRetired {
		t.Fatalf("重开后 M-2 应为正式内容与真实状态：%+v", g2)
	}
	g3, _ := reopened.Instrument("M-3")
	if g3.Name != "压力表" || g3.AllowedError != 1.0 || g3.Status != StatusPending {
		t.Fatalf("重开后 M-3 应为正式内容与待校准状态：%+v", g3)
	}
	if len(reopened.Instruments()) != 3 {
		t.Fatalf("重开后正式器具数量应为 3，得到 %d", len(reopened.Instruments()))
	}
}
