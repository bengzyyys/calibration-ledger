package calibrate

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件为“用证书完成校准计划”补充跨时区回归保障：计划建立的本机日期界限
// 必须以建立时刻自带偏移所落在的日历日期为准，完成操作时本机所处的时区
// （含中途切换系统时区）不能把建立时刻换算到别的日历日期后当作新的界限。
//
// 两个方向各守住一条：
//
//  1. 计划 2026-10-02T00:30:00+08:00 建立（换算 UTC 落在 10-01）：之后在 UTC
//     等其他时区下，校准日期 2026-10-01 的证书仍必须被拒绝（早于建立日期
//     2026-10-02），校准日期 2026-10-02 的证书可以完成。
//  2. 计划 2026-10-02T23:30:00-07:00 建立（换算 UTC 已是 10-03）：即使如此，
//     校准日期 2026-10-02 的证书仍满足日期条件——证书只记载日期、没有当天
//     时分秒，不能因为计划建立得较晚（或换算后的其他时区日期）而判过早。
//
// 所有场景的完成动作都发生在更晚的 2026-10-15，且在多个操作时区下重复，
// 保证业务结果不随实际运行日期或本机默认时区改变。

// fixedZone 构造固定偏移时区，用于模拟“本机当时处在某时区”。
func fixedZone(name string, offsetSeconds int) *time.Location {
	return time.FixedZone(name, offsetSeconds)
}

// atZone 把同一日历日 2026-10-15 10:00 放到各操作时区，模拟更晚日期在不同
// 本机时区下发起完成申请。
func atZone(loc *time.Location) time.Time {
	return time.Date(2026, 10, 15, 10, 0, 0, 0, loc)
}

// 完成阶段要遍历的操作时区：UTC、东八区（建立方向 1 的原时区）、西七区
// （建立方向 2 的原时区）以及两个跨日边界偏移，确保换算到任何基准都不会
// 改变按“建立自带偏移的日历日期”得出的结论。
var completionZones = []struct {
	name string
	loc  *time.Location
}{
	{"UTC", time.UTC},
	{"+08:00", fixedZone("UTC+8", 8*3600)},
	{"-07:00", fixedZone("UTC-7", -7*3600)},
	{"+05:30", fixedZone("UTC+5:30", 5*3600+30*60)},
	{"+14:00", fixedZone("UTC+14", 14*3600)},
}

// seedPlanInZone 模拟在 zone 时区的 when 时刻通过公开入口建立计划：
// 正式保存的 CreatedAt 带该时刻的偏移。随后台账可在任意其他时区重开。
func seedPlanInZone(t *testing.T, path string, when time.Time, number string) *Ledger {
	t.Helper()
	l, err := openAt(path, func() time.Time { return when })
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set in-use: %v", err)
	}
	mustPlan(t, l, PlanInput{
		Number: number, InstrumentID: "M-1", Date: "2030-10-20", Note: "年度例行校准",
	})
	return l
}

// reopenAt 用指定操作时区下的更晚时刻重开同一台账文件。
func reopenAt(t *testing.T, path string, loc *time.Location) *Ledger {
	t.Helper()
	l, err := openAt(path, func() time.Time { return atZone(loc) })
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	return l
}

func addDayBoundaryCerts(t *testing.T, l *Ledger) {
	t.Helper()
	// 两张证书校准日期不同、截止日都晚于完成动作日期，互不触发同日限制。
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OCT1", CalDate: "2026-10-01",
		Expiry: "2027-10-01", Method: "规范A", Error: 0.1, Summary: "换算后前一天",
	})
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-OCT2", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "规范A", Error: 0.1, Summary: "建立当天",
	})
}

// assertRejectedAndUntouched 核对日期不符的拒绝完全沿用既有业务拒绝方式：
// 计划保持未完成、留在待办，完成时间与关联证书为空，建立时间、计划日期、
// 说明等原有字段一字不改，已录入证书也保持原样。
func assertRejectedAndUntouched(t *testing.T, l *Ledger, createdAt string) {
	t.Helper()
	p := l.findPlan("PL-1")
	if p == nil {
		t.Fatal("计划 PL-1 应仍然存在")
	}
	if p.Status != PlanStatusOpen {
		t.Fatalf("被拒绝后计划应仍为未完成，得到 %s", p.Status)
	}
	if p.CompletedAt != "" || p.CertificateNumber != "" {
		t.Fatalf("被拒绝后不应写入完成时间或关联证书：%+v", p)
	}
	if p.CreatedAt != createdAt {
		t.Fatalf("原有建立时间不应被改动：得到 %s，want %s", p.CreatedAt, createdAt)
	}
	if p.PlannedDate != "2030-10-20" || p.OriginalDate != "2030-10-20" || p.Note != "年度例行校准" {
		t.Fatalf("计划日期与说明不应被改动：%+v", p)
	}
	if len(p.Changes) != 0 {
		t.Fatalf("拒绝完成不应产生改期记录：%+v", p.Changes)
	}
	if items, err := l.Todos("M-1"); err != nil || len(items) != 1 || items[0].PlanNumber != "PL-1" {
		t.Fatalf("计划应继续留在待办，err=%v items=%+v", err, items)
	}
	for number, cal := range map[string]string{"C-OCT1": "2026-10-01", "C-OCT2": "2026-10-02"} {
		c := l.findCertificate(number)
		if c == nil || c.CalDate != cal {
			t.Fatalf("已录入证书应保持原样：%s 得到 %+v", number, c)
		}
	}
}

