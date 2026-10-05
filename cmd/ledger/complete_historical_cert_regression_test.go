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

// 本文件从命令行端到端守住“用历史证书完成校准计划”：
//
//	计划关联证书（哪一张证书结束了当时建立的计划）与器具最近证书（按校准日期
//	决定能否使用）是两件事。较早、非最近的证书同样可以完成计划，但核对与使用
//	申请始终只认最近证书。
//
// 命令行使用真实本机时钟，无法把计划建立时刻倒拨到过去，因此这里预置一张
// 含“较早建立的未完成计划”的台账文件，再通过真实命令录入两张不同校准日期的
// 证书并完成计划；所有日期相对运行当天动态计算，保证两张证书截止日都晚于
// 核对当天、校准日期不晚于当天且不早于计划建立日。

// seedOldPlanLedger 在 path 写入一件在用器具和一项建立于 createdAgoDays 天前、
// 计划日期在未来的未完成计划。证书留空，由测试随后用真实命令录入。
func seedOldPlanLedger(t *testing.T, path string, createdAgoDays int) {
	t.Helper()
	now := time.Now()
	createdAt := now.AddDate(0, 0, -createdAgoDays).Format(time.RFC3339)
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status":        string(calibrate.StatusInUse),
			"registered_at": createdAt,
		}},
		"certificates": []any{},
		"usage":        []any{},
		"plans": []map[string]any{{
			"number": "PL-1", "instrument_id": "M-1",
			"planned_date": "2030-10-20", "original_date": "2030-10-20",
			"note": "年度例行校准", "created_at": createdAt, "status": calibrate.PlanStatusOpen,
		}},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化预置台账: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入预置台账 %s: %v", path, err)
	}
}

// daysAgo / daysAfter 按本机日历返回相对运行当天的 YYYY-MM-DD。
func daysAgo(t *testing.T, n int) string {
	t.Helper()
	return time.Now().AddDate(0, 0, -n).Format(calibrate.DateLayout)
}

func daysAfter(t *testing.T, n int) string {
	t.Helper()
	return time.Now().AddDate(0, 0, n).Format(calibrate.DateLayout)
}

func historicalCertArgs(t *testing.T, path, number, calDate, errVal string) []string {
	t.Helper()
	return certFullArgs(path, "M-1", number, calDate, daysAfter(t, 10*365), errVal)
}

// completeJSONView 是 complete / review --json 中本测试关心的字段。
type completeJSONView struct {
	Accepted   bool `json:"accepted"`
	Idempotent bool `json:"idempotent"`
	Plan       *struct {
		Number            string `json:"number"`
		Status            string `json:"status"`
		CertificateNumber string `json:"certificate_number"`
		CompletedAt       string `json:"completed_at"`
	} `json:"plan"`
}

// assertCompleteSuccess 核对完成成功在普通输出与 --json 下是同一业务结果：
// 退出码 0、计划已完成并保留指定证书编号与非空完成时间，且成功输出不得把
// 器具描述为已经可以使用。
func assertCompleteSuccess(t *testing.T, path, plan, cert string) {
	t.Helper()
	human := runArgs(t, "complete", "-f", path, "--number", plan, "--certificate", cert)
	if human.code != 0 {
		t.Fatalf("普通输出完成应退出 0，得到 %d，stderr=%s", human.code, human.stderr)
	}
	if !strings.Contains(human.stdout, "已完成：计划 "+plan) ||
		!strings.Contains(human.stdout, "证书 "+cert) ||
		!strings.Contains(human.stdout, "完成时间 ") {
		t.Fatalf("普通输出应保留计划、证书编号与完成时间：%q", human.stdout)
	}
	if strings.Contains(human.stdout, "可以使用") ||
		strings.Contains(human.stdout, "允许使用") ||
		strings.Contains(human.stderr, "可以使用") ||
		strings.Contains(human.stderr, "允许使用") {
		t.Fatalf("完成计划的成功输出不得把器具描述为已经可以使用：stdout=%q stderr=%q",
			human.stdout, human.stderr)
	}

	js := runArgs(t, "complete", "-f", path, "--json", "--number", plan, "--certificate", cert)
	if js.code != 0 {
		t.Fatalf("--json 重复完成（幂等）应退出 0，得到 %d，stderr=%s", js.code, js.stderr)
	}
	var v completeJSONView
	if err := json.Unmarshal([]byte(js.stdout), &v); err != nil {
		t.Fatalf("complete JSON 解析失败: %v\n%s", err, js.stdout)
	}
	if !v.Accepted || !v.Idempotent || v.Plan == nil {
		t.Fatalf("--json 幂等完成字段异常：%+v", v)
	}
	if v.Plan.Status != calibrate.PlanStatusDone ||
		v.Plan.CertificateNumber != cert || v.Plan.CompletedAt == "" {
		t.Fatalf("--json 计划应显示已完成并保留 %s 与完成时间：%+v", cert, v.Plan)
	}
	if strings.Contains(js.stdout, "可以使用") || strings.Contains(js.stdout, "允许使用") {
		t.Fatalf("--json 成功输出不得暗示器具已经可以使用：%s", js.stdout)
	}
}

