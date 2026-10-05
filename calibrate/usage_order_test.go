package calibrate

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// seedUsage 直接向台账写入带不同时区偏移的申请记录并保存，
// 模拟同一台机器在不同系统时区下先后留下的历史。
func seedUsage(t *testing.T, l *Ledger, recs ...UsageRecord) {
	t.Helper()
	l.data.Usage = append(l.data.Usage, recs...)
	if err := l.save(); err != nil {
		t.Fatalf("保存测试记录失败: %v", err)
	}
}

func usageKeys(recs []UsageRecord) [][2]string {
	got := make([][2]string, len(recs))
	for i, r := range recs {
		got[i] = [2]string{r.InstrumentID, r.RequestedAt}
	}
	return got
}

// TestUsageRecordsOrderByActualInstantAcrossOffsets 验证不同时区偏移下按申请
// 发生的实际时刻排序：2026-10-05T10:30:00+09:00（01:30Z）实际早于
// 2026-10-05T10:00:00+08:00（02:00Z），虽然后者文字上的小时、分钟更小。
// 获准与被拒绝的申请一起按时刻排序，结果不参与排序。
func TestUsageRecordsOrderByActualInstantAcrossOffsets(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	path := filepath.Join(t.TempDir(), "ledger.json")
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 按错误的字符串顺序写入（实际较晚的先入库），排序不能沿用入库或文字顺序。
	seedUsage(t, l,
		UsageRecord{InstrumentID: "M-2", RequestedAt: "2026-10-05T10:00:00+08:00",
			Allowed: false, Reasons: []string{"较晚的被拒绝申请"}},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:30:00+09:00",
			Allowed: true, Reasons: []string{}},
	)

	recs := l.UsageRecords()
	want := [][2]string{
		{"M-1", "2026-10-05T10:30:00+09:00"},
		{"M-2", "2026-10-05T10:00:00+08:00"},
	}
	if got := usageKeys(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("应按实际时刻从早到晚排列，得到 %v，期望 %v", got, want)
	}
	// 原文字及偏移、申请结果与拒绝原因保持保存时的内容。
	if !recs[0].Allowed || len(recs[0].Reasons) != 0 {
		t.Fatalf("获准记录的结果或原因被改动：%+v", recs[0])
	}
	if recs[1].Allowed || !sameReasons(recs[1].Reasons, []string{"较晚的被拒绝申请"}) {
		t.Fatalf("被拒绝记录的结果或冻结原因被改动：%+v", recs[1])
	}

	// 查询只重排返回的独立副本，不回写台账：台账内部仍按保存顺序保存。
	if got := usageKeys(l.data.Usage); !reflect.DeepEqual(got, [][2]string{
		{"M-2", "2026-10-05T10:00:00+08:00"},
		{"M-1", "2026-10-05T10:30:00+09:00"},
	}) {
		t.Fatalf("查询不应重排台账内存储：%v", got)
	}
	// 重新打开同一文件后，历史仍按各自保存的时间确定先后。
	reopened, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := usageKeys(reopened.UsageRecords()); !reflect.DeepEqual(got, want) {
		t.Fatalf("重开后排序应仍按实际时刻：%v", got)
	}
}

// TestUsageRecordsOrderAcrossMidnightOffsets 验证跨日记录同样按实际时刻：
// 2026-10-06T00:10:00+09:00（15:10Z）早于 2026-10-05T23:30:00+08:00（15:30Z）。
func TestUsageRecordsOrderAcrossMidnightOffsets(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	l := newTestLedger(t, clock)
	seedUsage(t, l,
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T23:30:00+08:00",
			Reasons: []string{}},
		UsageRecord{InstrumentID: "M-2", RequestedAt: "2026-10-06T00:10:00+09:00",
			Reasons: []string{}},
	)
	recs := l.UsageRecords()
	want := [][2]string{
		{"M-2", "2026-10-06T00:10:00+09:00"},
		{"M-1", "2026-10-05T23:30:00+08:00"},
	}
	if got := usageKeys(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("跨日记录应按实际时刻排列，得到 %v，期望 %v", got, want)
	}
}

