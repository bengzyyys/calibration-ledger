package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// certVerdictArgs 构造一条可指定器具、日期与测得误差的证书录入参数。
func certVerdictArgs(path, instrument, number, calDate, expiry, measured string) []string {
	return []string{"cert", "-f", path,
		"--instrument", instrument, "--number", number,
		"--cal-date", calDate, "--expiry", expiry,
		"--method", "JJF规范", "--error", measured, "--summary", "例行校准"}
}

// certResultJSON 是 cert --json 成功输出的解析结构。
type certResultJSON struct {
	Accepted    bool   `json:"accepted"`
	Duplicate   bool   `json:"duplicate"`
	Verdict     string `json:"verdict"`
	Certificate struct {
		Number       string  `json:"number"`
		InstrumentID string  `json:"instrument_id"`
		CalDate      string  `json:"cal_date"`
		Expiry       string  `json:"expiry"`
		Error        float64 `json:"error"`
	} `json:"certificate"`
}

// reviewVerdictJSON 是 review --json 中与证书结论相关的解析结构。
type reviewVerdictJSON struct {
	Instrument struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"instrument"`
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
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"history"`
}

func registerWithAllowed(t *testing.T, path, id, allowed string) {
	t.Helper()
	r := runArgs(t, "register", "-f", path,
		"--id", id, "--name", "器具-"+id, "--allowed", allowed)
	if r.code != 0 {
		t.Fatalf("登记 %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
}

func parseCertResult(t *testing.T, stdout string) certResultJSON {
	t.Helper()
	var got certResultJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("解析 cert JSON 失败: %v\n%s", err, stdout)
	}
	return got
}

func parseReviewVerdict(t *testing.T, stdout string) reviewVerdictJSON {
	t.Helper()
	var got reviewVerdictJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, stdout)
	}
	return got
}

// 结论由本次录入证书的测得误差绝对值与器具允许误差比较得出：正、负误差
// 同等对待，绝对值等于限值合格、超过限值超差；允许误差为零的器具零误差
// 合格、非零误差超差。超差证书仍录入成功（退出码 0）并保留带符号误差。
// 普通文字与 --json 对同一张证书表达的结论一致，JSON 中的证书编号、
// 所属器具与测得误差对应本次提交。
func TestCertVerdictByMeasuredErrorAndLimit(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerWithAllowed(t, path, "M-H", "0.5")
	registerWithAllowed(t, path, "M-Z", "0")

	cases := []struct {
		instrument string
		measured   string
		wantErr    float64
		want       string
	}{
		{"M-H", "0.5", 0.5, "合格"},   // 正误差等于限值
		{"M-H", "-0.5", -0.5, "合格"}, // 负误差绝对值等于限值
		{"M-H", "0.4", 0.4, "合格"},
		{"M-H", "-0.4", -0.4, "合格"},
		{"M-H", "0.6", 0.6, "超差"},   // 正误差超过限值
		{"M-H", "-0.6", -0.6, "超差"}, // 负误差绝对值超过限值
		{"M-Z", "0", 0, "合格"},       // 零限值器具零误差
		{"M-Z", "0.1", 0.1, "超差"},   // 零限值器具正误差
		{"M-Z", "-0.1", -0.1, "超差"}, // 零限值器具负误差
	}
	for i, tc := range cases {
		number := fmt.Sprintf("C-%02d", i+1)
		calDate := fmt.Sprintf("2026-08-%02d", i+1)
		args := certVerdictArgs(path, tc.instrument, number, calDate, "2027-08-01", tc.measured)

		// 普通文字：超差也是有效录入，退出码 0，结论按本张证书显示。
		human := runArgs(t, args...)
		if human.code != 0 {
			t.Fatalf("%s 误差 %s 应录入成功，code=%d stderr=%s",
				tc.instrument, tc.measured, human.code, human.stderr)
		}
		if !strings.Contains(human.stdout, "已录入新证书") {
			t.Fatalf("%s 误差 %s 应为新证书录入：stdout=%q", tc.instrument, tc.measured, human.stdout)
		}
		if !strings.Contains(human.stdout, "判定："+tc.want) {
			t.Fatalf("%s 误差 %s 应判定 %s：stdout=%q",
				tc.instrument, tc.measured, tc.want, human.stdout)
		}
		other := "超差"
		if tc.want == "超差" {
			other = "合格"
		}
		if strings.Contains(human.stdout, "判定："+other) {
			t.Fatalf("%s 误差 %s 不得显示相反结论 %s：stdout=%q",
				tc.instrument, tc.measured, other, human.stdout)
		}

		// --json 对同一提交内容（幂等返回原证书）结论一致，证书字段对应本次提交。
		js := runArgs(t, append(args, "--json")...)
		if js.code != 0 {
			t.Fatalf("%s 误差 %s JSON 录入应成功，code=%d stderr=%s",
				tc.instrument, tc.measured, js.code, js.stderr)
		}
		got := parseCertResult(t, js.stdout)
		if !got.Accepted || got.Verdict != tc.want {
			t.Fatalf("%s 误差 %s JSON 结论应为 %s：%+v", tc.instrument, tc.measured, tc.want, got)
		}
		if got.Certificate.Number != number || got.Certificate.InstrumentID != tc.instrument {
			t.Fatalf("JSON 证书归属不符：%+v，期望 %s/%s", got.Certificate, number, tc.instrument)
		}
		if got.Certificate.Error != tc.wantErr {
			t.Fatalf("JSON 应保留带符号误差 %v，得到 %v", tc.wantErr, got.Certificate.Error)
		}
	}
}