// assertPlanOpenInCLI 通过待办与按器具核对确认计划仍未完成、无完成时间与关联证书。
func assertPlanOpenInCLI(t *testing.T, path string) {
	t.Helper()
	tv := runArgs(t, "todos", "-f", path, "--json")
	if tv.code != 0 || !strings.Contains(tv.stdout, `"count": 1`) ||
		!strings.Contains(tv.stdout, "PL-1") {
		t.Fatalf("计划应仍以未完成身份留在待办：code=%d stdout=%q", tv.code, tv.stdout)
	}
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	var v struct {
		Plans []struct {
			Status            string `json:"status"`
			CompletedAt       string `json:"completed_at"`
			CertificateNumber string `json:"certificate_number"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, rv.stdout)
	}
	if len(v.Plans) != 1 || v.Plans[0].Status != calibrate.PlanStatusOpen ||
		v.Plans[0].CompletedAt != "" || v.Plans[0].CertificateNumber != "" {
		t.Fatalf("计划应继续未完成且无完成时间、关联证书：%+v", v.Plans)
	}
}

// TestCLIHistoricalPassingCertCompletesButUseFollowsNewerFailingCert 命令行主场景：
// 较早证书合格、较新证书超差，两证校准日期不同、截止日都晚于核对当天，器具在用。
// 先登记较新证书再补录较早证书，用较早证书完成计划后，最近证书仍是较新的超差
// 证书，使用申请被拒绝并按较新证书超差留痕。
func TestCLIHistoricalPassingCertCompletesButUseFollowsNewerFailingCert(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	// 计划建立于 60 天前：30 天前与 10 天前的证书都不早于建立日期。
	seedOldPlanLedger(t, path, 60)

	// 先登记校准日期较新（10 天前）的超差证书，再补录较早（30 天前）的合格证书。
	newer := historicalCertArgs(t, path, "C-NEW", daysAgo(t, 10), "0.8")
	if r := runArgs(t, newer...); r.code != 0 || !strings.Contains(r.stdout, "判定：超差") {
		t.Fatalf("较新超差证书应录入成功：code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	older := historicalCertArgs(t, path, "C-OLD", daysAgo(t, 30), "-0.1")
	if r := runArgs(t, older...); r.code != 0 || !strings.Contains(r.stdout, "判定：合格") {
		t.Fatalf("较早合格证书应录入成功：code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	// 仅录入两张证书：计划仍未完成，留在待办。
	assertPlanOpenInCLI(t, path)

	// 用较早、非最近的合格证书完成计划：普通与 JSON 同为成功（退出 0）。
	assertCompleteSuccess(t, path, "PL-1", "C-OLD")

	// 完成后计划移出待办。
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("完成后计划应从待办移除：%s", tv.stdout)
	}

	// 按器具核对：计划关联 C-OLD 且已完成；最近证书仍是较新的超差 C-NEW，
	// 器具不可用，原因针对 C-NEW；历史按校准日期由近到远；器具状态、允许
	// 误差与两张证书的误差、有效期都不被完成动作改动。
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if rv.code != 0 {
		t.Fatalf("review 失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	var view struct {
		CanUse  bool     `json:"can_use"`
		Reasons []string `json:"reasons"`
		Latest  *struct {
			Number  string  `json:"number"`
			Pass    bool    `json:"pass"`
			Expired bool    `json:"expired"`
			Error   float64 `json:"error"`
			Expiry  string  `json:"expiry"`
		} `json:"latest"`
		History []struct {
			Number string  `json:"number"`
			Pass   bool    `json:"pass"`
			Error  float64 `json:"error"`
		} `json:"history"`
		Instrument struct {
			Status       string  `json:"status"`
			AllowedError float64 `json:"allowed_error"`
		} `json:"instrument"`
		Plans []struct {
			Status            string `json:"status"`
			CertificateNumber string `json:"certificate_number"`
			CompletedAt       string `json:"completed_at"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, rv.stdout)
	}
	if view.CanUse || !containsStd(view.Reasons, "超差") || !containsStd(view.Reasons, "C-NEW") {
		t.Fatalf("核对应因较新证书 C-NEW 超差判不可用：canUse=%v reasons=%v", view.CanUse, view.Reasons)
	}
	if containsStd(view.Reasons, "C-OLD") {
		t.Fatalf("使用原因不应涉及计划关联的合格证书 C-OLD：%v", view.Reasons)
	}
	if view.Latest == nil || view.Latest.Number != "C-NEW" || view.Latest.Pass || view.Latest.Expired {
		t.Fatalf("最近证书应仍是较新、未到期的超差 C-NEW：%+v", view.Latest)
	}
	if len(view.History) != 2 ||
		view.History[0].Number != "C-NEW" || view.History[0].Pass ||
		view.History[1].Number != "C-OLD" || !view.History[1].Pass {
		t.Fatalf("历史应由近到远为超差 C-NEW、合格 C-OLD：%+v", view.History)
	}
	if view.History[0].Error != 0.8 || view.History[1].Error != -0.1 {
		t.Fatalf("完成动作不得改动两张证书的测得误差：%+v", view.History)
	}
	if view.Instrument.Status != "在用" || view.Instrument.AllowedError != 0.5 {
		t.Fatalf("完成动作不得更改器具状态或允许误差：%+v", view.Instrument)
	}
	if view.Latest.Expiry != daysAfter(t, 10*365) {
		t.Fatalf("较新证书有效期不应被改动，得到 %s", view.Latest.Expiry)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != calibrate.PlanStatusDone ||
		view.Plans[0].CertificateNumber != "C-OLD" || view.Plans[0].CompletedAt == "" {
		t.Fatalf("核对应显示计划已完成并关联 C-OLD：%+v", view.Plans)
	}

	// 普通文字核对也应体现同一业务结果。
	hv := runArgs(t, "review", "--id", "M-1", "-f", path)
	if !strings.Contains(hv.stdout, "最近证书：C-NEW") ||
		!strings.Contains(hv.stdout, "当前能否使用：不可以") ||
		!strings.Contains(hv.stdout, "证书 C-OLD") {
		t.Fatalf("普通核对应展示较新超差最近证书、不可用与计划关联证书：%s", hv.stdout)
	}

	// 随后申请使用：普通输出与 --json 都被拒绝（退出 1），原因针对 C-NEW 超差，
	// 不能因为计划关联了合格的 C-OLD 而获准。
	u := runArgs(t, "use", "--id", "M-1", "-f", path)
	if u.code != 1 || !strings.Contains(u.stderr, "超差") ||
		!strings.Contains(u.stderr, "C-NEW") || strings.Contains(u.stderr, "C-OLD") {
		t.Fatalf("使用申请应因 C-NEW 超差拒绝且不提 C-OLD：code=%d stderr=%q", u.code, u.stderr)
	}
	uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if uj.code != 1 || !strings.Contains(uj.stdout, `"allowed": false`) ||
		!strings.Contains(uj.stdout, "超差") || !strings.Contains(uj.stdout, "C-NEW") {
		t.Fatalf("--json 使用申请应同业务结果拒绝：code=%d stdout=%q", uj.code, uj.stdout)
	}
	// 拒绝留痕进入核对。
	after := mustReviewJSON(t, path, "M-1")
	if len(after.Rejections) != 2 ||
		!containsStd(after.Rejections[0].Reasons, "C-NEW") ||
		!containsStd(after.Rejections[1].Reasons, "C-NEW") {
		t.Fatalf("两次拒绝都应按较新证书超差留痕：%+v", after.Rejections)
	}
}

// TestCLIHistoricalFailingCertCompletesButUseFollowsNewerPassingCert 反向结论：
// 较早证书超差、较新证书合格且未到期，器具在用。较早的超差证书同样完成计划，
// 核对与使用申请仍以较新证书为准，申请获准并留痕。
func TestCLIHistoricalFailingCertCompletesButUseFollowsNewerPassingCert(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	seedOldPlanLedger(t, path, 60)

	if r := runArgs(t, historicalCertArgs(t, path, "C-NEW", daysAgo(t, 10), "0.1")...); r.code != 0 {
		t.Fatalf("较新合格证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, historicalCertArgs(t, path, "C-OLD", daysAgo(t, 30), "5")...); r.code != 0 {
		t.Fatalf("较早超差证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	assertPlanOpenInCLI(t, path)

	// 较早的超差证书同样可以完成计划；成功输出不得暗示器具可用。
	assertCompleteSuccess(t, path, "PL-1", "C-OLD")

	rv := mustReviewJSON(t, path, "M-1")
	if !rv.CanUse || len(rv.Reasons) != 0 {
		t.Fatalf("较新证书合格未到期时应可使用，reasons=%v", rv.Reasons)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" || !rv.Latest.Pass || rv.Latest.Expired {
		t.Fatalf("最近证书应是较新的合格未到期 C-NEW：%+v", rv.Latest)
	}
	if len(rv.History) != 2 ||
		rv.History[0].Number != "C-NEW" || !rv.History[0].Pass ||
		rv.History[1].Number != "C-OLD" || rv.History[1].Pass {
		t.Fatalf("历史应由近到远为合格 C-NEW、超差 C-OLD：%+v", rv.History)
	}
	if rv.Instrument.Status != "在用" {
		t.Fatalf("完成动作不应更改器具状态，得到 %s", rv.Instrument.Status)
	}

	// 使用申请以较新证书为准，获准（退出 0）并留痕。
	u := runArgs(t, "use", "--id", "M-1", "-f", path)
	if u.code != 0 || !strings.Contains(u.stdout, "允许使用器具 M-1") {
		t.Fatalf("较新证书合格时应获准使用：code=%d stdout=%q stderr=%s",
			u.code, u.stdout, u.stderr)
	}
	uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if uj.code != 0 || !strings.Contains(uj.stdout, `"allowed": true`) {
		t.Fatalf("--json 使用申请应获准：code=%d stdout=%q", uj.code, uj.stdout)
	}
	final := mustReviewJSON(t, path, "M-1")
	if len(final.Rejections) != 0 {
		t.Fatalf("获准使用不应留下拒绝记录，得到 %d 条", len(final.Rejections))
	}
}

// TestCLICompletePlanCertDateBoundary 命令行日期边界：计划建立于 30 天前。
// 校准日期早一天（31 天前）的证书在普通输出与 --json 下都按业务拒绝退出 1，
// 计划继续未完成、留在待办，不写入完成时间或关联证书；校准日期恰好等于建立
// 日期（30 天前）的证书可以完成，退出 0。
func TestCLICompletePlanCertDateBoundary(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	seedOldPlanLedger(t, path, 30)

	// 校准日期早于计划建立日期一天。
	if r := runArgs(t, historicalCertArgs(t, path, "C-TOO-EARLY", daysAgo(t, 31), "0.1")...); r.code != 0 {
		t.Fatalf("早一天的证书本身应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	human := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-TOO-EARLY")
	if human.code != 1 {
		t.Fatalf("校准日期早于建立日期应业务拒绝退出 1，得到 %d", human.code)
	}
	if !strings.Contains(human.stderr, "已拒绝") ||
		!strings.Contains(human.stderr, "早于") ||
		!strings.Contains(human.stderr, "建立日期") {
		t.Fatalf("普通输出应明确拒绝并说明日期边界：stderr=%q", human.stderr)
	}
	js := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-TOO-EARLY")
	if js.code != 1 || !strings.Contains(js.stdout, `"accepted": false`) {
		t.Fatalf("--json 同样应退出 1 且 accepted=false，code=%d stdout=%q", js.code, js.stdout)
	}
	assertPlanOpenInCLI(t, path)

	// 校准日期等于计划最初建立的本机日期：可以完成。
	if r := runArgs(t, historicalCertArgs(t, path, "C-ON-DAY", daysAgo(t, 30), "0.1")...); r.code != 0 {
		t.Fatalf("建立当天的证书应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	ok := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-ON-DAY")
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成：计划 PL-1，证书 C-ON-DAY") {
		t.Fatalf("校准日期等于建立日期应完成成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("边界当天完成后应移出待办：%s", tv.stdout)
	}
}
