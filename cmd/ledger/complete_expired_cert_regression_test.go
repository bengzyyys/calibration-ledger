package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件从命令行端到端守住“用已到期证书完成校准计划”：
//
//	证书是否已到期，与它能否证明一次校准工作已完成，是两件不同的事。只要证书
//	属于计划中的器具、校准日期不早于计划最初建立的本机日期、且未用于完成其他
//	计划，已到期（截止日当天或截止日已过）也必须允许完成。完成只结束计划；
//	完成后的使用判断仍按器具状态与最近证书（按校准日期确定）执行。
//
// 命令行使用真实本机时钟，无法把计划建立时刻倒拨到过去，因此预置含“较早建立
// 的未完成计划”的台账文件，证书日期相对运行当天动态计算：校准日期在过去、
// 截止日取今天（截止日当天即到期）或过去几天（截止日已过）。

// expiredCompleteReview 是 review --json 中本组测试关心的字段。
type expiredCompleteReview struct {
	CanUse     bool     `json:"can_use"`
	Reasons    []string `json:"reasons"`
	Instrument struct {
		Status       string  `json:"status"`
		AllowedError float64 `json:"allowed_error"`
	} `json:"instrument"`
	Latest *struct {
		Number  string  `json:"number"`
		CalDate string  `json:"cal_date"`
		Expiry  string  `json:"expiry"`
		Error   float64 `json:"error"`
		Pass    bool    `json:"pass"`
		Expired bool    `json:"expired"`
	} `json:"latest"`
	History []struct {
		Number  string `json:"number"`
		Expired bool   `json:"expired"`
	} `json:"history"`
	Plans []struct {
		Status            string `json:"status"`
		CertificateNumber string `json:"certificate_number"`
		CompletedAt       string `json:"completed_at"`
	} `json:"plans"`
	Rejections []struct {
		Allowed bool     `json:"allowed"`
		Reasons []string `json:"reasons"`
	} `json:"rejections"`
}

