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
//	证书当前是否有效，和它能否证明一次校准工作已经完成，是两件事。完成命令不得
//	仅因证书已到期而拒绝（截止日当天即到期，也不要求重新录入未到期证书）；完成只
//	结束计划，器具能否使用仍由状态与“最近证书”的结论和有效期决定。
//
// 命令行使用真实本机时钟，无法把计划建立时刻倒拨到过去，因此沿用既有做法：
// 预置一张含“较早建立的未完成计划”的台账，再通过真实命令录入证书并完成计划；
// 所有日期相对运行当天动态计算。

// expiredCertArgs 构造一条“当前已经到期”的合格证书录入参数。
func expiredCertArgs(path, number, calDate, expiry string) []string {
	return certFullArgs(path, "M-1", number, calDate, expiry, "0.1")
}

// assertExpiredCertCompleteSucceeds 普通输出与 --json 共用的成功断言：
// 到期证书完成计划退出 0，计划已完成并保留证书编号与本次完成时间；成功输出
// 不得把器具描述为已经可以使用。
func assertExpiredCertCompleteSucceeds(t *testing.T, path, plan, cert string) {
	t.Helper()
	human := runArgs(t, "complete", "-f", path, "--number", plan, "--certificate", cert)
	if human.code != 0 {
		t.Fatalf("已到期证书完成计划应退出 0，得到 %d，stderr=%s", human.code, human.stderr)
	}
	if !strings.Contains(human.stdout, "已完成：计划 "+plan) ||
		!strings.Contains(human.stdout, "证书 "+cert) ||
		!strings.Contains(human.stdout, "完成时间 ") {
		t.Fatalf("普通输出应保留计划、到期证书编号与完成时间：%q", human.stdout)
	}
	if strings.Contains(human.stdout, "可以使用") || strings.Contains(human.stderr, "可以使用") ||
		strings.Contains(human.stdout, "允许使用") || strings.Contains(human.stderr, "允许使用") {
		t.Fatalf("完成计划的输出不得暗示器具已经可以使用：stdout=%q stderr=%q",
			human.stdout, human.stderr)
	}
	// 同证书再次完成：幂等返回原结果，退出 0。
	js := runArgs(t, "complete", "-f", path, "--json", "--number", plan, "--certificate", cert)
	if js.code != 0 {
		t.Fatalf("--json 幂等完成应退出 0，得到 %d，stderr=%s", js.code, js.stderr)
	}
	var v completeJSONView
	if err := json.Unmarshal([]byte(js.stdout), &v); err != nil {
		t.Fatalf("complete JSON 解析失败: %v\n%s", err, js.stdout)
	}
	if !v.Accepted || !v.Idempotent || v.Plan == nil ||
		v.Plan.Status != calibrate.PlanStatusDone ||
		v.Plan.CertificateNumber != cert || v.Plan.CompletedAt == "" {
		t.Fatalf("--json 应显示已完成并保留到期证书 %s 与原完成时间：%+v", cert, v.Plan)
	}
	if strings.Contains(js.stdout, "可以使用") || strings.Contains(js.stdout, "允许使用") {
		t.Fatalf("--json 完成输出不得暗示器具可用：%s", js.stdout)
	}
}

