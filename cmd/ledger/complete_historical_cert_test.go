package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件为命令行“用历史证书完成校准计划”补充端到端回归保障，与
// calibrate 包内基于假时钟的用例呼应：命令行使用真实本机时间，无法把
// 计划建立在过去，因此这里直接预置一份“计划建立于较早本机日期”的台账
// 文件（与旧台账兼容的既有做法一致），随后的证书录入、完成、待办、核对
// 与使用申请全部通过命令行驱动，验证普通输出与 --json 输出体现同一业务
// 结果。
//
// 重点保住两个含义互不冒充：
//   - 计划关联证书：属于该器具、校准日期不早于计划最初建立的本机日期、
//     未用于完成其他计划即可；它不是最近证书、本身超差都不是拒绝理由；
//   - 器具最近证书：永远按校准日期取最新一张，能否使用只看状态、最近
//     证书结论与有效期。

// histSeed 是预置台账文件所需的最小结构，字段标签与正式台账一致。
type histSeed struct {
	Version      int               `json:"version"`
	Instruments  []histSeedInst    `json:"instruments"`
	Certificates []json.RawMessage `json:"certificates"`
	Usage        []json.RawMessage `json:"usage"`
	Plans        []histSeedPlan    `json:"plans,omitempty"`
}

type histSeedInst struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	AllowedError float64 `json:"allowed_error"`
	Status       string  `json:"status"`
	RegisteredAt string  `json:"registered_at"`
}

type histSeedPlan struct {
	Number       string `json:"number"`
	InstrumentID string `json:"instrument_id"`
	PlannedDate  string `json:"planned_date"`
	OriginalDate string `json:"original_date"`
	Note         string `json:"note"`
	CreatedAt    string `json:"created_at"`
	Status       string `json:"status"`
}

// seedHistLedger 在临时目录写入一份预置台账：一件在用器具（允许误差 0.5）
// 与一项未完成计划，计划的最初建立本机日期为 createdDay（created_at 取
// 当天 UTC 正午，使任何本机时区下解析出的日历日期都稳定）。
func seedHistLedger(t *testing.T, path, id, planNumber string, createdDay time.Time) {
	t.Helper()
	createdAt := time.Date(createdDay.Year(), createdDay.Month(), createdDay.Day(),
		12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	planned := time.Now().AddDate(0, 0, 10).Format(calibrate.DateLayout)
	seed := histSeed{
		Version:      1,
		Instruments:  []histSeedInst{{ID: id, Name: "器具-" + id, AllowedError: 0.5, Status: "在用", RegisteredAt: createdAt}},
		Certificates: []json.RawMessage{},
		Usage:        []json.RawMessage{},
		Plans: []histSeedPlan{{
			Number:       planNumber,
			InstrumentID: id,
			PlannedDate:  planned,
			OriginalDate: planned,
			Note:         "周期校准",
			CreatedAt:    createdAt,
			Status:       calibrate.PlanStatusOpen,
		}},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("序列化预置台账: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入预置台账: %v", err)
	}
}

// histCertArgs 构造录入历史证书的命令参数，截止日默认取很远的未来，
// 使两张证书在核对当天都未到期。
func histCertArgs(path, instrument, number, calDate, errVal string) []string {
	expiry := time.Now().AddDate(10, 0, 0).Format(calibrate.DateLayout)
	return []string{"cert", "-f", path,
		"--instrument", instrument, "--number", number,
		"--cal-date", calDate, "--expiry", expiry,
		"--method", "历史回归方法", "--error", errVal, "--summary", "历史回归摘要"}
}

// histReview 解析 review --json 中本测试关心的字段。
type histReview struct {
	CanUse  bool     `json:"can_use"`
	Reasons []string `json:"reasons"`
	Latest  *struct {
		Number  string  `json:"number"`
		CalDate string  `json:"cal_date"`
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"latest"`
	History []struct {
		Number  string  `json:"number"`
		CalDate string  `json:"cal_date"`
		Expiry  string  `json:"expiry"`
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"history"`
	Rejections []struct {
		Allowed bool     `json:"allowed"`
		Reasons []string `json:"reasons"`
	} `json:"rejections"`
	Plans []struct {
		Number            string `json:"number"`
		Status            string `json:"status"`
		CertificateNumber string `json:"certificate_number"`
		CompletedAt       string `json:"completed_at"`
	} `json:"plans"`
}

func mustHistReview(t *testing.T, path, id string) histReview {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var v histReview
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, r.stdout)
	}
	return v
}