// TestCompletePlanCreatedDayEastOfUTCRejectsConvertedEarlierDay 方向 1：
// 计划在 2026-10-02T00:30:00+08:00 建立，其绝对时刻换算 UTC 虽然落在 10-01，
// 但建立的本机日期是 10-02。更晚日期切换到 UTC（及其他时区）后，10-01 校准
// 的证书必须被拒绝且原因明确指出建立日期 2026-10-02；10-02 校准的证书正常
// 完成，不必等到原定计划日 2030-10-20。
func TestCompletePlanCreatedDayEastOfUTCRejectsConvertedEarlierDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	created := time.Date(2026, 10, 2, 0, 30, 0, 0, fixedZone("UTC+8", 8*3600))
	const createdText = "2026-10-02T00:30:00+08:00"
	if created.Format(time.RFC3339) != createdText {
		t.Fatalf("测试预置建立时间应为 %s，得到 %s", createdText, created.Format(time.RFC3339))
	}
	seedPlanInZone(t, path, created, "PL-1")

	// 更晚的日期在不同操作时区下分别重开台账并录入证书、尝试完成：
	// 日期界限的结果必须一致，与完成时所在时区无关（重开也验证了文件中
	// 保存的带偏移建立时间不被重开时的时区改写）。
	var completedAt string
	for i, z := range completionZones {
		l := reopenAt(t, path, z.loc)
		if i == 0 {
			addDayBoundaryCerts(t, l)
		}
		_, _, err := l.CompletePlan("PL-1", "C-OCT1")
		if !IsValidation(err) {
			t.Fatalf("时区 %s 下 10-01 校准的证书应按业务校验拒绝，得到 %v", z.name, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "2026-10-01") || !strings.Contains(msg, "建立日期 2026-10-02") {
			t.Fatalf("时区 %s 下拒绝原因应指出证书 10-01 早于计划建立日期 10-02，得到 %q",
				z.name, msg)
		}
		assertRejectedAndUntouched(t, l, createdText)
	}

	// 校准日期等于建立的本机日期 2026-10-02：在任一操作时区下都正常完成，
	// 保存该证书编号与本次完成时间，并从待办移除。
	l := reopenAt(t, path, time.UTC)
	done, idem, err := l.CompletePlan("PL-1", "C-OCT2")
	if err != nil || idem {
		t.Fatalf("10-02 校准的证书应能完成计划，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OCT2" {
		t.Fatalf("完成结果应关联 C-OCT2：%+v", done)
	}
	if !strings.HasPrefix(done.CompletedAt, "2026-10-15T") {
		t.Fatalf("完成时间应取本次成功操作的更晚日期，得到 %s", done.CompletedAt)
	}
	if done.CreatedAt != createdText {
		t.Fatalf("返回结果中建立时间应保持原文与偏移，得到 %s", done.CreatedAt)
	}
	completedAt = done.CompletedAt
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后应从待办移除，得到 %+v", items)
	}

	// 落盘内容：建立日期保留 +08:00 原文，两张证书校准日期不变，完成时间
	// 与关联证书已保存；再在另一个操作时区重开，核对中显示已完成，且同证书
	// 幂等完成返回原完成时间。
	for _, z := range []struct {
		name string
		loc  *time.Location
	}{
		{"-07:00", fixedZone("UTC-7", -7*3600)},
		{"+14:00", fixedZone("UTC+14", 14*3600)},
	} {
		re := reopenAt(t, path, z.loc)
		r, err := re.Review("M-1")
		if err != nil {
			t.Fatalf("时区 %s 下 review: %v", z.name, err)
		}
		if len(r.Plans) != 1 {
			t.Fatalf("时区 %s 下应只有一项计划，得到 %d", z.name, len(r.Plans))
		}
		pv := r.Plans[0]
		if pv.Status != PlanStatusDone || pv.CertificateNumber != "C-OCT2" ||
			pv.CompletedAt != completedAt || pv.CreatedAt != createdText {
			t.Fatalf("时区 %s 下核对中的完成信息异常：%+v", z.name, pv.Plan)
		}
		var calDates []string
		for _, c := range r.History {
			calDates = append(calDates, c.CalDate)
		}
		if len(calDates) != 2 {
			t.Fatalf("时区 %s 下两张证书都应保留，得到 %v", z.name, calDates)
		}
		again, idem2, err := re.CompletePlan("PL-1", "C-OCT2")
		if err != nil || !idem2 || again.CompletedAt != completedAt {
			t.Fatalf("时区 %s 下同证书重复完成应幂等返回原结果，err=%v idem=%v %+v",
				z.name, err, idem2, again)
		}
		if items, _ := re.Todos(""); len(items) != 0 {
			t.Fatalf("时区 %s 下待办应仍为空，得到 %+v", z.name, items)
		}
	}
}

// TestCompletePlanCreatedDayWestOfUTCAllowsSameCalendarDay 方向 2：
// 计划在 2026-10-02T23:30:00-07:00 建立，换算 UTC 已是 10-03。证书只记载
// 校准日期而无时分秒，2026-10-02 校准的证书仍满足“不早于建立的本机日期
// 2026-10-02”，在任何操作时区下都应完成；10-01 的证书仍被拒绝。
func TestCompletePlanCreatedDayWestOfUTCAllowsSameCalendarDay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	created := time.Date(2026, 10, 2, 23, 30, 0, 0, fixedZone("UTC-7", -7*3600))
	const createdText = "2026-10-02T23:30:00-07:00"
	if created.Format(time.RFC3339) != createdText {
		t.Fatalf("测试预置建立时间应为 %s，得到 %s", createdText, created.Format(time.RFC3339))
	}
	seedPlanInZone(t, path, created, "PL-1")

	// 在各个操作时区（含会把该绝对时刻显示为 10-03 的 UTC 与 +08:00）下重开：
	// 先录入两张证书并尝试 10-01 的证书，必须一致拒绝且计划原样保留。
	for i, z := range completionZones {
		l := reopenAt(t, path, z.loc)
		if i == 0 {
			addDayBoundaryCerts(t, l)
		}
		_, _, err := l.CompletePlan("PL-1", "C-OCT1")
		if !IsValidation(err) ||
			!strings.Contains(err.Error(), "2026-10-01") ||
			!strings.Contains(err.Error(), "建立日期 2026-10-02") {
			t.Fatalf("时区 %s 下 10-01 证书应被拒绝并指明建立日期 10-02，得到 %v",
				z.name, err)
		}
		assertRejectedAndUntouched(t, l, createdText)
	}

	// 10-02 校准的证书与建立的本机日期同一天：证书没有当天时分秒，不能因
	// 计划建立得较晚（23:30）或换算后已是 10-03 而判过早。各时区下结论一致，
	// 这里在 +08:00（该时刻在当地已是 10-03 下午）下完成，最能暴露错误换算。
	l := reopenAt(t, path, fixedZone("UTC+8", 8*3600))
	done, idem, err := l.CompletePlan("PL-1", "C-OCT2")
	if err != nil || idem {
		t.Fatalf("10-02 校准的证书在 +08:00 操作时区下应能完成，err=%v idempotent=%v", err, idem)
	}
	if done.Status != PlanStatusDone || done.CertificateNumber != "C-OCT2" ||
		done.CreatedAt != createdText {
		t.Fatalf("完成结果异常或建立时间被换算改写：%+v", done)
	}
	if !strings.HasPrefix(done.CompletedAt, "2026-10-15T") {
		t.Fatalf("完成时间应取本次更晚日期的操作时刻，得到 %s", done.CompletedAt)
	}
	if items, _ := l.Todos(""); len(items) != 0 {
		t.Fatalf("完成后应从待办移除，得到 %+v", items)
	}
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || r.Plans[0].Status != PlanStatusDone ||
		r.Plans[0].CertificateNumber != "C-OCT2" || r.Plans[0].CreatedAt != createdText {
		t.Fatalf("核对中应显示已完成、保留 -07:00 原文建立时间：%+v", r.Plans)
	}
	gotDates := map[string]bool{}
	for _, c := range r.History {
		gotDates[c.CalDate] = true
	}
	if !gotDates["2026-10-01"] || !gotDates["2026-10-02"] {
		t.Fatalf("两张证书的原校准日期都应保留，得到 %v", gotDates)
	}
}
