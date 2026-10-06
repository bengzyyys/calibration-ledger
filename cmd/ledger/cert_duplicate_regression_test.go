package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件为“同一证书编号只对应一份已经保存的业务事实”补充命令行回归保障：
//
//   - 同号同内容：退出码 0，普通输出说明重复，JSON accepted=true 且
//     duplicate=true，历史不增加，录入时间保留第一次成功保存的值；
//   - 同号不同内容：退出码 1，JSON accepted=false 且 conflict=true，
//     不返回录入成功的证书，已有数据与使用限制保持不变。
//
// 前后空白沿用现有去除规则、数字写法不同但数值相同仍是同一份内容；
// 方法/摘要内部文字变化、只改任一业务字段都属于冲突。

// certAllArgs 构造一条可逐字段指定的录入证书参数。
func certAllArgs(path, inst, number, cal, expiry, method, errVal, summary string) []string {
	return []string{"cert", "-f", path,
		"--instrument", inst, "--number", number,
		"--cal-date", cal, "--expiry", expiry,
		"--method", method, "--error", errVal, "--summary", summary}
}

// certDupJSON 是 cert 命令 --json 输出（成功与冲突拒绝）的解析结构。
type certDupJSON struct {
	Accepted    *bool  `json:"accepted"`
	Duplicate   bool   `json:"duplicate"`
	Conflict    bool   `json:"conflict"`
	Error       string `json:"error"`
	Certificate *struct {
		Number       string  `json:"number"`
		InstrumentID string  `json:"instrument_id"`
		CalDate      string  `json:"cal_date"`
		Expiry       string  `json:"expiry"`
		Method       string  `json:"method"`
		Error        float64 `json:"error"`
		Summary      string  `json:"summary"`
		CreatedAt    string  `json:"created_at"`
	} `json:"certificate"`
}

// runCertDupJSON 以 --json 方式提交证书并解析输出（成功或业务拒绝都有 JSON）。
func runCertDupJSON(t *testing.T, args ...string) (result, certDupJSON) {
	t.Helper()
	r := runArgs(t, append(append([]string{}, args...), "--json")...)
	var got certDupJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("cert JSON 解析失败: %v\ncode=%d\nstdout=%s\nstderr=%s",
			err, r.code, r.stdout, r.stderr)
	}
	return r, got
}

// certDupCertView 是 review --json 中本文件关心的证书字段。
type certDupCertView struct {
	Number    string  `json:"number"`
	CalDate   string  `json:"cal_date"`
	Expiry    string  `json:"expiry"`
	Method    string  `json:"method"`
	Summary   string  `json:"summary"`
	Error     float64 `json:"error"`
	Pass      bool    `json:"pass"`
	Expired   bool    `json:"expired"`
	CreatedAt string  `json:"created_at"`
}

type certDupReview struct {
	CanUse  bool              `json:"can_use"`
	Reasons []string          `json:"reasons"`
	Latest  *certDupCertView  `json:"latest"`
	History []certDupCertView `json:"history"`
}

func mustReviewDup(t *testing.T, path, id string) certDupReview {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var v certDupReview
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, r.stdout)
	}
	return v
}

