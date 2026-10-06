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

// 本文件从命令行黑盒为“录入证书”的同号判定补充自动化回归保障，守住一条
// 业务事实：同一台账中，同一个证书编号只能对应一份已经保存的证书。
//
// 普通输出与 --json 输出必须保持现有含义且彼此一致：
//
//   - 同号同内容：退出码 0，普通输出明确“返回原证书、未增加历史”，JSON 中
//     accepted=true、duplicate=true，并返回原证书（录入时间取首次保存值）；
//   - 同号不同内容：退出码 1，普通输出以“已拒绝”说明编号冲突，JSON 中
//     accepted=false、conflict=true，且不返回录入成功的证书。冲突不能被
//     降级成普通输入无效（那种拒绝 conflict=false），也不能悄悄替换原证书。
//
// 日期相对本机今天构造，不依赖任何固定日期恰好仍在未来或已过去。

// sameNumberCertArgs 构造一条可指定全部字段的证书录入命令，便于只改一个
// 字段重交同号证书。
func sameNumberCertArgs(path, instrument, number, calDate, expiry, method, errVal, summary string) []string {
	return []string{"cert", "-f", path,
		"--instrument", instrument, "--number", number,
		"--cal-date", calDate, "--expiry", expiry,
		"--method", method, "--error", errVal, "--summary", summary}
}

// persistedCert 是台账文件、cert 成功响应与 review 视图里证书共用的字段。
type persistedCert struct {
	Number       string  `json:"number"`
	InstrumentID string  `json:"instrument_id"`
	CalDate      string  `json:"cal_date"`
	Expiry       string  `json:"expiry"`
	Method       string  `json:"method"`
	Error        float64 `json:"error"`
	Summary      string  `json:"summary"`
	CreatedAt    string  `json:"created_at"`
}