func mustReviewExpired(t *testing.T, path string) expiredCompleteReview {
	t.Helper()
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if rv.code != 0 {
		t.Fatalf("review 失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	var v expiredCompleteReview
	if err := json.Unmarshal([]byte(rv.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, rv.stdout)
	}
	return v
}

// TestCLIExpiredCertCompletesPlanAndUseStillDenied 命令行主场景：器具在用，
// 用于完成计划的合格证书就是最近证书且已经到期（分别覆盖截止日当天与截止日
// 已过）。完成必须成功（退出 0），计划保留证书编号与完成时间并移出待办；
// 核对仍显示不能使用，使用申请因这张最近证书到期被拒绝（退出 1）并留痕；
// 完成动作不改动器具状态、允许误差与证书字段。
func TestCLIExpiredCertCompletesPlanAndUseStillDenied(t *testing.T) {
	cases := []struct {
		name         string
		expiryOffset int // 0 = 截止日当天（当天起即到期）；负数 = 截止日已过
	}{
		{"截止日当天已到期", 0},
		{"截止日已过", -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := chdirTemp(t)
			path := filepath.Join(dir, "台账.json")
			// 计划建立于 60 天前：30 天前校准的证书不早于建立日期。
			seedOldPlanLedger(t, path, 60)

			calDate := daysAgo(t, 30)
			expiry := daysAfter(t, tc.expiryOffset)
			args := certFullArgs(path, "M-1", "C-EXP", calDate, expiry, "0.1")
			if r := runArgs(t, args...); r.code != 0 ||
				!strings.Contains(r.stdout, "判定：合格") {
				t.Fatalf("证书应录入成功并判合格：code=%d stdout=%q stderr=%s",
					r.code, r.stdout, r.stderr)
			}
			// 仅录入证书不会自动完成计划。
			assertPlanOpenInCLI(t, path)

			// 已到期证书完成计划：普通输出与 --json 同为成功（退出 0）。
			assertCompleteSuccess(t, path, "PL-1", "C-EXP")

			// 完成后计划移出待办。
			if tv := runArgs(t, "todos", "-f", path, "--json"); tv.code != 0 ||
				!strings.Contains(tv.stdout, `"count": 0`) {
				t.Fatalf("完成后计划应从待办移除：code=%d stdout=%q", tv.code, tv.stdout)
			}

			// 按器具核对：计划已完成并关联 C-EXP；最近证书 C-EXP 已到期，
			// 器具不可用且原因对应到这张证书；器具状态、允许误差与证书的
			// 校准日期、截止日、测得误差都不被完成动作改动。
			view := mustReviewExpired(t, path)
			if view.CanUse || !containsStd(view.Reasons, "C-EXP") ||
				!containsStd(view.Reasons, "到期") {
				t.Fatalf("核对应因最近证书 C-EXP 到期判不可用：canUse=%v reasons=%v",
					view.CanUse, view.Reasons)
			}
			if containsStd(view.Reasons, "超差") {
				t.Fatalf("证书合格，到期拒绝不应混入超差：%v", view.Reasons)
			}
			if view.Latest == nil || view.Latest.Number != "C-EXP" ||
				!view.Latest.Expired || !view.Latest.Pass {
				t.Fatalf("最近证书应是已到期但合格的 C-EXP：%+v", view.Latest)
			}
			if view.Latest.CalDate != calDate || view.Latest.Expiry != expiry ||
				view.Latest.Error != 0.1 {
				t.Fatalf("完成动作不得改动证书的校准日期、截止日与测得误差：%+v", view.Latest)
			}
			if view.Instrument.Status != "在用" || view.Instrument.AllowedError != 0.5 {
				t.Fatalf("完成动作不得更改器具状态或允许误差：%+v", view.Instrument)
			}
			if len(view.Plans) != 1 || view.Plans[0].Status != calibrate.PlanStatusDone ||
				view.Plans[0].CertificateNumber != "C-EXP" ||
				view.Plans[0].CompletedAt == "" {
				t.Fatalf("核对应显示计划已完成并保留 C-EXP 与完成时间：%+v", view.Plans)
			}

			// 普通文字核对体现同一业务结果。
			hv := runArgs(t, "review", "--id", "M-1", "-f", path)
			if hv.code != 0 || !strings.Contains(hv.stdout, "最近证书：C-EXP") ||
				!strings.Contains(hv.stdout, "已到期") ||
				!strings.Contains(hv.stdout, "当前能否使用：不可以") ||
				!strings.Contains(hv.stdout, "证书 C-EXP") {
				t.Fatalf("普通核对应展示已到期的最近证书、不可用与计划关联证书：code=%d stdout=%q",
					hv.code, hv.stdout)
			}

			// 使用申请：普通输出与 --json 都被拒绝（退出 1），原因对应 C-EXP 到期。
			u := runArgs(t, "use", "--id", "M-1", "-f", path)
			if u.code != 1 || !strings.Contains(u.stderr, "C-EXP") ||
				!strings.Contains(u.stderr, "到期") {
				t.Fatalf("使用申请应因 C-EXP 到期被拒绝：code=%d stderr=%q", u.code, u.stderr)
			}
			uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
			if uj.code != 1 || !strings.Contains(uj.stdout, `"allowed": false`) ||
				!strings.Contains(uj.stdout, "C-EXP") {
				t.Fatalf("--json 使用申请应同业务结果拒绝：code=%d stdout=%q", uj.code, uj.stdout)
			}
			// 两次拒绝都按 C-EXP 到期留痕，已保存的拒绝记录保持这个结果。
			after := mustReviewExpired(t, path)
			if len(after.Rejections) != 2 {
				t.Fatalf("两次拒绝都应留痕，得到 %d 条", len(after.Rejections))
			}
			for _, rej := range after.Rejections {
				if rej.Allowed || !containsStd(rej.Reasons, "C-EXP") ||
					!containsStd(rej.Reasons, "到期") {
					t.Fatalf("拒绝记录应保持 C-EXP 到期的原因：%+v", rej)
				}
			}
		})
	}
}

// TestCLIOlderExpiredCertCompletesNewerValidKeepsUsable 反向情形：用于完成计划
// 的是较早的已到期证书，器具另有校准日期更近的合格未到期证书。仍可完成计划；
// 最近证书继续是较新的一张，器具保持可用，不因关联了旧证书而被判到期。
func TestCLIOlderExpiredCertCompletesNewerValidKeepsUsable(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	seedOldPlanLedger(t, path, 60)

	// 先录入较新（10 天前校准、一年后截止）的合格证书，再补录较早
	// （30 天前校准、5 天前已截止）的合格证书：录入先后不改变最近证书规则。
	if r := runArgs(t, certFullArgs(path, "M-1", "C-NEW",
		daysAgo(t, 10), daysAfter(t, 365), "0.1")...); r.code != 0 {
		t.Fatalf("较新合格证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, certFullArgs(path, "M-1", "C-OLD",
		daysAgo(t, 30), daysAgo(t, 5), "0.2")...); r.code != 0 {
		t.Fatalf("较早已到期证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	assertPlanOpenInCLI(t, path)

	// 用较早的已到期证书完成计划：成功（退出 0）。
	assertCompleteSuccess(t, path, "PL-1", "C-OLD")

	// 核对：最近证书仍是较新的合格未到期 C-NEW，器具可用；计划关联 C-OLD。
	view := mustReviewExpired(t, path)
	if !view.CanUse || len(view.Reasons) != 0 {
		t.Fatalf("最近证书合格未到期时应可使用，canUse=%v reasons=%v",
			view.CanUse, view.Reasons)
	}
	if view.Latest == nil || view.Latest.Number != "C-NEW" || !view.Latest.Pass ||
		view.Latest.Expired {
		t.Fatalf("最近证书应仍是较新的合格未到期 C-NEW：%+v", view.Latest)
	}
	if len(view.History) != 2 || view.History[0].Number != "C-NEW" ||
		view.History[1].Number != "C-OLD" || !view.History[1].Expired {
		t.Fatalf("历史应由近到远为 C-NEW、已到期 C-OLD：%+v", view.History)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != calibrate.PlanStatusDone ||
		view.Plans[0].CertificateNumber != "C-OLD" {
		t.Fatalf("计划应显示已完成并关联 C-OLD：%+v", view.Plans)
	}
	if view.Instrument.Status != "在用" {
		t.Fatalf("完成动作不应更改器具状态，得到 %s", view.Instrument.Status)
	}

	// 使用申请获准（退出 0），不因计划关联了已到期的旧证书而被拒。
	u := runArgs(t, "use", "--id", "M-1", "-f", path)
	if u.code != 0 || !strings.Contains(u.stdout, "允许使用器具 M-1") {
		t.Fatalf("应获准使用：code=%d stdout=%q stderr=%s", u.code, u.stdout, u.stderr)
	}
	uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if uj.code != 0 || !strings.Contains(uj.stdout, `"allowed": true`) {
		t.Fatalf("--json 使用申请应获准：code=%d stdout=%q", uj.code, uj.stdout)
	}
}

// TestCLIExpiredCertDateBoundary 命令行日期门槛：计划建立于 30 天前，两张证书
// 都已到期（截止日 10 天前）。校准日期早一天（31 天前）的证书在普通输出与
// --json 下都按业务拒绝退出 1，计划继续未完成、留在待办，不写入完成时间或
// 关联证书；校准日期恰好等于建立日期（30 天前）的证书可以完成，退出 0。
func TestCLIExpiredCertDateBoundary(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	seedOldPlanLedger(t, path, 30)

	// 校准日期早于计划建立日期一天的已到期证书：明确拒绝。
	if r := runArgs(t, certFullArgs(path, "M-1", "C-TOO-EARLY",
		daysAgo(t, 31), daysAgo(t, 10), "0.1")...); r.code != 0 {
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

	// 校准日期恰好等于计划建立日期的已到期证书：可以完成。
	if r := runArgs(t, certFullArgs(path, "M-1", "C-ON-DAY",
		daysAgo(t, 30), daysAgo(t, 10), "0.1")...); r.code != 0 {
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
