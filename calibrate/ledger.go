// Package calibrate 是本地器具校准与证书的本地台账。
//
// 台账只保存在本机指定文件中，不连接任何真实实验室。多次打开同一文件
// 可以继续查询与操作，不同文件之间的数据互不相干。
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

// 日期布局，所有入参日期都必须是实际存在的 YYYY-MM-DD。
const DateLayout = "2006-01-02"

// Status 是器具的运行状态。
type Status string

const (
	// StatusPending 表示待校准，新登记器具的初始状态。
	StatusPending Status = "待校准"
	// StatusInUse 表示在用；在用本身不代表已经校准。
	StatusInUse Status = "在用"
	// StatusRetired 表示停用；停用不删除证书与使用记录。
	StatusRetired Status = "停用"
)

// ParseStatus 把命令行或文件中的状态文本解析为 Status。
func ParseStatus(s string) (Status, error) {
	switch strings.TrimSpace(s) {
	case string(StatusPending), "pending":
		return StatusPending, nil
	case string(StatusInUse), "in-use", "in_use":
		return StatusInUse, nil
	case string(StatusRetired), "retired":
		return StatusRetired, nil
	default:
		return "", fmt.Errorf("无效状态 %q：只能是 在用、停用 或 待校准", s)
	}
}

// Instrument 是登记的器具信息。
type Instrument struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	AllowedError float64 `json:"allowed_error"`
	Status       Status  `json:"status"`
	RegisteredAt string  `json:"registered_at"`
}

// Certificate 是一张校准证书。
type Certificate struct {
	Number       string  `json:"number"`
	InstrumentID string  `json:"instrument_id"`
	CalDate      string  `json:"cal_date"`
	Expiry       string  `json:"expiry"`
	Method       string  `json:"method"`
	Error        float64 `json:"error"`
	Summary      string  `json:"summary"`
	CreatedAt    string  `json:"created_at"`
}

// Pass 按证书自身数据判断是否合格：测得误差绝对值不超过允许误差（含限值）。
// 结论由数据计算，录入者不能自行选择。
func (c Certificate) Pass(allowedError float64) bool {
	return math.Abs(c.Error) <= allowedError
}

// UsageRecord 是一次使用申请的留痕，原因在申请发生时冻结。
type UsageRecord struct {
	InstrumentID string   `json:"instrument_id"`
	RequestedAt  string   `json:"requested_at"`
	Allowed      bool     `json:"allowed"`
	Reasons      []string `json:"reasons"`
}

// ledgerData 是台账文件的持久化结构。
type ledgerData struct {
	Version      int           `json:"version"`
	Instruments  []Instrument  `json:"instruments"`
	Certificates []Certificate `json:"certificates"`
	Usage        []UsageRecord `json:"usage"`
	Plans        []Plan        `json:"plans,omitempty"`
}

// Ledger 是一份可操作的本地台账。
type Ledger struct {
	path string
	now  func() time.Time
	data ledgerData
}

// ValidationError 表示输入未通过业务校验，调用方不会改动任何已有记录。
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func validationError(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// IsValidation 报告 err 是否为业务校验错误。
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// ConflictError 表示同号证书内容不一致等冲突，已有数据保持不变。
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// IsConflict 报告 err 是否为冲突错误。
func IsConflict(err error) bool {
	var c *ConflictError
	return errors.As(err, &c)
}

// ErrNotFound 表示编号在台账中不存在。
var ErrNotFound = errors.New("记录不存在")

func cleanText(s string) (string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", validationError("不能留空或只有空白")
	}
	return t, nil
}

func parseFiniteDate(s, field string) (time.Time, string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return time.Time{}, "", validationError("%s不能留空", field)
	}
	parsed, err := time.Parse(DateLayout, t)
	if err != nil {
		return time.Time{}, "", validationError("%s必须是实际存在的日期（格式 YYYY-MM-DD）", field)
	}
	// time.Parse 对 YYYY-MM-DD 遇到超范围日期会直接报错；再用 Format
	// 往返一次，拒绝 2024-2-3 之类格式不符的输入。
	if parsed.Format(DateLayout) != t {
		return time.Time{}, "", validationError("%s必须是实际存在的日期（格式 YYYY-MM-DD）", field)
	}
	return parsed, t, nil
}

func finiteNumber(v float64, field string) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return validationError("%s必须是有限数", field)
	}
	return nil
}