// readPersistedCerts 直接读台账文件中已保存的全部证书（按文件原顺序）。
func readPersistedCerts(t *testing.T, path string) []persistedCert {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data struct {
		Certificates []persistedCert `json:"certificates"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	return data.Certificates
}

// rewritePersistedCertField 直接改写台账文件中某张证书的单个字段，用来在
// 黑盒测试里模拟“首次保存发生在更早时间”这类无法靠等待得到的状态，其余
// 历史原样保留。
func rewritePersistedCertField(t *testing.T, path, number, field string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	certs, _ := data["certificates"].([]any)
	found := false
	for _, item := range certs {
		c, _ := item.(map[string]any)
		if c["number"] == number {
			c[field] = value
			found = true
		}
	}
	if !found {
		t.Fatalf("台账中找不到证书 %s，无法改写字段 %s", number, field)
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化台账: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("写回台账 %s: %v", path, err)
	}
}

// certCLIResponse 是 cert 命令 --json 输出的最小解析：成功时有 certificate，
// 冲突/校验拒绝时 accepted=false 且没有 certificate。
type certCLIResponse struct {
	Accepted    bool           `json:"accepted"`
	Duplicate   bool           `json:"duplicate"`
	Conflict    bool           `json:"conflict"`
	Verdict     string         `json:"verdict"`
	Error       string         `json:"error"`
	Certificate *persistedCert `json:"certificate"`
}

func runCertCLIJSON(t *testing.T, args []string) (result, certCLIResponse) {
	t.Helper()
	r := runArgs(t, append(append([]string{}, args...), "--json")...)
	var resp certCLIResponse
	if r.code == 0 || strings.Contains(r.stdout, "accepted") {
		if err := json.Unmarshal([]byte(r.stdout), &resp); err != nil {
			t.Fatalf("cert JSON 解析失败: %v\n%s", err, r.stdout)
		}
	}
	return r, resp
}

// reviewCertView 在证书字段之外带当前日期下的判定。
type reviewCertView struct {
	persistedCert
	Pass    bool `json:"pass"`
	Expired bool `json:"expired"`
}

type sameNumberReview struct {
	CanUse  bool             `json:"can_use"`
	Reasons []string         `json:"reasons"`
	Latest  *reviewCertView  `json:"latest"`
	History []reviewCertView `json:"history"`
}

func reviewSameNumberJSON(t *testing.T, path, id string) sameNumberReview {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var v sameNumberReview
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, r.stdout)
	}
	return v
}

// TestCLISameNumberSameContentReturnsOriginalAndKeepsFirstSaveTime 是命令行
// 幂等主场景：首次录入成功后，在另一个时间提交相同编号与相同业务内容，
// 普通输出与 JSON 都退出 0、明确重复并返回原证书；历史仍是一张，录入时间
// 保留第一次成功保存的值（这里把台账中的录入时间拨到更早来确定性地模拟
// “两次提交发生在不同时间”），不会生成新证书。
func TestCLISameNumberSameContentReturnsOriginalAndKeepsFirstSaveTime(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	cal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	future := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	args := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.1", "摘要")

	first := runArgs(t, args...)
	if first.code != 0 || !strings.Contains(first.stdout, "已录入新证书") {
		t.Fatalf("首次应作为新证书录入成功：code=%d stdout=%q stderr=%s",
			first.code, first.stdout, first.stderr)
	}

	// 确定性模拟“首次成功保存发生在更早时间”：把录入时间拨到 2020 年。
	const firstSave = "2020-01-01T08:00:00Z"
	rewritePersistedCertField(t, path, "C-1", "created_at", firstSave)

	// 普通输出重交：退出 0，明确重复、不增加历史，且不是“已录入新证书”。
	human := runArgs(t, args...)
	if human.code != 0 {
		t.Fatalf("同号同内容应退出 0，得到 %d（stderr=%s）", human.code, human.stderr)
	}
	if !strings.Contains(human.stdout, "同号证书且内容一致，返回原证书，未增加历史记录") {
		t.Fatalf("普通输出应明确返回原证书且未增加历史：%q", human.stdout)
	}
	if strings.Contains(human.stdout, "已录入新证书") {
		t.Fatalf("重复提交不能被说成录入新证书：%q", human.stdout)
	}
	if !strings.Contains(human.stdout, "C-1") || !strings.Contains(human.stdout, "判定：合格") {
		t.Fatalf("普通输出应展示原证书编号与结论：%q", human.stdout)
	}

	// JSON 重交：accepted=true、duplicate=true，返回原证书，录入时间不被刷新。
	js, resp := runCertCLIJSON(t, args)
	if js.code != 0 {
		t.Fatalf("JSON 同号同内容应退出 0，得到 %d（stderr=%s）", js.code, js.stderr)
	}
	if !resp.Accepted || !resp.Duplicate || resp.Conflict {
		t.Fatalf("JSON 应 accepted=true duplicate=true conflict=false，得到 %+v", resp)
	}
	if resp.Certificate == nil {
		t.Fatal("重复提交应返回原证书内容")
	}
	want := persistedCert{
		Number: "C-1", InstrumentID: "M-1", CalDate: cal, Expiry: future,
		Method: "规范A", Error: 0.1, Summary: "摘要", CreatedAt: firstSave,
	}
	if *resp.Certificate != want {
		t.Fatalf("JSON 应返回原证书全部内容且录入时间不变：got=%+v want=%+v",
			*resp.Certificate, want)
	}

	// 历史仍只有原来那一张，录入时间保留首次值，最近证书也是它。
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 {
		t.Fatalf("重复提交不能生成新证书，台账中有 %d 张：%+v", len(certs), certs)
	}
	if certs[0] != want {
		t.Fatalf("台账中的原证书被改动：got=%+v want=%+v", certs[0], want)
	}
	rv := reviewSameNumberJSON(t, path, "M-1")
	if len(rv.History) != 1 || rv.Latest == nil ||
		rv.Latest.Number != "C-1" || rv.Latest.CreatedAt != firstSave {
		t.Fatalf("核对中历史应只有一张且最近证书录入时间不变：%+v", rv)
	}
}

// TestCLISameNumberWhitespaceAndNumberFormsNormalized 保护“相同内容”的
// 归一化边界：器具编号、证书编号、日期、方法、摘要前后的空白沿用现有去除
// 规则，误差换一种数字写法（1e-1 即 0.1）仍是同一数值——这些重交都算
// 重复；但方法、摘要内部的文字（空白）变化不能被去除空白抹平，必须冲突。
func TestCLISameNumberWhitespaceAndNumberFormsNormalized(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	cal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	future := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	base := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.1", "摘要")
	if r := runArgs(t, base...); r.code != 0 {
		t.Fatalf("首次录入失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 前后空白 + 误差科学计数法：归一化后与原内容一致，按重复返回。
	padded := sameNumberCertArgs(path, "  M-1  ", "\tC-1\t", " "+cal+" ",
		"  "+future+"  ", " 规范A\t", "1e-1", "摘要\n")
	r, resp := runCertCLIJSON(t, padded)
	if r.code != 0 || !resp.Accepted || !resp.Duplicate {
		t.Fatalf("前后空白被去除、误差数值相同应判重复：code=%d resp=%+v stderr=%s",
			r.code, resp, r.stderr)
	}
	if resp.Certificate == nil || resp.Certificate.Method != "规范A" ||
		resp.Certificate.Summary != "摘要" || resp.Certificate.Error != 0.1 ||
		resp.Certificate.InstrumentID != "M-1" || resp.Certificate.CalDate != cal {
		t.Fatalf("归一化后应返回原证书内容：%+v", resp.Certificate)
	}
	if n := len(readPersistedCerts(t, path)); n != 1 {
		t.Fatalf("归一化重复提交不应增加证书，得到 %d 张", n)
	}

	// 方法内部空白变化：是不同的方法文字，必须冲突而非重复。
	methodInside := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范 A", "0.1", "摘要")
	if r := runArgs(t, methodInside...); r.code != 1 ||
		!strings.Contains(r.stderr, "已拒绝") || !strings.Contains(r.stderr, "内容不同") {
		t.Fatalf("方法内部文字变化应冲突退出 1：code=%d stderr=%q", r.code, r.stderr)
	}
	// 摘要内部空白变化同理。
	summaryInside := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.1", "摘 要")
	jr, jresp := runCertCLIJSON(t, summaryInside)
	if jr.code != 1 || jresp.Accepted || !jresp.Conflict {
		t.Fatalf("摘要内部文字变化应冲突：code=%d resp=%+v", jr.code, jresp)
	}
	if jresp.Certificate != nil {
		t.Fatalf("冲突不能返回录入成功的证书：%+v", jresp.Certificate)
	}

	// 两次内部变化后原方法、原摘要与证书数量都不变。
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 || certs[0].Method != "规范A" || certs[0].Summary != "摘要" {
		t.Fatalf("内部空白冲突不得改动原证书：%+v", certs)
	}
}

// TestCLISameNumberEachFieldChangeReportedConflict 逐字段保护比较范围：
// 所属器具、校准日期、有效期截止日、校准方法、测得误差、摘要各自单独换成
// 本身合法的内容，普通输出与 JSON 都必须报编号冲突（退出 1、conflict=true），
// 而不是成功或普通输入无效；原证书内容、历史数量、最近证书不变，挂到
// 另一件器具时目标器具也不能多出这张证书。
func TestCLISameNumberEachFieldChangeReportedConflict(t *testing.T) {
	now := time.Now()
	cal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	calOther := now.AddDate(0, 0, -45).Format(calibrate.DateLayout)
	future := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	futureOther := now.AddDate(2, 0, 0).Format(calibrate.DateLayout)

	cases := []struct {
		name  string
		build func(path string) []string
	}{
		{"所属器具换成另一件已登记器具",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-2", "C-1", cal, future, "规范A", "0.1", "摘要")
			}},
		{"校准日期换成真实且不晚于今天的日期",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-1", "C-1", calOther, future, "规范A", "0.1", "摘要")
			}},
		{"有效期截止日换成更晚的真实日期",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-1", "C-1", cal, futureOther, "规范A", "0.1", "摘要")
			}},
		{"校准方法换成另一个非空方法",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-1", "C-1", cal, future, "规范B", "0.1", "摘要")
			}},
		{"测得误差换成另一个有限数值",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-1", "C-1", cal, future, "规范A", "0.2", "摘要")
			}},
		{"摘要换成另一段非空摘要",
			func(p string) []string {
				return sameNumberCertArgs(p, "M-1", "C-1", cal, future, "规范A", "0.1", "另一份摘要")
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := chdirTemp(t)
			path := filepath.Join(dir, "台账.json")
			registerAllowed(t, path, "M-1", "0.5")
			registerAllowed(t, path, "M-2", "1")

			base := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.1", "摘要")
			if r := runArgs(t, base...); r.code != 0 {
				t.Fatalf("首次录入失败 code=%d stderr=%s", r.code, r.stderr)
			}
			want := readPersistedCerts(t, path)[0]
			changed := tc.build(path)

			// 普通输出：退出 1，以“已拒绝”说明编号冲突。
			human := runArgs(t, changed...)
			if human.code != 1 {
				t.Fatalf("单字段变化应退出 1，得到 %d", human.code)
			}
			if !strings.Contains(human.stderr, "已拒绝") ||
				!strings.Contains(human.stderr, "C-1") ||
				!strings.Contains(human.stderr, "内容不同") {
				t.Fatalf("普通输出应说明证书 C-1 编号冲突：%q", human.stderr)
			}

			// JSON：退出 1、accepted=false、conflict=true，且不返回证书。
			jr, jresp := runCertCLIJSON(t, changed)
			if jr.code != 1 {
				t.Fatalf("JSON 单字段变化应退出 1，得到 %d", jr.code)
			}
			if jresp.Accepted || !jresp.Conflict {
				t.Fatalf("JSON 应明确未接受且属于冲突（区别于普通输入无效）：%+v", jresp)
			}
			if strings.Contains(jr.stdout, `"certificate"`) {
				t.Fatalf("冲突时不得返回录入成功的证书：%s", jr.stdout)
			}

			// 原证书全部内容与数量不变。
			certs := readPersistedCerts(t, path)
			if len(certs) != 1 || certs[0] != want {
				t.Fatalf("冲突提交不得改动原证书或增加记录：got=%+v want=%+v",
					certs, want)
			}
			rv := reviewSameNumberJSON(t, path, "M-1")
			if len(rv.History) != 1 || rv.Latest == nil ||
				rv.Latest.Number != "C-1" || rv.Latest.Error != 0.1 ||
				rv.Latest.Method != "规范A" || rv.Latest.Summary != "摘要" ||
				rv.Latest.CalDate != cal || rv.Latest.Expiry != future {
				t.Fatalf("冲突后 M-1 的历史与最近证书应与提交前一致：%+v", rv)
			}
			// 目标器具 M-2 不能多出这张同号证书。
			target := reviewSameNumberJSON(t, path, "M-2")
			if len(target.History) != 0 || target.Latest != nil {
				t.Fatalf("目标器具 M-2 不能多出同号证书：%+v", target)
			}
		})
	}
}

// TestCLISameNumberSignedErrorDifferenceConflict 专门保护误差符号：-0.4 与
// +0.4 绝对值相同、在允许误差 0.5 下结论同为合格，但数值不同仍属冲突；
// 只有逐字相同的 -0.4 重交才算重复。
func TestCLISameNumberSignedErrorDifferenceConflict(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")

	now := time.Now()
	cal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	future := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	neg := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "-0.4", "摘要")
	if r := runArgs(t, neg...); r.code != 0 ||
		!strings.Contains(r.stdout, "判定：合格") {
		t.Fatalf("-0.4 证书应录入成功且合格：code=%d %q %s", r.code, r.stdout, r.stderr)
	}

	// 反号误差：普通与 JSON 都必须冲突。
	pos := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.4", "摘要")
	human := runArgs(t, pos...)
	if human.code != 1 || !strings.Contains(human.stderr, "内容不同") {
		t.Fatalf("反号误差应冲突退出 1：code=%d stderr=%q", human.code, human.stderr)
	}
	jr, jresp := runCertCLIJSON(t, pos)
	if jr.code != 1 || jresp.Accepted || !jresp.Conflict || jresp.Certificate != nil {
		t.Fatalf("反号误差 JSON 应冲突且不返回证书：code=%d resp=%+v", jr.code, jresp)
	}
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 || certs[0].Error != -0.4 {
		t.Fatalf("原误差符号必须保留为 -0.4：%+v", certs)
	}

	// 控制对照：相同的 -0.4 重交仍是幂等重复。
	again, dup := runCertCLIJSON(t, neg)
	if again.code != 0 || !dup.Accepted || !dup.Duplicate ||
		dup.Certificate == nil || dup.Certificate.Error != -0.4 {
		t.Fatalf("相同 -0.4 重交应幂等返回原证书：code=%d resp=%+v", again.code, dup)
	}
	if n := len(readPersistedCerts(t, path)); n != 1 {
		t.Fatalf("整轮操作后应仍只有 1 张证书，得到 %d 张", n)
	}
}

// rollbackTomorrowDates 以真实运行当天为基准给出“明天”与“明年明天”的
// 日历日期：把已保存证书的校准日期/截止日改到这两个日期，就确定性地模拟
// 出“本机日期从校准当天回落到校准日期之前”的状态，且不依赖任何固定日期。
func rollbackTomorrowDates(t *testing.T) (string, string) {
	t.Helper()
	now := time.Now()
	return now.AddDate(0, 0, 1).Format(calibrate.DateLayout),
		now.AddDate(1, 0, 1).Format(calibrate.DateLayout)
}

// seedCertThenRollBackClock 在当天合法录入 C-1（校准日期为运行当天），再把
// 台账中 C-1 的校准日期与截止日改写为明天/明年明天，模拟“首次保存发生在
// 校准当天，之后本机日历日期回到校准日期之前”。返回模拟后的日期与首次
// 保存时的证书（录入时间保持首次保存值）。
func seedCertThenRollBackClock(t *testing.T, path string) (cal, expiry string, first persistedCert) {
	t.Helper()
	now := time.Now()
	todayCal := now.Format(calibrate.DateLayout)
	todayExpiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	args := sameNumberCertArgs(path, "M-1", "C-1", todayCal, todayExpiry, "规范A", "0.1", "摘要")
	if r := runArgs(t, args...); r.code != 0 {
		t.Fatalf("首次录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	cal, expiry = rollbackTomorrowDates(t)
	rewritePersistedCertField(t, path, "C-1", "cal_date", cal)
	rewritePersistedCertField(t, path, "C-1", "expiry", expiry)
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 {
		t.Fatalf("前置条件失效：改写后应只有 1 张证书，得到 %+v", certs)
	}
	return cal, expiry, certs[0]
}

// readInstrumentStatus 直接读台账中某器具保存的状态，用来核对证书提交没有
// 顺带切换器具状态。
func readInstrumentStatus(t *testing.T, path, id string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data struct {
		Instruments []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"instruments"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	for _, in := range data.Instruments {
		if in.ID == id {
			return in.Status
		}
	}
	t.Fatalf("台账中找不到器具 %s", id)
	return ""
}

// TestCLISameNumberAfterClockRollbackSameContentReturnsOriginal 从命令行守住
// 主场景：首次合法保存后本机日历日期回到校准日期之前，原样提交同号同内容
// 仍退出 0、明确返回原证书且未增加历史；JSON 保留 duplicate=true 与原证书
// 内容（含首次录入时间）。不切换器具状态、不新增使用申请，核对中历史与
// 最近证书仍是原来那一张。
func TestCLISameNumberAfterClockRollbackSameContentReturnsOriginal(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	cal, expiry, first := seedCertThenRollBackClock(t, path)
	args := sameNumberCertArgs(path, "M-1", "C-1", cal, expiry, "规范A", "0.1", "摘要")

	// 普通输出：退出 0，明确重复、返回原证书，而不是报未来日期或录入新证书。
	human := runArgs(t, args...)
	if human.code != 0 {
		t.Fatalf("日期回落后同号同内容应退出 0，得到 %d（stderr=%s）",
			human.code, human.stderr)
	}
	if !strings.Contains(human.stdout, "同号证书且内容一致，返回原证书，未增加历史记录") {
		t.Fatalf("普通输出应明确返回原证书且未增加历史：%q", human.stdout)
	}
	if strings.Contains(human.stdout, "已录入新证书") {
		t.Fatalf("重复提交不能被说成录入新证书：%q", human.stdout)
	}
	if strings.Contains(human.stderr, "不能晚于本机今天") {
		t.Fatalf("同号同内容不能报未来日期：%q", human.stderr)
	}

	// JSON：退出 0、accepted=true、duplicate=true、conflict=false，原证书
	// 内容与首次录入时间不变。
	js, resp := runCertCLIJSON(t, args)
	if js.code != 0 {
		t.Fatalf("JSON 应退出 0，得到 %d（stderr=%s）", js.code, js.stderr)
	}
	if !resp.Accepted || !resp.Duplicate || resp.Conflict || resp.Certificate == nil {
		t.Fatalf("JSON 应 accepted=true duplicate=true conflict=false 且返回原证书：%+v", resp)
	}
	if *resp.Certificate != first {
		t.Fatalf("JSON 应返回首次保存的原证书：got=%+v want=%+v", *resp.Certificate, first)
	}

	// 历史仍只有原来那一张；录入时间保留首次值；最近证书也是它。
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 || certs[0] != first {
		t.Fatalf("重复提交不得改动台账：got=%+v want=%+v", certs, first)
	}
	rv := reviewSameNumberJSON(t, path, "M-1")
	if len(rv.History) != 1 || rv.Latest == nil ||
		rv.Latest.Number != "C-1" || rv.Latest.CreatedAt != first.CreatedAt {
		t.Fatalf("核对中历史应只有一张且最近证书不变：%+v", rv)
	}

	// 不切换器具状态、不新增使用申请或历史拒绝原因。
	if got := readInstrumentStatus(t, path, "M-1"); got != string(calibrate.StatusPending) {
		t.Fatalf("重复提交不得切换器具状态，得到 %q", got)
	}
	if n := readUsageCount(t, path); n != 0 {
		t.Fatalf("重复提交不得新增使用申请，得到 %d 条", n)
	}
	if rv.CanUse {
		t.Fatalf("待校准器具不应因重复提交变得可以使用：%+v", rv.Reasons)
	}
}

// TestCLINewNumberFutureCalDateStillRejectedAfterClockRollback 守住另一侧：
// 日期回落不能放宽对新编号的未来日期限制。校准日期为明天的新证书必须继续
// 退出 1、JSON conflict=false（普通业务拒绝而非同号冲突），不新增证书、
// 不占用编号；改用当天日期提交同一新编号则正常录入。
func TestCLINewNumberFutureCalDateStillRejectedAfterClockRollback(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	cal, expiry, first := seedCertThenRollBackClock(t, path)
	future := sameNumberCertArgs(path, "M-1", "C-NEW", cal, expiry, "规范A", "0.1", "摘要")

	// 普通输出：退出 1，以“已拒绝”说明晚于本机今天，而不是编号冲突。
	human := runArgs(t, future...)
	if human.code != 1 {
		t.Fatalf("新编号未来校准日期应退出 1，得到 %d", human.code)
	}
	if !strings.Contains(human.stderr, "已拒绝") ||
		!strings.Contains(human.stderr, "不能晚于本机今天") {
		t.Fatalf("普通输出应说明校准日期晚于本机今天：%q", human.stderr)
	}
	if strings.Contains(human.stderr, "内容不同") {
		t.Fatalf("新编号未来日期不能报成同号内容冲突：%q", human.stderr)
	}

	// JSON：退出 1、accepted=false、conflict=false，不返回录入成功的证书。
	jr, jresp := runCertCLIJSON(t, future)
	if jr.code != 1 || jresp.Accepted || jresp.Conflict || jresp.Duplicate {
		t.Fatalf("JSON 应明确未接受且属于普通校验拒绝（conflict=false）：code=%d %+v",
			jr.code, jresp)
	}
	if !strings.Contains(jresp.Error, "不能晚于本机今天") {
		t.Fatalf("JSON 错误原因应为未来日期：%q", jresp.Error)
	}
	if strings.Contains(jr.stdout, `"certificate"`) {
		t.Fatalf("未来日期拒绝时不得返回证书：%s", jr.stdout)
	}

	// 不新增证书、不占用编号：台账仍只有 C-1 且内容不变。
	certs := readPersistedCerts(t, path)
	if len(certs) != 1 || certs[0] != first {
		t.Fatalf("未来日期拒绝不得改动台账：got=%+v want=%+v", certs, first)
	}

	// 同一新编号改用运行当天日期（与已存 C-1 的明天不同日）应正常录入，
	// 证明编号未被占用，本机日期正常推进时的录入行为保持不变。
	now := time.Now()
	todayCal := now.Format(calibrate.DateLayout)
	todayExpiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
	okArgs := sameNumberCertArgs(path, "M-1", "C-NEW", todayCal, todayExpiry, "规范A", "0.1", "摘要")
	sr, sresp := runCertCLIJSON(t, okArgs)
	if sr.code != 0 || !sresp.Accepted || sresp.Duplicate || sresp.Certificate == nil {
		t.Fatalf("新编号改用当天日期应正常录入：code=%d resp=%+v stderr=%s",
			sr.code, sresp, sr.stderr)
	}
	if n := len(readPersistedCerts(t, path)); n != 2 {
		t.Fatalf("正常录入后台账应有 2 张证书，得到 %d 张", n)
	}
}

// TestCLISameNumberDifferentContentAfterClockRollbackConflict 守住冲突方向：
// 原证书校准日期晚于当前本机日期时，同号提交内容本身合法但修改了校准方法、
// 摘要或测得误差，必须退出 1 且 JSON conflict=true（而不是未来日期的普通
// 校验拒绝或重复成功），原证书不被覆盖；随后原样重交仍幂等成功。
func TestCLISameNumberDifferentContentAfterClockRollbackConflict(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		errVal  string
		summary string
	}{
		{"校准方法改成另一个非空方法", "规范B", "0.1", "摘要"},
		{"摘要改成另一段非空摘要", "规范A", "0.1", "另一份摘要"},
		{"方法内部空白变化", "规范 A", "0.1", "摘要"},
		{"摘要内部空白变化", "规范A", "0.1", "摘 要"},
		{"测得误差改成另一个有限数值", "规范A", "0.2", "摘要"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := chdirTemp(t)
			path := filepath.Join(dir, "台账.json")
			registerAllowed(t, path, "M-1", "0.5")
			cal, expiry, first := seedCertThenRollBackClock(t, path)
			changed := sameNumberCertArgs(path, "M-1", "C-1", cal, expiry,
				tc.method, tc.errVal, tc.summary)

			// 普通输出：退出 1，以“已拒绝”说明内容冲突，而不是未来日期。
			human := runArgs(t, changed...)
			if human.code != 1 {
				t.Fatalf("同号内容变化应退出 1，得到 %d", human.code)
			}
			if !strings.Contains(human.stderr, "已拒绝") ||
				!strings.Contains(human.stderr, "C-1") ||
				!strings.Contains(human.stderr, "内容不同") {
				t.Fatalf("普通输出应说明证书 C-1 内容冲突：%q", human.stderr)
			}
			if strings.Contains(human.stderr, "不能晚于本机今天") {
				t.Fatalf("同号内容变化不能报成未来日期：%q", human.stderr)
			}

			// JSON：退出 1、accepted=false、conflict=true，不返回证书。
			jr, jresp := runCertCLIJSON(t, changed)
			if jr.code != 1 || jresp.Accepted || !jresp.Conflict || jresp.Certificate != nil {
				t.Fatalf("JSON 应明确未接受且属于冲突：code=%d resp=%+v", jr.code, jresp)
			}

			// 原证书不被覆盖；历史与最近证书仍是原来那一张；状态与使用记录不变。
			certs := readPersistedCerts(t, path)
			if len(certs) != 1 || certs[0] != first {
				t.Fatalf("冲突提交不得改动原证书：got=%+v want=%+v", certs, first)
			}
			rv := reviewSameNumberJSON(t, path, "M-1")
			if len(rv.History) != 1 || rv.Latest == nil || rv.Latest.Number != "C-1" {
				t.Fatalf("冲突后历史与最近证书应不变：%+v", rv)
			}
			if got := readInstrumentStatus(t, path, "M-1"); got != string(calibrate.StatusPending) {
				t.Fatalf("冲突提交不得切换器具状态，得到 %q", got)
			}
			if n := readUsageCount(t, path); n != 0 {
				t.Fatalf("冲突提交不得新增使用申请，得到 %d 条", n)
			}

			// 控制对照：原样重交仍幂等成功，保留首次录入时间。
			again := sameNumberCertArgs(path, "M-1", "C-1", cal, expiry, "规范A", "0.1", "摘要")
			ar, aresp := runCertCLIJSON(t, again)
			if ar.code != 0 || !aresp.Accepted || !aresp.Duplicate ||
				aresp.Certificate == nil || *aresp.Certificate != first {
				t.Fatalf("原样重交应幂等返回原证书：code=%d resp=%+v", ar.code, aresp)
			}
			if n := len(readPersistedCerts(t, path)); n != 1 {
				t.Fatalf("整轮操作后应仍只有 1 张证书，得到 %d 张", n)
			}
		})
	}
}

// 能改变使用资格：原证书超差时改成合格值重交、原证书已到期时改成未来截止
// 日重交，内容本身都合法，但只能报编号冲突；按器具核对看到的结论、历史
// 数量、最近证书与使用限制都与提交前一致，同号证书不会被换内容而生效。
func TestCLISameNumberConflictCannotLiftFailOrExpiryRestriction(t *testing.T) {
	t.Run("超差证书改成合格值重交不能解除限制", func(t *testing.T) {
		dir := chdirTemp(t)
		path := filepath.Join(dir, "台账.json")
		registerAllowed(t, path, "M-1", "0.5")
		if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
			t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
		}
		now := time.Now()
		cal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
		future := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
		bad := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.8", "摘要")
		if r := runArgs(t, bad...); r.code != 0 ||
			!strings.Contains(r.stdout, "判定：超差") {
			t.Fatalf("超差证书应录入成功并显示超差：code=%d %q %s", r.code, r.stdout, r.stderr)
		}
		if u := runArgs(t, "use", "--id", "M-1", "-f", path); u.code != 1 ||
			!strings.Contains(u.stderr, "超差") {
			t.Fatalf("前置条件失效：应因最近证书超差被拒，code=%d stderr=%q",
				u.code, u.stderr)
		}

		// 同号改成合格误差重交：只能报冲突。
		good := sameNumberCertArgs(path, "M-1", "C-1", cal, future, "规范A", "0.1", "摘要")
		jr, jresp := runCertCLIJSON(t, good)
		if jr.code != 1 || jresp.Accepted || !jresp.Conflict || jresp.Certificate != nil {
			t.Fatalf("合格值重交应报冲突且不返回证书：code=%d resp=%+v", jr.code, jresp)
		}

		// 限制不被解除：最近证书仍是误差 0.8 的原超差证书，申请仍被拒。
		rv := reviewSameNumberJSON(t, path, "M-1")
		if rv.CanUse || !containsStd(rv.Reasons, "超差") {
			t.Fatalf("冲突重交不能解除超差限制：canUse=%v reasons=%v", rv.CanUse, rv.Reasons)
		}
		if containsStd(rv.Reasons, "到期") {
			t.Fatalf("未来截止日不应混入到期原因：%v", rv.Reasons)
		}
		if len(rv.History) != 1 || rv.Latest == nil ||
			rv.Latest.Error != 0.8 || rv.Latest.Pass {
			t.Fatalf("最近证书应仍是误差 0.8 的超差原证书：%+v", rv.Latest)
		}
		u := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
		if u.code != 1 || !strings.Contains(u.stdout, `"allowed": false`) ||
			!strings.Contains(u.stdout, "超差") {
			t.Fatalf("重交后正式申请仍应因原超差证书被拒：code=%d stdout=%q",
				u.code, u.stdout)
		}
	})

	t.Run("到期证书改成未来截止日重交不能恢复资格", func(t *testing.T) {
		dir := chdirTemp(t)
		path := filepath.Join(dir, "台账.json")
		registerAllowed(t, path, "M-1", "0.5")
		if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
			t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
		}
		now := time.Now()
		oldCal := now.AddDate(-2, 0, 0).Format(calibrate.DateLayout)
		pastExpiry := now.AddDate(-1, 0, 0).Format(calibrate.DateLayout) // 已到期。
		futureExpiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)
		expired := sameNumberCertArgs(path, "M-1", "C-1", oldCal, pastExpiry, "规范A", "0.1", "摘要")
		if r := runArgs(t, expired...); r.code != 0 {
			t.Fatalf("到期证书录入失败 code=%d stderr=%s", r.code, r.stderr)
		}
		if u := runArgs(t, "use", "--id", "M-1", "-f", path); u.code != 1 ||
			!strings.Contains(u.stderr, "到期") {
			t.Fatalf("前置条件失效：应因最近证书到期被拒，code=%d stderr=%q",
				u.code, u.stderr)
		}

		// 同号改成未来截止日重交（仍晚于校准日期，内容本身合法）：只能报冲突。
		updated := sameNumberCertArgs(path, "M-1", "C-1", oldCal, futureExpiry, "规范A", "0.1", "摘要")
		human := runArgs(t, updated...)
		if human.code != 1 || !strings.Contains(human.stderr, "内容不同") {
			t.Fatalf("未来截止日重交应冲突退出 1：code=%d stderr=%q",
				human.code, human.stderr)
		}
		jr, jresp := runCertCLIJSON(t, updated)
		if jr.code != 1 || jresp.Accepted || !jresp.Conflict || jresp.Certificate != nil {
			t.Fatalf("未来截止日重交 JSON 应冲突且不返回证书：code=%d resp=%+v",
				jr.code, jresp)
		}

		// 使用资格不恢复：最近证书仍是合格但已到期的原证书，申请仍只因到期被拒。
		rv := reviewSameNumberJSON(t, path, "M-1")
		if rv.CanUse || !containsStd(rv.Reasons, "到期") {
			t.Fatalf("冲突重交不能恢复使用资格：canUse=%v reasons=%v", rv.CanUse, rv.Reasons)
		}
		if containsStd(rv.Reasons, "超差") {
			t.Fatalf("合格证书不应混入超差原因：%v", rv.Reasons)
		}
		if len(rv.History) != 1 || rv.Latest == nil ||
			rv.Latest.Expiry != pastExpiry || !rv.Latest.Expired || !rv.Latest.Pass {
			t.Fatalf("最近证书应仍是合格但已到期的原证书：%+v", rv.Latest)
		}
		certs := readPersistedCerts(t, path)
		if len(certs) != 1 || certs[0].Expiry != pastExpiry {
			t.Fatalf("原截止日不得被未来值替换：%+v", certs)
		}
		u := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
		if u.code != 1 || !strings.Contains(u.stdout, `"allowed": false`) ||
			!strings.Contains(u.stdout, "到期") {
			t.Fatalf("重交后正式申请仍应因原证书到期被拒：code=%d stdout=%q",
				u.code, u.stdout)
		}
	})
}