// TestCLIExpiredLatestCertCompletesButUseStillRejected 主场景：器具在用，用于
// 完成计划的合格证书同时是最近证书且已到期。覆盖截止日当天已经到期与截止日已过：
// 两种情况下完成都成功，计划移出待办、核对仍可查到计划与证书；但核对仍判不能
// 使用，使用申请（普通与 --json）都因这张最近证书到期被拒绝（退出 1）并留痕。
func TestCLIExpiredLatestCertCompletesButUseStillRejected(t *testing.T) {
	cases := []struct {
		name   string
		expiry string
	}{
		{"截止日当天已经到期", daysAgo(t, 0)},
		{"截止日已过", daysAgo(t, 3)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := chdirTemp(t)
			path := filepath.Join(dir, "台账.json")
			// 计划建立于 60 天前；证书校准日期为 30 天前，不早于建立日期。
			seedOldPlanLedger(t, path, 60)

			calDate := daysAgo(t, 30)
			if r := runArgs(t, expiredCertArgs(path, "C-EXP", calDate, tc.expiry)...); r.code != 0 {
				t.Fatalf("到期证书本身应能正常录入：code=%d stderr=%s", r.code, r.stderr)
			}
			// 录入证书不自动完成计划。
			assertPlanOpenInCLI(t, path)

			// 用已到期证书完成计划：普通输出退出 0，--json 重复完成幂等退出 0。
			assertExpiredCertCompleteSucceeds(t, path, "PL-1", "C-EXP")
			if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
				t.Fatalf("完成后计划应从待办移除：%s", tv.stdout)
			}

			// 按器具核对：计划已完成并保留 C-EXP 与完成时间；最近证书就是这张
			// 合格但已到期的证书，器具不能使用，原因能对应到 C-EXP；完成动作
			// 不改动器具状态、允许误差与证书的校准日期、截止日、误差和结论。
			rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
			if rv.code != 0 {
				t.Fatalf("review 失败 code=%d stderr=%s", rv.code, rv.stderr)
			}
			var view struct {
				CanUse  bool     `json:"can_use"`
				Reasons []string `json:"reasons"`
				Latest  *struct {
					Number  string  `json:"number"`
					CalDate string  `json:"cal_date"`
					Expiry  string  `json:"expiry"`
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
			if view.CanUse || !containsStd(view.Reasons, "到期") || !containsStd(view.Reasons, "C-EXP") {
				t.Fatalf("完成后核对仍应因最近证书 C-EXP 到期判不可用：canUse=%v reasons=%v",
					view.CanUse, view.Reasons)
			}
			if containsStd(view.Reasons, "超差") {
				t.Fatalf("合格证书到期的原因中不应混入超差：%v", view.Reasons)
			}
			if view.Latest == nil || view.Latest.Number != "C-EXP" ||
				!view.Latest.Pass || !view.Latest.Expired ||
				view.Latest.CalDate != calDate || view.Latest.Expiry != tc.expiry ||
				view.Latest.Error != 0.1 {
				t.Fatalf("最近证书应为合格但已到期的 C-EXP 且字段不变：%+v", view.Latest)
			}
			if len(view.History) != 1 || view.History[0].Number != "C-EXP" ||
				!view.History[0].Expired || view.History[0].Expiry != tc.expiry {
				t.Fatalf("核对历史应仍能查到这张到期证书：%+v", view.History)
			}
			if view.Instrument.Status != "在用" || view.Instrument.AllowedError != 0.5 {
				t.Fatalf("完成动作不得更改器具状态或允许误差：%+v", view.Instrument)
			}
			if len(view.Plans) != 1 || view.Plans[0].Status != calibrate.PlanStatusDone ||
				view.Plans[0].CertificateNumber != "C-EXP" || view.Plans[0].CompletedAt == "" {
				t.Fatalf("核对应显示计划已完成并关联 C-EXP：%+v", view.Plans)
			}

			// 普通文字核对体现同一业务结果：不可用、到期最近证书、计划完成信息。
			hv := runArgs(t, "review", "--id", "M-1", "-f", path)
			for _, want := range []string{
				"当前能否使用：不可以",
				"最近证书：C-EXP",
				"已到期",
				"完成于 ",
				"证书 C-EXP",
			} {
				if !strings.Contains(hv.stdout, want) {
					t.Fatalf("普通核对缺少 %q：%s", want, hv.stdout)
				}
			}

			// 使用申请：普通输出与 --json 都退出 1，原因对应 C-EXP 到期；
			// 已保存的拒绝记录保持这一结果。
			u := runArgs(t, "use", "--id", "M-1", "-f", path)
			if u.code != 1 || !strings.Contains(u.stderr, "到期") ||
				!strings.Contains(u.stderr, "C-EXP") || strings.Contains(u.stderr, "超差") {
				t.Fatalf("普通使用申请应因 C-EXP 到期被拒绝：code=%d stderr=%q", u.code, u.stderr)
			}
			uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
			if uj.code != 1 || !strings.Contains(uj.stdout, `"allowed": false`) ||
				!strings.Contains(uj.stdout, "到期") || !strings.Contains(uj.stdout, "C-EXP") {
				t.Fatalf("--json 使用申请应同样因到期拒绝：code=%d stdout=%q", uj.code, uj.stdout)
			}
			final := mustReviewJSON(t, path, "M-1")
			if len(final.Rejections) != 2 {
				t.Fatalf("两次申请都应留下拒绝记录，得到 %d 条", len(final.Rejections))
			}
			for i, rec := range final.Rejections {
				if rec.Allowed || !containsStd(rec.Reasons, "到期") ||
					!containsStd(rec.Reasons, "C-EXP") {
					t.Fatalf("第 %d 条拒绝记录应保持因 C-EXP 到期而拒绝：%+v", i+1, rec)
				}
			}
		})
	}
}

