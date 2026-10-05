package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件为“命令行录入证书后的结论显示”补充回归保障，重点守住两条结论的边界：
//
//  1. 本次录入的那张证书本身是否合格（只由该证书的测得误差与所属器具的允许误差决定）；
//  2. 器具当前能否使用（还要看状态、最近证书与到期情况）。
//
// 任一一条都不能冒充另一条：器具停用/待校准或最近证书超差，不能把本次合格证书
// 说成超差；器具当前可用，也不能把本次补录的超差证书说成合格。

// certFullArgs 构造一条录入证书的完整参数，允许显式指定每个字段。
func certFullArgs(path, instrument, number, calDate, expiry, errVal string) []string {
	return []string{"cert", "-f", path,
		"--instrument", instrument, "--number", number,
		"--cal-date", calDate, "--expiry", expiry,
		"--method", "回归方法", "--error", errVal, "--summary", "回归摘要"}
}

func registerAllowed(t *testing.T, path, id, allowed string) {
	t.Helper()
	if r := runArgs(t, "register", "-f", path,
		"--id", id, "--name", "器具-"+id, "--allowed", allowed); r.code != 0 {
		t.Fatalf("登记器具 %s（允许误差 %s）失败 code=%d stderr=%s", id, allowed, r.code, r.stderr)
	}
}

func mustParseFloat(t *testing.T, raw string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("测试用例的误差 %q 不是数字：%v", raw, err)
	}
	return v
}

// certCLIResult 是 cert 命令 --json 成功输出的最小解析结构。
type certCLIResult struct {
	Accepted    bool `json:"accepted"`
	Duplicate   bool `json:"duplicate"`
	Verdict     string
	Certificate struct {
		Number       string  `json:"number"`
		InstrumentID string  `json:"instrument_id"`
		CalDate      string  `json:"cal_date"`
		Expiry       string  `json:"expiry"`
		Error        float64 `json:"error"`
	} `json:"certificate"`
}

// runCertJSON 以 --json 方式录入（或幂等重提）证书并解析结论。
func runCertJSON(t *testing.T, args []string) (result, certCLIResult) {
	t.Helper()
	r := runArgs(t, append(append([]string{}, args...), "--json")...)
	var got certCLIResult
	if r.code == 0 {
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("cert JSON 解析失败: %v\n%s", err, r.stdout)
		}
	}
	return r, got
}

// assertCertVerdict 核对同一条证书内容在普通文字与 --json 下结论一致：
// 首次以普通文字录入（应明确“已录入新证书”），再以 --json 幂等重提，
// 两种表达的结论必须相同，JSON 中的编号、所属器具、带符号误差都对应该内容。
func assertCertVerdict(t *testing.T, args []string, instrument, wantVerdict, wantErrRaw string) certCLIResult {
	t.Helper()
	wantErr := mustParseFloat(t, wantErrRaw)
	number := args[flagValueIndex(t, args, "--number")+1]

	human := runArgs(t, args...)
	if human.code != 0 {
		t.Fatalf("证书 %s 录入失败 code=%d stderr=%s", number, human.code, human.stderr)
	}
	if !strings.Contains(human.stdout, "已录入新证书") {
		t.Fatalf("证书 %s 应作为新证书录入成功：%q", number, human.stdout)
	}
	if !strings.Contains(human.stdout, "判定："+wantVerdict) {
		t.Fatalf("证书 %s 普通文字应显示判定：%s，实际 %q", number, wantVerdict, human.stdout)
	}
	// 超差只是证书结论，不是录入被拒：不能写成“已拒绝”，也不能走退出码 1/2。
	if strings.Contains(human.stderr, "已拒绝") {
		t.Fatalf("证书 %s 的超差结论不应被当成业务拒绝：%q", number, human.stderr)
	}
	// 普通文字也必须保留原始带符号误差（%g 对 -0.5 这类值保留符号）。
	if !strings.Contains(human.stdout, "测得误差 "+wantErrRaw) {
		t.Fatalf("证书 %s 普通文字应保留带符号误差 %s，实际 %q", number, wantErrRaw, human.stdout)
	}

	dup, js := runCertJSON(t, args)
	if dup.code != 0 {
		t.Fatalf("证书 %s 幂等重提失败 code=%d stderr=%s", number, dup.code, dup.stderr)
	}
	if !js.Accepted || !js.Duplicate {
		t.Fatalf("证书 %s 第二次提交应 accepted=true 且 duplicate=true，得到 %+v", number, js)
	}
	if js.Verdict != wantVerdict {
		t.Fatalf("证书 %s 的 JSON 结论应为 %s，得到 %q", number, wantVerdict, js.Verdict)
	}
	if js.Certificate.Number != number {
		t.Fatalf("JSON 证书编号应对应本次提交 %s，得到 %s", number, js.Certificate.Number)
	}
	if js.Certificate.InstrumentID != instrument {
		t.Fatalf("证书 %s 的 JSON 所属器具应为 %s，得到 %s",
			number, instrument, js.Certificate.InstrumentID)
	}
	if js.Certificate.Error != wantErr {
		t.Fatalf("证书 %s 的 JSON 测得误差应为带符号的 %g，得到 %g",
			number, wantErr, js.Certificate.Error)
	}
	return js
}

