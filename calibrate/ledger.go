// Package calibrate 是本地器具校准与证书台账。
//
// 台账保存在用户指定的本机 JSON 文件中，不连接任何真实实验室。
// 退出后重新打开同一台账可继续查询与操作；不同台账文件的数据互不混入。
package calibrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 器具状态。
const (
	StatusActive   Status = "在用"
	StatusInactive Status = "停用"
	StatusPending  Status = "待校准"
)

// Status 表示器具所处的状态。
type Status string

// 业务错误。调用方可以用 errors.Is 区分无效输入、不存在与冲突。
var (
	// ErrInvalidInput 表示用户填写的内容不符合规则，台账未被修改。
	ErrInvalidInput = errors.New("无效输入")
	// ErrNotFound 表示指定的器具在台账中不存在，台账未被修改。
	ErrNotFound = errors.New("不存在")
	// ErrConflict 表示与已有记录冲突（如编号重复且内容不一致），已有数据未被修改。
	ErrConflict = errors.New("冲突")
)

// Instrument 是登记在册的器具。
type Instrument struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	AllowedError float64 `json:"allowed_error"`
	Status       Status  `json:"status"`
}

// Certificate 是录入的校准证书。
type Certificate struct {
	Number        string  `json:"number"`
	InstrumentID  string  `json:"instrument_id"`
	CalDate       string  `json:"cal_date"`   // YYYY-MM-DD
	ExpiryDate    string  `json:"expiry_date"` // YYYY-MM-DD
	Method        string  `json:"method"`
	MeasuredError float64 `json:"measured_error"`
	Summary       string  `json:"summary"`
}

// UsageRecord 是一次使用申请的结果记录。
type UsageRecord struct {
	InstrumentID string    `json:"instrument_id"`
	Time         time.Time `json:"time"`
	Approved     bool      `json:"approved"`
	Reasons      []string  `json:"reasons"`
}

// CheckResult 是按器具核对时返回的完整信息。
type CheckResult struct {
	Instrument         Instrument
	Approved           bool
	Reasons            []string
	LatestCertificate  *Certificate
	Certificates       []Certificate
	UsageRecords       []UsageRecord
}

// Ledger 是一个本地台账实例。零值不可使用，请用 Open 打开。
type Ledger struct {
	path         string
	Instruments  []Instrument  `json:"instruments"`
	Certificates []Certificate `json:"certificates"`
	Usage        []UsageRecord `json:"usage"`

	// now 返回本机当前时间；测试时可替换。
	now func() time.Time
}

// Open 打开指定路径的台账文件。文件不存在时返回一个空台账（首次保存时创建）；
// 文件存在但内容损坏时返回错误，不会覆盖原文件。
func Open(path string) (*Ledger, error) {
	l := &Ledger{path: path, now: time.Now}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("读取台账失败: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("台账文件内容无法解析: %w", err)
	}
	if l.now == nil {
		l.now = time.Now
	}
	return l, nil
}

// Path 返回台账文件路径。
func (l *Ledger) Path() string { return l.path }

// save 原子化写入：先写同目录临时文件，再重命名，避免异常情况下留下半份记录。
func (l *Ledger) save() error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(l.path)
	tmp, err := os.CreateTemp(dir, ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时台账文件失败: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("写入台账失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("同步台账失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		cleanup()
		return fmt.Errorf("保存台账失败: %w", err)
	}
	return nil
}

// today 返回本机当前日期（按日历日，去掉时分秒）。
func (l *Ledger) today() time.Time {
	now := l.now()
	loc := now.Location()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

func parseDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("日期 %q 无效，必须为 YYYY-MM-DD 且实际存在", s)
	}
	return t, nil
}

func (l *Ledger) findInstrument(id string) *Instrument {
	for i := range l.Instruments {
		if l.Instruments[i].ID == id {
			return &l.Instruments[i]
		}
	}
	return nil
}

// Instrument 返回指定编号的器具，不存在时返回 nil。
func (l *Ledger) Instrument(id string) *Instrument {
	return l.findInstrument(strings.TrimSpace(id))
}

// RegisterInstrument 登记一台新器具。
//
// 编号与名称不能留空或只有空白；允许误差必须是有限且不小于零的数；
// 编号在台账中唯一。新器具一律处于待校准状态。任何校验失败都不会改动台账。
func (l *Ledger) RegisterInstrument(id, name string, allowedError float64) error {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" {
		return fmt.Errorf("%w: 器具编号不能留空或只有空白", ErrInvalidInput)
	}
	if name == "" {
		return fmt.Errorf("%w: 器具名称不能留空或只有空白", ErrInvalidInput)
	}
	if math.IsNaN(allowedError) || math.IsInf(allowedError, 0) {
		return fmt.Errorf("%w: 允许误差必须是有限数", ErrInvalidInput)
	}
	if allowedError < 0 {
		return fmt.Errorf("%w: 允许误差不能小于零", ErrInvalidInput)
	}
	for _, inst := range l.Instruments {
		if inst.ID == id {
			return fmt.Errorf("%w: 器具编号 %q 已存在，原记录保持不变", ErrConflict, id)
		}
	}
	l.Instruments = append(l.Instruments, Instrument{
		ID:           id,
		Name:         name,
		AllowedError: allowedError,
		Status:       StatusPending,
	})
	return l.save()
}

