package calibrate

import (
	"strings"
	"testing"
	"time"
)

// steppingClock 前几次调用返回 before，之后返回 after，模拟一次查询
// 进行中跨过午夜：第一次取时还在前一天，随后的取时已到新的一天。
type steppingClock struct {
	before time.Time
	after  time.Time
	calls  int
}

func (c *steppingClock) now() time.Time {
	c.calls++
	if c.calls == 1 {
		return c.before
	}
	return c.after
}

// 建立“在用 + 最近证书合格（截止 2026-10-04）+ 未结束计划（计划日 2026-10-03）”
// 的台账，时钟固定在 2026-10-03。
func setupCrossMidnightLedger(t *testing.T) *Ledger {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)}
	l := newTestLedger(t, clock)
	if _, err := l.Register(RegisterInput{ID: "M-1", Name: "万用表", AllowedError: 0.5}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, _, err := l.AddCertificate(CertificateInput{
		InstrumentID: "M-1", Number: "C-1",
		CalDate: "2026-09-01", Expiry: "2026-10-04",
		Method: "JJF", Error: 0.1, Summary: "例行校准",
	}); err != nil {
		t.Fatalf("add certificate: %v", err)
	}
	if _, err := l.CreatePlan(PlanInput{
		Number: "PL-1", InstrumentID: "M-1", Date: "2026-10-03", Note: "年度例行校准",
	}); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return l
}

// 核对在 10 月 3 日结束前开始、查询过程中跨过午夜进入 10 月 4 日时，
// 整份结果仍应统一按 10 月 3 日判断：可以使用、两处证书均未到期、
// 计划标记“今天需校准”。
func TestReviewUsesSingleDateAcrossMidnight(t *testing.T) {
	l := setupCrossMidnightLedger(t)
	l.now = (&steppingClock{
		before: time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local),
		after:  time.Date(2026, 10, 4, 0, 0, 1, 0, time.Local),
	}).now

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !r.CanUse {
		t.Fatalf("跨午夜核对仍应按 10-03 判定为可用，实际拒绝原因：%v", r.Reasons)
	}
	if r.Latest == nil || r.Latest.Expired {
		t.Fatalf("最近证书应按 10-03 判定为未到期：%+v", r.Latest)
	}
	if len(r.History) != 1 || r.History[0].Expired {
		t.Fatalf("历史中的同一证书应与最近证书一致（未到期）：%+v", r.History)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoToday {
		t.Fatalf("未结束计划应按 10-03 标记为“今天需校准”：%+v", r.Plans)
	}
}

// 进入 10 月 4 日后重新核对：必须重新采用当时的本机日期（不沿用上次结果），
// 显示不能使用并列出到期原因，两处证书均已到期，计划标记“逾期”。
func TestReviewReReadsDateOnNextQuery(t *testing.T) {
	l := setupCrossMidnightLedger(t)
	l.now = (&fakeClock{t: time.Date(2026, 10, 4, 8, 0, 0, 0, time.Local)}).now

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse {
		t.Fatalf("10-04 重新核对判定为不可用，实际为可用")
	}
	foundExpiryReason := false
	for _, reason := range r.Reasons {
		if strings.Contains(reason, "到期") {
			foundExpiryReason = true
		}
	}
	if !foundExpiryReason {
		t.Fatalf("拒绝原因应包含最近证书到期：%v", r.Reasons)
	}
	if r.Latest == nil || !r.Latest.Expired {
		t.Fatalf("最近证书应按 10-04 判定为已到期：%+v", r.Latest)
	}
	if len(r.History) != 1 || !r.History[0].Expired {
		t.Fatalf("历史中的同一证书应与最近证书一致（已到期）：%+v", r.History)
	}
	if len(r.Plans) != 1 || r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("未结束计划应按 10-04 标记为“逾期”：%+v", r.Plans)
	}
}

// 同一台账对象连续两次核对，各自取当时的本机日期：第一次按 10-03 可用，
// 时钟拨到 10-04 后第二次核对无需重新打开台账即按新日期判定为不可用。
func TestReviewDoesNotReusePreviousResult(t *testing.T) {
	l := setupCrossMidnightLedger(t)
	clock := &fakeClock{t: time.Date(2026, 10, 3, 23, 0, 0, 0, time.Local)}
	l.now = clock.now

	first, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("first review: %v", err)
	}
	if !first.CanUse || first.Latest.Expired || first.Plans[0].Marker != TodoToday {
		t.Fatalf("10-03 核对应为可用、未到期、今天需校准：%+v", first)
	}

	clock.t = time.Date(2026, 10, 4, 0, 30, 0, 0, time.Local)
	second, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("second review: %v", err)
	}
	if second.CanUse || !second.Latest.Expired || !second.History[0].Expired ||
		second.Plans[0].Marker != TodoOverdue {
		t.Fatalf("10-04 重新核对应为不可用、两处证书到期、计划逾期：%+v", second)
	}
}

// 已结束计划不随核对日期改变展示：取消与完成信息保持原样，不带待办标记。
func TestReviewKeepsClosedPlansUnchanged(t *testing.T) {
	l := setupCrossMidnightLedger(t)
	if _, err := l.CancelPlan("PL-1", "暂停送检"); err != nil {
		t.Fatalf("cancel plan: %v", err)
	}
	l.now = (&steppingClock{
		before: time.Date(2026, 10, 3, 23, 59, 59, 0, time.Local),
		after:  time.Date(2026, 10, 4, 0, 0, 1, 0, time.Local),
	}).now

	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 {
		t.Fatalf("应保留已结束计划：%+v", r.Plans)
	}
	p := r.Plans[0]
	if p.Status != PlanStatusCanceled || p.CancelReason != "暂停送检" || p.CanceledAt == "" {
		t.Fatalf("已取消计划应保留原状态与取消信息：%+v", p)
	}
	if p.Marker != "" {
		t.Fatalf("已结束计划不应带待办标记：%q", p.Marker)
	}
}

// 核对只查询：不产生使用记录，也不改动计划与证书。
func TestReviewDoesNotWrite(t *testing.T) {
	l := setupCrossMidnightLedger(t)
	usageBefore := len(l.data.Usage)
	if _, err := l.Review("M-1"); err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(l.data.Usage) != usageBefore {
		t.Fatalf("核对不应新增使用记录")
	}
	reopened, err := openAt(l.Path(), func() time.Time {
		return time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(reopened.data.Usage) != usageBefore {
		t.Fatalf("核对不应向台账文件写入使用记录")
	}
	if len(reopened.data.Plans) != 1 || reopened.data.Plans[0].Status != PlanStatusOpen {
		t.Fatalf("核对不应改动计划：%+v", reopened.data.Plans)
	}
}

// 无证书、无计划的器具与未知编号的行为保持不变。
func TestReviewEmptyAndUnknownUnchanged(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)}
	l := newTestLedger(t, clock)
	if _, err := l.Register(RegisterInput{ID: "M-9", Name: "空器具", AllowedError: 1}); err != nil {
		t.Fatalf("register: %v", err)
	}
	r, err := l.Review("M-9")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if r.CanUse || len(r.Reasons) == 0 || r.Latest != nil || len(r.History) != 0 || len(r.Plans) != 0 {
		t.Fatalf("无证书无计划器具应按原有方式展示：%+v", r)
	}
	if _, err := l.Review("不存在"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("未知编号应明确报不存在：%v", err)
	}
}
