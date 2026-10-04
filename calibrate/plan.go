package calibrate

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 计划状态：进行中的计划可改期、取消或完成；已完成与已取消均为终态，
// 不能再改期、取消或重新开启。
const (
	PlanStatusOpen     = "未完成"
	PlanStatusDone     = "已完成"
	PlanStatusCanceled = "已取消"
)

// 待办相对本机今天的标记。
const (
	TodoToday   = "今天需校准"
	TodoOverdue = "逾期"
	TodoFuture  = "未到计划日"
)

// PlanChange 记录一次改期：保留修改前后的日期、操作时间和原因。
type PlanChange struct {
	From      string `json:"from"`
	To        string `json:"to"`
	ChangedAt string `json:"changed_at"`
	Reason    string `json:"reason"`
}

// Plan 是一项校准计划。编号在全台账唯一，即使计划已结束编号也不再复用。
type Plan struct {
	Number       string `json:"number"`
	InstrumentID string `json:"instrument_id"`
	// PlannedDate 是当前计划日期；OriginalDate 保留建立时的计划日期。
	PlannedDate  string       `json:"planned_date"`
	OriginalDate string       `json:"original_date"`
	Note         string       `json:"note"`
	CreatedAt    string       `json:"created_at"`
	Changes      []PlanChange `json:"changes,omitempty"`
	Status       string       `json:"status"`
	// 取消时保留原计划日期、取消时间和原因。
	CanceledAt   string `json:"canceled_at,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`
	// 完成时保存完成时间和所用证书编号。
	CompletedAt       string `json:"completed_at,omitempty"`
	CertificateNumber string `json:"certificate_number,omitempty"`
}

// Open 报告计划是否尚未结束（未完成、未取消）。
func (p Plan) Open() bool { return p.Status == PlanStatusOpen }

// PlanInput 是建立校准计划的输入。
type PlanInput struct {
	Number       string
	InstrumentID string
	Date         string
	Note         string
}

func (l *Ledger) findPlan(number string) *Plan {
	for i := range l.data.Plans {
		if l.data.Plans[i].Number == number {
			return &l.data.Plans[i]
		}
	}
	return nil
}

// validatePlanDate 解析 YYYY-MM-DD 真实日期，且不得早于本机今天。
func (l *Ledger) validatePlanDate(raw, field string) (string, error) {
	_, text, err := parseFiniteDate(raw, field)
	if err != nil {
		return "", err
	}
	todayText := l.now().Format(DateLayout)
	if text < todayText {
		return "", validationError("%s %s 不能早于本机今天 %s", field, text, todayText)
	}
	return text, nil
}

// CreatePlan 为已登记器具建立一项校准计划。计划编号全台账唯一（即使对应
// 已结束计划也不能复用），计划日期须真实存在且不早于本机今天，说明非空；
// 每件器具最多有一项未完成、未取消的计划。任一条件不满足都明确拒绝，
// 不新增或改动任何记录。
func (l *Ledger) CreatePlan(in PlanInput) (*Plan, error) {
	number, err := cleanText(in.Number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	instID, err := cleanText(in.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("器具编号无效：%w", err)
	}
	note, err := cleanText(in.Note)
	if err != nil {
		return nil, fmt.Errorf("计划说明无效：%w", err)
	}
	if l.findInstrument(instID) == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", instID, ErrNotFound)
	}
	dateText, err := l.validatePlanDate(in.Date, "计划日期")
	if err != nil {
		return nil, err
	}
	if l.findPlan(number) != nil {
		return nil, validationError("计划编号 %s 已存在，编号必须全台账唯一且不能复用", number)
	}
	for i := range l.data.Plans {
		p := &l.data.Plans[i]
		if p.InstrumentID == instID && p.Open() {
			return nil, validationError(
				"器具 %s 已有未结束的计划 %s（计划日期 %s），每件器具最多一项未结束计划",
				instID, p.Number, p.PlannedDate)
		}
	}
	p := Plan{
		Number:       number,
		InstrumentID: instID,
		PlannedDate:  dateText,
		OriginalDate: dateText,
		Note:         note,
		CreatedAt:    l.now().Format(time.RFC3339),
		Status:       PlanStatusOpen,
	}
	l.data.Plans = append(l.data.Plans, p)
	if err := l.save(); err != nil {
		return nil, err
	}
	return l.findPlan(number), nil
}

