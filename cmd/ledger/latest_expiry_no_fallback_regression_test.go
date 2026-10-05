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

// 本文件从命令行端到端守住：
//
//	“最近证书到期后，不能借用较早证书继续获准使用”。
//
// 命令行使用真实本机时钟，无法把时钟倒拨到截止日之前，因此这里直接预置一份
// 历史台账（与 cmd/ledger 既有的回归测试同样的做法）：器具保持在用，两张证书
// 都按允许误差判为合格——校准日期较新的 C-NEW 截止日就是运行当天（当天即到期），
// 校准较早的 C-OLD 有效期仍覆盖当天；再通过真实的 use/review/cert 子命令验证
// 普通文本与 --json 输出表达同一个拒绝结果。
//
// 所有日期相对运行当天动态计算，保证日历变化后仍准确覆盖“截止日前已获准”与
// “截止日当天拒绝”的区别，不依赖某个写死的日期恰好仍在未来。

// expiringScenario 持有相对运行当天派生的日期。
type expiringScenario struct {
	newCal  string // C-NEW 校准日期（40 天前）
	oldCal  string // C-OLD 校准日期（200 天前）
	newExp  string // C-NEW 截止日（运行当天：当天即到期）
	oldExp  string // C-OLD 截止日（400 天后，仍覆盖全部申请当天）
	allowed string // 截止日前两天的 RFC3339 申请时间（当时获准）
}

func expiringDates(t *testing.T) expiringScenario {
	t.Helper()
	now := time.Now()
	return expiringScenario{
		newCal:  now.AddDate(0, 0, -40).Format(calibrate.DateLayout),
		oldCal:  now.AddDate(0, 0, -200).Format(calibrate.DateLayout),
		newExp:  now.Format(calibrate.DateLayout),
		oldExp:  now.AddDate(0, 0, 400).Format(calibrate.DateLayout),
		allowed: now.AddDate(0, 0, -2).Format(time.RFC3339),
	}
}

// expiringCert 构造一张合格证书的台账记录（误差绝对值均不超过 0.5）。
func expiringCert(number, cal, expiry string, errV float64, createdAt time.Time) map[string]any {
	return map[string]any{
		"number": number, "instrument_id": "M-1",
		"cal_date": cal, "expiry": expiry,
		"method": "规范A", "error": errV, "summary": "回归校准",
		"created_at": createdAt.Format(time.RFC3339),
	}
}

// seedExpiringLatestLedger 在 path 写入一件在用器具；按 withOld 决定是否同时
// 预置校准较早、有效期更长的合格 C-OLD。总是预置一条截止日前两天获准的使用
// 记录（该记录必须在后来到期后仍保持获准）。
func seedExpiringLatestLedger(t *testing.T, path string, s expiringScenario, withOld bool) {
	t.Helper()
	now := time.Now()
	certs := []any{
		expiringCert("C-NEW", s.newCal, s.newExp, 0.1, now.AddDate(0, 0, -40)),
	}
	if withOld {
		// C-OLD 录入更晚（截止日当天前一刻的“现在”），但校准更早、有效期更长。
		certs = append(certs, expiringCert("C-OLD", s.oldCal, s.oldExp, -0.2, now.AddDate(0, 0, -1)))
	}
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status":        string(calibrate.StatusInUse),
			"registered_at": now.AddDate(0, 0, -300).Format(time.RFC3339),
		}},
		"certificates": certs,
		// 截止日前两天已获准并保存：当时 C-NEW 未到期，原因为空。
		"usage": []map[string]any{{
			"instrument_id": "M-1", "requested_at": s.allowed,
			"allowed": true, "reasons": []string{},
		}},
		"plans": []any{},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化预置台账: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入预置台账 %s: %v", path, err)
	}
}

// historyLine 返回普通核对输出“历史证书”区中包含编号的那一整行
// （以 “- ” 开头），避免误取“最近证书：…”行。
func historyLine(t *testing.T, out, number string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		// 历史行形如 “- C-NEW 2026-… 截止 … 合格 已到期（…）”，以编号开头；
		// 原因行是 “- 最近证书 C-NEW 已于 … 到期”，不能误取。
		if strings.HasPrefix(trimmed, "- "+number+" ") {
			return line
		}
	}
	t.Fatalf("核对历史输出中找不到证书 %s：\n%s", number, out)
	return ""
}