// SetStatus 切换器具状态。可在在用、停用、待校准之间切换；
// 切换为在用本身不代表已经校准，停用也不会删除证书和使用记录。
func (l *Ledger) SetStatus(id string, status Status) error {
	id = strings.TrimSpace(id)
	switch status {
	case StatusActive, StatusInactive, StatusPending:
	default:
		return fmt.Errorf("%w: 无效状态 %q，应为 在用、停用或待校准", ErrInvalidInput, status)
	}
	inst := l.findInstrument(id)
	if inst == nil {
		return fmt.Errorf("%w: 器具 %q 不存在，台账未改动", ErrNotFound, id)
	}
	inst.Status = status
	return l.save()
}

// sameCertificate 判断两张证书的业务字段是否完全一致。
func sameCertificate(a, b *Certificate) bool {
	return a.Number == b.Number &&
		a.InstrumentID == b.InstrumentID &&
		a.CalDate == b.CalDate &&
		a.ExpiryDate == b.ExpiryDate &&
		a.Method == b.Method &&
		a.MeasuredError == b.MeasuredError &&
		a.Summary == b.Summary
}

// AddCertificate 录入校准证书。
//
// 规则：
//   - 器具必须已登记；
//   - 编号、方法、摘要不能留空或只有空白；
//   - 日期必须为 YYYY-MM-DD 且实际存在；校准日期不能晚于本机今天；截止日必须晚于校准日期；
//   - 测得误差必须是有限数；
//   - 是否超差由系统按 |测得误差| 与器具允许误差比较得出（等于限值算合格），录入者不能自行选择结论；
//   - 证书编号在整个台账中唯一：同号且业务字段完全一致时返回原证书、不增加历史；
//     同号但内容冲突时拒绝，已有数据不变；
//   - 同一器具同一校准日期只允许一张证书（同号幂等除外）。
func (l *Ledger) AddCertificate(c Certificate) (cert *Certificate, created bool, err error) {
	c.Number = strings.TrimSpace(c.Number)
	c.InstrumentID = strings.TrimSpace(c.InstrumentID)
	c.Method = strings.TrimSpace(c.Method)
	c.Summary = strings.TrimSpace(c.Summary)

	if c.Number == "" {
		return nil, false, fmt.Errorf("%w: 证书编号不能留空或只有空白", ErrInvalidInput)
	}
	if c.InstrumentID == "" {
		return nil, false, fmt.Errorf("%w: 器具编号不能留空或只有空白", ErrInvalidInput)
	}
	if c.Method == "" {
		return nil, false, fmt.Errorf("%w: 校准方法不能留空或只有空白", ErrInvalidInput)
	}
	if c.Summary == "" {
		return nil, false, fmt.Errorf("%w: 摘要不能留空或只有空白", ErrInvalidInput)
	}
	if math.IsNaN(c.MeasuredError) || math.IsInf(c.MeasuredError, 0) {
		return nil, false, fmt.Errorf("%w: 测得误差必须是有限数", ErrInvalidInput)
	}
	cal, err := parseDate(c.CalDate)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	exp, err := parseDate(c.ExpiryDate)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	today := l.today()
	if cal.After(today) {
		return nil, false, fmt.Errorf("%w: 校准日期 %s 晚于本机今天 %s", ErrInvalidInput, c.CalDate, today.Format("2006-01-02"))
	}
	if !exp.After(cal) {
		return nil, false, fmt.Errorf("%w: 有效期截止日 %s 必须晚于校准日期 %s", ErrInvalidInput, c.ExpiryDate, c.CalDate)
	}
	inst := l.findInstrument(c.InstrumentID)
	if inst == nil {
		return nil, false, fmt.Errorf("%w: 器具 %q 不存在，台账未改动", ErrNotFound, c.InstrumentID)
	}

	// 证书编号全局唯一：同号幂等，异号冲突。
	for i := range l.Certificates {
		existing := &l.Certificates[i]
		if existing.Number == c.Number {
			if sameCertificate(existing, &c) {
				return existing, false, nil
			}
			return nil, false, fmt.Errorf("%w: 证书编号 %q 已存在且业务字段不一致，已有证书未改动", ErrConflict, c.Number)
		}
	}
	// 同一器具同一天只接受一张证书。
	for i := range l.Certificates {
		existing := &l.Certificates[i]
		if existing.InstrumentID == c.InstrumentID && existing.CalDate == c.CalDate {
			return nil, false, fmt.Errorf(
				"%w: 器具 %s 在校准日期 %s 已有证书 %q，同一天不能接受两张不同编号的证书，原记录保持不变",
				ErrConflict, c.InstrumentID, c.CalDate, existing.Number)
		}
	}

	l.Certificates = append(l.Certificates, c)
	if err := l.save(); err != nil {
		// 保存失败时回滚内存中的追加，避免内存与文件不一致。
		l.Certificates = l.Certificates[:len(l.Certificates)-1]
		return nil, false, err
	}
	return &l.Certificates[len(l.Certificates)-1], true, nil
}