// flagValueIndex 返回某个标志在参数切片中的位置，便于测试取回编号等字段。
func flagValueIndex(t *testing.T, args []string, flag string) int {
	t.Helper()
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	t.Fatalf("参数中缺少标志 %s：%v", flag, args)
	return -1
}

// cliReview 是 review --json 输出中本测试关心的字段。
type cliReview struct {
	CanUse     bool     `json:"can_use"`
	Reasons    []string `json:"reasons"`
	Instrument struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"instrument"`
	Latest *struct {
		Number  string  `json:"number"`
		CalDate string  `json:"cal_date"`
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"latest"`
	History []struct {
		Number  string  `json:"number"`
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"history"`
	Rejections []struct {
		Allowed bool     `json:"allowed"`
		Reasons []string `json:"reasons"`
	} `json:"rejections"`
}

func mustReviewJSON(t *testing.T, path, id string) cliReview {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var v cliReview
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, r.stdout)
	}
	return v
}

// TestCertVerdictPositiveNegativeAndBoundary 覆盖正、负测得误差对结论的影响：
// 绝对值等于限值（无论正负）显示合格，超过限值明确显示超差；
// 超差证书同样录入成功，普通文字与 JSON 结论一致且保留带符号误差。
func TestCertVerdictPositiveNegativeAndBoundary(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	past := func(days int) string { return now.AddDate(0, 0, -days).Format(calibrate.DateLayout) }
	future := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	cases := []struct {
		number string
		err    string
		want   string
	}{
		{"C-EQ-POS", "0.5", "合格"},       // 正误差，绝对值恰好等于限值
		{"C-EQ-NEG", "-0.5", "合格"},      // 负误差，绝对值恰好等于限值
		{"C-OVER-POS", "0.5001", "超差"},  // 正误差，绝对值超过限值
		{"C-OVER-NEG", "-0.5001", "超差"}, // 负误差，绝对值超过限值
	}
	for i, tc := range cases {
		args := certFullArgs(path, "M-1", tc.number, past(10+i), future, tc.err)
		assertCertVerdict(t, args, "M-1", tc.want, tc.err)
	}

	// 全部录入成功：历史中四张证书按各自误差带正确结论，符号也原样保留。
	rv := mustReviewJSON(t, path, "M-1")
	if len(rv.History) != 4 {
		t.Fatalf("四张证书都应录入成功，历史有 %d 张", len(rv.History))
	}
	want := map[string]struct {
		pass bool
		err  float64
	}{
		"C-EQ-POS":   {true, 0.5},
		"C-EQ-NEG":   {true, -0.5},
		"C-OVER-POS": {false, 0.5001},
		"C-OVER-NEG": {false, -0.5001},
	}
	for _, h := range rv.History {
		w, ok := want[h.Number]
		if !ok {
			t.Fatalf("历史出现意外证书 %s", h.Number)
		}
		if h.Pass != w.pass {
			t.Fatalf("证书 %s 历史结论应为 合格=%v，得到 %v", h.Number, w.pass, h.Pass)
		}
		if h.Error != w.err {
			t.Fatalf("证书 %s 应保留原始带符号误差 %g，得到 %g", h.Number, w.err, h.Error)
		}
	}
}

// TestCertVerdictZeroAllowedError 覆盖允许误差为零的器具：零误差仍合格，
// 任意非零误差（无论正负）都超差，且超差证书仍录入成功。
func TestCertVerdictZeroAllowedError(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-0", "0")

	now := time.Now()
	past := func(days int) string { return now.AddDate(0, 0, -days).Format(calibrate.DateLayout) }
	future := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	cases := []struct {
		number string
		err    string
		want   string
	}{
		{"C-ZERO", "0", "合格"},
		{"C-POS", "0.0001", "超差"},
		{"C-NEG", "-0.0001", "超差"},
	}
	for i, tc := range cases {
		assertCertVerdict(t,
			certFullArgs(path, "M-0", tc.number, past(20+i), future, tc.err),
			"M-0", tc.want, tc.err)
	}
	rv := mustReviewJSON(t, path, "M-0")
	got := map[string]bool{}
	for _, h := range rv.History {
		got[h.Number] = h.Pass
	}
	if !got["C-ZERO"] || got["C-POS"] || got["C-NEG"] {
		t.Fatalf("零允许误差下结论错误：%v", got)
	}
}

// TestCertVerdictFollowsEachInstrumentLimit 同一测得误差放在允许误差不同的
// 器具上，结论分别遵从各自限值，不能混用另一件器具的数据。
func TestCertVerdictFollowsEachInstrumentLimit(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-LOOSE", "1")
	registerAllowed(t, path, "M-TIGHT", "0.1")

	now := time.Now()
	cal := now.AddDate(0, 0, -5).Format(calibrate.DateLayout)
	expiry := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	// 同一次校准日、同一个 -0.5 误差：宽松器具合格，严格器具超差。
	looseArgs := certFullArgs(path, "M-LOOSE", "C-LOOSE", cal, expiry, "-0.5")
	tightArgs := certFullArgs(path, "M-TIGHT", "C-TIGHT", cal, expiry, "-0.5")

	looseHuman := runArgs(t, looseArgs...)
	if looseHuman.code != 0 || !strings.Contains(looseHuman.stdout, "判定：合格") {
		t.Fatalf("允许误差 1 的器具录入 -0.5 应合格：code=%d %q %s",
			looseHuman.code, looseHuman.stdout, looseHuman.stderr)
	}
	tightHuman := runArgs(t, tightArgs...)
	if tightHuman.code != 0 || !strings.Contains(tightHuman.stdout, "判定：超差") {
		t.Fatalf("允许误差 0.1 的器具录入 -0.5 应超差但录入成功：code=%d %q %s",
			tightHuman.code, tightHuman.stdout, tightHuman.stderr)
	}

	// JSON 中证书编号、所属器具、误差都对应本次提交，结论与文字一致。
	_, looseJSON := runCertJSON(t, looseArgs)
	_, tightJSON := runCertJSON(t, tightArgs)
	if looseJSON.Verdict != "合格" || tightJSON.Verdict != "超差" {
		t.Fatalf("JSON 结论串台：loose=%q tight=%q", looseJSON.Verdict, tightJSON.Verdict)
	}
	if looseJSON.Certificate.Number != "C-LOOSE" || looseJSON.Certificate.InstrumentID != "M-LOOSE" ||
		looseJSON.Certificate.Error != -0.5 {
		t.Fatalf("宽松器具的 JSON 证书字段不对应本次提交：%+v", looseJSON.Certificate)
	}
	if tightJSON.Certificate.Number != "C-TIGHT" || tightJSON.Certificate.InstrumentID != "M-TIGHT" ||
		tightJSON.Certificate.Error != -0.5 {
		t.Fatalf("严格器具的 JSON 证书字段不对应本次提交：%+v", tightJSON.Certificate)
	}

	// 各器具的最近证书与结论互不混入。
	looseReview := mustReviewJSON(t, path, "M-LOOSE")
	tightReview := mustReviewJSON(t, path, "M-TIGHT")
	if looseReview.Latest == nil || looseReview.Latest.Number != "C-LOOSE" || !looseReview.Latest.Pass {
		t.Fatalf("宽松器具最近证书应是自身的合格证书：%+v", looseReview.Latest)
	}
	if tightReview.Latest == nil || tightReview.Latest.Number != "C-TIGHT" || tightReview.Latest.Pass {
		t.Fatalf("严格器具最近证书应是自身的超差证书：%+v", tightReview.Latest)
	}
}

// TestCertVerdictIndependentOfInstrumentStatus 证书结论只属于证书本身：
// 待校准或停用器具录入合格证书，录入结果仍显示合格；“当前能否使用”另由
// 核对按状态判断，不能拿器具的使用判断替换证书结论。
func TestCertVerdictIndependentOfInstrumentStatus(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-PENDING", "0.5") // 新登记即为待校准
	registerAllowed(t, path, "M-RETIRED", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-RETIRED", "--status", "停用"); r.code != 0 {
		t.Fatalf("切换停用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	cal := now.AddDate(0, 0, -7).Format(calibrate.DateLayout)
	expiry := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	for _, tc := range []struct {
		id, number, reason string
	}{
		{"M-PENDING", "C-PENDING", "待校准"},
		{"M-RETIRED", "C-RETIRED", "停用"},
	} {
		args := certFullArgs(path, tc.id, tc.number, cal, expiry, "-0.2")
		human := runArgs(t, args...)
		if human.code != 0 || !strings.Contains(human.stdout, "判定：合格") {
			t.Fatalf("%s 器具录入合格证书应显示合格：code=%d %q %s",
				tc.reason, human.code, human.stdout, human.stderr)
		}
		_, js := runCertJSON(t, args)
		if js.Verdict != "合格" || js.Certificate.Number != tc.number ||
			js.Certificate.InstrumentID != tc.id || js.Certificate.Error != -0.2 {
			t.Fatalf("%s 器具的证书 JSON 字段不应被使用判断替换：%+v", tc.reason, js)
		}

		// 证书本身合格，但器具当前仍不可使用，原因来自状态而非证书超差。
		rv := mustReviewJSON(t, path, tc.id)
		if rv.CanUse {
			t.Fatalf("%s 器具不应因证书合格就能使用", tc.reason)
		}
		if !containsStd(rv.Reasons, tc.reason) {
			t.Fatalf("不可使用的原因应包含%s，得到 %v", tc.reason, rv.Reasons)
		}
		if rv.Latest == nil || !rv.Latest.Pass {
			t.Fatalf("%s 器具的最近证书自身结论仍应是合格：%+v", tc.reason, rv.Latest)
		}
	}
}

// TestBackfillOlderPassingCertKeepsNewerFailingLatest 守住补录主场景：
// 器具在用、已有一张校准日期较新的超差证书，再补录一张校准日期较早、合格且
// 未到期的证书——补录结果必须显示旧证书合格并进入历史，但最近证书仍是较新
// 的超差证书，申请使用仍因最近证书超差被拒绝。先后依据校准日期，而不是证书
// 编号或录入顺序（这里较新证书编号 C-1 更小、录入更早）。
func TestBackfillOlderPassingCertKeepsNewerFailingLatest(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	newerCal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	olderCal := now.AddDate(0, 0, -200).Format(calibrate.DateLayout)
	expiry := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	// 先录入校准日期较新的超差证书（编号 C-1，按编号反而更小）。
	newer := certFullArgs(path, "M-1", "C-1", newerCal, expiry, "0.8")
	r := runArgs(t, newer...)
	if r.code != 0 || !strings.Contains(r.stdout, "已录入新证书") ||
		!strings.Contains(r.stdout, "判定：超差") {
		t.Fatalf("较新超差证书应录入成功并显示超差：code=%d %q %s", r.code, r.stdout, r.stderr)
	}

	// 补录前：历史 1 张、最近证书超差、使用被拒绝并留痕。
	before := mustReviewJSON(t, path, "M-1")
	if len(before.History) != 1 || before.Latest == nil || before.Latest.Number != "C-1" || before.Latest.Pass {
		t.Fatalf("补录前最近证书应是超差的 C-1：%+v", before)
	}
	use1 := runArgs(t, "use", "--id", "M-1", "-f", path)
	if use1.code != 1 || !strings.Contains(use1.stderr, "超差") {
		t.Fatalf("最近证书超差应拒绝使用，code=%d stderr=%q", use1.code, use1.stderr)
	}

	// 补录校准日期较早、合格、未到期的旧证书（编号 C-9 更大、录入更晚）。
	older := certFullArgs(path, "M-1", "C-9", olderCal, expiry, "-0.1")
	jr, js := runCertJSON(t, older)
	if jr.code != 0 || !js.Accepted || js.Duplicate {
		t.Fatalf("旧合格证书应作为新证书录入成功：code=%d stdout=%q stderr=%s",
			jr.code, jr.stdout, jr.stderr)
	}
	if js.Verdict != "合格" {
		t.Fatalf("补录结果必须显示旧证书自身合格，不能被最近证书的超差覆盖，得到 %q", js.Verdict)
	}
	if js.Certificate.Number != "C-9" || js.Certificate.InstrumentID != "M-1" ||
		js.Certificate.Error != -0.1 || js.Certificate.CalDate != olderCal {
		t.Fatalf("JSON 证书字段应对应本次补录：%+v", js.Certificate)
	}
	// 同内容普通文字重提：结论仍为合格，并标明返回原证书。
	human2 := runArgs(t, older...)
	if human2.code != 0 || !strings.Contains(human2.stdout, "判定：合格") ||
		!strings.Contains(human2.stdout, "未增加历史") {
		t.Fatalf("普通文字下补录证书也应显示合格：code=%d %q", human2.code, human2.stdout)
	}

	// 补录后：历史增加这张证书；最近证书仍是校准日期较新的超差 C-1，
	// 不能因为补录证书合格就解除使用限制；器具状态保持在用。
	after := mustReviewJSON(t, path, "M-1")
	if len(after.History) != len(before.History)+1 {
		t.Fatalf("补录应只增加一张历史证书：%d -> %d",
			len(before.History), len(after.History))
	}
	if after.Instrument.Status != "在用" {
		t.Fatalf("补录证书不应改写器具状态，得到 %s", after.Instrument.Status)
	}
	if after.Latest == nil || after.Latest.Number != "C-1" ||
		after.Latest.CalDate != newerCal || after.Latest.Error != 0.8 || after.Latest.Pass {
		t.Fatalf("最近证书必须仍是较新的超差 C-1（按校准日期而非编号/录入顺序）：%+v", after.Latest)
	}
	found := false
	for i := range after.History {
		h := after.History[i]
		if h.Number == "C-9" {
			found = true
			if !h.Pass {
				t.Fatal("历史中的旧证书应显示合格")
			}
			if h.Expired {
				t.Fatal("补录的旧证书应未到期")
			}
			if h.Error != -0.1 {
				t.Fatalf("旧证书应保留原始带符号误差 -0.1，得到 %g", h.Error)
			}
		}
		if h.Number == "C-1" && !h.Pass {
			// 较新证书结论同样保持超差。
		}
	}
	if !found {
		t.Fatalf("历史中应增加补录的 C-9：%+v", after.History)
	}
	if after.CanUse || !containsStd(after.Reasons, "超差") {
		t.Fatalf("补录合格旧证书不应解除使用限制：canUse=%v reasons=%v",
			after.CanUse, after.Reasons)
	}

	// 再次申请使用：仍因最近证书 C-1 超差被拒绝，并新增一条拒绝留痕。
	use2 := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if use2.code != 1 || !strings.Contains(use2.stdout, `"allowed": false`) ||
		!strings.Contains(use2.stdout, "超差") {
		t.Fatalf("补录后申请使用仍应因最近证书超差被拒绝：code=%d stdout=%q",
			use2.code, use2.stdout)
	}
	final := mustReviewJSON(t, path, "M-1")
	if len(final.Rejections) != 2 {
		t.Fatalf("两次拒绝都应留痕且补录不改写历史，得到 %d 条", len(final.Rejections))
	}
	for i, rej := range final.Rejections {
		if rej.Allowed || !containsStd(rej.Reasons, "超差") {
			t.Fatalf("第 %d 条拒绝记录应保留申请当时的超差原因：%+v", i+1, rej)
		}
	}
}

// TestBackfillOlderFailingCertKeepsInstrumentUsable 反方向守住同一条边界：
// 器具在用、最近证书合格时补录一张更早的超差证书，补录结果必须如实显示超差，
// 但最近证书与当前能否使用都不被这张旧证书改变。
func TestBackfillOlderFailingCertKeepsInstrumentUsable(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-2", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-2", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	newerCal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	olderCal := now.AddDate(0, 0, -200).Format(calibrate.DateLayout)
	expiry := now.AddDate(10, 0, 0).Format(calibrate.DateLayout)

	// 较新的合格证书：器具当前可使用。
	newer := certFullArgs(path, "M-2", "C-2", newerCal, expiry, "0.1")
	if r := runArgs(t, newer...); r.code != 0 ||
		!strings.Contains(r.stdout, "判定：合格") {
		t.Fatalf("较新合格证书录入异常：code=%d %q %s", r.code, r.stdout, r.stderr)
	}
	if u := runArgs(t, "use", "--id", "M-2", "-f", path); u.code != 0 {
		t.Fatalf("最近证书合格未到期时应允许使用，code=%d stderr=%s", u.code, u.stderr)
	}

	// 补录更早的超差证书：本次录入结果必须明确显示超差。
	older := certFullArgs(path, "M-2", "C-1", olderCal, expiry, "-5")
	jr, js := runCertJSON(t, older)
	if jr.code != 0 || !js.Accepted || js.Duplicate {
		t.Fatalf("旧超差证书也应作为新证书录入成功：code=%d stdout=%q stderr=%s",
			jr.code, jr.stdout, jr.stderr)
	}
	if js.Verdict != "超差" || js.Certificate.Number != "C-1" ||
		js.Certificate.InstrumentID != "M-2" || js.Certificate.Error != -5 {
		t.Fatalf("补录结果应如实显示本次证书超差且字段对应提交：%+v", js)
	}

	// 最近证书仍是较新的合格 C-2，器具当前可以使用，使用申请不因旧超差证书被拒。
	rv := mustReviewJSON(t, path, "M-2")
	if len(rv.History) != 2 {
		t.Fatalf("补录应使历史变为 2 张，得到 %d", len(rv.History))
	}
	if rv.Latest == nil || rv.Latest.Number != "C-2" || !rv.Latest.Pass {
		t.Fatalf("最近证书应仍是较新的合格 C-2：%+v", rv.Latest)
	}
	if !rv.CanUse || len(rv.Reasons) != 0 {
		t.Fatalf("最近证书合格时旧超差证书不应限制使用：canUse=%v reasons=%v",
			rv.CanUse, rv.Reasons)
	}
	for _, h := range rv.History {
		switch h.Number {
		case "C-1":
			if h.Pass || h.Error != -5 {
				t.Fatalf("旧超差证书应保留超差结论与带符号误差：%+v", h)
			}
		case "C-2":
			if !h.Pass {
				t.Fatalf("较新合格证书不应被旧证书改写结论：%+v", h)
			}
		}
	}
	if u := runArgs(t, "use", "--id", "M-2", "-f", path, "--json"); u.code != 0 ||
		!strings.Contains(u.stdout, `"allowed": true`) {
		t.Fatalf("补录旧超差证书后仍应允许使用：code=%d stdout=%q stderr=%s",
			u.code, u.stdout, u.stderr)
	}
}

func containsStd(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
