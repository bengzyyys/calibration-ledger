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

// cloneChanges 返回改期历史切片的独立副本，使对外返回的计划与台账内部
// 存储不共享底层数组：调用方调换顺序、删改或追加展示用记录，不会改写
// 正式的改期历史。nil 保持 nil，以维持“没有改期历史”的既有表现。
func cloneChanges(in []PlanChange) []PlanChange {
	if in == nil {
		return nil
	}
	return append([]PlanChange(nil), in...)
}

// clonePlan 返回计划的独立深拷贝，连同改期历史一起复制。
func clonePlan(p Plan) Plan {
	p.Changes = cloneChanges(p.Changes)
	return p
}

// clonePlanPtr 返回台账内计划的独立深拷贝；找不到时返回 nil。
// 各计划操作返回的计划只是已保存内容的展示副本：调用方为了显示而改动
// 编号、日期、状态、关联证书或改期记录，都不能成为绕过计划操作直接改写
// 台账的途径，也不会在此后正常写盘时被带入台账文件。
func clonePlanPtr(p *Plan) *Plan {
	if p == nil {
		return nil
	}
	cp := clonePlan(*p)
	return &cp
}

// validatePlanDate 解析 YYYY-MM-DD 真实日期，且不得早于 now 所在的本机
// 日历日期。调用方传入本次操作开始时所取的本机时间，使日期合法性与操作
// 记录的时间对应同一时刻。
func (l *Ledger) validatePlanDate(raw, field string, now time.Time) (string, error) {
	_, text, err := parseFiniteDate(raw, field)
	if err != nil {
		return "", err
	}
	todayText := now.Format(DateLayout)
	if text < todayText {
		return "", validationError("%s %s 不能早于本机今天 %s", field, text, todayText)
	}
	return text, nil
}

// CreatePlan 为已登记器具建立一项校准计划。计划编号全台账唯一（即使对应
// 已结束计划也不能复用），计划日期须真实存在且不早于本机今天，说明非空；
// 每件器具最多有一项未完成、未取消的计划。任一条件不满足都明确拒绝，
// 不新增或改动任何记录。
//
// 业务校验全部通过后才写盘：若台账文件无法写入或替换，明确返回保存错误，
// 台账（含同一对象随后的计划查询、按器具核对与待办）与建立前完全一致——
// 失败的计划既不占用编号，也不占用该器具唯一的未结束计划名额，更不会在此后
// 其他成功操作写盘时被顺带写入；文件仍不可写时再次提交相同编号仍报保存错误
// （不会被当成编号重复或器具已有未结束计划而拒绝），恢复可写后无需重新打开
// 台账，重新提交才真正建立，建立时间取本次成功申请的时间。
//
// 返回的计划只是已保存内容的展示副本，与台账内存储不共享内存：调用方为
// 显示而改动编号、日期、状态或关联证书，不占用或释放正式计划编号，不影响
// 每件器具最多一项未结束计划的判断，也不会在此后正常写盘时被带入台账文件；
// 重新查询同一台账得到的仍是最后成功保存的计划。
// 每次建立申请只在开始处理时取一次本机时间：计划日期是否不早于“今天”
// 按该时刻的日历日期判断，正式保存的建立时间也取该时刻——即使校验与写盘
// 之间跨过午夜，建立时间仍属于申请开始的日历日期，当天计划不会被记成
// 次日建立，也不会在随后用申请当天的证书完成时被误判为证书早于建立。
// 该时刻不延续到下一次申请：下次建立重新取当时的本机时间判断。
func (l *Ledger) CreatePlan(in PlanInput) (*Plan, error) {
	now := l.now()
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
	dateText, err := l.validatePlanDate(in.Date, "计划日期", now)
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
		CreatedAt:    now.Format(time.RFC3339),
		Status:       PlanStatusOpen,
	}
	// 在独立的新切片上暂存新计划：只有原子替换台账文件成功后才提交。
	// 写盘失败时恢复原切片，保证同一台账对象随后看到的计划查询、待办与
	// 按器具核对都与建立前一致——失败计划不占用编号或该器具唯一的未结束
	// 计划名额，再次提交仍会走到保存而不会被当成重复计划拒绝，失败计划也
	// 不会被之后的写盘顺带写入。
	original := l.data.Plans
	staged := make([]Plan, 0, len(original)+1)
	staged = append(staged, original...)
	staged = append(staged, p)
	l.data.Plans = staged
	if err := l.save(); err != nil {
		l.data.Plans = original
		return nil, err
	}
	return clonePlanPtr(l.findPlan(number)), nil
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

