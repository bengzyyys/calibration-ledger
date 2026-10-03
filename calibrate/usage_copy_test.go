package calibrate

import (
	"path/filepath"
	"testing"
)

// TestReturnedReasonsCannotRewriteHistory 验证调用方整理各公开入口返回的
// 原因不会改写台账内已冻结的历史，也不会随正常写盘进入文件。
func TestReturnedReasonsCannotRewriteHistory(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 待校准且没有证书：申请被拒绝，原记录同时保留两条原因。
	d, err := l.RequestUse("M-1")
	if err != nil || d.Allowed {
		t.Fatalf("待校准无证书应拒绝，err=%v allowed=%v", err, d.Allowed)
	}
	want := append([]string(nil), d.Reasons...)
	if len(want) != 2 {
		t.Fatalf("应同时保留状态与无证书两个原因，得到 %v", want)
	}

	// 调用方在返回结果中替换、删减、补充原因，只影响自己那份展示数据。
	d.Reasons[0] = "显示用短句"
	d.Reasons = append(d.Reasons[:1], "调用方自己补的原因")

	recs := l.UsageRecords()
	if len(recs) != 1 || !sameReasons(recs[0].Reasons, want) {
		t.Fatalf("全部使用记录被申请返回值改写：保存=%v 读到=%v", want, recs)
	}
	r, _ := l.Review("M-1")
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, want) {
		t.Fatalf("核对视图历史拒绝被申请返回值改写：保存=%v 读到=%v",
			want, r.Rejections)
	}

	// 整理核对结果中的历史拒绝，也不能影响全部使用记录或再次核对。
	r.Rejections[0].Reasons[0] = "另一条显示短句"
	r.Rejections[0].Reasons = r.Rejections[0].Reasons[:1]
	recs = l.UsageRecords()
	if !sameReasons(recs[0].Reasons, want) {
		t.Fatalf("整理核对结果改写了全部使用记录：保存=%v 读到=%v",
			want, recs[0].Reasons)
	}
	r2, _ := l.Review("M-1")
	if !sameReasons(r2.Rejections[0].Reasons, want) {
		t.Fatalf("整理核对结果改写了再次核对的历史：保存=%v 读到=%v",
			want, r2.Rejections[0].Reasons)
	}

	// 整理全部使用记录，不能影响已经取得或之后取得的核对结果。
	recs[0].Reasons[0] = "记录清单里的短句"
	if !sameReasons(r2.Rejections[0].Reasons, want) {
		t.Fatal("整理全部使用记录影响了此前已取得的核对结果")
	}
	r3, _ := l.Review("M-1")
	if !sameReasons(r3.Rejections[0].Reasons, want) {
		t.Fatalf("整理全部使用记录改写了之后核对的历史：%v",
			r3.Rejections[0].Reasons)
	}

	// 补录合格证书并切换为在用：当前核对允许使用，但此前拒绝保持原原因。
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0, Summary: "s",
	})
	r, _ = l.Review("M-1")
	if !r.CanUse {
		t.Fatalf("补录合格证书并在用后当前应允许使用，得到 %v", r.Reasons)
	}
	if len(r.Rejections) != 1 || !sameReasons(r.Rejections[0].Reasons, want) {
		t.Fatalf("状态变化后历史拒绝原因被改写：保存=%v 读到=%v",
			want, r.Rejections)
	}

	// 正常操作触发台账写盘后，从同一文件读出的历史仍保持原样。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	recs = reopened.UsageRecords()
	if len(recs) != 1 || recs[0].Allowed || !sameReasons(recs[0].Reasons, want) {
		t.Fatalf("写盘后历史拒绝被污染：%+v", recs)
	}
	rr, _ := reopened.Review("M-1")
	if len(rr.Rejections) != 1 || !sameReasons(rr.Rejections[0].Reasons, want) {
		t.Fatalf("写盘后核对视图历史被污染：%+v", rr.Rejections)
	}
}