func todoPlanNumbers(t *testing.T, path string) []string {
	t.Helper()
	r := runArgs(t, "todos", "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("todos 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	var v struct {
		Todos []struct {
			PlanNumber string `json:"plan_number"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("todos JSON 解析失败: %v\n%s", err, r.stdout)
	}
	out := make([]string, 0, len(v.Todos))
	for _, it := range v.Todos {
		out = append(out, it.PlanNumber)
	}
	return out
}

// TestCLICompleteWithOlderPassingCertButNewerFailing 主场景的命令行版本：
// 先登记校准日期较新的超差证书，再补录较早的合格证书，用较早（非最近）
// 证书完成计划；计划显示已完成并移出待办，但最近证书仍是较新的超差证书，
// 器具不能使用，使用申请被拒绝并按较新证书超差留痕。
func TestCLICompleteWithOlderPassingCertButNewerFailing(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账-主场景.json")
	now := time.Now()
	created := now.AddDate(0, 0, -250)
	olderCal := now.AddDate(0, 0, -200).Format(calibrate.DateLayout)
	newerCal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	seedHistLedger(t, path, "M-1", "PL-1", created)

	// 较新超差、较早合格，两张证书校准日期不同、截止日均晚于核对当天。
	if r := runArgs(t, histCertArgs(path, "M-1", "C-NEW", newerCal, "0.8")...); r.code != 0 {
		t.Fatalf("录入较新证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, histCertArgs(path, "M-1", "C-OLD", olderCal, "-0.1")...); r.code != 0 {
		t.Fatalf("补录较早证书失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 仅录入两张证书：计划仍未完成并留在待办。
	if nums := todoPlanNumbers(t, path); len(nums) != 1 || nums[0] != "PL-1" {
		t.Fatalf("仅录入证书不应自动完成计划，待办=%v", nums)
	}

	// 普通输出：用较早（非最近）证书完成成功，退出码 0；成功输出不得把
	// 器具描述成已经可以使用。
	ok := runArgs(t, completeArgs(path, "PL-1", "C-OLD")...)
	if ok.code != 0 {
		t.Fatalf("较早历史证书应能完成计划，code=%d stderr=%s", ok.code, ok.stderr)
	}
	if !strings.Contains(ok.stdout, "已完成：计划 PL-1，证书 C-OLD，完成时间") {
		t.Fatalf("普通输出应显示已完成并保留证书编号与完成时间：%q", ok.stdout)
	}
	for _, banned := range []string{"可以使用", "允许使用", "当前能否使用：可以"} {
		if strings.Contains(ok.stdout, banned) {
			t.Fatalf("完成计划的成功输出不能声称器具 %s：%q", banned, ok.stdout)
		}
	}

	// --json 模式重复完成同一计划同一证书：同一业务结果（已完成），
	// 幂等返回原结果、退出码 0。
	dup := runArgs(t, append(append([]string{}, completeArgs(path, "PL-1", "C-OLD")...), "--json")...)
	if dup.code != 0 {
		t.Fatalf("--json 重复完成应退出 0，得到 %d（stderr=%s）", dup.code, dup.stderr)
	}
	var dupJSON struct {
		Accepted   bool `json:"accepted"`
		Idempotent bool `json:"idempotent"`
		Plan       struct {
			Status            string `json:"status"`
			CertificateNumber string `json:"certificate_number"`
			CompletedAt       string `json:"completed_at"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(dup.stdout), &dupJSON); err != nil {
		t.Fatalf("complete JSON 解析失败: %v\n%s", err, dup.stdout)
	}
	if !dupJSON.Accepted || !dupJSON.Idempotent ||
		dupJSON.Plan.Status != "已完成" || dupJSON.Plan.CertificateNumber != "C-OLD" ||
		dupJSON.Plan.CompletedAt == "" {
		t.Fatalf("--json 完成结果异常：%+v", dupJSON)
	}
	if nums := todoPlanNumbers(t, path); len(nums) != 0 {
		t.Fatalf("完成后计划应移出待办：%v", nums)
	}

	// 已完成计划改用另一张（较新超差）证书：业务拒绝退出 1，关联不变。
	sw := runArgs(t, completeArgs(path, "PL-1", "C-NEW")...)
	if sw.code != 1 || !strings.Contains(sw.stderr, "已拒绝") {
		t.Fatalf("已完成计划换证书应退出 1，code=%d stderr=%q", sw.code, sw.stderr)
	}

	// 按器具核对：计划保留 C-OLD 与完成时间；最近证书是较新超差 C-NEW，
	// 器具不能使用，原因指向 C-NEW；历史按校准日期由近到远，两张证书的
	// 测得误差与有效期保持录入值且都未到期。
	rv := mustHistReview(t, path, "M-1")
	if len(rv.Plans) != 1 || rv.Plans[0].Status != "已完成" ||
		rv.Plans[0].CertificateNumber != "C-OLD" || rv.Plans[0].CompletedAt == "" {
		t.Fatalf("核对中的计划完成信息异常：%+v", rv.Plans)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" || rv.Latest.Pass || rv.Latest.Expired {
		t.Fatalf("最近证书必须是较新的超差未到期 C-NEW：%+v", rv.Latest)
	}
	if rv.CanUse {
		t.Fatalf("计划关联合格历史证书不应使器具可用：%v", rv.Reasons)
	}
	if !containsStd(rv.Reasons, "超差") || !containsStd(rv.Reasons, "C-NEW") ||
		containsStd(rv.Reasons, "C-OLD") {
		t.Fatalf("不可用原因应针对 C-NEW 超差且不牵涉 C-OLD：%v", rv.Reasons)
	}
	if len(rv.History) != 2 || rv.History[0].Number != "C-NEW" ||
		rv.History[1].Number != "C-OLD" {
		t.Fatalf("历史应按校准日期由近到远：C-NEW、C-OLD，得到 %+v", rv.History)
	}
	errByNumber := map[string]float64{}
	for _, h := range rv.History {
		errByNumber[h.Number] = h.Error
		if h.Expired {
			t.Fatalf("证书 %s 截止日晚于核对当天，不应到期", h.Number)
		}
	}
	if errByNumber["C-NEW"] != 0.8 || errByNumber["C-OLD"] != -0.1 {
		t.Fatalf("完成动作不应改写两张证书的测得误差：%v", errByNumber)
	}

	// 随后申请使用：普通输出拒绝（退出 1），原因是较新证书超差；--json
	// 同样业务拒绝、退出 1 且 recorded=true。
	hu := runArgs(t, "use", "--id", "M-1", "-f", path)
	if hu.code != 1 || !strings.Contains(hu.stderr, "拒绝使用器具 M-1") ||
		!strings.Contains(hu.stderr, "C-NEW") || !strings.Contains(hu.stderr, "超差") ||
		strings.Contains(hu.stderr, "C-OLD") {
		t.Fatalf("普通输出应按 C-NEW 超差拒绝使用：code=%d stderr=%q",
			hu.code, hu.stderr)
	}
	ju := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if ju.code != 1 || !strings.Contains(ju.stdout, `"allowed": false`) ||
		!strings.Contains(ju.stdout, `"recorded": true`) ||
		!strings.Contains(ju.stdout, "C-NEW") || !strings.Contains(ju.stdout, "超差") {
		t.Fatalf("--json 应同样拒绝并留痕，code=%d stdout=%q", ju.code, ju.stdout)
	}
	final := mustHistReview(t, path, "M-1")
	if len(final.Rejections) != 2 {
		t.Fatalf("两次拒绝使用都应留痕，得到 %d 条", len(final.Rejections))
	}
	for i, rej := range final.Rejections {
		if rej.Allowed || !containsStd(rej.Reasons, "C-NEW") ||
			!containsStd(rej.Reasons, "超差") {
			t.Fatalf("第 %d 条拒绝留痕应冻结 C-NEW 超差原因：%+v", i+1, rej)
		}
	}
}

// TestCLICompleteWithOlderFailingCertButNewerPassing 反方向：较早历史证书
// 超差、较新证书合格未到期。较早的超差证书同样可以完成计划；核对与使用
// 申请仍以较新证书为准，申请获准（退出码 0）并留痕。
func TestCLICompleteWithOlderFailingCertButNewerPassing(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账-反向.json")
	now := time.Now()
	created := now.AddDate(0, 0, -250)
	olderCal := now.AddDate(0, 0, -200).Format(calibrate.DateLayout)
	newerCal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	seedHistLedger(t, path, "M-2", "PL-2", created)

	if r := runArgs(t, histCertArgs(path, "M-2", "C-NEW", newerCal, "0.1")...); r.code != 0 {
		t.Fatalf("录入较新合格证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, histCertArgs(path, "M-2", "C-OLD", olderCal, "5")...); r.code != 0 {
		t.Fatalf("补录较早超差证书失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 较早的超差证书同样可以完成符合条件的计划：普通输出成功退出 0。
	ok := runArgs(t, completeArgs(path, "PL-2", "C-OLD")...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成：计划 PL-2，证书 C-OLD") {
		t.Fatalf("较早超差历史证书也应能完成计划：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}

	rv := mustHistReview(t, path, "M-2")
	if len(rv.Plans) != 1 || rv.Plans[0].Status != "已完成" ||
		rv.Plans[0].CertificateNumber != "C-OLD" || rv.Plans[0].CompletedAt == "" {
		t.Fatalf("计划应保留较早超差证书编号与完成时间：%+v", rv.Plans)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" || !rv.Latest.Pass || rv.Latest.Expired {
		t.Fatalf("最近证书应是合格未到期的较新 C-NEW：%+v", rv.Latest)
	}
	if !rv.CanUse || len(rv.Reasons) != 0 {
		t.Fatalf("较新证书合格未到期时器具应可以使用：canUse=%v reasons=%v",
			rv.CanUse, rv.Reasons)
	}
	if len(rv.History) != 2 || rv.History[0].Number != "C-NEW" ||
		rv.History[1].Number != "C-OLD" {
		t.Fatalf("历史仍应按校准日期由近到远：%+v", rv.History)
	}
	verdicts := map[string]bool{}
	errs := map[string]float64{}
	for _, h := range rv.History {
		verdicts[h.Number] = h.Pass
		errs[h.Number] = h.Error
		if h.Expired {
			t.Fatalf("证书 %s 不应到期", h.Number)
		}
	}
	if verdicts["C-NEW"] != true || verdicts["C-OLD"] != false {
		t.Fatalf("两张证书应各自保留结论：%v", verdicts)
	}
	if errs["C-NEW"] != 0.1 || errs["C-OLD"] != 5 {
		t.Fatalf("完成动作不应改写测得误差：%v", errs)
	}

	// 使用申请以较新证书为准：普通输出获准退出 0；--json allowed=true、
	// recorded=true。
	hu := runArgs(t, "use", "--id", "M-2", "-f", path)
	if hu.code != 0 || !strings.Contains(hu.stdout, "允许使用器具 M-2") {
		t.Fatalf("普通输出应允许使用：code=%d stdout=%q stderr=%s",
			hu.code, hu.stdout, hu.stderr)
	}
	ju := runArgs(t, "use", "--id", "M-2", "-f", path, "--json")
	if ju.code != 0 || !strings.Contains(ju.stdout, `"allowed": true`) ||
		!strings.Contains(ju.stdout, `"recorded": true`) {
		t.Fatalf("--json 应获准并留痕：code=%d stdout=%q stderr=%s",
			ju.code, ju.stdout, ju.stderr)
	}
	after := mustHistReview(t, path, "M-2")
	if len(after.Rejections) != 0 {
		t.Fatalf("获准使用不应产生拒绝记录，得到 %d 条", len(after.Rejections))
	}
}

// TestCLICompleteHistoricalCertDateBoundary 命令行日期边界：校准日期等于
// 计划最初建立的本机日期可完成（退出 0）；早一天则普通输出与 --json 都
// 明确拒绝（退出 1，错误指向建立日期），计划继续未完成、留在待办，不
// 写入完成时间或关联证书。
func TestCLICompleteHistoricalCertDateBoundary(t *testing.T) {
	dir := chdirTemp(t)
	now := time.Now()
	created := now.AddDate(0, 0, -150)
	createdDay := created.Format(calibrate.DateLayout)
	dayBefore := created.AddDate(0, 0, -1).Format(calibrate.DateLayout)
	expiry := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	// 等于建立日期：完成成功，退出码 0。
	eqPath := filepath.Join(dir, "台账-边界相等.json")
	seedHistLedger(t, eqPath, "M-EQ", "PL-EQ", created)
	eqCert := []string{"cert", "-f", eqPath,
		"--instrument", "M-EQ", "--number", "C-EQ",
		"--cal-date", createdDay, "--expiry", expiry,
		"--method", "m", "--error", "0.1", "--summary", "建立当天校准"}
	if r := runArgs(t, eqCert...); r.code != 0 {
		t.Fatalf("录入建立当天证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	eq := runArgs(t, completeArgs(eqPath, "PL-EQ", "C-EQ")...)
	if eq.code != 0 || !strings.Contains(eq.stdout, "已完成：计划 PL-EQ，证书 C-EQ") {
		t.Fatalf("校准日期等于建立日期应可完成：code=%d stdout=%q stderr=%s",
			eq.code, eq.stdout, eq.stderr)
	}
	eqj := runArgs(t, append(append([]string{}, completeArgs(eqPath, "PL-EQ", "C-EQ")...), "--json")...)
	if eqj.code != 0 || !strings.Contains(eqj.stdout, `"accepted": true`) {
		t.Fatalf("--json 也应体现完成成功：code=%d stdout=%q", eqj.code, eqj.stdout)
	}

	// 早一天：普通输出明确拒绝、退出 1，错误信息指出建立日期。
	beforePath := filepath.Join(dir, "台账-边界早一天.json")
	seedHistLedger(t, beforePath, "M-BE", "PL-BE", created)
	beforeCert := []string{"cert", "-f", beforePath,
		"--instrument", "M-BE", "--number", "C-BE",
		"--cal-date", dayBefore, "--expiry", expiry,
		"--method", "m", "--error", "0.1", "--summary", "早一天校准"}
	if r := runArgs(t, beforeCert...); r.code != 0 {
		t.Fatalf("录入早一天证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	hb := runArgs(t, completeArgs(beforePath, "PL-BE", "C-BE")...)
	if hb.code != 1 || !strings.Contains(hb.stderr, "已拒绝") ||
		!strings.Contains(hb.stderr, "建立日期") {
		t.Fatalf("早一天应业务拒绝退出 1 并指出建立日期：code=%d stderr=%q",
			hb.code, hb.stderr)
	}
	// --json 体现同一业务结果：accepted=false、退出 1，错误同样指出建立日期。
	jb := runArgs(t, append(append([]string{}, completeArgs(beforePath, "PL-BE", "C-BE")...), "--json")...)
	if jb.code != 1 || !strings.Contains(jb.stdout, `"accepted": false`) ||
		!strings.Contains(jb.stdout, "建立日期") {
		t.Fatalf("--json 应同样拒绝退出 1：code=%d stdout=%q stderr=%s",
			jb.code, jb.stdout, jb.stderr)
	}

	// 计划继续未完成并留在待办，没有完成时间或关联证书。
	if nums := todoPlanNumbers(t, beforePath); len(nums) != 1 || nums[0] != "PL-BE" {
		t.Fatalf("被拒绝的计划应继续留在待办：%v", nums)
	}
	rv := mustHistReview(t, beforePath, "M-BE")
	if len(rv.Plans) != 1 || rv.Plans[0].Status != "未完成" ||
		rv.Plans[0].CompletedAt != "" || rv.Plans[0].CertificateNumber != "" {
		t.Fatalf("拒绝后不得写入完成时间或关联证书：%+v", rv.Plans)
	}

	// 早一天拒绝后，改用不早于建立日期的证书完成同一计划应成功，且关联
	// 的是新证书；普通输出与 --json 都退出 0。
	okDay := now.AddDate(0, 0, -100).Format(calibrate.DateLayout)
	okCert := []string{"cert", "-f", beforePath,
		"--instrument", "M-BE", "--number", "C-OK",
		"--cal-date", okDay, "--expiry", expiry,
		"--method", "m", "--error", "0.1", "--summary", "符合条件"}
	if r := runArgs(t, okCert...); r.code != 0 {
		t.Fatalf("录入符合条件证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, completeArgs(beforePath, "PL-BE", "C-OK")...); r.code != 0 {
		t.Fatalf("符合条件证书应能完成曾被拒绝的计划：code=%d stderr=%s",
			r.code, r.stderr)
	}
	after := mustHistReview(t, beforePath, "M-BE")
	if len(after.Plans) != 1 || after.Plans[0].Status != "已完成" ||
		after.Plans[0].CertificateNumber != "C-OK" || after.Plans[0].CompletedAt == "" {
		t.Fatalf("计划应保留新证书编号与本次完成时间：%+v", after.Plans)
	}
}