// 同一测得误差落在允许误差不同的器具上，结论分别遵从各自限值，
// 不混用另一件器具的数据。
func TestCertVerdictFollowsOwnInstrumentLimit(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerWithAllowed(t, path, "M-WIDE", "0.5")
	registerWithAllowed(t, path, "M-NARROW", "0.2")

	wide := runArgs(t, certVerdictArgs(path, "M-WIDE", "C-W", "2026-08-01", "2027-08-01", "0.3")...)
	if wide.code != 0 || !strings.Contains(wide.stdout, "判定：合格") {
		t.Fatalf("0.3 对限值 0.5 的器具应合格：code=%d stdout=%q", wide.code, wide.stdout)
	}
	narrow := runArgs(t, certVerdictArgs(path, "M-NARROW", "C-N", "2026-08-01", "2027-08-01", "0.3")...)
	if narrow.code != 0 || !strings.Contains(narrow.stdout, "判定：超差") {
		t.Fatalf("0.3 对限值 0.2 的器具应超差：code=%d stdout=%q", narrow.code, narrow.stdout)
	}

	// 核对结果中两件器具各自的最近证书结论也分别遵从各自限值。
	for id, wantPass := range map[string]bool{"M-WIDE": true, "M-NARROW": false} {
		rv := runArgs(t, "review", "--id", id, "-f", path, "--json")
		if rv.code != 0 {
			t.Fatalf("review %s 失败：%s", id, rv.stderr)
		}
		view := parseReviewVerdict(t, rv.stdout)
		if view.Latest == nil || view.Latest.Pass != wantPass {
			t.Fatalf("%s 最近证书结论应为 pass=%v：%+v", id, wantPass, view.Latest)
		}
	}
}