// assertExpiredLatestRejection 核对一次拒绝结果（普通或 JSON 共同的业务含义）：
// 退出 1，原因点名真正到期的最近证书 C-NEW 及其截止日，既不借用 C-OLD，
// 也不把到期误说成超差。
func assertExpiredLatestRejection(t *testing.T, r result, s expiringScenario, jsonMode bool) {
	t.Helper()
	if r.code != 1 {
		t.Fatalf("最近证书到期应业务拒绝、退出码 1，得到 %d（json=%v）", r.code, jsonMode)
	}
	blob := r.stderr
	if jsonMode {
		blob = r.stdout
		if !strings.Contains(r.stdout, `"allowed": false`) ||
			!strings.Contains(r.stdout, `"recorded": true`) {
			t.Fatalf("--json 拒绝应明确 allowed=false 且 recorded=true：%s", r.stdout)
		}
	} else {
		if !strings.Contains(r.stderr, "拒绝使用器具 M-1") {
			t.Fatalf("普通文本应明确拒绝使用，stderr=%q", r.stderr)
		}
	}
	for _, want := range []string{"C-NEW", s.newExp, "到期"} {
		if !strings.Contains(blob, want) {
			t.Fatalf("拒绝原因应指出真正到期的最近证书及截止日，缺少 %q：%s", want, blob)
		}
	}
	if strings.Contains(blob, "C-OLD") {
		t.Fatalf("不能因为历史里还有合格未到期的 C-OLD 而改用旧结论：%s", blob)
	}
	if strings.Contains(blob, "超差") {
		t.Fatalf("两张证书都合格，不能把到期误说成超差：%s", blob)
	}
}

// assertReviewShowsNewExpiredOldValid 核对 review（已解析的 JSON 视图）：
// 最近证书是合格但已到期的 C-NEW，历史按校准日期由近到远同时显示
// 新证书已到期、旧证书尚未到期，两张证书各自保留合格结论与原截止日。
func assertReviewShowsNewExpiredOldValid(t *testing.T, v cliReview, s expiringScenario) {
	t.Helper()
	if v.CanUse {
		t.Fatalf("最近证书到期时不应可使用，reasons=%v", v.Reasons)
	}
	if !containsStd(v.Reasons, "C-NEW") || !containsStd(v.Reasons, s.newExp) ||
		!containsStd(v.Reasons, "到期") || containsStd(v.Reasons, "C-OLD") {
		t.Fatalf("当前不可用原因应只点名 C-NEW 到期，得到 %v", v.Reasons)
	}
	if v.Latest == nil || v.Latest.Number != "C-NEW" {
		t.Fatalf("最近证书应是校准日期较新的 C-NEW，得到 %+v", v.Latest)
	}
	if !v.Latest.Pass || !v.Latest.Expired {
		t.Fatalf("最近证书 C-NEW 应判合格但已到期（到期不是超差）：%+v", v.Latest)
	}
	if len(v.History) != 2 ||
		v.History[0].Number != "C-NEW" || v.History[1].Number != "C-OLD" {
		t.Fatalf("历史应按校准日期由近到远为 C-NEW、C-OLD：%+v", v.History)
	}
	n, o := v.History[0], v.History[1]
	if !n.Pass || !n.Expired {
		t.Fatalf("历史中的新证书应合格但已到期，不能只在最近证书处到期：%+v", n)
	}
	if !o.Pass || o.Expired {
		t.Fatalf("历史中的旧证书应合格且尚未到期，不能被显示成到期：%+v", o)
	}
}