// TestCertDuplicateSameContentReturnsOriginalCLI 同一台账中首次成功录入后，
// 再提交相同编号和相同业务内容：退出码 0 并明确表示重复，历史中仍只有原来
// 那一张，录入时间保留第一次成功保存的值；后一次提交发生在不同时间（秒级
// 时间戳已不同）也不能因此生成新证书。
func TestCertDuplicateSameContentReturnsOriginalCLI(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	cal := now.AddDate(0, 0, -10).Format(calibrate.DateLayout)
	expiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	args := certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.2", "例行校准")

	// 首次以 --json 录入：accepted=true、duplicate=false，记下录入时间。
	fjCode, fj := runCertDupJSON(t, args...)
	if fjCode.code != 0 || fj.Accepted == nil || !*fj.Accepted || fj.Duplicate ||
		fj.Certificate == nil || fj.Certificate.CreatedAt == "" {
		t.Fatalf("首次 JSON 录入应 accepted=true、duplicate=false 且带证书：code=%d %+v",
			fjCode.code, fj)
	}

	// 跨过至少一秒再以普通输出提交相同内容：时间戳必然不同，不能生成新证书。
	time.Sleep(1100 * time.Millisecond)
	dup := runArgs(t, args...)
	if dup.code != 0 ||
		!strings.Contains(dup.stdout, "同号证书且内容一致") ||
		!strings.Contains(dup.stdout, "未增加历史记录") {
		t.Fatalf("同号同内容应退出 0 并说明重复：code=%d stdout=%q stderr=%s",
			dup.code, dup.stdout, dup.stderr)
	}
	djCode, dj := runCertDupJSON(t, args...)
	if djCode.code != 0 || dj.Accepted == nil || !*dj.Accepted || !dj.Duplicate {
		t.Fatalf("重复提交 JSON 应 accepted=true 且 duplicate=true：code=%d %+v",
			djCode.code, dj)
	}
	if dj.Certificate == nil || dj.Certificate.CreatedAt != fj.Certificate.CreatedAt {
		t.Fatalf("录入时间应保留第一次成功保存的值：首次 %+v，重复 %+v",
			fj.Certificate, dj.Certificate)
	}

	// 历史中仍只有原来那一张，录入时间与内容不变。
	rv := mustReviewDup(t, path, "M-1")
	if len(rv.History) != 1 {
		t.Fatalf("重复提交不应增加历史，共 %d 张", len(rv.History))
	}
	got := rv.History[0]
	if got.Number != "C-1" || got.CalDate != cal || got.Expiry != expiry ||
		got.Method != "规范甲" || got.Summary != "例行校准" || got.Error != 0.2 ||
		got.CreatedAt != fj.Certificate.CreatedAt {
		t.Fatalf("历史中的证书应是第一次保存的内容：%+v", got)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-1" {
		t.Fatalf("最近证书应仍是 C-1：%+v", rv.Latest)
	}
}

