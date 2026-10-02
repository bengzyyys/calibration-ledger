// 校准计划：为已登记器具安排下一次校准，支持改期、取消、完成与待办查询。
//
// 每件器具最多有一项未完成、未取消的计划；计划编号全台账唯一，计划结束后
// 也不能重复使用。所有日期均为 YYYY-MM-DD 日历日期，与本机今天比较。
package calibrate

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// PlanStatus 是校准计划的状态。
type PlanStatus string

const (
	// PlanActive 表示未完成、未取消的计划，列入待办。
	PlanActive PlanStatus = "未完成"
	// PlanCompleted 表示已用证书完成的计划，不再列入待办。
	PlanCompleted PlanStatus = "已完成"
	// PlanCancelled 表示已取消的计划，不再列入待办。
	PlanCancelled PlanStatus = "已取消"
)

// PlanChange 记录一次改期：修改前后的日期、操作时间与原因。
type PlanChange struct {
	OldDate string `json:"old_date"`
	NewDate string `json:"new_date"`
	At      string `json:"at"`
	Reason  string `json:"reason"`
}

// Plan 是一件器具的校准计划。
type Plan struct {
	Number       string     `json:"number"`
	InstrumentID string     `json:"instrument_id"`
	PlanDate     string     `json:"plan_date"`
	Description  string     `json:"description"`
	CreatedAt    string     `json:"created_at"`
	Status       PlanStatus `json:"status"`

	// 改期历史：每次改期保留前后日期、操作时间与原因。
	Changes []PlanChange `json:"changes,omitempty"`

	// 取消：保留原计划日期、取消时间与原因。
	CancelledAt  string `json:"cancelled_at,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`

	// 完成：保留完成时间与关联证书编号。
	CompletedAt       string `json:"completed_at,omitempty"`
	CertificateNumber string `json:"certificate_number,omitempty"`
}

func (l *Ledger) findPlan(number string) *Plan {
	for i := range l.data.Plans {
		if l.data.Plans[i].Number == number {
			return &l.data.Plans[i]
		}
	}
	return nil
}

// CreatePlanInput 是建立校准计划的输入。
type CreatePlanInput struct {
	InstrumentID string
	Number       string
	PlanDate     string
	Description  string
}

// CreatePlan 为已登记器具建立校准计划。器具编号、计划编号、说明为必填且不得为
// 空白；计划日期必须是实际存在的 YYYY-MM-DD 且不早于本机今天。计划编号全台账
// 唯一（计划结束后也不能重复使用），每件器具最多有一项未完成计划。任何校验
// 失败都明确拒绝，不新增或改动记录。
func (l *Ledger) CreatePlan(in CreatePlanInput) (*Plan, error) {
	instID, err := cleanText(in.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("器具编号无效：%w", err)
	}
	number, err := cleanText(in.Number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	desc, err := cleanText(in.Description)
	if err != nil {
		return nil, fmt.Errorf("计划说明无效：%w", err)
	}
	inst := l.findInstrument(instID)
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", instID, ErrNotFound)
	}
	_, dateText, err := parseFiniteDate(in.PlanDate, "计划日期")
	if err != nil {
		return nil, err
	}
	// 与“本机今天”按日历日期比较，计划日不早于操作当天。
	todayText := l.now().Format(DateLayout)
	if dateText < todayText {
		return nil, validationError("计划日期 %s 不能早于本机今天 %s", dateText, todayText)
	}
	if l.findPlan(number) != nil {
		return nil, validationError(
			"计划编号 %s 已存在；计划编号即使对应已结束的计划也不能重复使用", number)
	}
	for i := range l.data.Plans {
		p := &l.data.Plans[i]
		if p.InstrumentID == instID && p.Status == PlanActive {
			return nil, validationError(
				"器具 %s 已有未完成的计划 %s；每件器具最多有一项未完成、未取消的计划",
				instID, p.Number)
		}
	}

	plan := Plan{
		Number:       number,
		InstrumentID: instID,
		PlanDate:     dateText,
		Description:  desc,
		CreatedAt:    l.now().Format(time.RFC3339),
		Status:       PlanActive,
	}
	l.data.Plans = append(l.data.Plans, plan)
	if err := l.save(); err != nil {
		return nil, err
	}
	return l.findPlan(number), nil
}

