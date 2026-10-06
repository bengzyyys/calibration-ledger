package calibrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“用证书完成校准计划”补上跨时区回归保障，专门保护一条界限：
//
//	证书校准日期不得早于“计划最初建立时的本机日历日期”，等于该日期即可完成。
//
// 计划保存的建立时间是带当时时区偏移的 RFC3339（如 2026-10-02T00:30:00+08:00）。
// 这条界限锚定在该偏移所表示的那一个本机日历日（2026-10-02）上，与后来完成操作
// 发生时机器所在的时区、运行日期都无关：既不能把建立时刻换算到 UTC 得到的另一
// 日历日（东八区凌晨会落到 10-01）当成更早的界限，也不能换算到当前时区取日
// （西七区深夜会落到 10-03）当成更晚的界限。证书只记载校准日期、没有时分秒，
// 建立当天的证书即使晚于建立的钟点也满足日期条件。
//
// 两个场景各自卡住一个出错方向，并在多个“完成时所在时区”下重放，保证业务结果
// 不随实际运行日期或本机默认时区改变：
//   - 东八区凌晨建立：换算 UTC 是前一天，不能因此接受前一天的证书；
//   - 西七区深夜建立：换算 UTC 是后一天，不能因此拒绝建立当天的证书。

// 创建计划时所在的固定时区偏移（与 IANA 时区解耦，避免夏令时干扰用例）。
var (
	creationEast = time.FixedZone("创建地+08", 8*60*60)
	creationWest = time.FixedZone("创建地-07", -7*60*60)
)

// 完成操作可能发生在任一时区；这些位置仅影响“本次运行”的时间，绝不允许改变
// 由计划建立时刻自带偏移确定的日期界限。
var completionZones = []struct {
	name string
	loc  *time.Location
}{
	{"切换为UTC", time.UTC},
	{"留在东八区", creationEast},
	{"切换到西七区", creationWest},
}

// seedPlanLedgerCreatedAt 通过公开入口建立一件器具与一项计划，建立申请的本机
// 时刻精确取 created（自带固定偏移），返回台账文件路径作为各时区重放的模板。
// 计划日期取未来固定日，避免与运行日期相关。
func seedPlanLedgerCreatedAt(t *testing.T, created time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.json")
	clock := &fakeClock{t: created.AddDate(0, 0, -10)}
	l, err := openAt(path, clock.now)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)

	// 建立申请只取一次本机时间：计划日期合法性与保存的建立时间都对应 created。
	clock.t = created
	p, err := l.CreatePlan(PlanInput{
		Number:       "PL-1",
		InstrumentID: "M-1",
		Date:         "2026-10-20",
		Note:         "跨时区周期校准",
	})
	if err != nil {
		t.Fatalf("建立计划失败: %v", err)
	}
	if p.CreatedAt != created.Format(time.RFC3339) {
		t.Fatalf("测试前置：建立时间应为 %s，得到 %s",
			created.Format(time.RFC3339), p.CreatedAt)
	}
	return path
}

// cloneLedgerFile 复制模板台账到一个全新的临时文件，使每个运行时区在互不影响的
// 副本上重放“拒绝 → 完成”，避免一次完成后无法再验证拒绝分支。
func cloneLedgerFile(t *testing.T, src string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取模板台账: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatalf("复制模板台账: %v", err)
	}
	return dst
}

