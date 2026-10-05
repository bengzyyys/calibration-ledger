package calibrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedUsageLedger 直接写出台账文件，使使用记录可以带不同的时区偏移，
// 模拟同一本机台账在系统时区变化前后留下的历史记录。记录按给定次序保存，
// 该次序故意与“实际时刻从早到晚”不同，以区分按文字排序与按实际时刻排序。
func seedUsageLedger(t *testing.T, records ...UsageRecord) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.json")
	data := ledgerData{Version: 1, Usage: records}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write seed ledger: %v", err)
	}
	return path, raw
}

// TestUsageRecordsOrderByActualInstant 验证使用记录按申请发生的实际时刻
// （各自保存的时间与偏移解析后）从早到晚排列，而不是按时间文字排序：
//   - 10:30:00+09:00 实际比 10:00:00+08:00 早半小时，必须排在前面；
//   - 跨日记录 2026-10-06T00:10:00+09:00 排在 2026-10-05T23:30:00+08:00 前；
//   - Z 与 +00:00 表示同一时刻时不再按文字分先后，而是按器具编号升序；
//   - 同一器具同一时刻的多次申请保留台账原次序，不合并，获准与否不参与排序。
func TestUsageRecordsOrderByActualInstant(t *testing.T) {
	seed := []UsageRecord{
		// 台账保存次序故意按实际时刻打乱，且获准/拒绝混杂。
		{InstrumentID: "M-2", RequestedAt: "2026-10-05T10:00:00+08:00", Allowed: true, Reasons: []string{}},
		{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:30:00+09:00", Allowed: false, Reasons: []string{"较早的一条拒绝"}},
		{InstrumentID: "M-3", RequestedAt: "2026-10-06T00:10:00+09:00", Allowed: false, Reasons: []string{"跨日较早"}},
		{InstrumentID: "M-4", RequestedAt: "2026-10-05T23:30:00+08:00", Allowed: true, Reasons: []string{}},
		{InstrumentID: "M-5", RequestedAt: "2026-10-05T12:00:00Z", Allowed: true, Reasons: []string{}},
		{InstrumentID: "M-6", RequestedAt: "2026-10-05T12:00:00+00:00", Allowed: true, Reasons: []string{}},
		// 同一器具同一时刻两次申请：先拒绝后获准，次序须按台账原样保留。
		{InstrumentID: "M-7", RequestedAt: "2026-10-05T12:00:00Z", Allowed: false, Reasons: []string{"同刻第一次（拒绝）"}},
		{InstrumentID: "M-7", RequestedAt: "2026-10-05T12:00:00Z", Allowed: true, Reasons: []string{}},
	}
	path, rawOnDisk := seedUsageLedger(t, seed...)

	// 查询时钟处在另一个时区（查询当天的时区），历史顺序不得因此改变。
	queryZone := time.FixedZone("查询地+05:30", 5*3600+1800)
	queryNow := func() time.Time {
		return time.Date(2026, 10, 5, 18, 0, 0, 0, queryZone)
	}
	l, err := openAt(path, queryNow)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	got := l.UsageRecords()
	wantIDs := []string{"M-1", "M-2", "M-5", "M-6", "M-7", "M-7", "M-3", "M-4"}
	if len(got) != len(wantIDs) {
		t.Fatalf("每条已保存申请都要保留，不合并：期望 %d 条，得到 %d 条", len(wantIDs), len(got))
	}
	for i, want := range wantIDs {
		if got[i].InstrumentID != want {
			gotIDs := make([]string, len(got))
			for j := range got {
				gotIDs[j] = got[j].InstrumentID
			}
			t.Fatalf("实际时刻排序错误：期望 %v，得到 %v", wantIDs, gotIDs)
		}
	}

	// M-7 同一时刻的两次申请保持台账原次序：拒绝在前、获准在后，
	// 申请结果不能成为排序条件。
	if got[4].Allowed || !got[5].Allowed {
		t.Fatalf("同器具同时刻申请应保留台账原次序（先拒绝后获准）：%+v %+v", got[4], got[5])
	}
	if sameReasons(got[4].Reasons, []string{"同刻第一次（拒绝）"}) == false {
		t.Fatalf("同刻第一次申请原因应原样保留：%v", got[4].Reasons)
	}

	// 申请时间保留原文字及偏移，器具编号、结果与原因保持保存时的内容。
	rawByPos := []string{
		"2026-10-05T10:30:00+09:00",
		"2026-10-05T10:00:00+08:00",
		"2026-10-05T12:00:00Z",
		"2026-10-05T12:00:00+00:00",
		"2026-10-05T12:00:00Z",
		"2026-10-05T12:00:00Z",
		"2026-10-06T00:10:00+09:00",
		"2026-10-05T23:30:00+08:00",
	}
	for i := range got {
		if got[i].RequestedAt != rawByPos[i] {
			t.Fatalf("第 %d 条申请时间应保留原文字及偏移：期望 %q，得到 %q",
				i, rawByPos[i], got[i].RequestedAt)
		}
	}

	// 查询不写回台账：文件内容与查询前逐字节相同。
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread ledger: %v", err)
	}
	if !bytes.Equal(again, rawOnDisk) {
		t.Fatal("取得使用记录清单改写了台账文件")
	}
	// 查询不新增申请。
	reopened, err := openAt(path, queryNow)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if n := len(reopened.UsageRecords()); n != len(seed) {
		t.Fatalf("查询不应新增申请，重开后得到 %d 条", n)
	}

	// 返回的是独立副本：整理一份清单不影响台账、另一份清单或再次查询。
	listA := l.UsageRecords()
	listB := l.UsageRecords()
	listA[0].Reasons[0] = "调用方改写的显示原因"
	listB0 := append([]string(nil), listB[0].Reasons...)
	if !sameReasons(listB[0].Reasons, listB0) || listB[0].Reasons[0] != "较早的一条拒绝" {
		t.Fatal("整理一份查询结果影响了另一份结果")
	}
	fresh := l.UsageRecords()
	if fresh[0].Reasons[0] != "较早的一条拒绝" {
		t.Fatalf("整理查询结果改写了台账内冻结的原因：%v", fresh[0].Reasons)
	}
}

// TestUsageRecordsEmptyAndSingle 验证没有申请记录时仍返回空结果，
// 只有一条时原样返回（原文字与偏移不变）。
func TestUsageRecordsEmptyAndSingle(t *testing.T) {
	path, _ := seedUsageLedger(t)
	l, err := openAt(path, func() time.Time { return mustDate(t, "2026-10-05") })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("没有申请记录时应返回空结果，得到 %+v", recs)
	}

	only := UsageRecord{
		InstrumentID: "M-9",
		RequestedAt:  "2026-10-05T23:59:00-04:00",
		Allowed:      false,
		Reasons:      []string{"唯一一条"},
	}
	path2, _ := seedUsageLedger(t, only)
	l2, err := openAt(path2, func() time.Time { return mustDate(t, "2026-10-06") })
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	recs := l2.UsageRecords()
	if len(recs) != 1 {
		t.Fatalf("只有一条时应原样返回一条，得到 %d 条", len(recs))
	}
	if recs[0].InstrumentID != only.InstrumentID ||
		recs[0].RequestedAt != only.RequestedAt ||
		recs[0].Allowed != only.Allowed ||
		!sameReasons(recs[0].Reasons, only.Reasons) {
		t.Fatalf("单条记录应原样返回：保存=%+v 得到=%+v", only, recs[0])
	}
}