// TestCertDuplicateTrimsPaddingAndNumericSpellingCLI 器具编号、证书编号、
// 日期、方法和摘要前后的空白沿用现有去除规则；数字写法不同但测得误差
// 数值相同，也仍是同一份业务内容（判重复）。方法或摘要内部的文字发生
// 变化则必须报冲突。
func TestCertDuplicateTrimsPaddingAndNumericSpellingCLI(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	cal := now.AddDate(0, 0, -10).Format(calibrate.DateLayout)
	expiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)

	first := runArgs(t, certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.10", "例行校准")...)
	if first.code != 0 {
		t.Fatalf("首次录入失败 code=%d stderr=%s", first.code, first.stderr)
	}

	// 数字写法不同但数值相同：0.10、0.1、0.100、1e-1 是同一份业务内容。
	for _, spelling := range []string{"0.1", "0.100", "1e-1"} {
		r, js := runCertDupJSON(t,
			certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", spelling, "例行校准")...)
		if r.code != 0 || !js.Duplicate {
			t.Fatalf("误差写法 %q 应判为重复：code=%d %+v stderr=%s",
				spelling, r.code, js, r.stderr)
		}
	}

	// 各字段前后空白不同，裁剪后内容一致：仍判重复，返回裁剪后的原证书。
	padded := certAllArgs(path, "  M-1\t", "\tC-1  ", " "+cal+" ", "\t"+expiry,
		"  规范甲 ", "0.1", " 例行校准\t")
	r, js := runCertDupJSON(t, padded...)
	if r.code != 0 || !js.Duplicate {
		t.Fatalf("前后空白差异应判为重复：code=%d %+v stderr=%s", r.code, js, r.stderr)
	}
	if js.Certificate == nil || js.Certificate.InstrumentID != "M-1" ||
		js.Certificate.Number != "C-1" || js.Certificate.CalDate != cal ||
		js.Certificate.Expiry != expiry || js.Certificate.Method != "规范甲" ||
		js.Certificate.Summary != "例行校准" {
		t.Fatalf("返回的应是裁剪后保存的原证书：%+v", js.Certificate)
	}

	// 方法或摘要内部的文字变化不能借去除空白当成相同内容。
	innerMethod := runArgs(t,
		certAllArgs(path, "M-1", "C-1", cal, expiry, "规范 甲", "0.1", "例行校准")...)
	if innerMethod.code != 1 {
		t.Fatalf("方法内部文字变化应冲突退出 1，得到 code=%d stdout=%q",
			innerMethod.code, innerMethod.stdout)
	}
	innerSummary := runArgs(t,
		certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.1", "例行 校准")...)
	if innerSummary.code != 1 {
		t.Fatalf("摘要内部文字变化应冲突退出 1，得到 code=%d stdout=%q",
			innerSummary.code, innerSummary.stdout)
	}

	if rv := mustReviewDup(t, path, "M-1"); len(rv.History) != 1 {
		t.Fatalf("上述提交都不应增加历史，共 %d 张", len(rv.History))
	}
}

// TestCertConflictRejectsEachFieldChangeCLI 同号内容的比较覆盖所属器具、
// 校准日期、有效期截止日、校准方法、测得误差和摘要：只改任一项（各项新
// 内容本身合法）都退出 1，JSON 明确表示未接受且属于冲突，不返回录入成功
// 的证书；冲突后原证书、历史数量、最近证书选择不变，目标器具也不多出
// 这张同号证书。
func TestCertConflictRejectsEachFieldChangeCLI(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	registerAllowed(t, path, "M-2", "1")

	now := time.Now()
	cal := now.AddDate(0, 0, -10).Format(calibrate.DateLayout)
	expiry := now.AddDate(0, 6, 0).Format(calibrate.DateLayout)
	base := certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.2", "例行校准")
	if r := runArgs(t, base...); r.code != 0 {
		t.Fatalf("首次录入失败 code=%d stderr=%s", r.code, r.stderr)
	}

	cases := []struct {
		name string
		args []string
	}{
		// 换到另一件已经登记的器具。
		{"所属器具", certAllArgs(path, "M-2", "C-1", cal, expiry, "规范甲", "0.2", "例行校准")},
		// 换成真实且不晚于今天的校准日期。
		{"校准日期", certAllArgs(path, "M-1", "C-1",
			now.AddDate(0, 0, -11).Format(calibrate.DateLayout), expiry, "规范甲", "0.2", "例行校准")},
		// 换成真实且晚于校准日期的截止日。
		{"有效期截止日", certAllArgs(path, "M-1", "C-1", cal,
			now.AddDate(0, 7, 0).Format(calibrate.DateLayout), "规范甲", "0.2", "例行校准")},
		// 换成非空方法。
		{"校准方法", certAllArgs(path, "M-1", "C-1", cal, expiry, "规范乙", "0.2", "例行校准")},
		// 正负误差绝对值相同、合格结论相同，数值不同也属于内容冲突。
		{"测得误差", certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "-0.2", "例行校准")},
		// 换成非空摘要。
		{"摘要", certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.2", "周期校准")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, js := runCertDupJSON(t, tc.args...)
			if r.code != 1 {
				t.Fatalf("只改%s应冲突退出 1，得到 code=%d stdout=%q stderr=%s",
					tc.name, r.code, r.stdout, r.stderr)
			}
			if js.Accepted == nil || *js.Accepted || !js.Conflict {
				t.Fatalf("只改%s的 JSON 应 accepted=false 且 conflict=true：%+v",
					tc.name, js)
			}
			if js.Certificate != nil || strings.Contains(r.stdout, `"certificate"`) {
				t.Fatalf("冲突不应返回录入成功的证书：%s", r.stdout)
			}
			if !strings.Contains(r.stderr, "已拒绝") {
				t.Fatalf("冲突应在 stderr 说明已拒绝：%q", r.stderr)
			}
		})
	}

	// 普通输出下同样是退出码 1 的拒绝，而不是成功或参数错误。
	human := runArgs(t, certAllArgs(path, "M-1", "C-1", cal, expiry, "规范甲", "0.2", "另一份摘要")...)
	if human.code != 1 || !strings.Contains(human.stderr, "已拒绝") || human.stdout != "" {
		t.Fatalf("普通输出下冲突应退出 1 并说明拒绝：code=%d stdout=%q stderr=%s",
			human.code, human.stdout, human.stderr)
	}

	// 冲突后按器具核对：原证书全部内容、历史数量和最近证书选择与提交前一致。
	rv := mustReviewDup(t, path, "M-1")
	if len(rv.History) != 1 {
		t.Fatalf("冲突提交不应增加历史，共 %d 张", len(rv.History))
	}
	got := rv.History[0]
	if got.Number != "C-1" || got.CalDate != cal || got.Expiry != expiry ||
		got.Method != "规范甲" || got.Summary != "例行校准" || got.Error != 0.2 {
		t.Fatalf("冲突后原证书内容被改动：%+v", got)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-1" || rv.Latest.Error != 0.2 {
		t.Fatalf("最近证书选择被冲突提交改变：%+v", rv.Latest)
	}
	// 目标器具不能多出这张同号证书。
	other := mustReviewDup(t, path, "M-2")
	if len(other.History) != 0 || other.Latest != nil {
		t.Fatalf("M-2 不应出现同号证书：历史=%d 最近=%+v", len(other.History), other.Latest)
	}
}