// 证书结论不被器具的使用判断替换：待校准、停用器具录入合格证书仍显示
// 证书本身合格（器具能否使用是另一回事）；超差证书在停用器具上也照常
// 录入成功并保留原始带符号误差。
func TestCertVerdictNotReplacedByUsability(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerWithAllowed(t, path, "M-PEND", "0.5") // 新登记即为待校准
	registerWithAllowed(t, path, "M-STOP", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-STOP", "--status", "停用"); r.code != 0 {
		t.Fatalf("切换停用失败：%s", r.stderr)
	}

	// 待校准器具录入合格证书：结果显示证书合格，而非“不能使用”。
	pend := runArgs(t, certVerdictArgs(path, "M-PEND", "C-P", "2026-08-01", "2027-08-01", "0.1")...)
	if pend.code != 0 || !strings.Contains(pend.stdout, "判定：合格") {
		t.Fatalf("待校准器具的合格证书应显示合格：code=%d stdout=%q", pend.code, pend.stdout)
	}
	if strings.Contains(pend.stdout, "超差") || strings.Contains(pend.stdout, "不可以") {
		t.Fatalf("证书结论不得被器具使用判断替换：stdout=%q", pend.stdout)
	}

	// 停用器具录入合格证书：同样显示证书合格。
	stop := runArgs(t, certVerdictArgs(path, "M-STOP", "C-S1", "2026-08-01", "2027-08-01", "0.1")...)
	if stop.code != 0 || !strings.Contains(stop.stdout, "判定：合格") {
		t.Fatalf("停用器具的合格证书应显示合格：code=%d stdout=%q", stop.code, stop.stdout)
	}

	// 停用器具录入超差证书：仍是有效录入（退出码 0），明确显示超差，
	// JSON 保留原始带符号误差，不把超差当作参数错误丢掉证书。
	bad := runArgs(t, append(certVerdictArgs(path, "M-STOP", "C-S2", "2026-08-02", "2027-08-02", "-0.9"), "--json")...)
	if bad.code != 0 {
		t.Fatalf("超差证书应录入成功，code=%d stderr=%s", bad.code, bad.stderr)
	}
	got := parseCertResult(t, bad.stdout)
	if !got.Accepted || got.Verdict != "超差" || got.Certificate.Error != -0.9 {
		t.Fatalf("超差证书结论或带符号误差不符：%+v", got)
	}

	// 器具能否使用仍按既有规则判断，与证书结论各自独立。
	for id, canUse := range map[string]bool{"M-PEND": false, "M-STOP": false} {
		rv := runArgs(t, "review", "--id", id, "-f", path, "--json")
		view := parseReviewVerdict(t, rv.stdout)
		if view.CanUse != canUse {
			t.Fatalf("%s can_use 应为 %v：%+v", id, canUse, view)
		}
	}
	rv := runArgs(t, "review", "--id", "M-STOP", "-f", path, "--json")
	view := parseReviewVerdict(t, rv.stdout)
	if view.Latest == nil || view.Latest.Number != "C-S2" || view.Latest.Error != -0.9 || view.Latest.Pass {
		t.Fatalf("停用器具的超差证书应原样成为最近证书：%+v", view.Latest)
	}
}

