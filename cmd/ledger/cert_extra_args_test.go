package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件守住“证书录入只接受已有选项及其对应值”这条边界：凡是独立传入、
// 没有被任何选项作为值接收的内容，无论夹在证书字段之间、放在末尾，还是
// 位于“--”之后，都必须让整次录入失败（普通输出与 --json 均退出 2），
// 不能替用户挑选一部分内容形成正式证书。

// certExtraBase 构造一条字段齐全的 cert 参数，允许调用方在任意位置追加内容。
func certExtraBase(path string) []string {
	return []string{"cert", "-f", path,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行校准"}
}

// assertExtraRejected 核对一次多余参数录入被明确拒绝：退出 2，错误信息指出
// 存在多余参数并展示至少一个具体内容，stdout 不得出现任何成功结果。
func assertExtraRejected(t *testing.T, args []string, token string) {
	t.Helper()
	r := runArgs(t, args...)
	if r.code != 2 {
		t.Fatalf("多余参数 %q 应使录入退出 2，得到 %d；stdout=%q stderr=%q",
			token, r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "多余参数") {
		t.Fatalf("错误信息应指出存在多余参数，stderr=%q", r.stderr)
	}
	if token != "" && !strings.Contains(r.stderr, token) {
		t.Fatalf("错误信息应展示具体多余内容 %q，stderr=%q", token, r.stderr)
	}
	if strings.Contains(r.stdout, "已录入") ||
		strings.Contains(r.stdout, "未增加历史") ||
		strings.Contains(r.stdout, `"accepted": true`) {
		t.Fatalf("多余参数时不得输出成功结果：stdout=%q", r.stdout)
	}
}

// TestCertStrayArgumentBetweenFieldsRejected 复现题述主场景：允许误差 0.5，
// 先给测得误差 0.1，中间夹一个没有对应字段的词，后面再给测得误差 0.8。
// 旧实现会保存 0.1 并显示合格；现在必须报参数错误，不能替用户选值。
func TestCertStrayArgumentBetweenFieldsRejected(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	args := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "意外词", "--error", "0.8",
		"--summary", "例行校准"}

	// 普通输出与 --json 模式都必须退出 2，且不产出成功结果。
	assertExtraRejected(t, args, "意外词")
	jsonArgs := append(append([]string{}, args...), "--json")
	assertExtraRejected(t, jsonArgs, "意外词")

	// 台账保持原样：没有任何证书写入。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if strings.Contains(rv.stdout, "C-1") {
		t.Fatalf("被拒绝的证书不得进入历史：%s", rv.stdout)
	}
}

// TestCertStrayArgumentAtEndAndAfterDashDashRejected 覆盖末尾多余参数，以及
// “--” 不能把剩余内容变成可忽略备注：后面有任何独立参数时同样拒绝。
func TestCertStrayArgumentAtEndAndAfterDashDashRejected(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 末尾多一个独立参数（普通与 --json）。
	end := append(certExtraBase(target), "多余项")
	assertExtraRejected(t, end, "多余项")
	assertExtraRejected(t, append(append([]string{}, end...), "--json"), "多余项")

	// “--” 之后跟独立参数：同样拒绝，备注不被静默忽略。
	afterDD := append(certExtraBase(target), "--", "备注一句")
	assertExtraRejected(t, afterDD, "备注一句")
	// “--” 后哪怕是看起来像开关的内容，也属于未被选项接收的独立参数。
	afterDDFlag := append(certExtraBase(target), "--", "--json")
	assertExtraRejected(t, afterDDFlag, "--json")

	// 字段之间再夹一个独立参数，同样拒绝。
	middle := []string{"cert", "-f", target,
		"--instrument", "M-1", "夹心儿", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行校准"}
	assertExtraRejected(t, middle, "夹心儿")

	// 任何被拒绝的尝试都没有写盘：历史仍为空。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if strings.Contains(rv.stdout, "C-1") {
		t.Fatalf("被拒绝的证书不得进入历史：%s", rv.stdout)
	}
}

