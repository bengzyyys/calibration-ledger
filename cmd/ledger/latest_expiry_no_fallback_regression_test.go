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

// 本文件从命令行黑盒补充“申请使用器具”的自动化回归保障，重点保护：
//
//	最近证书（按校准日期确定，与录入先后、有效期长短无关）一旦到期，
//	不能因为历史里还有一张合格、未到期的较早证书而继续获准使用。
//
// 场景：器具保持在用；两张证书按允许误差都判合格；校准日期较新的证书
// 先到期（截止日就取本机今天，天然覆盖“截止日当天即到期”），较早证书
// 有效期更长、仍覆盖申请当天。
//
// 日期全部相对本机今天构造（较新证书截止日＝今天、旧证书截止日＝一年后），
// 不依赖任何固定日期恰好仍在未来，日历日期如何变化测试都可重复执行；
// “截止日前获准”的历史则通过先以未来截止日获准、再把台账中的截止日改写
// 为今天来模拟日历推进到截止日，而不依赖运行速度或睡眠等待。

// expiryReview 是 review --json 中本回归关心的字段。
type expiryReview struct {
	CanUse  bool     `json:"can_use"`
	Reasons []string `json:"reasons"`
	Latest  *struct {
		Number  string `json:"number"`
		CalDate string `json:"cal_date"`
		Expiry  string `json:"expiry"`
		Pass    bool   `json:"pass"`
		Expired bool   `json:"expired"`
	} `json:"latest"`
	History []struct {
		Number  string `json:"number"`
		CalDate string `json:"cal_date"`
		Expiry  string `json:"expiry"`
		Pass    bool   `json:"pass"`
		Expired bool   `json:"expired"`
	} `json:"history"`
	Rejections []struct {
		RequestedAt string   `json:"requested_at"`
		Allowed     bool     `json:"allowed"`
		Reasons     []string `json:"reasons"`
	} `json:"rejections"`
}

func reviewExpiryJSON(t *testing.T, path, id string) expiryReview {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var v expiryReview
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, r.stdout)
	}
	return v
}