// TestCLILatestCertExpiryRejectsInTextAndJSON 命令行主场景：两张合格证书共存，
// 校准日期较新的 C-NEW 截止日就是运行当天（当天即到期），较早 C-OLD 仍有效。
// use 的普通文本与 --json 必须表达同一个拒绝结果（退出码 1），原因指出真正
// 到期的最近证书及其截止日；review 同时显示新证书已到期、旧证书尚未到期；
// review/list/todos 等查询不写台账、不变成新的使用申请。
func TestCLILatestCertExpiryRejectsInTextAndJSON(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	s := expiringDates(t)
	seedExpiringLatestLedger(t, path, s, true)

	// 截止日前已保存的获准记录：1 条；查询前后都不应变化。
	beforeCount := readUsageCount(t, path)
	if beforeCount != 1 {
		t.Fatalf("预置应有 1 条截止日前获准记录，得到 %d", beforeCount)
	}
	rawBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账: %v", err)
	}

	// 只查询（普通与 JSON、多个只读入口）不得把查询变成一次使用申请，
	// 台账文件与使用记录保持不变。
	for _, args := range [][]string{
		{"review", "--id", "M-1", "-f", path},
		{"review", "--id", "M-1", "-f", path, "--json"},
		{"list", "-f", path},
		{"todos", "-f", path},
	} {
		if r := runArgs(t, args...); r.code != 0 {
			t.Fatalf("只读查询 %v 应成功，code=%d stderr=%s", args[0], r.code, r.stderr)
		}
	}
	if got := readUsageCount(t, path); got != beforeCount {
		t.Fatalf("查询不应新增使用记录：%d -> %d", beforeCount, got)
	}
	if rawAfter, _ := os.ReadFile(path); string(rawAfter) != string(rawBefore) {
		t.Fatal("只读核对/列出/待办不应改写台账文件")
	}

	// 普通核对文本：不可用原因、最近证书合格但已到期、新旧证书各自到期状态。
	human := runArgs(t, "review", "--id", "M-1", "-f", path)
	if human.code != 0 ||
		!strings.Contains(human.stdout, "当前能否使用：不可以") ||
		!strings.Contains(human.stdout, "最近证书：C-NEW") {
		t.Fatalf("普通核对应显示不可用且最近证书为 C-NEW：code=%d %s", human.code, human.stdout)
	}
	if line := historyLine(t, human.stdout, "C-NEW"); !strings.Contains(line, "合格") ||
		!strings.Contains(line, "已到期") {
		t.Fatalf("新证书应显示合格、已到期：%q", line)
	}
	if line := historyLine(t, human.stdout, "C-OLD"); !strings.Contains(line, "合格") ||
		!strings.Contains(line, "未到期") {
		t.Fatalf("旧证书应显示合格、未到期，不能被显示成到期：%q", line)
	}

	// JSON 核对：结构上确认同一业务结果。
	rv := mustReviewJSON(t, path, "M-1")
	assertReviewShowsNewExpiredOldValid(t, rv, s)
	if len(rv.Rejections) != 0 {
		t.Fatalf("申请前不应有拒绝记录，得到 %d 条", len(rv.Rejections))
	}

	// 实际申请使用：普通文本与 JSON 表达同一拒绝结果，退出码 1。
	assertExpiredLatestRejection(t,
		runArgs(t, "use", "--id", "M-1", "-f", path), s, false)
	assertExpiredLatestRejection(t,
		runArgs(t, "use", "--id", "M-1", "-f", path, "--json"), s, true)

	// 成功保存的拒绝申请随后仍能核对到，且带当时的到期原因；此前的获准记录
	// 不被改成拒绝（review 只列拒绝，故另查台账 usage 总数为 3）。
	if got := readUsageCount(t, path); got != 3 {
		t.Fatalf("两次申请都应留痕，加截止日前获准共 3 条，得到 %d", got)
	}
	after := mustReviewJSON(t, path, "M-1")
	if len(after.Rejections) != 2 {
		t.Fatalf("核对中应能查到当天两次拒绝，得到 %d 条", len(after.Rejections))
	}
	for i, rej := range after.Rejections {
		if rej.Allowed || !containsStd(rej.Reasons, "C-NEW") ||
			!containsStd(rej.Reasons, s.newExp) || containsStd(rej.Reasons, "C-OLD") {
			t.Fatalf("第 %d 条拒绝应保留 C-NEW 到期原因且不涉及 C-OLD：%+v", i+1, rej)
		}
	}
}