// TestCertBareDashDashWithLegalCertificateAccepted 只有结束符而没有后续内容时，
// 合法证书仍可正常录入（含负数测得误差）。
func TestCertBareDashDashWithLegalCertificateAccepted(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	args := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "-0.3", "--summary", "例行校准", "--"}
	if r := runArgs(t, args...); r.code != 0 {
		t.Fatalf("裸 -- 结尾的合法证书应录入成功，code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, append(append([]string{}, args[:len(args)-1]...), "--json", "--")...); r.code != 0 {
		t.Fatalf("幂等重提（--json + 裸 --）应成功，code=%d stderr=%s", r.code, r.stderr)
	}
}

// TestCertStrayArgumentRejectsEvenForDuplicateNumber 即使字段已经填齐、且编号
// 与已保存的同号证书内容完全一致，多带一个独立参数也不能走幂等返回原证书：
// 必须按参数错误拒绝，不输出 accepted:true。
func TestCertStrayArgumentRejectsEvenForDuplicateNumber(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	base := certExtraBase(target)
	if r := runArgs(t, base...); r.code != 0 {
		t.Fatalf("首次合法录入应成功，code=%d stderr=%s", r.code, r.stderr)
	}

	dup := append(append([]string{}, base...), "重复提交时的尾巴")
	assertExtraRejected(t, dup, "重复提交时的尾巴")

	// --json 下同样不能返回原证书或 accepted:true。
	assertExtraRejected(t, append(dup, "--json"), "重复提交时的尾巴")

	// 历史仍只有一张，且没有新增记录。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var view struct {
		History []struct {
			Number string `json:"number"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.History) != 1 || view.History[0].Number != "C-1" {
		t.Fatalf("失败的同号提交不得增加历史：%+v", view.History)
	}
}

// TestCertStrayArgumentDoesNotOccupyNumberOrDay 失败的新编号不被占用，该器具
// 当天的证书位置也不被占用；恢复为合法内容后可正常录入。一旦真正保存成功，
// 同日不同编号仍按既有规则业务拒绝（退出 1）。
func TestCertStrayArgumentDoesNotOccupyNumberOrDay(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if r := runArgs(t, registerArgs(target, "M-2")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// M-1：同编号 C-1 先因多余参数失败，再以合法内容同编号同日期提交应成功。
	bad := append(certExtraBase(target), "噪声")
	assertExtraRejected(t, bad, "噪声")
	good := certExtraBase(target)
	if r := runArgs(t, good...); r.code != 0 {
		t.Fatalf("失败不占用编号，合法重提应成功，code=%d stderr=%s", r.code, r.stderr)
	}

	// M-2：失败编号 C-9 与当天位置都不应被占用——换用不同编号 C-8 在同一天
	// （2026-09-01）合法录入应成功。
	bad2 := []string{"cert", "-f", target,
		"--instrument", "M-2", "--number", "C-9",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行", "噪声2"}
	assertExtraRejected(t, bad2, "噪声2")
	good2 := []string{"cert", "-f", target,
		"--instrument", "M-2", "--number", "C-8",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行"}
	if r := runArgs(t, good2...); r.code != 0 {
		t.Fatalf("失败不占用当天位置，合法录入应成功，code=%d stderr=%s", r.code, r.stderr)
	}
	// 真正保存成功后，同一器具同一天的不同编号仍按既有规则业务拒绝（退出 1）。
	sameDay := []string{"cert", "-f", target,
		"--instrument", "M-2", "--number", "C-7",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行"}
	if r := runArgs(t, sameDay...); r.code != 1 {
		t.Fatalf("成功保存后同日不同编号应业务拒绝退出 1，得到 %d", r.code)
	}
}

// TestCertStrayArgumentLeavesLatestAndUsageOnSavedData 最近证书与当前能否使用
// 仍只依据此前成功保存的数据：带多余参数的失败证书（即便其误差本应超差）
// 不会成为最近证书，也不影响在用器具的使用资格。
func TestCertStrayArgumentLeavesLatestAndUsageOnSavedData(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	// 先保存一张合格、未到期的最近证书 C-OK。
	ok := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-OK",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "规范法", "--error", "0.1", "--summary", "例行"}
	if r := runArgs(t, ok...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 试图录入编号更新、误差 0.8（本应超差）的证书，但夹带多余参数而失败。
	bad := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-BAD",
		"--cal-date", "2026-09-02", "--expiry", "2027-09-02",
		"--method", "规范法", "--error", "0.8", "--summary", "例行", "多余"}
	assertExtraRejected(t, bad, "多余")

	// 最近证书仍是此前保存的 C-OK 且合格，器具仍可使用。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var view struct {
		CanUse bool `json:"can_use"`
		Latest *struct {
			Number string `json:"number"`
			Pass   bool   `json:"pass"`
		} `json:"latest"`
		History []struct {
			Number string `json:"number"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatal(err)
	}
	if view.Latest == nil || view.Latest.Number != "C-OK" || !view.Latest.Pass {
		t.Fatalf("最近证书应仍是已保存的合格 C-OK：%+v", view.Latest)
	}
	if len(view.History) != 1 || !view.CanUse {
		t.Fatalf("失败证书不应影响历史与使用资格：canUse=%v history=%+v", view.CanUse, view.History)
	}
	if u := runArgs(t, "use", "--id", "M-1", "-f", target); u.code != 0 {
		t.Fatalf("器具仍应可使用，code=%d stderr=%s", u.code, u.stderr)
	}
}