// requireOpenPlan 找到计划并确认其尚未结束；已结束计划返回明确的校验错误。
func (l *Ledger) requireOpenPlan(number string) (*Plan, error) {
	p := l.findPlan(number)
	if p == nil {
		return nil, fmt.Errorf("计划编号 %s：%w", number, ErrNotFound)
	}
	if !p.Open() {
		return nil, validationError("计划 %s %s，已结束的计划不能再改期、取消或重新开启",
			number, p.Status)
	}
	return p, nil
}

// ReschedulePlan 改期一项未结束的计划。原因必须非空，新日期必须真实存在且
// 不早于操作当天；每次改期保留修改前后的日期、操作时间和原因。
// 找不到计划或计划已结束时拒绝且不改动记录。
//
// 业务校验全部通过后才写盘：若台账文件无法写入或替换，明确返回保存错误，
// 台账（含同一对象随后的核对、计划查询与待办）与改期前完全一致——当前计划
// 日期不改变，失败申请的原因和时间既不进入改期历史、不改变待办标记，也不会
// 在此后其他成功操作写盘时被顺带写入；恢复可写后重新提交才按当时时间生效，
// 且只新增一条从最后成功保存的日期到新日期的记录。
func (l *Ledger) ReschedulePlan(number, newDate, reason string) (*Plan, error) {
	number, err := cleanText(number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	reason, err = cleanText(reason)
	if err != nil {
		return nil, fmt.Errorf("改期原因无效：%w", err)
	}
	dateText, err := l.validatePlanDate(newDate, "新计划日期")
	if err != nil {
		return nil, err
	}
	p, err := l.requireOpenPlan(number)
	if err != nil {
		return nil, err
	}
	// 即使新日期与当前日期相同也照常记录：保留修改前后的日期、操作时间和原因。
	change := PlanChange{
		From:      p.PlannedDate,
		To:        dateText,
		ChangedAt: l.now().Format(time.RFC3339),
		Reason:    reason,
	}
	// 在整份计划切片的独立副本上暂存改期：只有原子替换台账文件成功后才提交。
	// 写盘失败时恢复原切片（含原计划日期与改期历史），保证同一台账对象随后看到的
	// 计划日期、改期历史与待办标记都与改期前一致，且不会被之后的写盘顺带写入。
	original := l.data.Plans
	staged := make([]Plan, len(original))
	copy(staged, original)
	for i := range staged {
		if staged[i].Number != number {
			continue
		}
		staged[i].Changes = append(append([]PlanChange(nil), staged[i].Changes...), change)
		staged[i].PlannedDate = dateText
	}
	l.data.Plans = staged
	if err := l.save(); err != nil {
		l.data.Plans = original
		return nil, err
	}
	return l.findPlan(number), nil
}

// CancelPlan 取消一项未结束的计划，原因必须非空。取消后保留原计划、取消时间
// 和原因，不再列入待办，也不能重新开启。找不到计划或计划已结束时拒绝。
//
// 业务校验全部通过后才写盘：若台账文件无法写入或替换，明确返回保存错误，
// 台账（含同一对象随后的计划查询、按器具核对与待办）与取消前完全一致——
// 计划仍为未完成并继续列入待办，取消时间和原因不留下本次申请的内容，也不会
// 在此后其他成功操作写盘时被顺带写入；文件仍不可写时再次提交仍报保存错误
// （不会被当成“已结束”拒绝），恢复可写后无需重新打开台账，重新提交才真正
// 取消，取消时间和原因取本次成功提交而非失败那次。
func (l *Ledger) CancelPlan(number, reason string) (*Plan, error) {
	number, err := cleanText(number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	reason, err = cleanText(reason)
	if err != nil {
		return nil, fmt.Errorf("取消原因无效：%w", err)
	}
	if _, err := l.requireOpenPlan(number); err != nil {
		return nil, err
	}
	canceledAt := l.now().Format(time.RFC3339)
	// 在整份计划切片的独立副本上暂存取消：只有原子替换台账文件成功后才提交。
	// 写盘失败时恢复原切片（计划仍为未完成、取消时间与原因为空），保证同一
	// 台账对象随后看到的计划状态、待办与核对都与取消前一致，再次提交不会被
	// 当成“已结束”，失败信息也不会被之后的写盘顺带写入。
	original := l.data.Plans
	staged := make([]Plan, len(original))
	copy(staged, original)
	for i := range staged {
		if staged[i].Number != number {
			continue
		}
		staged[i].Status = PlanStatusCanceled
		staged[i].CanceledAt = canceledAt
		staged[i].CancelReason = reason
	}
	l.data.Plans = staged
	if err := l.save(); err != nil {
		l.data.Plans = original
		return nil, err
	}
	return l.findPlan(number), nil
}

// CompletePlan 用一张台账中已有的证书完成计划。证书必须属于该器具，校准日期
// 不得早于计划最初建立的本机日期，且未被用于完成其他计划。符合条件后保存
// 完成时间和证书编号；超差证书同样表示校准工作已完成。
//
// 业务校验全部通过后才写盘：若台账文件无法写入或替换，明确返回保存错误，
// 台账（含同一对象随后的计划查询、按器具核对与待办）与完成前完全一致——
// 计划仍为未完成并继续列入待办，完成时间与关联证书编号保持操作前的值，
// 计划日期、已有改期记录和待办日期标记都不改变，该证书也不会被算作已用于
// 完成计划（既不会被随后的提交当成“此前已完成”跳过保存，也不会被其他成功
// 写盘顺带写入文件）；恢复可写后无需重新打开台账，重新提交才真正完成，
// 完成时间取本次成功操作而非失败时刻。
//
// 再次用同一证书完成同一已保存为完成的计划返回原结果，不增加记录或刷新
// 完成时间；已完成计划改用另一证书、证书不存在或不符合条件、完成已取消
// 计划均拒绝。
func (l *Ledger) CompletePlan(number, certificateNumber string) (*Plan, bool, error) {
	number, err := cleanText(number)
	if err != nil {
		return nil, false, fmt.Errorf("计划编号无效：%w", err)
	}
	certificateNumber, err = cleanText(certificateNumber)
	if err != nil {
		return nil, false, fmt.Errorf("证书编号无效：%w", err)
	}
	p := l.findPlan(number)
	if p == nil {
		return nil, false, fmt.Errorf("计划编号 %s：%w", number, ErrNotFound)
	}
	// 幂等：同一证书再次完成同一计划，返回原结果，不刷新完成时间。
	if p.Status == PlanStatusDone {
		if p.CertificateNumber == certificateNumber {
			return p, true, nil
		}
		return nil, false, validationError(
			"计划 %s 已用证书 %s 完成，已完成计划不能改用另一证书",
			number, p.CertificateNumber)
	}
	if p.Status == PlanStatusCanceled {
		return nil, false, validationError("计划 %s 已取消，取消的计划不能完成；需要继续安排请建立新计划", number)
	}

	cert := l.findCertificate(certificateNumber)
	if cert == nil {
		return nil, false, fmt.Errorf("证书编号 %s：%w", certificateNumber, ErrNotFound)
	}
	if cert.InstrumentID != p.InstrumentID {
		return nil, false, validationError(
			"证书 %s 属于器具 %s，不能用于完成器具 %s 的计划 %s",
			certificateNumber, cert.InstrumentID, p.InstrumentID, number)
	}
	// 计划建立的本机日期：CreatedAt 是 RFC3339，取其日历日期；与证书校准日期
	// 按日历日期比较，校准日期不得早于该日期（等于可以）。
	created, perr := time.Parse(time.RFC3339, p.CreatedAt)
	if perr != nil {
		return nil, false, fmt.Errorf("计划 %s 建立时间已损坏: %w", number, perr)
	}
	createdDay := created.Format(DateLayout)
	if cert.CalDate < createdDay {
		return nil, false, validationError(
			"证书 %s 的校准日期 %s 早于计划 %s 的建立日期 %s，不能用于完成该计划",
			certificateNumber, cert.CalDate, number, createdDay)
	}
	// 同一证书不能用于完成两项计划；以幂等方式再次完成本计划已在前面处理。
	for i := range l.data.Plans {
		other := &l.data.Plans[i]
		if other.Number != number && other.Status == PlanStatusDone &&
			other.CertificateNumber == certificateNumber {
			return nil, false, validationError(
				"证书 %s 已用于完成计划 %s，不能再用于完成其他计划",
				certificateNumber, other.Number)
		}
	}

	completedAt := l.now().Format(time.RFC3339)
	// 在整份计划切片的独立副本上暂存完成：只有原子替换台账文件成功后才提交。
	// 写盘失败时恢复原切片（计划仍为未完成、完成时间与关联证书编号为空），
	// 保证同一台账对象随后看到的计划状态、待办与证书占用都与完成前一致，
	// 再次提交不会被当成“此前已完成”，失败信息也不会被之后的写盘顺带写入。
	original := l.data.Plans
	staged := make([]Plan, len(original))
	copy(staged, original)
	for i := range staged {
		if staged[i].Number != number {
			continue
		}
		staged[i].Status = PlanStatusDone
		staged[i].CompletedAt = completedAt
		staged[i].CertificateNumber = certificateNumber
	}
	l.data.Plans = staged
	if err := l.save(); err != nil {
		l.data.Plans = original
		return nil, false, err
	}
	return l.findPlan(number), false, nil
}

// TodoItem 是待办查询中的一项计划及其按本机日期重新判断的标记。
type TodoItem struct {
	PlanNumber     string `json:"plan_number"`
	InstrumentID   string `json:"instrument_id"`
	InstrumentName string `json:"instrument_name"`
	Status         Status `json:"status"`
	PlannedDate    string `json:"planned_date"`
	Note           string `json:"note"`
	Marker         string `json:"marker"`
}

// planMarker 按计划日期相对今天给出标记：今天“今天需校准”，已过“逾期”，
// 未来“未到计划日”。
func planMarker(plannedDate, todayText string) string {
	switch {
	case plannedDate < todayText:
		return TodoOverdue
	case plannedDate == todayText:
		return TodoToday
	default:
		return TodoFuture
	}
}

// Todos 列出未结束的计划作为待办。instrumentID 为空时列出全部器具的待办，
// 否则只列该器具（器具不存在时报 ErrNotFound）。结果按计划日期从早到晚、
// 同日按器具编号排列。查询不产生任何使用记录或写盘。
func (l *Ledger) Todos(instrumentID string) ([]TodoItem, error) {
	id := strings.TrimSpace(instrumentID)
	if id != "" && l.findInstrument(id) == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	todayText := l.now().Format(DateLayout)
	items := []TodoItem{}
	for _, p := range l.data.Plans {
		if !p.Open() {
			continue
		}
		if id != "" && p.InstrumentID != id {
			continue
		}
		inst := l.findInstrument(p.InstrumentID)
		if inst == nil {
			continue
		}
		items = append(items, TodoItem{
			PlanNumber:     p.Number,
			InstrumentID:   inst.ID,
			InstrumentName: inst.Name,
			Status:         inst.Status,
			PlannedDate:    p.PlannedDate,
			Note:           p.Note,
			Marker:         planMarker(p.PlannedDate, todayText),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].PlannedDate != items[j].PlannedDate {
			return items[i].PlannedDate < items[j].PlannedDate
		}
		return items[i].InstrumentID < items[j].InstrumentID
	})
	return items, nil
}

// PlanView 是计划的完整视图，供按器具核对时展示当前与已结束计划、
// 改期记录和关联证书编号。
type PlanView struct {
	Plan
	Marker string `json:"marker"`
}

// plansOf 返回某器具的全部计划（含已结束），按建立先后排列。
func (l *Ledger) plansOf(id string) []Plan {
	var out []Plan
	for _, p := range l.data.Plans {
		if p.InstrumentID == id {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// planViews 构造器具核对用的计划视图；标记仅对未结束计划有意义。
func (l *Ledger) planViews(id string, today time.Time) []PlanView {
	todayText := today.Format(DateLayout)
	plans := l.plansOf(id)
	out := make([]PlanView, 0, len(plans))
	for _, p := range plans {
		v := PlanView{Plan: p}
		if p.Open() {
			v.Marker = planMarker(p.PlannedDate, todayText)
		}
		out = append(out, v)
	}
	return out
}

// Plans 返回某器具的全部计划（含已结束），按建立先后排列；
// 器具不存在时报 ErrNotFound。
func (l *Ledger) Plans(instrumentID string) ([]PlanView, error) {
	id := strings.TrimSpace(instrumentID)
	inst := l.findInstrument(id)
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	return l.planViews(id, l.now()), nil
}