// 补录场景：器具已有一张校准日期较新的超差证书，再补录一张校准日期较早、
// 合格且未到期的证书时——录入结果显示旧证书合格、历史增加；但最近证书仍
// 按校准日期取较新的超差证书，在用器具申请使用仍因最近证书超差被拒绝。
// 先后依据校准日期而非证书编号或录入顺序；补录不改写器具状态与已有证书。
func TestCertBackfillOlderPassingCertKeepsLatestVerdict(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerWithAllowed(t, path, "M-1", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败：%s", r.stderr)
	}

	// 较新的超差证书：编号小但校准日期新，用来证明先后依据校准日期。
	newer := runArgs(t, certVerdictArgs(path, "M-1", "C-001", "2026-09-01", "2027-09-01", "0.9")...)
	if newer.code != 0 || !strings.Contains(newer.stdout, "判定：超差") {
		t.Fatalf("较新超差证书应录入成功并显示超差：code=%d stdout=%q", newer.code, newer.stdout)
	}
	useBefore := runArgs(t, "use", "--id", "M-1", "-f", path)
	if useBefore.code != 1 || !strings.Contains(useBefore.stderr, "超差") {
		t.Fatalf("最近证书超差应拒绝使用：code=%d stderr=%q", useBefore.code, useBefore.stderr)
	}

	// 补录较早、合格且未到期的证书：编号大但校准日期早。
	olderArgs := certVerdictArgs(path, "M-1", "C-999", "2026-01-10", "2027-06-01", "0.1")
	human := runArgs(t, olderArgs...)
	if human.code != 0 {
		t.Fatalf("补录合格旧证书应成功，code=%d stderr=%s", human.code, human.stderr)
	}
	// 录入结果显示的是本次这张旧证书的合格结论，不被最近证书的超差覆盖。
	if !strings.Contains(human.stdout, "判定：合格") || strings.Contains(human.stdout, "超差") {
		t.Fatalf("补录结果应显示旧证书合格：stdout=%q", human.stdout)
	}
	js := runArgs(t, append(olderArgs, "--json")...)
	if js.code != 0 {
		t.Fatalf("补录 JSON 应成功，code=%d stderr=%s", js.code, js.stderr)
	}
	got := parseCertResult(t, js.stdout)
	if got.Verdict != "合格" || got.Certificate.Number != "C-999" ||
		got.Certificate.InstrumentID != "M-1" || got.Certificate.Error != 0.1 {
		t.Fatalf("补录 JSON 应表达旧证书合格：%+v", got)
	}

	// 历史中增加这张证书，且最近证书仍是校准日期较新的超差证书。
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if rv.code != 0 {
		t.Fatalf("review 失败：%s", rv.stderr)
	}
	view := parseReviewVerdict(t, rv.stdout)
	if len(view.History) != 2 {
		t.Fatalf("补录后历史应为两张证书：%+v", view.History)
	}
	if view.Latest == nil || view.Latest.Number != "C-001" || view.Latest.Pass {
		t.Fatalf("最近证书仍应是较新的超差证书 C-001：%+v", view.Latest)
	}
	byNumber := map[string]struct {
		CalDate string
		Error   float64
		Pass    bool
		Expired bool
	}{}
	for _, h := range view.History {
		byNumber[h.Number] = struct {
			CalDate string
			Error   float64
			Pass    bool
			Expired bool
		}{h.CalDate, h.Error, h.Pass, h.Expired}
	}
	old := byNumber["C-999"]
	if old.CalDate != "2026-01-10" || old.Error != 0.1 || !old.Pass || old.Expired {
		t.Fatalf("补录的旧证书应合格且未到期：%+v", old)
	}
	// 已有证书不被补录改写。
	newerView := byNumber["C-001"]
	if newerView.CalDate != "2026-09-01" || newerView.Error != 0.9 || newerView.Pass {
		t.Fatalf("已有的超差证书不应被补录改写：%+v", newerView)
	}
	// 器具状态不因补录改变，当前能否使用仍按最近证书判断。
	if view.Instrument.Status != "在用" {
		t.Fatalf("器具状态不应被补录改写：%s", view.Instrument.Status)
	}
	if view.CanUse {
		t.Fatalf("最近证书超差时补录合格旧证书不得解除使用限制：%+v", view)
	}
	joined := strings.Join(view.Reasons, "\n")
	if !strings.Contains(joined, "超差") || !strings.Contains(joined, "C-001") {
		t.Fatalf("核对原因应仍指向最近证书 C-001 超差：%v", view.Reasons)
	}

	// 在用器具申请使用仍因最近证书超差被拒绝：补录合格旧证书不解限。
	useAfter := runArgs(t, "use", "--id", "M-1", "-f", path)
	if useAfter.code != 1 || !strings.Contains(useAfter.stderr, "超差") {
		t.Fatalf("补录后申请使用仍应因最近证书超差被拒绝：code=%d stderr=%q",
			useAfter.code, useAfter.stderr)
	}
	if strings.Contains(useAfter.stdout, "允许使用") {
		t.Fatalf("补录合格旧证书不得让申请被允许：stdout=%q", useAfter.stdout)
	}
}