// TestCertFlagLikeValuesStillAcceptedBesideStrayCheck 区分多余参数与合法字段值：
// 方法、摘要等接收的一个值可以恰好是 --json、-f备用.json 或 --file=备用.json，
// 这些文字属于证书内容，不能报多余参数、切换台账或改变输出格式；
// 只有真正独立的参数才被拒绝。
func TestCertFlagLikeValuesStillAcceptedBesideStrayCheck(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "真台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 摘要值恰为 --json：仍是普通文字输出，不当成输出开关。
	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "比较法", "--error", "-0.4", "--summary", "--json")
	if r.code != 0 || strings.HasPrefix(strings.TrimSpace(r.stdout), "{") {
		t.Fatalf("摘要为 --json 应按普通文字录入成功，code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}

	// 方法值为 -f备用.json / --file=另一本.json：不切换台账、不创建该文件。
	for i, tc := range []struct {
		number, method string
	}{
		{"C-2", "-f备用.json"},
		{"C-3", "--file=另一本.json"},
	} {
		r := runArgs(t, "cert", "-f", target,
			"--instrument", "M-1", "--number", tc.number,
			"--cal-date=2026-09-0"+string(rune('1'+i+1)), "--expiry=2027-09-0"+string(rune('1'+i+1)),
			"--method", tc.method, "--error=0.1", "--summary=s")
		if r.code != 0 {
			t.Fatalf("方法值 %q 应作为证书内容接收，code=%d stderr=%s", tc.method, r.code, r.stderr)
		}
	}
	for _, stray := range []string{"备用.json", "另一本.json", "ledger.json"} {
		if fileExists(t, filepath.Join(dir, stray)) {
			t.Fatalf("字段值 %s 被误当成台账文件参数", stray)
		}
	}

	// 但等号写法把字段都喂满后，末尾再独立跟一个参数，仍必须拒绝。
	bad := []string{"cert", "-f", target,
		"--instrument=M-1", "--number=C-4",
		"--cal-date=2026-09-04", "--expiry=2027-09-04",
		"--method=比较法", "--error=0.1", "--summary=--json", "独立尾巴"}
	assertExtraRejected(t, bad, "独立尾巴")
}