// TestCLIBackfillOlderCertAfterLatestExpiredKeepsRejection 守住录入先后无关：
// 较新证书到期后才通过真实 cert 命令补录校准更早、录入更晚、有效期更长的
// 合格 C-OLD。补录只增加历史：最近证书仍是已到期的 C-NEW，申请继续被拒；
// 截止日前获准与补录前的拒绝都保留原结果与当时原因，不被补录重写。
func TestCLIBackfillOlderCertAfterLatestExpiredKeepsRejection(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	s := expiringDates(t)
	// 预置时只有较新证书和一条截止日前的获准记录。
	seedExpiringLatestLedger(t, path, s, false)

	// 截止日当天首次申请：因最近证书 C-NEW 到期被拒并留痕。
	first := runArgs(t, "use", "--id", "M-1", "-f", path)
	assertExpiredLatestRejection(t, first, s, false)

	// 在到期之后补录校准更早、有效期更长的合格旧证书（不同编号、不同校准日）。
	older := certFullArgs(path, "M-1", "C-OLD", s.oldCal, s.oldExp, "-0.2")
	hr := runArgs(t, older...)
	if hr.code != 0 || !strings.Contains(hr.stdout, "已录入新证书") ||
		!strings.Contains(hr.stdout, "判定：合格") {
		t.Fatalf("旧合格证书应补录成功并显示合格：code=%d stdout=%q stderr=%s",
			hr.code, hr.stdout, hr.stderr)
	}

	// 补录后核对：最近证书仍是 C-NEW（合格但已到期）；历史新增 C-OLD 且
	// 合格、未到期；录入更晚、有效期更长都不能让旧证书替代新证书。
	rv := mustReviewJSON(t, path, "M-1")
	assertReviewShowsNewExpiredOldValid(t, rv, s)
	if len(rv.Rejections) != 1 {
		t.Fatalf("补录前的拒绝应有 1 条，得到 %d", len(rv.Rejections))
	}

	// 补录后再次申请：普通文本与 JSON 仍只按 C-NEW 到期拒绝。
	assertExpiredLatestRejection(t,
		runArgs(t, "use", "--id", "M-1", "-f", path), s, false)
	assertExpiredLatestRejection(t,
		runArgs(t, "use", "--id", "M-1", "-f", path, "--json"), s, true)

	// 核对最终历史：补录前后共三条拒绝（补录前 1 条、补录后普通文本与 JSON
	// 各 1 条），原因一致都点名 C-NEW 到期，补录不重写第一条。
	final := mustReviewJSON(t, path, "M-1")
	assertReviewShowsNewExpiredOldValid(t, final, s)
	if len(final.Rejections) != 3 {
		t.Fatalf("应有补录前 1 条、补录后 2 条拒绝，得到 %d", len(final.Rejections))
	}
	for i, rej := range final.Rejections {
		if !containsStd(rej.Reasons, "C-NEW") || !containsStd(rej.Reasons, s.newExp) ||
			containsStd(rej.Reasons, "C-OLD") {
			t.Fatalf("第 %d 条拒绝原因异常：%+v", i+1, rej)
		}
	}
	if final.Rejections[0].Reasons[0] != final.Rejections[1].Reasons[0] ||
		final.Rejections[0].Reasons[0] != final.Rejections[2].Reasons[0] {
		t.Fatalf("补录旧证书重写了此前已保存的到期原因：%q / %q / %q",
			final.Rejections[0].Reasons, final.Rejections[1].Reasons, final.Rejections[2].Reasons)
	}

	// 直接核对台账文件：截止日前的获准记录仍为获准，三条拒绝保留当时原因，
	// 记录总数为 4（查询不会新增）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账: %v", err)
	}
	var data struct {
		Usage []struct {
			RequestedAt string   `json:"requested_at"`
			Allowed     bool     `json:"allowed"`
			Reasons     []string `json:"reasons"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账: %v", err)
	}
	if len(data.Usage) != 4 {
		t.Fatalf("应有 截止日前获准 + 三条拒绝 共 4 条记录，得到 %d", len(data.Usage))
	}
	if !data.Usage[0].Allowed || len(data.Usage[0].Reasons) != 0 ||
		!strings.HasPrefix(data.Usage[0].RequestedAt, s.allowed[:10]) {
		t.Fatalf("截止日前已获准并保存的申请不应被后来到期或补录改成拒绝：%+v",
			data.Usage[0])
	}
	for i := 1; i < 4; i++ {
		if data.Usage[i].Allowed || !containsStd(data.Usage[i].Reasons, "C-NEW") ||
			!containsStd(data.Usage[i].Reasons, s.newExp) {
			t.Fatalf("第 %d 条应为保留当时到期原因的拒绝：%+v", i+1, data.Usage[i])
		}
	}
}