// TestUsageRecordsSameInstantTieBreak 验证实际时刻相同时的次序规则：
// Z 与显式零偏移表示同一时刻时不产生额外优先级；先按器具编号升序，
// 同一器具同一时刻的多次申请保留台账原有次序且全部保留、不合并。
func TestUsageRecordsSameInstantTieBreak(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	l := newTestLedger(t, clock)
	seedUsage(t, l,
		// 同一时刻的两种文字形式、不同器具：按编号升序，与文字形式无关。
		UsageRecord{InstrumentID: "M-2", RequestedAt: "2026-10-05T10:00:00Z", Reasons: []string{}},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00+00:00", Reasons: []string{}},
		// 同一器具同一时刻的三次申请（含 Z 与零偏移、+08:00 混用）：保留入库次序。
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00Z",
			Allowed: false, Reasons: []string{"第一次"}},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00+00:00",
			Allowed: true, Reasons: []string{}},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T18:00:00+08:00",
			Allowed: false, Reasons: []string{"第三次"}},
	)
	recs := l.UsageRecords()
	want := [][2]string{
		{"M-1", "2026-10-05T10:00:00+00:00"},
		{"M-1", "2026-10-05T10:00:00Z"},
		{"M-1", "2026-10-05T10:00:00+00:00"},
		{"M-1", "2026-10-05T18:00:00+08:00"},
		{"M-2", "2026-10-05T10:00:00Z"},
	}
	if got := usageKeys(recs); !reflect.DeepEqual(got, want) {
		t.Fatalf("同一时刻应按编号升序、同编号保留原次序，得到 %v，期望 %v", got, want)
	}
	if len(recs) != 5 {
		t.Fatalf("同一器具同一时刻的多次申请必须全部保留、不得合并，得到 %d 条", len(recs))
	}
	if !sameReasons(recs[1].Reasons, []string{"第一次"}) ||
		!recs[2].Allowed ||
		!sameReasons(recs[3].Reasons, []string{"第三次"}) {
		t.Fatalf("同刻多次申请的结果与冻结原因次序被打乱：%+v", recs)
	}

	// 同刻同编号记录若以相反顺序入库，稳定排序应保留相反的原次序，
	// 时间的文字形式不产生额外优先级。
	l2 := newTestLedger(t, clock)
	seedUsage(t, l2,
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00+00:00",
			Reasons: []string{"先入库"}},
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00Z",
			Reasons: []string{"后入库"}},
	)
	got2 := l2.UsageRecords()
	if !sameReasons(got2[0].Reasons, []string{"先入库"}) ||
		!sameReasons(got2[1].Reasons, []string{"后入库"}) {
		t.Fatalf("同刻同编号应保留台账原次序，文字形式不得加优先级：%+v", got2)
	}
}

// TestUsageRecordsEmptyAndSingle 验证没有记录时返回空结果，
// 只有一条时原样返回（文字与偏移不变）。
func TestUsageRecordsEmptyAndSingle(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	l := newTestLedger(t, clock)
	if recs := l.UsageRecords(); len(recs) != 0 {
		t.Fatalf("没有申请记录时应返回空结果，得到 %+v", recs)
	}
	seedUsage(t, l, UsageRecord{InstrumentID: "M-9", RequestedAt: "2026-10-05T23:59:00-03:00",
		Allowed: true, Reasons: []string{}})
	recs := l.UsageRecords()
	if len(recs) != 1 ||
		recs[0].InstrumentID != "M-9" ||
		recs[0].RequestedAt != "2026-10-05T23:59:00-03:00" {
		t.Fatalf("只有一条记录时应原样返回：%+v", recs)
	}
}

// TestUsageRecordsOrderingUnaffectedByCurrentZone 验证查询当天的系统时区
// 变化不能改变历史顺序：同一份文件分别用不同时区的“现在”打开，先后一致。
func TestUsageRecordsOrderingUnaffectedByCurrentZone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	clock := &fakeClock{t: mustDate(t, "2026-10-05")}
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedUsage(t, l,
		UsageRecord{InstrumentID: "M-1", RequestedAt: "2026-10-05T10:00:00+08:00", Reasons: []string{}},
		UsageRecord{InstrumentID: "M-2", RequestedAt: "2026-10-05T10:30:00+09:00", Reasons: []string{}},
	)
	want := [][2]string{
		{"M-2", "2026-10-05T10:30:00+09:00"},
		{"M-1", "2026-10-05T10:00:00+08:00"},
	}
	zones := []struct {
		name string
		loc  *time.Location
	}{
		{"UTC", time.UTC},
		{"UTC+05:30", time.FixedZone("UTC+05:30", 5*3600+1800)},
		{"UTC-07:00", time.FixedZone("UTC-07:00", -7*3600)},
	}
	for _, z := range zones {
		loc := z.loc
		opened, err := openAt(path, func() time.Time { return clock.t.In(loc) })
		if err != nil {
			t.Fatalf("reopen in %s: %v", z.name, err)
		}
		if got := usageKeys(opened.UsageRecords()); !reflect.DeepEqual(got, want) {
			t.Fatalf("在时区 %s 查询改变了历史顺序：%v", z.name, got)
		}
	}
}