// persistedUsage 直接读台账中的使用申请留痕（CLI 没有专门的使用记录命令）。
func persistedUsage(t *testing.T, path string) []struct {
	RequestedAt string   `json:"requested_at"`
	Allowed     bool     `json:"allowed"`
	Reasons     []string `json:"reasons"`
} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data struct {
		Usage []struct {
			RequestedAt string   `json:"requested_at"`
			Allowed     bool     `json:"allowed"`
			Reasons     []string `json:"reasons"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	return data.Usage
}

// rewriteCertExpiry 直接改写台账文件中指定证书的截止日，用来模拟日历推进
// （把截止日前已经保存的申请带到截止日当天），其余历史原样保留。
func rewriteCertExpiry(t *testing.T, path, number, expiry string) {
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
			c["expiry"] = expiry
			found = true
		}
	}
	if !found {
		t.Fatalf("台账中找不到证书 %s，无法改写截止日", number)
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化台账: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("写回台账 %s: %v", path, err)
	}
}

// TestUseRejectedOnLatestCertExpiryEvenWhenOlderCertStillValid 是命令行主场景：
// 较新证书截止日就是今天（当天即到期），较早证书合格且一年后才到期。
// 普通文本与 --json 必须表达同一个拒绝结果：退出码 1、指出真正到期的最近
// 证书及其截止日、已留痕；不得回退旧证书，也不得把到期误说成超差。
// 核对中最近证书是较新一张（合格但已到期），历史由近到远，新旧到期状态分列。
func TestUseRejectedOnLatestCertExpiryEvenWhenOlderCertStillValid(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	today := now.Format(calibrate.DateLayout)
	newerCal := now.AddDate(0, 0, -30).Format(calibrate.DateLayout)
	olderCal := now.AddDate(0, 0, -200).Format(calibrate.DateLayout)
	olderExpiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)

	// 先录入校准日期较新的证书，截止日就是今天：误差 -0.1，合格，今天到期。
	newerArgs := certFullArgs(path, "M-1", "C-NEW", newerCal, today, "-0.1")
	if r := runArgs(t, newerArgs...); r.code != 0 ||
		!strings.Contains(r.stdout, "判定：合格") {
		t.Fatalf("较新合格证书录入异常：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}

	// 只有较新证书时，今天的使用申请已应因它到期而拒绝。
	first := runArgs(t, "use", "--id", "M-1", "-f", path)
	if first.code != 1 {
		t.Fatalf("最近证书截止日当天应退出 1，得到 %d", first.code)
	}
	if !strings.Contains(first.stderr, "C-NEW") ||
		!strings.Contains(first.stderr, today) ||
		!strings.Contains(first.stderr, "到期") {
		t.Fatalf("普通文本拒绝应指出最近证书 C-NEW 于 %s 到期，stderr=%q",
			today, first.stderr)
	}
	if strings.Contains(first.stderr, "C-OLD") || strings.Contains(first.stderr, "超差") {
		t.Fatalf("拒绝原因不应涉及旧证书或超差：%q", first.stderr)
	}

	// 事后补录校准日期更早、有效期更长的合格旧证书：录入更晚、有效期更长
	// 都不能让它替代校准日期较新的证书。
	olderArgs := certFullArgs(path, "M-1", "C-OLD", olderCal, olderExpiry, "0.4")
	if r := runArgs(t, olderArgs...); r.code != 0 ||
		!strings.Contains(r.stdout, "判定：合格") {
		t.Fatalf("旧合格证书补录异常：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}

	// 普通文本再次申请：同一个拒绝结果，仍只针对 C-NEW 到期。
	human := runArgs(t, "use", "--id", "M-1", "-f", path)
	if human.code != 1 {
		t.Fatalf("补录旧证书后仍应退出 1，得到 %d", human.code)
	}
	if !strings.Contains(human.stderr, "拒绝使用器具 M-1") ||
		!strings.Contains(human.stderr, "C-NEW") ||
		!strings.Contains(human.stderr, today) {
		t.Fatalf("普通文本应表达对 M-1 的拒绝并指出 C-NEW 到期：%q", human.stderr)
	}
	if strings.Contains(human.stderr, "C-OLD") || strings.Contains(human.stderr, "超差") {
		t.Fatalf("不能借用旧证书，也不能把到期说成超差：%q", human.stderr)
	}

	// --json 表达同一个拒绝结果：退出码 1、allowed/recorded 字段与原因一致。
	js := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if js.code != 1 {
		t.Fatalf("JSON 拒绝应退出 1，得到 %d", js.code)
	}
	var decision struct {
		Allowed  bool `json:"allowed"`
		Recorded bool `json:"recorded"`
		Decision struct {
			Reasons []string `json:"Reasons"`
			Latest  *struct {
				Number  string `json:"Number"`
				Pass    bool   `json:"Pass"`
				Expired bool   `json:"Expired"`
				Expiry  string `json:"Expiry"`
			} `json:"Latest"`
		} `json:"decision"`
	}
	if err := json.Unmarshal([]byte(js.stdout), &decision); err != nil {
		t.Fatalf("use JSON 解析失败: %v\n%s", err, js.stdout)
	}
	if decision.Allowed || !decision.Recorded {
		t.Fatalf("JSON 应为已留痕的业务拒绝 allowed=false recorded=true，得到 %+v", decision)
	}
	if !containsStd(decision.Decision.Reasons, "C-NEW") ||
		!containsStd(decision.Decision.Reasons, today) ||
		containsStd(decision.Decision.Reasons, "C-OLD") ||
		containsStd(decision.Decision.Reasons, "超差") {
		t.Fatalf("JSON 拒绝原因应只指出 C-NEW 于 %s 到期：%v", today, decision.Decision.Reasons)
	}
	if decision.Decision.Latest == nil ||
		decision.Decision.Latest.Number != "C-NEW" ||
		!decision.Decision.Latest.Pass || !decision.Decision.Latest.Expired ||
		decision.Decision.Latest.Expiry != today {
		t.Fatalf("JSON 最近证书应为合格但今天到期的 C-NEW：%+v", decision.Decision.Latest)
	}

	// 核对：最近证书是较新的 C-NEW（合格、已到期）；历史由近到远两张，
	// 新证书已到期、旧证书未到期，各自结论不被对方带偏。
	rv := reviewExpiryJSON(t, path, "M-1")
	if rv.CanUse || !containsStd(rv.Reasons, "C-NEW") || !containsStd(rv.Reasons, today) {
		t.Fatalf("核对应判不可使用且原因指向 C-NEW 到期：canUse=%v reasons=%v",
			rv.CanUse, rv.Reasons)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" ||
		rv.Latest.CalDate != newerCal || !rv.Latest.Pass || !rv.Latest.Expired {
		t.Fatalf("最近证书应是校准日期较新、合格但已到期的 C-NEW：%+v", rv.Latest)
	}
	if len(rv.History) != 2 {
		t.Fatalf("历史应有两张证书，得到 %d", len(rv.History))
	}
	hNew, hOld := rv.History[0], rv.History[1]
	if hNew.Number != "C-NEW" || !hNew.Pass || !hNew.Expired {
		t.Fatalf("历史第一张应为已到期仍合格的 C-NEW：%+v", hNew)
	}
	if hOld.Number != "C-OLD" || hOld.CalDate != olderCal ||
		!hOld.Pass || hOld.Expired || hOld.Expiry != olderExpiry {
		t.Fatalf("历史第二张应为未到期合格的 C-OLD：%+v", hOld)
	}

	// 普通文本核对也要同时看得出新证书已到期、旧证书尚未到期。
	plain := runArgs(t, "review", "--id", "M-1", "-f", path)
	if plain.code != 0 {
		t.Fatalf("普通文本核对失败 code=%d stderr=%s", plain.code, plain.stderr)
	}
	if !strings.Contains(plain.stdout, "最近证书：C-NEW") ||
		!strings.Contains(plain.stdout, "判定：合格，已到期") {
		t.Fatalf("普通文本应显示最近证书 C-NEW 合格但已到期：%q", plain.stdout)
	}
	if !strings.Contains(plain.stdout, "C-OLD "+olderCal+" 截止 "+olderExpiry) ||
		!strings.Contains(plain.stdout, "合格 未到期") {
		t.Fatalf("普通文本历史应显示 C-OLD 合格未到期：%q", plain.stdout)
	}
}

// TestExpiryRejectionPersistedAndReviewIsReadOnly 保护留痕与只读核对：
// 成功保存的拒绝申请保留申请时间与当时的到期原因，核对能查到；连续核对
// 不新增使用记录（查询不会变成一次新的使用申请）；随后补录旧证书不重写
// 这条历史拒绝。
func TestExpiryRejectionPersistedAndReviewIsReadOnly(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	today := now.Format(calibrate.DateLayout)
	newerCal := now.AddDate(0, 0, -45).Format(calibrate.DateLayout)

	// 只有较新证书（截止日＝今天）时拒绝一次并保存。
	newerArgs := certFullArgs(path, "M-1", "C-NEW", newerCal, today, "0")
	if r := runArgs(t, newerArgs...); r.code != 0 {
		t.Fatalf("录入较新证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	use := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if use.code != 1 {
		t.Fatalf("今天到期应拒绝，得到 code=%d", use.code)
	}
	usageAfterUse := len(persistedUsage(t, path))
	if usageAfterUse != 1 {
		t.Fatalf("拒绝应保存 1 条使用记录，得到 %d 条", usageAfterUse)
	}

	// 连续核对两次：只读，不新增使用记录、不改动证书。
	rv1 := reviewExpiryJSON(t, path, "M-1")
	rv2 := reviewExpiryJSON(t, path, "M-1")
	if got := len(persistedUsage(t, path)); got != usageAfterUse {
		t.Fatalf("只查看核对结果不应新增使用申请：%d -> %d", usageAfterUse, got)
	}
	for i, rv := range []expiryReview{rv1, rv2} {
		if len(rv.Rejections) != 1 {
			t.Fatalf("第 %d 次核对应查到 1 条拒绝，得到 %d 条", i+1, len(rv.Rejections))
		}
		rej := rv.Rejections[0]
		if rej.Allowed {
			t.Fatalf("第 %d 次核对中的记录应保持拒绝结果", i+1)
		}
		if !strings.HasPrefix(rej.RequestedAt, today+"T") {
			t.Fatalf("第 %d 次核对应保留申请时间（今天 %s），得到 %s",
				i+1, today, rej.RequestedAt)
		}
		if !containsStd(rej.Reasons, "C-NEW") || !containsStd(rej.Reasons, today) {
			t.Fatalf("第 %d 次核对应保留当时的到期原因：%v", i+1, rej.Reasons)
		}
	}

	// 补录一张更早、合格、有效期更长的旧证书：历史拒绝原样保留，不被重写。
	olderCal := now.AddDate(0, 0, -300).Format(calibrate.DateLayout)
	olderExpiry := now.AddDate(2, 0, 0).Format(calibrate.DateLayout)
	olderArgs := certFullArgs(path, "M-1", "C-OLD", olderCal, olderExpiry, "-0.2")
	if r := runArgs(t, olderArgs...); r.code != 0 {
		t.Fatalf("补录旧证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	rv3 := reviewExpiryJSON(t, path, "M-1")
	if len(rv3.Rejections) != 1 {
		t.Fatalf("补录旧证书不应新增或删除拒绝记录，得到 %d 条", len(rv3.Rejections))
	}
	rej := rv3.Rejections[0]
	if rej.Allowed || !strings.HasPrefix(rej.RequestedAt, today+"T") ||
		!containsStd(rej.Reasons, "C-NEW") || !containsStd(rej.Reasons, today) ||
		containsStd(rej.Reasons, "C-OLD") {
		t.Fatalf("补录旧证书不能重写冻结的拒绝历史：%+v", rej)
	}
	// 补录后仍因同一张最近证书被拒，旧证书在历史中显示合格未到期。
	if rv3.Latest == nil || rv3.Latest.Number != "C-NEW" || !rv3.Latest.Expired {
		t.Fatalf("补录后最近证书仍应是已到期的 C-NEW：%+v", rv3.Latest)
	}
	if len(rv3.History) != 2 || rv3.History[1].Number != "C-OLD" || rv3.History[1].Expired {
		t.Fatalf("补录只增加历史，旧证书应未到期：%+v", rv3.History)
	}
	if got := len(persistedUsage(t, path)); got != 1 {
		t.Fatalf("补录证书是独立录入，不应顺带新增使用记录，得到 %d 条", got)
	}
}

// TestAllowedUseBeforeExpiryKeepsVerdictAfterLatestExpires 保护历史冻结的
// 另一面：截止日前已经获准并保存的申请，在最近证书到期、又补录了旧证书
// 之后仍是获准结果，不会被改成拒绝；到期当天的新申请单独拒绝。
func TestAllowedUseBeforeExpiryKeepsVerdictAfterLatestExpires(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账.json")
	registerAllowed(t, path, "M-1", "0.5")
	if r := runArgs(t, "status", "-f", path, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}

	now := time.Now()
	today := now.Format(calibrate.DateLayout)
	newerCal := now.AddDate(0, 0, -60).Format(calibrate.DateLayout)
	futureExpiry := now.AddDate(1, 0, 0).Format(calibrate.DateLayout)

	// 较新证书截止日尚在一年后：申请获准并保存。
	newerArgs := certFullArgs(path, "M-1", "C-NEW", newerCal, futureExpiry, "0.1")
	if r := runArgs(t, newerArgs...); r.code != 0 {
		t.Fatalf("录入较新证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	allowed := runArgs(t, "use", "--id", "M-1", "-f", path, "--json")
	if allowed.code != 0 || !strings.Contains(allowed.stdout, `"allowed": true`) {
		t.Fatalf("截止日前应获准 code=%d stdout=%q stderr=%s",
			allowed.code, allowed.stdout, allowed.stderr)
	}

	// 模拟日历推进到该证书的截止日当天：把截止日改写为今天，其余历史保留。
	rewriteCertExpiry(t, path, "C-NEW", today)

	// 再补录一张更早、合格、很久以后才到期的旧证书：不能替代最近证书，
	// 也不能重写此前的获准记录。
	olderCal := now.AddDate(0, 0, -250).Format(calibrate.DateLayout)
	olderExpiry := now.AddDate(3, 0, 0).Format(calibrate.DateLayout)
	olderArgs := certFullArgs(path, "M-1", "C-OLD", olderCal, olderExpiry, "0.3")
	if r := runArgs(t, olderArgs...); r.code != 0 {
		t.Fatalf("补录旧证书失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 截止日当天的新申请：必须拒绝并留痕。
	denied := runArgs(t, "use", "--id", "M-1", "-f", path)
	if denied.code != 1 || !strings.Contains(denied.stderr, "C-NEW") ||
		!strings.Contains(denied.stderr, today) ||
		strings.Contains(denied.stderr, "C-OLD") {
		t.Fatalf("到期当天应只因 C-NEW 到期拒绝：code=%d stderr=%q",
			denied.code, denied.stderr)
	}

	// 两条使用留痕：较早的获准保持 allowed=true、原因为空；较晚的拒绝
	// 保留 C-NEW 到期原因。
	recs := persistedUsage(t, path)
	if len(recs) != 2 {
		t.Fatalf("应保留两次申请，得到 %d 条：%+v", len(recs), recs)
	}
	if !recs[0].Allowed || len(recs[0].Reasons) != 0 {
		t.Fatalf("截止日前的获准记录不应被后来到期改成拒绝：%+v", recs[0])
	}
	if recs[1].Allowed ||
		!containsStd(recs[1].Reasons, "C-NEW") ||
		!containsStd(recs[1].Reasons, today) {
		t.Fatalf("到期当天的拒绝应保留 C-NEW 到期原因：%+v", recs[1])
	}

	// 核对当前不可用；拒绝列表只有到期当天这一条，获准记录不被翻成拒绝；
	// 最近证书仍是较新的 C-NEW，旧证书合格未到期。
	rv := reviewExpiryJSON(t, path, "M-1")
	if rv.CanUse {
		t.Fatalf("到期当天核对应不可使用：%v", rv.Reasons)
	}
	if len(rv.Rejections) != 1 || rv.Rejections[0].Allowed ||
		!containsStd(rv.Rejections[0].Reasons, "C-NEW") {
		t.Fatalf("核对中只应保留到期当天的 1 条拒绝：%+v", rv.Rejections)
	}
	if rv.Latest == nil || rv.Latest.Number != "C-NEW" ||
		!rv.Latest.Pass || !rv.Latest.Expired {
		t.Fatalf("最近证书应是合格但已到期的 C-NEW：%+v", rv.Latest)
	}
	if len(rv.History) != 2 || rv.History[0].Number != "C-NEW" ||
		rv.History[1].Number != "C-OLD" || rv.History[1].Expired || !rv.History[1].Pass {
		t.Fatalf("历史应由近到远为已到期合格 C-NEW、未到期合格 C-OLD：%+v", rv.History)
	}
}