// TestCompletePlanCreationLocalDateStableAcrossRuntimeZones 是核心回归：两个建立
// 方向 × 三个完成时区，业务结论必须完全一致——校准日期 2026-10-01 拒绝并指出
// 早于计划的 2026-10-02 建立日期，2026-10-02 正常完成。
func TestCompletePlanCreationLocalDateStableAcrossRuntimeZones(t *testing.T) {
	cases := []struct {
		name    string
		created time.Time
	}{
		{
			// 2026-10-02T00:30:00+08:00 == 2026-10-01T16:30:00Z：
			// 换算 UTC 落在前一天，前一天的证书绝不能被放行。
			name:    "东八区凌晨建立_换算UTC为前一天",
			created: time.Date(2026, 10, 2, 0, 30, 0, 0, creationEast),
		},
		{
			// 2026-10-02T23:30:00-07:00 == 2026-10-03T06:30:00Z：
			// 换算 UTC 落在后一天，建立当天的证书绝不能被当成过早。
			name:    "西七区深夜建立_换算UTC为后一天",
			created: time.Date(2026, 10, 2, 23, 30, 0, 0, creationWest),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCreatedAt := tc.created.Format(time.RFC3339)
			template := seedPlanLedgerCreatedAt(t, tc.created)
			for _, zone := range completionZones {
				zone := zone
				t.Run(zone.name, func(t *testing.T) {
					path := cloneLedgerFile(t, template)
					// 完成操作发生在更晚的 10-10、位于该运行时区。证书均已按原
					// 有规则在此前合法录入（校准日期不晚于本机今天）。
					runNow := time.Date(2026, 10, 10, 12, 0, 0, 0, zone.loc)
					l, err := openAt(path, func() time.Time { return runNow })
					if err != nil {
						t.Fatalf("open: %v", err)
					}

					// 两张不同校准日期的证书都属于该器具、彼此不同日，均可合法
					// 录入；截止日晚于校准日期、晚于运行当天。
					mustAddCert(t, l, CertificateInput{
						InstrumentID: "M-1", Number: "C-PREV", CalDate: "2026-10-01",
						Expiry: "2027-10-01", Method: "m", Error: 0.1, Summary: "建立前一天",
					})
					mustAddCert(t, l, CertificateInput{
						InstrumentID: "M-1", Number: "C-DAY", CalDate: "2026-10-02",
						Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "建立当天",
					})

					// 校准日期 10-01：无论运行时区如何，都必须被拒绝，并明确指出
					// 它早于计划的 10-02 建立日期——而不是换算出来的 10-01/10-03。
					_, _, err = l.CompletePlan("PL-1", "C-PREV")
					if !IsValidation(err) {
						t.Fatalf("10-01 的证书应被业务拒绝（界限必须是建立日本机日 10-02），得到 %v", err)
					}
					msg := err.Error()
					for _, want := range []string{"C-PREV", "2026-10-01", "PL-1", "2026-10-02", "早于", "建立日期"} {
						if !strings.Contains(msg, want) {
							t.Fatalf("拒绝信息应包含 %q（明确以 10-02 为建立日），得到 %q", want, msg)
						}
					}

					// 拒绝是业务拒绝：计划继续未完成、留在待办，完成时间与关联
					// 证书仍为空；原建立时间、计划日期、说明及已录入证书保持原样。
					assertPlanStillOpen(t, l, "PL-1")
					p := l.findPlan("PL-1")
					if p.CreatedAt != wantCreatedAt {
						t.Fatalf("拒绝后建立时间串不应被运行时区改写：want %s got %s",
							wantCreatedAt, p.CreatedAt)
					}
					if p.PlannedDate != "2026-10-20" || p.OriginalDate != "2026-10-20" ||
						p.Note != "跨时区周期校准" {
						t.Fatalf("拒绝后计划日期/最初日期/说明被改动：%+v", p)
					}
					if c := l.findCertificate("C-PREV"); c == nil || c.CalDate != "2026-10-01" {
						t.Fatalf("被拒证书应原样保留，得到 %+v", c)
					}
					if c := l.findCertificate("C-DAY"); c == nil || c.CalDate != "2026-10-02" {
						t.Fatalf("建立当天证书应原样保留，得到 %+v", c)
					}

					// 校准日期 10-02（恰为建立日本机日）：正常完成，不必等到原定
					// 的 10-20 计划日。保存该证书编号与本次完成时间并移出待办。
					done, idem, err := l.CompletePlan("PL-1", "C-DAY")
					if err != nil || idem {
						t.Fatalf("建立当天（10-02）的证书应完成计划，err=%v idempotent=%v", err, idem)
					}
					if done.Status != PlanStatusDone || done.CertificateNumber != "C-DAY" ||
						done.CompletedAt == "" {
						t.Fatalf("完成结果应保存证书编号与完成时间：%+v", done)
					}
					if !strings.HasPrefix(done.CompletedAt, "2026-10-10T") {
						t.Fatalf("完成时间应取本次成功操作时刻（10-10），得到 %s", done.CompletedAt)
					}
					if done.CreatedAt != wantCreatedAt {
						t.Fatalf("完成后建立时间串应保持原样：want %s got %s",
							wantCreatedAt, done.CreatedAt)
					}
					completedAt := done.CompletedAt
					if items, _ := l.Todos(""); len(items) != 0 {
						t.Fatalf("完成后计划应从待办移除，得到 %+v", items)
					}

					// 同证书重复完成：幂等返回原结果，完成时间不随时区/日期刷新。
					l.now = func() time.Time {
						return time.Date(2026, 10, 11, 8, 0, 0, 0, zone.loc)
					}
					again, idem2, err := l.CompletePlan("PL-1", "C-DAY")
					if err != nil || !idem2 {
						t.Fatalf("同证书重复完成应幂等成功，err=%v idempotent=%v", err, idem2)
					}
					if again.Status != PlanStatusDone || again.CertificateNumber != "C-DAY" ||
						again.CompletedAt != completedAt || again.CreatedAt != wantCreatedAt {
						t.Fatalf("幂等返回应原样保留证书、完成时间与建立时间：%+v", again)
					}
				})
			}
		})
	}
}