// TestCLIExpiredOlderCertCompletesButNewerValidKeepsUsable 另一关键情况：用于完成
// 计划的是校准日期较早、已到期的合格证书，器具还有校准日期更近的合格未到期证书。
// 仍可用旧到期证书完成计划；最近证书继续是较新一张（与录入先后无关），器具保持
// 可用、申请获准，不能因为关联旧证书被判到期。
func TestCLIExpiredOlderCertCompletesButNewerValidKeepsUsable(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	// 计划建立于 90 天前：60 天前与 10 天前的证书都不早于建立日期。
	seedOldPlanLedger(t, path, 90)

	oldCal, oldExpiry := daysAgo(t, 60), daysAgo(t, 20)
	newCal, newExpiry := daysAgo(t, 10), daysAfter(t, 365)

	// 先录入校准日期较新的合格未到期证书，再事后补录较早、已到期的合格证书：
	// 录入先后不得改变按校准日期确定最近证书的规则。
	if r := runArgs(t, certFullArgs(path, "M-1", "C-NEW", newCal, newExpiry, "0.2")...); r.code != 0 {
		t.Fatalf("较新未到期证书录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, certFullArgs(path, "M-1", "C-OLD", oldCal, oldExpiry, "-0.1")...); r.code != 0 {
		t.Fatalf("较早的已到期证书应能正常录入 code=%d stderr=%s", r.code, r.stderr)
	}
	assertPlanOpenInCLI(t, path)

	// 用较早、已到期的 C-OLD 完成计划。
	assertExpiredCertCompleteSucceeds(t, path, "PL-1", "C-OLD")
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("完成后计划应移出待办：%s", tv.stdout)
	}

	rv := mustReviewJSON(t, path, "M-1")
	if !rv.CanUse || len(rv.Reasons) != 0 {
		t.Fatalf("最近证书合格未到期时关联旧到期证书不应限制使用：reasons=%v", rv.Reasons)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" ||
		!rv.Latest.Pass || rv.Latest.Expired ||
		rv.Latest.CalDate != newCal || rv.Latest.Error != 0.2 {
		t.Fatalf("最近证书应继续是较新的合格未到期 C-NEW：%+v", rv.Latest)
	}
	if len(rv.History) != 2 {
		t.Fatalf("历史应有两张证书，得到 %d", len(rv.History))
	}
	// mustReviewJSON 的历史按核对输出顺序（校准日期由近到远）。
	if rv.History[0].Number != "C-NEW" || rv.History[0].Expired || !rv.History[0].Pass {
		t.Fatalf("历史第一张应为未到期合格的 C-NEW：%+v", rv.History[0])
	}
	if rv.History[1].Number != "C-OLD" || !rv.History[1].Expired || !rv.History[1].Pass ||
		rv.History[1].Error != -0.1 {
		t.Fatalf("历史第二张应为合格但已到期、误差保持 -0.1 的 C-OLD：%+v", rv.History[1])
	}
	if rv.Instrument.Status != "在用" {
		t.Fatalf("完成动作不应更改器具状态，得到 %s", rv.Instrument.Status)
	}

	// 普通核对：器具可用、最近证书是未到期的 C-NEW，历史中的 C-OLD 如实标注已到期，
	// 计划显示由 C-OLD 完成。
	hv := runArgs(t, "review", "--id", "M-1", "-f", path)
	for _, want := range []string{
		"当前能否使用：可以",
		"最近证书：C-NEW",
		"C-OLD " + oldCal + " 截止 " + oldExpiry,
		"已到期",
		"完成于 ",
		"证书 C-OLD",
	} {
		if !strings.Contains(hv.stdout, want) {
			t.Fatalf("普通核对缺少 %q：%s", want, hv.stdout)
		}
	}
	if strings.Contains(hv.stdout, "不可以") {
		t.Fatalf("关联旧到期证书后不应判不可用：%s", hv.stdout)
	}

	// 使用申请只认最近证书：普通与 --json 均获准（退出 0），无拒绝记录。
	u := runArgs(t, "use", "--id", "M-1", "-f", path)
	if u.code != 0 || !strings.Contains(u.stdout, "允许使用器具 M-1") {
		t.Fatalf("较新证书合格未到期时应获准使用：code=%d stdout=%q stderr=%s",
			u.code, u.stdout, u.stderr)
	}
	uj := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if uj.code != 0 || !strings.Contains(uj.stdout, `"allowed": true`) {
		t.Fatalf("--json 使用申请应获准：code=%d stdout=%q", uj.code, uj.stdout)
	}
	if final := mustReviewJSON(t, path, "M-1"); len(final.Rejections) != 0 {
		t.Fatalf("获准使用不应留下拒绝记录，得到 %d 条", len(final.Rejections))
	}
}

// TestCLICompleteExpiredCertKeepsCalDateGate 命令行日期门槛对到期证书同样适用：
// 校准日期早于计划建立日一天的到期证书在普通输出与 --json 下都按业务拒绝退出 1，
// 计划继续未完成、留在待办，无完成时间与关联证书；校准日期恰好等于建立日期的
// 到期证书（截止日即运行当天）可以完成，退出 0。
func TestCLICompleteExpiredCertKeepsCalDateGate(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	// 计划建立于 30 天前。
	seedOldPlanLedger(t, path, 30)

	// 校准日期 31 天前（早一天），截止日 2 天前（已到期）：证书本身可录入。
	if r := runArgs(t, expiredCertArgs(path, "C-TOO-EARLY", daysAgo(t, 31), daysAgo(t, 2))...); r.code != 0 {
		t.Fatalf("早一天且已到期的证书应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	human := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-TOO-EARLY")
	if human.code != 1 {
		t.Fatalf("校准日期早于建立日期应业务拒绝退出 1，得到 %d", human.code)
	}
	if !strings.Contains(human.stderr, "已拒绝") ||
		!strings.Contains(human.stderr, "早于") ||
		!strings.Contains(human.stderr, "建立日期") {
		t.Fatalf("普通输出应按原有日期门槛明确拒绝：stderr=%q", human.stderr)
	}
	if strings.Contains(human.stderr, "到期") || strings.Contains(human.stderr, "有效期") {
		t.Fatalf("拒绝理由不应新增证书有效期规则：stderr=%q", human.stderr)
	}
	js := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-TOO-EARLY")
	if js.code != 1 || !strings.Contains(js.stdout, `"accepted": false`) {
		t.Fatalf("--json 同样应退出 1 且 accepted=false，code=%d stdout=%q", js.code, js.stdout)
	}
	assertPlanOpenInCLI(t, path)

	// 校准日期恰好等于建立日期（30 天前）、截止日为运行当天（截止日当天即到期）：
	// 仍可完成。
	if r := runArgs(t, expiredCertArgs(path, "C-ON-DAY", daysAgo(t, 30), daysAgo(t, 0))...); r.code != 0 {
		t.Fatalf("建立当天校准、当天到期的证书应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	ok := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-ON-DAY")
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成：计划 PL-1，证书 C-ON-DAY") {
		t.Fatalf("校准日期等于建立日期的到期证书应完成成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("边界当天完成后应移出待办：%s", tv.stdout)
	}
}