// Open 打开 path 所指的本机台账；文件不存在时建立一份新台账。
// 同一文件可以反复打开继续操作，不同 path 的数据互不混入。
func Open(path string) (*Ledger, error) {
	return openAt(path, time.Now)
}

func openAt(path string, now func() time.Time) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("台账文件路径不能为空")
	}
	l := &Ledger{
		path: path,
		now:  now,
		data: ledgerData{Version: 1},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l, nil
		}
		return nil, fmt.Errorf("读取台账 %s: %w", path, err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &l.data); err != nil {
			return nil, fmt.Errorf("台账文件 %s 已损坏: %w", path, err)
		}
	}
	if l.data.Version == 0 {
		l.data.Version = 1
	}
	return l, nil
}

// Path 返回台账文件位置。
func (l *Ledger) Path() string { return l.path }

// save 把当前台账完整写回文件：先写临时文件再原子改名，
// 保证无效录入中断时不会留下半份记录。
func (l *Ledger) save() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("写入台账文件 %s 前创建目录失败: %w", l.path, err)
	}
	raw, err := json.MarshalIndent(l.data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化台账 %s: %w", l.path, err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("为台账 %s 创建临时文件: %w", l.path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("写入台账 %s 的临时文件: %w", l.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭台账 %s 的临时文件: %w", l.path, err)
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return fmt.Errorf("替换台账文件 %s: %w", l.path, err)
	}
	return nil
}

func (l *Ledger) findInstrument(id string) *Instrument {
	for i := range l.data.Instruments {
		if l.data.Instruments[i].ID == id {
			return &l.data.Instruments[i]
		}
	}
	return nil
}

func (l *Ledger) findCertificate(number string) *Certificate {
	for i := range l.data.Certificates {
		if l.data.Certificates[i].Number == number {
			return &l.data.Certificates[i]
		}
	}
	return nil
}

// RegisterInput 是登记器具的输入。
type RegisterInput struct {
	ID           string
	Name         string
	AllowedError float64
}

// Register 登记一件新器具。编号、名称不得为空白，允许误差必须是不小于零的
// 有限数；编号重复或输入无效时明确拒绝，原记录不变。新器具处于待校准状态。
func (l *Ledger) Register(in RegisterInput) (*Instrument, error) {
	id, err := cleanText(in.ID)
	if err != nil {
		return nil, fmt.Errorf("器具编号无效：%w", err)
	}
	name, err := cleanText(in.Name)
	if err != nil {
		return nil, fmt.Errorf("器具名称无效：%w", err)
	}
	if err := finiteNumber(in.AllowedError, "允许的绝对误差"); err != nil {
		return nil, err
	}
	if in.AllowedError < 0 {
		return nil, validationError("允许的绝对误差不能小于零")
	}
	if l.findInstrument(id) != nil {
		return nil, validationError("器具编号 %s 已存在，编号必须唯一", id)
	}
	inst := Instrument{
		ID:           id,
		Name:         name,
		AllowedError: in.AllowedError,
		Status:       StatusPending,
		RegisteredAt: l.now().Format(time.RFC3339),
	}
	l.data.Instruments = append(l.data.Instruments, inst)
	if err := l.save(); err != nil {
		return nil, err
	}
	return &inst, nil
}