// TestCertConflictKeepsUsageRestrictionCLI 原证书若为超差，把误差改成合格值
// 重交不能解除使用限制；原证书若已到期，改成未来截止日重交也不能恢复使用
// 资格。两次重交都是冲突拒绝，原证书内容与限制保持不变。
func TestCertConflictKeepsUsageRestrictionCLI(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")

	now := time.Now()
	past := func(days int) string { return now.AddDate(0, 0, -days).Format(calibrate.DateLayout) }
	future := now.AddDate(0, 6, 0).Format(calibrate.DateLayout)

	// 超差原证书：改成合格误差重交仍冲突，限制不解除。
	registerAllowed(t, path, "M-FAIL", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-FAIL", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}
	badCert := certAllArgs(path, "M-FAIL", "C-FAIL", past(10), future, "规范甲", "0.8", "超差批次")
	if r := runArgs(t, badCert...); r.code != 0 ||
		!strings.Contains(r.stdout, "判定：超差") {
		t.Fatalf("超差证书应录入成功并显示超差：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}
	if u := runArgs(t, "use", "--id", "M-FAIL", "-f", path); u.code != 1 ||
		!strings.Contains(u.stderr, "超差") {
		t.Fatalf("前提：超差器具应被拒绝使用，code=%d stderr=%q", u.code, u.stderr)
	}
	fix := certAllArgs(path, "M-FAIL", "C-FAIL", past(10), future, "规范甲", "0.1", "超差批次")
	r, js := runCertDupJSON(t, fix...)
	if r.code != 1 || !js.Conflict {
		t.Fatalf("改成合格误差重交应冲突退出 1：code=%d %+v", r.code, js)
	}
	rv := mustReviewDup(t, path, "M-FAIL")
	if rv.CanUse || !containsStd(rv.Reasons, "超差") {
		t.Fatalf("冲突重交不应解除超差限制：canUse=%v reasons=%v", rv.CanUse, rv.Reasons)
	}
	if len(rv.History) != 1 || rv.Latest == nil || rv.Latest.Error != 0.8 || rv.Latest.Pass {
		t.Fatalf("原超差证书不应被改写：历史=%d 最近=%+v", len(rv.History), rv.Latest)
	}
	if u := runArgs(t, "use", "--id", "M-FAIL", "-f", path); u.code != 1 ||
		!strings.Contains(u.stderr, "超差") {
		t.Fatalf("冲突重交后申请使用仍应因超差被拒绝：code=%d stderr=%q", u.code, u.stderr)
	}

	// 已到期原证书：改成未来截止日重交仍冲突，资格不恢复。
	registerAllowed(t, path, "M-EXP", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-EXP", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}
	expiredCert := certAllArgs(path, "M-EXP", "C-EXP", past(400), past(30), "规范甲", "0.1", "例行校准")
	if r := runArgs(t, expiredCert...); r.code != 0 {
		t.Fatalf("到期证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if u := runArgs(t, "use", "--id", "M-EXP", "-f", path); u.code != 1 ||
		!strings.Contains(u.stderr, "到期") {
		t.Fatalf("前提：证书已到期应被拒绝使用，code=%d stderr=%q", u.code, u.stderr)
	}
	renew := certAllArgs(path, "M-EXP", "C-EXP", past(400), future, "规范甲", "0.1", "例行校准")
	r, js = runCertDupJSON(t, renew...)
	if r.code != 1 || !js.Conflict {
		t.Fatalf("改成未来截止日重交应冲突退出 1：code=%d %+v", r.code, js)
	}
	rv = mustReviewDup(t, path, "M-EXP")
	if rv.CanUse || !containsStd(rv.Reasons, "到期") {
		t.Fatalf("冲突重交不应恢复使用资格：canUse=%v reasons=%v", rv.CanUse, rv.Reasons)
	}
	if len(rv.History) != 1 || rv.Latest == nil ||
		rv.Latest.Expiry != past(30) || !rv.Latest.Expired {
		t.Fatalf("原到期证书不应被改写：历史=%d 最近=%+v", len(rv.History), rv.Latest)
	}
}
