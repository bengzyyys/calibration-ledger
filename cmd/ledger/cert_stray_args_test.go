package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// fullCertArgs 构造一条字段齐全的证书录入参数，允许在任意位置插入额外内容。
func fullCertArgs(path, number, calDate string, errorValue string) []string {
	return []string{"cert", "-f", path,
		"--instrument", "M-1", "--number", number,
		"--cal-date", calDate, "--expiry", "2027-09-01",
		"--method", "比较法", "--error", errorValue, "--summary", "例行校准"}
}

// historyNumbers 读取核对结果中的全部证书编号。
func historyNumbers(t *testing.T, path string) []string {
	t.Helper()
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if rv.code != 0 {
		t.Fatalf("review 失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	var view struct {
		History []struct {
			Number string `json:"number"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, rv.stdout)
	}
	got := make([]string, 0, len(view.History))
	for _, h := range view.History {
		got = append(got, h.Number)
	}
	return got
}

// TestCertStrayArgumentRejected 验证证书录入对“字段之外的独立参数”零容忍：
// 多余的普通词夹在字段之间时，不能用它前面的 --error 0.1 组成一张合格证书
// 而静默忽略后面的 --error 0.8；无论普通输出还是 --json 模式都必须退出 2，
// 错误信息指出存在多余参数并显示具体内容，且不产生任何证书。
func TestCertStrayArgumentRejected(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 允许误差 0.5：先给测得误差 0.1，中间夹一个无对应字段的词，再给 0.8。
	// 旧逻辑会在“多余词”处停止解析、保存 0.1 并显示合格；新逻辑必须拒绝。
	middle := fullCertArgs(target, "C-1", "2026-09-01", "0.1")
	middle = insertAt(middle, len(middle)-2, "多余词", "--error", "0.8")
	// 上面把多余词插在 --summary 之前，最终序列为：
	// ... --error 0.1 多余词 --error 0.8 --summary 例行校准
	r := runArgs(t, middle...)
	if r.code != 2 {
		t.Fatalf("夹入多余词应退出 2，得到 %d（stdout=%q stderr=%q）", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "多余参数") || !strings.Contains(r.stderr, "多余词") {
		t.Fatalf("错误信息应指出多余参数及具体内容：%q", r.stderr)
	}
	if strings.Contains(r.stdout, "已录入") || strings.Contains(r.stdout, "accepted") {
		t.Fatalf("参数错误不得输出任何成功结果：%q", r.stdout)
	}
	if nums := historyNumbers(t, target); len(nums) != 0 {
		t.Fatalf("失败录入不应产生证书，历史=%v", nums)
	}

	// 多余参数放在末尾同样拒绝。
	trailing := fullCertArgs(target, "C-2", "2026-09-02", "0.1")
	trailing = append(trailing, "结尾多余词")
	r = runArgs(t, trailing...)
	if r.code != 2 || !strings.Contains(r.stderr, "结尾多余词") {
		t.Fatalf("末尾多余词应退出 2 且显示具体内容：code=%d stderr=%q", r.code, r.stderr)
	}

	// --json 模式：退出 2，stdout 不得出现 accepted:true 等成功结果。
	jsonArgs := []string{"-f", target, "--json", "cert",
		"--instrument", "M-1", "--number", "C-3",
		"--cal-date", "2026-09-03", "--expiry", "2027-09-03",
		"--method", "比较法", "--error", "0.1", "--summary", "例行校准", "json多余词"}
	rj := runArgs(t, jsonArgs...)
	if rj.code != 2 {
		t.Fatalf("--json 模式多余参数应退出 2，得到 %d", rj.code)
	}
	if strings.TrimSpace(rj.stdout) != "" {
		t.Fatalf("--json 模式参数错误不应输出结果 JSON：%q", rj.stdout)
	}
	if !strings.Contains(rj.stderr, "json多余词") {
		t.Fatalf("--json 模式错误信息也应显示具体多余内容：%q", rj.stderr)
	}

	// 未登记的标志也是多余参数。
	bogus := append(fullCertArgs(target, "C-4", "2026-09-04", "0.1"), "--bogus")
	if r := runArgs(t, bogus...); r.code != 2 || !strings.Contains(r.stderr, "--bogus") {
		t.Fatalf("未知标志应按多余参数退出 2：code=%d stderr=%q", r.code, r.stderr)
	}

	if nums := historyNumbers(t, target); len(nums) != 0 {
		t.Fatalf("所有失败录入都不应产生证书，历史=%v", nums)
	}
}

// TestCertDashDashDoesNotSwallowArguments 验证单独的 -- 仍终止选项解析，
// 但不能把后续内容变成可忽略的备注；-- 后还有独立参数时同样拒绝，只有结束
// 符而没有后续内容时合法证书仍可正常录入（负数误差按原规则处理）。
func TestCertDashDashDoesNotSwallowArguments(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	withNote := append(fullCertArgs(target, "C-1", "2026-09-01", "0.1"), "--", "备注内容")
	if r := runArgs(t, withNote...); r.code != 2 || !strings.Contains(r.stderr, "备注内容") {
		t.Fatalf("-- 之后有内容应退出 2 并显示该内容：code=%d stderr=%q", r.code, r.stderr)
	}
	if nums := historyNumbers(t, target); len(nums) != 0 {
		t.Fatalf("-- 后多余内容不应产生证书，历史=%v", nums)
	}

	// 只有结束符、没有后续内容：合法证书正常录入，-0.4 在 0.5 限值内判定合格。
	bare := append(fullCertArgs(target, "C-2", "2026-09-02", "-0.4"), "--")
	r := runArgs(t, bare...)
	if r.code != 0 || !strings.Contains(r.stdout, "已录入新证书") || !strings.Contains(r.stdout, "合格") {
		t.Fatalf("末尾单独 -- 不应影响合法录入：code=%d stdout=%q stderr=%q",
			r.code, r.stdout, r.stderr)
	}
	if nums := historyNumbers(t, target); len(nums) != 1 || nums[0] != "C-2" {
		t.Fatalf("应只有 C-2 一张证书，历史=%v", nums)
	}
}

// TestCertStrayArgumentBypassesDuplicateAndLeavesSlotFree 验证即使录入内容与
// 已有同号证书完全一致，只要带多余参数也不能走“返回原证书”的成功路径；
// 失败的新编号既不占用编号，也不占用该器具当天的证书位置，去掉多余参数后
// 可用同一编号、同一天重新录入。
func TestCertStrayArgumentBypassesDuplicateAndLeavesSlotFree(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	good := fullCertArgs(target, "C-1", "2026-09-01", "0.1")
	if r := runArgs(t, good...); r.code != 0 {
		t.Fatalf("准备合法证书失败：%s", r.stderr)
	}

	// 与已有证书完全同号同内容，但多一个参数：必须退出 2，不能返回原证书。
	dup := append(append([]string{}, good...), "多余词")
	if r := runArgs(t, dup...); r.code != 2 ||
		strings.Contains(r.stdout, "未增加历史") || strings.Contains(r.stdout, "accepted") {
		t.Fatalf("同号证书带多余参数也应退出 2 且无成功输出：code=%d stdout=%q stderr=%q",
			r.code, r.stdout, r.stderr)
	}

	// 此前失败的新编号 C-2 与 2026-09-02 当天的位置都不应被占用。
	failed := fullCertArgs(target, "C-2", "2026-09-02", "0.1")
	failed = append(failed, "另一个多余词")
	if r := runArgs(t, failed...); r.code != 2 {
		t.Fatalf("带多余参数应退出 2，得到 %d", r.code)
	}
	retry := fullCertArgs(target, "C-2", "2026-09-02", "0.1")
	if r := runArgs(t, retry...); r.code != 0 || !strings.Contains(r.stdout, "已录入新证书") {
		t.Fatalf("失败后同编号同日期应可重新录入：code=%d stdout=%q stderr=%q",
			r.code, r.stdout, r.stderr)
	}
	if nums := historyNumbers(t, target); len(nums) != 2 {
		t.Fatalf("历史应只有 C-1 与 C-2 两张，历史=%v", nums)
	}

	// 合法申请的结论计算保持不变：误差 0.8 超过允许误差 0.5，明确判超差。
	over := fullCertArgs(target, "C-3", "2026-09-03", "0.8")
	r := runArgs(t, over...)
	if r.code != 0 || !strings.Contains(r.stdout, "超差") {
		t.Fatalf("合法超差证书应正常录入并显示超差：code=%d stdout=%q", r.code, r.stdout)
	}
}

// TestCertFlagLikeValuesStillAcceptedAfterStrictParse 确保严格解析不会误伤
// 合法字段值：分开写与等号写两种形式下，方法/摘要的值恰好是 --json、
// -f备用.json、--file=备用.json 时仍按证书内容保存，不切换台账、不改格式。
func TestCertFlagLikeValuesStillAcceptedAfterStrictParse(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	split := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "-f备用.json", "--error", "-0.1", "--summary", "--json"}
	if r := runArgs(t, split...); r.code != 0 || strings.HasPrefix(strings.TrimSpace(r.stdout), "{") {
		t.Fatalf("标志样子的字段值应原样保存且不开 JSON：code=%d stdout=%q stderr=%q",
			r.code, r.stdout, r.stderr)
	}
	equals := []string{"cert", "-f", target,
		"--instrument=M-1", "--number=C-2",
		"--cal-date=2026-09-02", "--expiry=2027-09-02",
		"--method=--file=备用.json", "--error=-0.2", "--summary=--json"}
	if r := runArgs(t, equals...); r.code != 0 {
		t.Fatalf("等号形式且值像标志时应录入成功：code=%d stderr=%s", r.code, r.stderr)
	}
	for _, stray := range []string{"备用.json", "ledger.json"} {
		if fileExists(t, filepath.Join(dir, stray)) {
			t.Fatalf("字段值被误当成全局台账参数，产生了 %s", stray)
		}
	}

	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	want := map[string][2]string{
		"C-1": {"-f备用.json", "--json"},
		"C-2": {"--file=备用.json", "--json"},
	}
	var view struct {
		History []struct {
			Number  string `json:"number"`
			Method  string `json:"method"`
			Summary string `json:"summary"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatal(err)
	}
	for _, h := range view.History {
		if ms, ok := want[h.Number]; ok && ([2]string{h.Method, h.Summary} != ms) {
			t.Fatalf("%s 内容被改写：got=%v want=%v", h.Number,
				[2]string{h.Method, h.Summary}, ms)
		}
	}
}

// insertAt 把 extra 插入 ss 的下标 pos 处，返回新切片。
func insertAt(ss []string, pos int, extra ...string) []string {
	out := make([]string, 0, len(ss)+len(extra))
	out = append(out, ss[:pos]...)
	out = append(out, extra...)
	out = append(out, ss[pos:]...)
	return out
}