// TestSeparatelyFetchedResultsAreIndependent 验证分别取得的两份结果
// 互不影响：整理其中一份的原因，另一份已取得的原因不能跟着改变。
func TestSeparatelyFetchedResultsAreIndependent(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_, _ = l.RequestUse("M-1")

	first, _ := l.Review("M-1")
	want := append([]string(nil), first.Rejections[0].Reasons...)
	second, _ := l.Review("M-1")
	listA := l.UsageRecords()
	listB := l.UsageRecords()

	// 整理第一份核对结果，第二份核对结果与两份清单都不能跟着改变。
	first.Rejections[0].Reasons[0] = "第一份核对的改写"
	if !sameReasons(second.Rejections[0].Reasons, want) {
		t.Fatal("整理第一份核对结果影响了第二份核对结果")
	}
	if !sameReasons(listA[0].Reasons, want) || !sameReasons(listB[0].Reasons, want) {
		t.Fatal("整理第一份核对结果影响了已取得的使用记录清单")
	}

	// 整理第二份核对结果，第一份与台账内历史都不能跟着改变。
	second.Rejections[0].Reasons = append(second.Rejections[0].Reasons, "第二份追加")
	if sameReasons(second.Rejections[0].Reasons, want) {
		t.Fatal("测试前提失效：第二份自身的整理没有生效")
	}
	if first.Rejections[0].Reasons[0] != "第一份核对的改写" {
		t.Fatal("整理第二份核对结果影响了第一份")
	}
	if !sameReasons(l.UsageRecords()[0].Reasons, want) {
		t.Fatal("整理核对结果改写了台账内的历史")
	}

	// 分别取得的两份清单互相独立，整理任何一份都不回写台账。
	listA[0].Reasons[0] = "第一份清单的改写"
	listB[0].Reasons = listB[0].Reasons[:0]
	if listA[0].Reasons[0] != "第一份清单的改写" || len(listB[0].Reasons) != 0 {
		t.Fatal("测试前提失效：清单自身的整理没有生效")
	}
	if !sameReasons(l.UsageRecords()[0].Reasons, want) {
		t.Fatal("整理返回清单改写了台账内的历史")
	}
}

// TestAllowedDecisionMutationStaysAllowed 验证允许使用的申请留痕原因为空，
// 调用方自行给返回结果添加原因不得把这次申请变成历史拒绝。
func TestAllowedDecisionMutationStaysAllowed(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	_ = l.SetStatus("M-1", StatusInUse)
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-09-01",
		Expiry: "2027-09-01", Method: "m", Error: 0, Summary: "s",
	})

	d, err := l.RequestUse("M-1")
	if err != nil || !d.Allowed {
		t.Fatalf("应允许使用，err=%v allowed=%v", err, d.Allowed)
	}
	if len(d.Reasons) != 0 {
		t.Fatalf("允许使用时原因应为空，得到 %v", d.Reasons)
	}
	// 调用方给自己的返回结果添加原因。
	d.Reasons = append(d.Reasons, "调用方自行添加的展示原因")

	recs := l.UsageRecords()
	if len(recs) != 1 || !recs[0].Allowed || len(recs[0].Reasons) != 0 {
		t.Fatalf("允许记录被变成历史拒绝或原因被污染：%+v", recs[0])
	}
	r, _ := l.Review("M-1")
	if len(r.Rejections) != 0 {
		t.Fatalf("允许的使用申请不得出现在历史拒绝中：%+v", r.Rejections)
	}

	// 写盘重开后仍是允许、原因空。
	_ = l.SetStatus("M-1", StatusRetired)
	reopened, _ := openAt(path, clock.now)
	recs = reopened.UsageRecords()
	if len(recs) != 1 || !recs[0].Allowed || len(recs[0].Reasons) != 0 {
		t.Fatalf("写盘后允许记录被污染：%+v", recs[0])
	}
}

// TestQueriesDoNotRecordAndEmptyStaysEmpty 验证查询不新增申请，
// 没有使用记录时返回空结果，未知器具申请报错且不留痕。
func TestQueriesDoNotRecordAndEmptyStaysEmpty(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)

	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("没有使用记录时应返回空结果，得到 %+v", recs)
	}
	if _, err := l.CanUse("M-1"); err != nil {
		t.Fatalf("can use: %v", err)
	}
	if _, err := l.Review("M-1"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("查询本身不应新增申请，得到 %d 条", len(recs))
	}
	if _, err := l.RequestUse("NOPE"); err == nil {
		t.Fatal("未知器具的申请应明确报错")
	}
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatal("未知器具的申请不应留痕")
	}
}