// SetStatus 切换器具状态。切换为在用不代表已校准；停用不删除证书与使用记录。
//
// 目标状态合法且与最后成功保存的状态不同后才写盘：若台账文件无法写入或替换，
// 明确返回保存错误，台账（含同一对象随后的列出、按器具核对、待办与使用判断）
// 与切换前完全一致——器具仍保持最后成功保存的状态，失败的目标状态既不会提前
// 参与使用资格判断，也不会在此后其他成功操作写盘时被顺带写入；文件仍不可写时
// 再次提交同一目标状态仍报保存错误（不会因内存已被改成目标状态而被当成
// “状态未变”跳过保存返回成功）。恢复可写后无需重新打开台账，重新提交且保存
// 成功，新状态才生效。请求状态本来就等于最后成功保存的状态时直接成功，不写盘。
func (l *Ledger) SetStatus(id string, status Status) error {
	switch status {
	case StatusInUse, StatusRetired, StatusPending:
	default:
		return validationError("无效状态：只能是 在用、停用 或 待校准")
	}
	inst := l.findInstrument(id)
	if inst == nil {
		return fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	if inst.Status == status {
		return nil
	}
	// 在整份器具切片的独立副本上暂存新状态：只有原子替换台账文件成功后才
	// 提交。写盘失败时恢复原切片，保证同一台账对象随后看到的器具状态、列出
	// 结果、待办与使用判断都与切换前一致；再次提交相同状态仍会走到保存，
	// 失败状态也不会被之后的写盘顺带写入。
	original := l.data.Instruments
	staged := make([]Instrument, len(original))
	copy(staged, original)
	for i := range staged {
		if staged[i].ID == id {
			staged[i].Status = status
			break
		}
	}
	l.data.Instruments = staged
	if err := l.save(); err != nil {
		l.data.Instruments = original
		return err
	}
	return nil
}

// CertificateInput 是录入校准证书的输入。
type CertificateInput struct {
	InstrumentID string
	Number       string
	CalDate      string
	Expiry       string
	Method       string
	Error        float64
	Summary      string
}

// AddCertificate 录入一张校准证书并返回它；若同号证书且全部业务字段一致，
// 返回原证书且不增加历史（幂等）。同号内容不同报冲突且不改动已有数据。
//
// 返回的证书只是本次已保存内容的展示副本，与台账内存储不共享内存：调用方
// 整理返回结果中的误差、截止日、编号或其他字段，不会改写台账里的正式证书，
// 不影响最近证书的选择、使用资格判断与编号占用，也不会被此后的写盘带入文件；
// 重新查询得到的仍是实际保存的内容。
//
// 业务校验全部通过后才写盘：若台账文件无法写入或替换，明确返回保存错误，
// 台账（含同一对象随后的查询）与录入前完全一致——失败的证书既不进入历史、
// 不占用证书编号或该器具当天的位置，也不会在此后其他成功操作写盘时被顺带写入；
// 恢复可写后重新提交同号同内容会作为新证书正常录入。
func (l *Ledger) AddCertificate(in CertificateInput) (*Certificate, bool, error) {
	instID, err := cleanText(in.InstrumentID)
	if err != nil {
		return nil, false, fmt.Errorf("器具编号无效：%w", err)
	}
	number, err := cleanText(in.Number)
	if err != nil {
		return nil, false, fmt.Errorf("证书编号无效：%w", err)
	}
	method, err := cleanText(in.Method)
	if err != nil {
		return nil, false, fmt.Errorf("校准方法无效：%w", err)
	}
	summary, err := cleanText(in.Summary)
	if err != nil {
		return nil, false, fmt.Errorf("摘要无效：%w", err)
	}
	if err := finiteNumber(in.Error, "测得误差"); err != nil {
		return nil, false, err
	}

	inst := l.findInstrument(instID)
	if inst == nil {
		return nil, false, fmt.Errorf("器具编号 %s：%w", instID, ErrNotFound)
	}

	_, calText, err := parseFiniteDate(in.CalDate, "校准日期")
	if err != nil {
		return nil, false, err
	}
	_, expText, err := parseFiniteDate(in.Expiry, "有效期截止日")
	if err != nil {
		return nil, false, err
	}
	// 与“本机今天”按日历日期比较，避免时分秒和时区造成边界误差。
	todayText := l.now().Format(DateLayout)
	if calText > todayText {
		return nil, false, validationError("校准日期 %s 不能晚于本机今天 %s", calText, todayText)
	}
	if expText <= calText {
		return nil, false, validationError("有效期截止日 %s 必须晚于校准日期 %s", expText, calText)
	}

	if existing := l.findCertificate(number); existing != nil {
		same := existing.InstrumentID == instID &&
			existing.CalDate == calText &&
			existing.Expiry == expText &&
			existing.Method == method &&
			existing.Summary == summary &&
			existing.Error == in.Error
		if !same {
			return nil, false, &ConflictError{Msg: fmt.Sprintf(
				"证书编号 %s 已存在且内容不同，拒绝覆盖已有数据", number)}
		}
		// 同号且全部业务字段一致：返回原证书，不增加历史记录。
		// 返回独立副本：调用方对返回结果的整理不能成为改写正式证书的途径。
		dup := *existing
		return &dup, true, nil
	}

	// 同一器具同一天不接受两张不同编号的证书。
	for i := range l.data.Certificates {
		c := &l.data.Certificates[i]
		if c.InstrumentID == instID && c.CalDate == calText {
			return nil, false, validationError(
				"器具 %s 在 %s 已有证书 %s，同一天不能再登记不同编号的证书",
				instID, calText, c.Number)
		}
	}

	cert := Certificate{
		Number:       number,
		InstrumentID: instID,
		CalDate:      calText,
		Expiry:       expText,
		Method:       method,
		Error:        in.Error,
		Summary:      summary,
		CreatedAt:    l.now().Format(time.RFC3339),
	}
	// 在独立的新切片上暂存新证书：只有原子替换台账文件成功后才提交。
	// 写盘失败时恢复原切片，保证同一台账对象随后看到的历史、最近证书、
	// 证书编号占用与同日位置都与录入前一致，且不会被之后的写盘顺带写入。
	original := l.data.Certificates
	staged := make([]Certificate, 0, len(original)+1)
	staged = append(staged, original...)
	staged = append(staged, cert)
	l.data.Certificates = staged
	if err := l.save(); err != nil {
		l.data.Certificates = original
		return nil, false, err
	}
	// 返回已保存证书的独立副本：返回结果只表示本次保存的内容，调用方的
	// 后续整理不会连带改写台账内的正式证书。
	saved := *l.findCertificate(number)
	return &saved, false, nil
}

// certificatesOf 返回某器具的证书，按校准日期从新到旧排序；日期相同时
// （实际业务不会允许同日多证）以证书编号排序保证结果稳定。
func (l *Ledger) certificatesOf(id string) []Certificate {
	var out []Certificate
	for _, c := range l.data.Certificates {
		if c.InstrumentID == id {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CalDate != out[j].CalDate {
			return out[i].CalDate > out[j].CalDate
		}
		return out[i].Number < out[j].Number
	})
	return out
}

// LatestCertificate 返回器具按校准日期确定的最近证书；没有证书时返回 nil。
// 最近证书与录入先后无关，补录较早证书只增加历史。
func (l *Ledger) LatestCertificate(id string) *Certificate {
	cs := l.certificatesOf(id)
	if len(cs) == 0 {
		return nil
	}
	return &cs[0]
}

// CertificateView 是证书及其在当前日期下的判定结果。
type CertificateView struct {
	Certificate
	Pass    bool `json:"pass"`
	Expired bool `json:"expired"`
}

func viewCertificate(c Certificate, allowed float64, today time.Time) CertificateView {
	return CertificateView{
		Certificate: c,
		Pass:        c.Pass(allowed),
		// 有效期截止日当天起即到期：按日历日期比较，今天 >= 截止日即为到期。
		Expired: today.Format(DateLayout) >= c.Expiry,
	}
}

// UsageDecision 是一次使用申请的评估结果，原因在申请时冻结留痕。
type UsageDecision struct {
	InstrumentID string
	Allowed      bool
	Reasons      []string
	Latest       *CertificateView
	evaluatedAt  time.Time
}

// evaluateAt 按器具状态与“最近证书”判定 now 所在本机日历日期能否使用，
// 并列出全部适用原因。最近证书超差或到期时不会回退到更早的合格证书。
// 同一次核对的各部分必须共用同一个 now，避免跨午夜时判断不一致。
func (l *Ledger) evaluateAt(id string, now time.Time) (*Instrument, *UsageDecision) {
	today := now
	inst := l.findInstrument(id)
	if inst == nil {
		return nil, nil
	}
	d := &UsageDecision{InstrumentID: id, evaluatedAt: today}
	if inst.Status != StatusInUse {
		d.Reasons = append(d.Reasons, fmt.Sprintf("器具当前状态为%s，只有在用器具可以申请使用", inst.Status))
	}
	latest := l.LatestCertificate(id)
	if latest == nil {
		d.Reasons = append(d.Reasons, "没有校准证书")
	} else {
		v := viewCertificate(*latest, inst.AllowedError, today)
		d.Latest = &v
		if !v.Pass {
			d.Reasons = append(d.Reasons, fmt.Sprintf(
				"最近证书 %s 测得误差绝对值 %g 超过允许误差 %g，判定超差",
				latest.Number, math.Abs(latest.Error), inst.AllowedError))
		}
		if v.Expired {
			d.Reasons = append(d.Reasons, fmt.Sprintf("最近证书 %s 已于 %s 到期（截止日当天即到期）",
				latest.Number, latest.Expiry))
		}
	}
	d.Allowed = len(d.Reasons) == 0
	return inst, d
}

// CanUse 只按当前数据评估能否使用，不留存申请记录。
func (l *Ledger) CanUse(id string) (*UsageDecision, error) {
	inst, d := l.evaluateAt(id, l.now())
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	return d, nil
}

// cloneReasons 返回原因切片的独立副本，使对外返回的数据与台账内部存储
// 不共享底层数组：调用方替换、删减或补充返回结果中的原因，不会改写
// 已留痕的历史原因。nil 保持 nil，以维持空结果的既有表现。
func cloneReasons(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// RequestUse 针对已有器具提交使用申请：无论允许或拒绝都保存申请时间、结果
// 与当时全部原因。未知编号报错且不新增记录。
func (l *Ledger) RequestUse(id string) (*UsageDecision, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("器具编号无效：%w", validationError("不能留空或只有空白"))
	}
	inst, d := l.evaluateAt(id, l.now())
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	// 留痕使用独立副本：返回给调用方的 d.Reasons 与其后的整理操作
	// 不能成为改写台账内历史原因的途径。
	rec := UsageRecord{
		InstrumentID: id,
		RequestedAt:  d.evaluatedAt.Format(time.RFC3339),
		Allowed:      d.Allowed,
		Reasons:      cloneReasons(d.Reasons),
	}
	if rec.Reasons == nil {
		rec.Reasons = []string{}
	}
	l.data.Usage = append(l.data.Usage, rec)
	if err := l.save(); err != nil {
		return nil, err
	}
	return d, nil
}

// InstrumentReview 是按器具核对的完整视图。
type InstrumentReview struct {
	Instrument Instrument        `json:"instrument"`
	CanUse     bool              `json:"can_use"`
	Reasons    []string          `json:"reasons"`
	Latest     *CertificateView  `json:"latest"`
	History    []CertificateView `json:"history"`
	Rejections []UsageRecord     `json:"rejections"`
	Plans      []PlanView        `json:"plans"`
}

// Review 按器具核对：登记信息、当前能否使用及原因、最近证书与到期日、
// 全部历史证书、被拒绝的使用记录以及该器具当前与已结束的校准计划。
// 整份核对只采用核对开始时的一次本机日期：即使查询结束前跨过午夜，
// 能否使用、最近证书、历史证书到期判断与未结束计划标记也不会各自变成新一天。
// 历史拒绝原因按申请当时的冻结内容展示，不随本次采用的日期重新计算。
// 返回的证书、拒绝记录与计划（含改期历史）都是独立展示副本，调用方的
// 整理不回写台账，也不影响此前或之后分别取得的其他结果。
func (l *Ledger) Review(id string) (*InstrumentReview, error) {
	// 本次核对只在开始时取一次本机时间，整份结果共用，跨午夜不分裂判断；
	// 下一次核对会重新取当时的本机日期，不沿用本次结果。
	now := l.now()
	inst, d := l.evaluateAt(id, now)
	if inst == nil {
		return nil, fmt.Errorf("器具编号 %s：%w", id, ErrNotFound)
	}
	r := &InstrumentReview{
		Instrument: *inst,
		CanUse:     d.Allowed,
		Reasons:    d.Reasons,
		Latest:     d.Latest,
	}
	if r.Reasons == nil {
		r.Reasons = []string{}
	}
	today := now
	for _, c := range l.certificatesOf(id) {
		r.History = append(r.History, viewCertificate(c, inst.AllowedError, today))
	}
	for _, u := range l.data.Usage {
		if u.InstrumentID == id && !u.Allowed {
			// 逐条深拷贝原因：对外核对视图中的历史拒绝记录只是展示副本，
			// 调用方的整理不能回写台账，也不能影响其他入口已取得的结果。
			u.Reasons = cloneReasons(u.Reasons)
			r.Rejections = append(r.Rejections, u)
		}
	}
	r.Plans = l.planViews(id, today)
	return r, nil
}

// Instruments 返回全部登记器具的副本，按编号排序。
func (l *Ledger) Instruments() []Instrument {
	out := append([]Instrument(nil), l.data.Instruments...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// UsageRecords 返回全部使用申请记录，按申请时间排序。
// 返回的是展示副本：整理其中原因不会改动台账内已冻结的留痕，
// 也不会被后续正常写盘带入文件。
func (l *Ledger) UsageRecords() []UsageRecord {
	out := append([]UsageRecord(nil), l.data.Usage...)
	for i := range out {
		out[i].Reasons = cloneReasons(out[i].Reasons)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RequestedAt != out[j].RequestedAt {
			return out[i].RequestedAt < out[j].RequestedAt
		}
		return out[i].InstrumentID < out[j].InstrumentID
	})
	return out
}