// savePlanChange 是改期、取消、完成共用的一条规则：只有本次保存成功，对已有
// 计划的修改才算生效。它在整份计划切片的独立副本上暂存 change 对指定计划的
// 修改，只有原子替换台账文件成功后才提交；写盘失败时恢复原切片并明确返回保存
// 错误，保证同一台账对象随后看到的计划查询、待办与按器具核对都与修改前完全
// 一致——计划日期、状态、改期历史、取消信息与完成信息都保持修改前的内容，
// 失败的改动也不会被之后其他成功操作的写盘顺带写入。保存成功后返回该计划
// 已保存内容的独立展示副本：调用方为展示而改动字段或增删、调换改期记录，
// 不回写正式台账，也不影响此前或之后分别取得的其他结果。
//
// 调用方必须先完成全部业务校验再进入这里：未满足业务条件的申请按原有原因
// 拒绝，不会走到写盘而被混成保存错误。
func (l *Ledger) savePlanChange(number string, change func(*Plan)) (*Plan, error) {
	original := l.data.Plans
	staged := make([]Plan, len(original))
	copy(staged, original)
	for i := range staged {
		if staged[i].Number != number {
			continue
		}
		// 深拷贝改期历史：暂存副本与台账原切片不共享底层数组，change 对
		// 改期历史的追加不会提前渗进正式台账，写盘失败时恢复原切片即可。
		staged[i].Changes = cloneChanges(staged[i].Changes)
		change(&staged[i])
	}
	l.data.Plans = staged
	if err := l.save(); err != nil {
		l.data.Plans = original
		return nil, err
	}
	return clonePlanPtr(l.findPlan(number)), nil
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
//
// 返回的计划（含改期历史）是已保存内容的独立展示副本：调用方改动其中的
// 当前日期、状态或任意一条改期记录的原因、前后日期、操作时间，调换、删去
// 或追加列表记录，都只影响自己手中的那一份，不改写正式计划，也不影响
// 此前分别取得的操作结果、计划查询结果和核对结果。
func (l *Ledger) ReschedulePlan(number, newDate, reason string) (*Plan, error) {
	// 本次改期只取一次本机时间：新日期的合法性判断与留痕的操作时间对应
	// 同一时刻，跨午夜不会分裂到两天。
	now := l.now()
	number, err := cleanText(number)
	if err != nil {
		return nil, fmt.Errorf("计划编号无效：%w", err)
	}
	reason, err = cleanText(reason)
	if err != nil {
		return nil, fmt.Errorf("改期原因无效：%w", err)
	}
	dateText, err := l.validatePlanDate(newDate, "新计划日期", now)
	if err != nil {
		return nil, err
	}
	if _, err := l.requireOpenPlan(number); err != nil {
		return nil, err
	}
	// 即使新日期与当前日期相同也照常记录：保留修改前后的日期、操作时间和原因。
	// 从最后成功保存的日期出发追加一条记录，保存规则见 savePlanChange。
	return l.savePlanChange(number, func(p *Plan) {
		p.Changes = append(p.Changes, PlanChange{
			From:      p.PlannedDate,
			To:        dateText,
			ChangedAt: now.Format(time.RFC3339),
			Reason:    reason,
		})
		p.PlannedDate = dateText
	})
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
//
// 返回的取消结果是独立展示副本：调用方把状态改回未完成或清空取消信息，
// 只影响自己那一份，正式计划仍保持已取消且不再列入待办。
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
	// 保留原计划、取消时间和原因，按共用规则提交：保存成功才生效，
	// 见 savePlanChange。
	return l.savePlanChange(number, func(p *Plan) {
		p.Status = PlanStatusCanceled
		p.CanceledAt = canceledAt
		p.CancelReason = reason
	})
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
//
// 返回的完成结果（含再次完成返回的原结果）是独立展示副本：调用方把它改成
// 未完成或换掉证书编号，正式计划仍保持完成，原证书仍算已用于该计划，不能
// 因此拿去完成另一项计划，完成时间也不会被刷新。
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
	// 返回的同样是独立副本：即使把它改成未完成或换掉证书编号，正式计划
	// 仍保持完成、原证书仍算已用于该计划。
	if p.Status == PlanStatusDone {
		if p.CertificateNumber == certificateNumber {
			return clonePlanPtr(p), true, nil
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
	// 计划建立的本机日期：CreatedAt 是带“建立当时时区偏移”的 RFC3339。
	// time.Parse 返回的时刻自带该固定偏移，直接 Format 取到的就是建立时所在的
	// 本机日历日期；绝不能先 In(time.Local) 或 .UTC() 换算再取日——完成操作
	// 可能发生在另一个时区（或本机此后切换了时区），换算会把跨日的建立时刻移到
	// 相邻的日历日：东八区凌晨（00:30+08:00）换算 UTC 落到前一天、西七区深夜
	// （23:30-07:00）换算 UTC 落到后一天，都会使日期界限被错误地提前或推后。
	// 界限锚定在记录保存的偏移所表示的那一天上，与本次运行的时区、日期无关。
	// 与证书校准日期按日历日期比较，校准日期不得早于该日期（等于可以）；
	// 证书只记载日期、没有时分秒，建立当天的证书不因建立钟点较晚而被判过早。
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
	// 保存完成时间和所用证书编号，按共用规则提交：保存成功才算完成，
	// 见 savePlanChange。
	saved, err := l.savePlanChange(number, func(p *Plan) {
		p.Status = PlanStatusDone
		p.CompletedAt = completedAt
		p.CertificateNumber = certificateNumber
	})
	if err != nil {
		return nil, false, err
	}
	return saved, false, nil
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

// plansOf 返回某器具的全部计划（含已结束），按建立的实际时刻从早到晚排列。
// 返回的是深拷贝（含改期历史）：调用方整理按器具查询或核对结果中的
// 计划与改期记录，不会回写台账，也不会影响此前或之后分别取得的其他结果。
//
// 时间能否识别、按实际时刻比较以及异常时间的排列，统一走与全部使用申请
// 记录共用的规则（见 compareHistoryTime）：CreatedAt 是带偏移的 RFC3339
// 时间，同一件器具的计划可能在不同系统时区偏移下（含 Z 与显式零偏移）先后
// 建立，按实际时刻排列——不能按时间文字里的日期、小时大小判断先后，文字
// 日期更小或小时更小的记录未必更早，跨日记录也按实际时刻排列；各记录保存
// 时自带的偏移决定先后，查询当天的系统时区不能改变这些历史的先后关系。
// 无法识别建立时间的计划排在有效时间计划之后，彼此按时间原文字升序。
// 已完成、已取消与未完成的计划一起参与排序，状态、当前计划日期、改期时间
// 与完成时间均不作排序条件。时间无法区分先后时（实际时刻相同，或两者建立
// 时间都无法识别且原文字相同）由稳定排序保留其在台账中的原有次序，每项都
// 单独保留、不合并。
func (l *Ledger) plansOf(id string) []Plan {
	var out []Plan
	for _, p := range l.data.Plans {
		if p.InstrumentID == id {
			out = append(out, clonePlan(p))
		}
	}
	// 计划没有器具编号之外的同刻细分：plansOf 只收集同一器具的计划，
	// 建立时刻相同（含异常时间原文字相同）时直接保留台账原有次序，故不
	// 传入 tieBefore。
	sortHistoryByTime(out, func(p Plan) string { return p.CreatedAt }, nil)
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

// Plans 返回某器具的全部计划（含已完成、已取消与未完成），按建立的实际
// 时刻从早到晚排列；器具不存在时报 ErrNotFound。
//
// CreatedAt 是带偏移的 RFC3339 时间：同一件器具的计划可能在不同系统时区
// 偏移下（含 Z 与显式零偏移）先后建立，排序换算为同一基准下的绝对时刻，
// 因此显示文字里日期、小时更大的计划未必更晚，跨日计划也按实际时刻排列；
// 各计划保存时自带的偏移决定先后，查询当天的系统时区不能改变历史顺序。
// 已完成、已取消与未完成的计划一起参与排序，计划状态、当前计划日期、改期
// 时间与完成时间均不作为排序条件。实际时刻相同的计划（即使分别使用 Z、
// +00:00 或其他偏移）保留其在台账中的原有次序，每项都单独返回、不合并。
//
// 返回的计划（含改期历史）都是独立展示副本：建立时间保留原文字及偏移，
// 计划日期、改期历史、取消信息及关联证书编号都保持保存时的内容；调用方
// 调换顺序、删改或追加记录或改动任何字段，只影响自己手中的结果，不回写
// 台账，也不影响此前或之后分别取得的操作结果与核对结果。查询不写盘。
func (l *Ledger) Plans(instrumentID string) ([]PlanView, error) {
	id := strings.TrimSpace(instrumentID)
	inst := l.findInstrument(id)
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	return l.planViews(id, l.now()), nil
}