// ReschedulePlan 改期未结束的计划：新日期不能早于操作当天，必须填写非空原因。
// 改期保留修改前后的日期、操作时间与原因。已完成、已取消或不存在的计划被拒绝，
// 不改动任何记录。
func (l *Ledger) ReschedulePlan(number, newDate, reason string) (*Plan, error) {
	num, err := cleanText(number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	why, err := cleanText(reason)
	if err != nil {
		return nil, fmt.Errorf("改期原因无效：%w", err)
	}
	_, dateText, err := parseFiniteDate(newDate, "新计划日期")
	if err != nil {
		return nil, err
	}
	todayText := l.now().Format(DateLayout)
	if dateText < todayText {
		return nil, validationError("新计划日期 %s 不能早于操作当天 %s", dateText, todayText)
	}
	plan := l.findPlan(num)
	if plan == nil {
		return nil, fmt.Errorf("计划编号 %s：%w", num, ErrNotFound)
	}
	if plan.Status != PlanActive {
		return nil, validationError("计划 %s 已%s，不能改期；需要继续安排时请建立新计划", num, plan.Status)
	}

	plan.Changes = append(plan.Changes, PlanChange{
		OldDate: plan.PlanDate,
		NewDate: dateText,
		At:      l.now().Format(time.RFC3339),
		Reason:  why,
	})
	plan.PlanDate = dateText
	if err := l.save(); err != nil {
		return nil, err
	}
	return plan, nil
}

// CancelPlan 取消未结束的计划：必须填写非空原因。取消后保留原计划日期、取消
// 时间与原因，计划不再列入待办。已完成、已取消或不存在的计划被拒绝，不改动记录。
func (l *Ledger) CancelPlan(number, reason string) (*Plan, error) {
	num, err := cleanText(number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	why, err := cleanText(reason)
	if err != nil {
		return nil, fmt.Errorf("取消原因无效：%w", err)
	}
	plan := l.findPlan(num)
	if plan == nil {
		return nil, fmt.Errorf("计划编号 %s：%w", num, ErrNotFound)
	}
	if plan.Status != PlanActive {
		return nil, validationError("计划 %s 已%s，不能取消；需要继续安排时请建立新计划", num, plan.Status)
	}

	plan.Status = PlanCancelled
	plan.CancelledAt = l.now().Format(time.RFC3339)
	plan.CancelReason = why
	if err := l.save(); err != nil {
		return nil, err
	}
	return plan, nil
}

// CompletePlan 用台账中已有的证书完成计划。证书必须属于该器具，校准日期不得
// 早于计划最初建立的本机日期，且不能已用于完成其他计划。条件满足时保存完成
// 时间与证书编号。超差证书也表示校准工作已完成，器具是否可用仍按现有状态、
// 最近证书结论与有效期判断。再次用同一证书完成同一计划返回原结果，不增加记录
// 或刷新完成时间。
func (l *Ledger) CompletePlan(number, certNumber string) (*Plan, bool, error) {
	num, err := cleanText(number)
	if err != nil {
		return nil, false, fmt.Errorf("计划编号无效：%w", err)
	}
	certNum, err := cleanText(certNumber)
	if err != nil {
		return nil, false, fmt.Errorf("证书编号无效：%w", err)
	}
	plan := l.findPlan(num)
	if plan == nil {
		return nil, false, fmt.Errorf("计划编号 %s：%w", num, ErrNotFound)
	}
	// 已完成计划：同一证书幂等返回原结果；改用其他证书一律拒绝。
	if plan.Status == PlanCompleted {
		if plan.CertificateNumber == certNum {
			return plan, true, nil
		}
		return nil, false, validationError(
			"计划 %s 已完成，不能改用证书 %s；需要继续安排时请建立新计划", num, certNum)
	}
	if plan.Status == PlanCancelled {
		return nil, false, validationError("计划 %s 已取消，不能完成；需要继续安排时请建立新计划", num)
	}

	cert := l.findCertificate(certNum)
	if cert == nil {
		return nil, false, validationError("证书 %s 不存在，不能用于完成计划", certNum)
	}
	if cert.InstrumentID != plan.InstrumentID {
		return nil, false, validationError(
			"证书 %s 属于器具 %s，与计划 %s 的器具 %s 不一致，不能用于完成该计划",
			certNum, cert.InstrumentID, num, plan.InstrumentID)
	}
	// 计划最初建立的本机日期：CreatedAt 的日历日期部分。
	createdDate := plan.CreatedAt
	if len(createdDate) > len(DateLayout) {
		createdDate = createdDate[:len(DateLayout)]
	}
	if cert.CalDate < createdDate {
		return nil, false, validationError(
			"证书 %s 的校准日期 %s 早于计划 %s 最初建立日期 %s，不能用于完成该计划",
			certNum, cert.CalDate, num, createdDate)
	}
	for i := range l.data.Plans {
		p := &l.data.Plans[i]
		if p.Status == PlanCompleted && p.CertificateNumber == certNum && p.Number != num {
			return nil, false, validationError(
				"证书 %s 已用于完成计划 %s，不能重复用于完成其他计划", certNum, p.Number)
		}
	}

	plan.Status = PlanCompleted
	plan.CompletedAt = l.now().Format(time.RFC3339)
	plan.CertificateNumber = certNum
	if err := l.save(); err != nil {
		return nil, false, err
	}
	return plan, false, nil
}

// PlanDueLabel 按计划日期与本机今天（YYYY-MM-DD 日历日期比较）给出到期标记：
// 今天为“今天需校准”，已过为“逾期”，未来为“未到计划日”。
func PlanDueLabel(planDate, today string) string {
	switch {
	case planDate == today:
		return "今天需校准"
	case planDate < today:
		return "逾期"
	default:
		return "未到计划日"
	}
}

// TodoPlan 是待办计划视图，附带器具信息与按查询当日计算的到期标记。
type TodoPlan struct {
	Plan
	InstrumentName   string `json:"instrument_name"`
	InstrumentStatus Status `json:"instrument_status"`
	DueLabel         string `json:"due_label"`
}

// TodoPlans 返回未结束（未完成、未取消）的计划，可按器具编号筛选；按计划日期
// 从早到晚排列，同日按器具编号排列。到期标记在每次查询时按本机日期重新判断，
// 查询不产生任何使用记录。
func (l *Ledger) TodoPlans(instrumentID string) ([]TodoPlan, error) {
	filter := strings.TrimSpace(instrumentID)
	if filter != "" {
		if l.findInstrument(filter) == nil {
			return nil, fmt.Errorf("器具编号 %s：%w", filter, ErrNotFound)
		}
	}
	todayText := l.now().Format(DateLayout)
	var out []TodoPlan
	for i := range l.data.Plans {
		p := &l.data.Plans[i]
		if p.Status != PlanActive {
			continue
		}
		if filter != "" && p.InstrumentID != filter {
			continue
		}
		item := TodoPlan{Plan: *p, DueLabel: PlanDueLabel(p.PlanDate, todayText)}
		if inst := l.findInstrument(p.InstrumentID); inst != nil {
			item.InstrumentName = inst.Name
			item.InstrumentStatus = inst.Status
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PlanDate != out[j].PlanDate {
			return out[i].PlanDate < out[j].PlanDate
		}
		return out[i].InstrumentID < out[j].InstrumentID
	})
	return out, nil
}

// PlansOf 返回某器具的全部计划（含已结束），按计划日期从早到晚、同日按计划
// 编号排列。
func (l *Ledger) PlansOf(id string) []Plan {
	var out []Plan
	for i := range l.data.Plans {
		if l.data.Plans[i].InstrumentID == id {
			out = append(out, l.data.Plans[i])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PlanDate != out[j].PlanDate {
			return out[i].PlanDate < out[j].PlanDate
		}
		return out[i].Number < out[j].Number
	})
	return out
}