// TestCompletePlanPersistedBoundarySurvivesReopenInAnotherZone 直接对齐题述顺序：
// 在 +08 建立、随后把本机时区切换为 UTC 完成，前一天证书被拒、当天证书完成；
// 完成后再在第三个时区重开同一台账文件，已完成状态、关联证书、完成时间以及
// 保存的建立时间串与证书校准日期都保持原内容，操作时区不改写历史。
func TestCompletePlanPersistedBoundarySurvivesReopenInAnotherZone(t *testing.T) {
	created := time.Date(2026, 10, 2, 0, 30, 0, 0, creationEast)
	wantCreatedAt := created.Format(time.RFC3339)
	path := seedPlanLedgerCreatedAt(t, created)

	// 更晚的日期、本机时区已切换为 UTC。
	utcNow := func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) }
	l, err := openAt(path, utcNow)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-PREV", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "m", Error: 0.1, Summary: "前一天",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-DAY", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "建立当天",
	})

	if _, _, err := l.CompletePlan("PL-1", "C-PREV"); !IsValidation(err) {
		t.Fatalf("UTC 下 10-01 证书必须被拒（建立时刻换算 UTC 虽落在 10-01），得到 %v", err)
	}
	assertPlanStillOpen(t, l, "PL-1")

	done, idem, err := l.CompletePlan("PL-1", "C-DAY")
	if err != nil || idem {
		t.Fatalf("UTC 下 10-02 证书应正常完成，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-DAY" {
		t.Fatalf("完成结果异常：%+v", done)
	}
	completedAt := done.CompletedAt

	// 切换到西七区、更晚日期重开同一台账文件复查。
	reopened, err := openAt(path, func() time.Time {
		return time.Date(2026, 10, 12, 9, 0, 0, 0, creationWest)
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	plans, err := reopened.Plans("M-1")
	if err != nil || len(plans) != 1 {
		t.Fatalf("重开后应仍只有一项计划，plans=%d err=%v", len(plans), err)
	}
	pv := plans[0]
	if pv.Status != PlanStatusDone || pv.CertificateNumber != "C-DAY" ||
		pv.CompletedAt != completedAt {
		t.Fatalf("换时区重开后完成状态/证书/完成时间被改动：%+v", pv.Plan)
	}
	if pv.CreatedAt != wantCreatedAt {
		t.Fatalf("建立时间串应保留 +08 原文 %s，得到 %s", wantCreatedAt, pv.CreatedAt)
	}
	if items, _ := reopened.Todos(""); len(items) != 0 {
		t.Fatalf("换时区重开后已完成计划不应回到待办，得到 %+v", items)
	}
	r, err := reopened.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-DAY" {
		t.Fatalf("核对中计划应显示已完成并关联 C-DAY，得到 %+v", r.Plans)
	}
	var dayCert *Certificate
	for i := range r.History {
		if r.History[i].Number == "C-DAY" {
			c := r.History[i].Certificate
			dayCert = &c
		}
	}
	if dayCert == nil || dayCert.CalDate != "2026-10-02" {
		t.Fatalf("核对中建立当天证书的校准日期应保留 2026-10-02，得到 %+v", dayCert)
	}
}