// Qualified 判断证书是否合格：|测得误差| 不超过器具允许误差（等于限值算合格）。
// 结论由系统计算，不接受录入者自行指定。
func Qualified(allowedError, measuredError float64) bool {
	return math.Abs(measuredError) <= allowedError
}

// Expired 判断有效期截止日在今天是否已到期。截止日当天起即算到期。
func Expired(expiryDate string, today time.Time) bool {
	exp, err := parseDate(expiryDate)
	if err != nil {
		return true
	}
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	return !today.Before(exp)
}

// LatestCertificate 返回器具最近的证书。最近与否按校准日期确定，与录入先后无关；
// 没有证书时返回 nil。
func (l *Ledger) LatestCertificate(instrumentID string) *Certificate {
	var best *Certificate
	for i := range l.Certificates {
		c := &l.Certificates[i]
		if c.InstrumentID != instrumentID {
			continue
		}
		if best == nil || c.CalDate > best.CalDate {
			best = c
		}
	}
	return best
}

// evaluate 评估器具当前能否使用，返回全部适用的拒绝原因（为空表示可用）。
func (l *Ledger) evaluate(inst *Instrument) (reasons []string, latest *Certificate) {
	switch inst.Status {
	case StatusInactive:
		reasons = append(reasons, "器具已停用")
	case StatusPending:
		reasons = append(reasons, "器具处于待校准状态")
	}
	latest = l.LatestCertificate(inst.ID)
	if latest == nil {
		reasons = append(reasons, "没有校准证书")
		return reasons, nil
	}
	if math.Abs(latest.MeasuredError) > inst.AllowedError {
		reasons = append(reasons, "最近证书超差")
	}
	if Expired(latest.ExpiryDate, l.today()) {
		reasons = append(reasons, "最近证书已到期")
	}
	return reasons, latest
}

// ApplyUse 申请使用器具。
//
// 只有器具处于在用状态、最近证书合格且未到期时才批准；
// 停用、待校准、没有证书、超差或到期都会被拒绝，并列出全部适用原因。
// 每次申请（无论批准还是拒绝）都保存申请时间、结果与当时原因；
// 器具编号不存在时明确报错且不产生任何记录。
func (l *Ledger) ApplyUse(id string) (approved bool, reasons []string, err error) {
	id = strings.TrimSpace(id)
	inst := l.findInstrument(id)
	if inst == nil {
		return false, nil, fmt.Errorf("%w: 器具 %q 不存在，未保存申请记录", ErrNotFound, id)
	}
	reasons, _ = l.evaluate(inst)
	rec := UsageRecord{
		InstrumentID: id,
		Time:         l.now(),
		Approved:     len(reasons) == 0,
		Reasons:      reasons,
	}
	l.Usage = append(l.Usage, rec)
	if err := l.save(); err != nil {
		l.Usage = l.Usage[:len(l.Usage)-1]
		return false, nil, err
	}
	return rec.Approved, rec.Reasons, nil
}

// Check 按器具核对：登记信息、当前能否使用及原因、最近证书与到期日、
// 全部历史证书和全部使用申请记录（含被拒绝记录）。
// 历史拒绝原因在申请当时固化，不随后来补录证书或切换状态而改变。
func (l *Ledger) Check(id string) (*CheckResult, error) {
	id = strings.TrimSpace(id)
	inst := l.findInstrument(id)
	if inst == nil {
		return nil, fmt.Errorf("%w: 器具 %q 不存在", ErrNotFound, id)
	}
	reasons, latest := l.evaluate(inst)

	certs := make([]Certificate, 0)
	for _, c := range l.Certificates {
		if c.InstrumentID == id {
			certs = append(certs, c)
		}
	}
	sort.Slice(certs, func(i, j int) bool { return certs[i].CalDate < certs[j].CalDate })

	records := make([]UsageRecord, 0)
	for _, u := range l.Usage {
		if u.InstrumentID == id {
			records = append(records, u)
		}
	}

	return &CheckResult{
		Instrument:        *inst,
		Approved:          len(reasons) == 0,
		Reasons:           reasons,
		LatestCertificate: latest,
		Certificates:      certs,
		UsageRecords:      records,
	}, nil
}

// ListInstruments 返回全部器具（按登记顺序）。
func (l *Ledger) ListInstruments() []Instrument {
	out := make([]Instrument, len(l.Instruments))
	copy(out, l.Instruments)
	return out
}
