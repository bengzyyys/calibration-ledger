package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdirTemp 切换到全新的临时目录，避免测试触碰真实的 ledger.json。
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换临时目录: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

type result struct {
	code   int
	stdout string
	stderr string
}

func runArgs(t *testing.T, args ...string) result {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := run(args, &out, &errBuf)
	return result{code: code, stdout: out.String(), stderr: errBuf.String()}
}

func registerArgs(path, id string) []string {
	return []string{"register", "-f", path, "--id", id, "--name", "万用表-" + id, "--allowed", "0.5"}
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

func readUsageCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data struct {
		Usage []json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	return len(data.Usage)
}

// 子命令之后给出 -f 时，记录必须落到该文件，而不是默认 ledger.json。
func TestFileFlagAfterSubcommandSelectsTarget(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "other.json")

	r := runArgs(t, "register", "--id", "M-1", "--name", "万用表",
		"--allowed", "0.5", "-f", target)
	if r.code != 0 {
		t.Fatalf("register 退出码 %d，stderr=%s", r.code, r.stderr)
	}
	if !fileExists(t, target) {
		t.Fatal("子命令后的 -f 未让记录进入指定文件")
	}
	if fileExists(t, filepath.Join(dir, "ledger.json")) {
		t.Fatal("误创建了默认 ledger.json")
	}

	lr := runArgs(t, "list", "-f", target)
	if lr.code != 0 || !strings.Contains(lr.stdout, "M-1") {
		t.Fatalf("从指定文件查询失败 code=%d stdout=%q", lr.code, lr.stdout)
	}
}

// 多个文件参数交错出现时，按命令行顺序以最后一次为准。
func TestMultipleFileFlagsLastWins(t *testing.T) {
	dir := chdirTemp(t)
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	c := filepath.Join(dir, "含 空格 的目录", "c.json")

	r := runArgs(t,
		"-f", a,
		"register",
		"--id", "M-1",
		"--file", b,
		"--name", "万用表",
		"--allowed", "0.5",
		"-f", c,
	)
	if r.code != 0 {
		t.Fatalf("register code=%d stderr=%s", r.code, r.stderr)
	}
	if fileExists(t, a) || fileExists(t, b) {
		t.Fatal("被覆盖的文件路径不应被创建或读取")
	}
	if !fileExists(t, c) {
		t.Fatal("最后一次指定的路径才应是目标台账")
	}

	// 短名与长名混合、按命令行顺序覆盖：中间给过不存在的路径，最后一次
	// 落在真正有数据的 c；再验证最后一次落到空路径时不会回头使用 c。
	none := filepath.Join(dir, "none.json")
	r2 := runArgs(t, "list", "--file", a, "-f", none, "--file", c)
	if r2.code != 0 || !strings.Contains(r2.stdout, "M-1") {
		t.Fatalf("短长名混合的 last-wins 查询失败 code=%d stdout=%q", r2.code, r2.stdout)
	}
	r3 := runArgs(t, "list", "-f", c, "--file", none)
	if r3.code != 0 || strings.Contains(r3.stdout, "M-1") {
		t.Fatalf("最后一次为空路径时不应再使用前面的 c，stdout=%q", r3.stdout)
	}
	if fileExists(t, none) {
		t.Fatal("查询不应创建仅作为中间值出现过的文件")
	}
}

// --file=路径、-f路径、单横线长名等写法都可用，路径中的中文与空格保持完整。
func TestFileFlagEquivalentForms(t *testing.T) {
	dir := chdirTemp(t)
	cases := [][]string{
		{"register", "--id", "EQ", "--name", "n", "--allowed", "1",
			"--file=" + filepath.Join(dir, "等号 长名.json")},
		{"register", "--id", "AT", "--name", "n", "--allowed", "1",
			"-f" + filepath.Join(dir, "短名粘连.json")},
		{"register", "--id", "DS", "--name", "n", "--allowed", "1",
			"-file=" + filepath.Join(dir, "单横线.json")},
		{"-f" + filepath.Join(dir, "前置 粘连.json"),
			"register", "--id", "FS", "--name", "器具 名称", "--allowed", "1"},
	}
	for i, args := range cases {
		r := runArgs(t, args...)
		if r.code != 0 {
			t.Fatalf("写法 %d 失败 code=%d stderr=%s", i, r.code, r.stderr)
		}
	}
	for _, name := range []string{"等号 长名.json", "短名粘连.json", "单横线.json", "前置 粘连.json"} {
		if !fileExists(t, filepath.Join(dir, name)) {
			t.Fatalf("文件 %q 未被创建，路径中的中文或空格被破坏", name)
		}
	}
}

// 查询不存在的台账按空台账处理，且不得仅为查询创建文件。
func TestQueryMissingFileDoesNotCreate(t *testing.T) {
	dir := chdirTemp(t)
	missing := filepath.Join(dir, "absent.json")

	if r := runArgs(t, "list", "-f", missing); r.code != 0 {
		t.Fatalf("空台账 list 应成功，code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, "todos", "-f", missing); r.code != 0 || !strings.Contains(r.stdout, "无待办") {
		t.Fatalf("空台账 todos 异常 code=%d stdout=%q", r.code, r.stdout)
	}
	if r := runArgs(t, "review", "--id", "X", "-f", missing); r.code != 1 {
		t.Fatalf("空台账查不存在器具应退出 1，得到 %d", r.code)
	}
	if fileExists(t, missing) {
		t.Fatal("查询不应创建台账文件")
	}

	// 成功写入后文件才出现，并能继续查阅此前保存的数据。
	if r := runArgs(t, registerArgs(missing, "M-9")...); r.code != 0 {
		t.Fatalf("写入新台账失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if !fileExists(t, missing) {
		t.Fatal("成功写入后应创建台账文件")
	}
	if r := runArgs(t, "review", "--id", "M-9", "-f", missing); r.code != 0 {
		t.Fatalf("重开指定文件查不到此前数据 code=%d stderr=%s", r.code, r.stderr)
	}
}

// 默认文件或被覆盖的前一个文件损坏，不能妨碍最终目标文件。
func TestCorruptOverriddenAndDefaultFilesIgnored(t *testing.T) {
	dir := chdirTemp(t)
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{这不是JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.json"), []byte("坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.json")
	if r := runArgs(t, registerArgs(good, "M-1")...); r.code != 0 {
		t.Fatalf("准备 good 台账失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 损坏文件出现在前、被后续 -f 覆盖：查询与写入都不受影响。
	r := runArgs(t, "list", "-f", corrupt, "--file", good)
	if r.code != 0 || !strings.Contains(r.stdout, "M-1") {
		t.Fatalf("覆盖损坏路径后查询失败 code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}
	r = runArgs(t, "-f", corrupt, "register", "--id", "M-2", "--name", "n",
		"--allowed", "1", "-f", good)
	if r.code != 0 {
		t.Fatalf("覆盖损坏路径后写入失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 默认 ledger.json 损坏但显式指定了有效文件。
	r = runArgs(t, "list", "-f", good)
	if r.code != 0 || !strings.Contains(r.stdout, "M-2") {
		t.Fatalf("默认文件损坏干扰了显式目标 code=%d stderr=%s", r.code, r.stderr)
	}
}

// 目标文件损坏时必须明确报告该目标并退出 2，不能回退到默认或前一个路径。
func TestCorruptTargetReported(t *testing.T) {
	dir := chdirTemp(t)
	corrupt := filepath.Join(dir, "目标 损坏.json")
	if err := os.WriteFile(corrupt, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.json")
	if r := runArgs(t, registerArgs(good, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	r := runArgs(t, "list", "-f", good, "--file", corrupt)
	if r.code != 2 {
		t.Fatalf("损坏目标应退出 2，得到 %d", r.code)
	}
	if !strings.Contains(r.stderr, "目标 损坏.json") {
		t.Fatalf("错误信息必须指出目标文件，stderr=%q", r.stderr)
	}
	if strings.Contains(r.stdout, "M-1") {
		t.Fatal("不得改用前一个有效路径继续执行")
	}

	// 默认文件就是目标时也直接报错，不能默默换文件。
	def := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(def, []byte("坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = runArgs(t, "list")
	if r.code != 2 || !strings.Contains(r.stderr, "ledger.json") {
		t.Fatalf("默认文件损坏应退出 2 并指出文件，code=%d stderr=%q", r.code, r.stderr)
	}
}

// 文件参数缺值、空或只有空白都是参数错误：退出 2，不创建、不改动台账。
func TestFileFlagMissingOrBlank(t *testing.T) {
	dir := chdirTemp(t)
	bad := [][]string{
		{"-f"},
		{"--file"},
		{"register", "--id", "M", "-f"},
		{"list", "--file"},
		{"list", "--file="},
		{"list", "-f", ""},
		{"list", "--file", "   "},
		{"-f", "  ", "list"},
	}
	for i, args := range bad {
		r := runArgs(t, args...)
		if r.code != 2 {
			t.Fatalf("用例 %d 应退出 2，得到 %d（%v）", i, r.code, args)
		}
	}
	if fileExists(t, filepath.Join(dir, "ledger.json")) {
		t.Fatal("参数错误时不得创建默认台账")
	}
}

// 两份台账可以使用相同的器具、证书、计划编号，所有业务判断只依据目标文件。
func TestBusinessRulesScopedToTarget(t *testing.T) {
	dir := chdirTemp(t)
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")

	for _, p := range []string{a, b} {
		if r := runArgs(t, registerArgs(p, "DUP")...); r.code != 0 {
			t.Fatalf("在 %s 登记 DUP 失败: %s", p, r.stderr)
		}
	}
	// 第三份文件中同号器具也独立；重复登记仅在同一文件内被业务拒绝（退出 1）。
	if r := runArgs(t, registerArgs(a, "DUP")...); r.code != 1 {
		t.Fatalf("同文件重复登记应退出 1，得到 %d", r.code)
	}
	if r := runArgs(t, registerArgs(b, "DUP")...); r.code != 1 {
		t.Fatalf("b 文件重复登记应独立判重，得到 %d", r.code)
	}

	cert := func(path string, number, cal, expiry, summary string) result {
		return runArgs(t, "cert", "-f", path,
			"--instrument", "DUP", "--number", number,
			"--cal-date", cal, "--expiry", expiry,
			"--method", "JJF", "--error", "0.1", "--summary", summary)
	}
	if r := cert(a, "C-1", "2020-09-01", "2030-09-01", "首次"); r.code != 0 {
		t.Fatalf("a 录入证书失败: %s", r.stderr)
	}
	if r := cert(b, "C-1", "2020-09-02", "2030-09-02", "另一份"); r.code != 0 {
		t.Fatalf("b 录入同号证书应独立通过: %s", r.stderr)
	}
	// 同号内容不同只在 a 内冲突。
	if r := cert(a, "C-1", "2020-09-03", "2030-09-03", "冲突内容"); r.code != 1 {
		t.Fatalf("a 中同号内容不同应冲突退出 1，得到 %d", r.code)
	}
	if r := cert(b, "C-1", "2020-09-02", "2030-09-02", "另一份"); r.code != 0 {
		t.Fatalf("b 的幂等证书不应受 a 影响，code=%d stderr=%s", r.code, r.stderr)
	}

	plan := func(path string) result {
		return runArgs(t, "plan", "-f", path,
			"--instrument", "DUP", "--number", "PL-1",
			"--date", "2030-10-20", "--note", "计划")
	}
	if r := plan(a); r.code != 0 {
		t.Fatalf("a 建计划失败: %s", r.stderr)
	}
	if r := plan(b); r.code != 0 {
		t.Fatalf("b 使用同号计划应独立通过: %s", r.stderr)
	}
	if r := plan(a); r.code != 1 {
		t.Fatalf("a 中重复计划号应被拒，得到 %d", r.code)
	}

	// 查询“最近证书”只看目标文件：a、b 的 C-1 校准日期不同。
	ra := runArgs(t, "review", "--id", "DUP", "-f", a, "--json")
	rb := runArgs(t, "review", "--id", "DUP", "-f", b, "--json")
	if ra.code != 0 || rb.code != 0 {
		t.Fatalf("review 失败 %d/%d", ra.code, rb.code)
	}
	var va, vb struct {
		Latest struct {
			CalDate string `json:"cal_date"`
		} `json:"latest"`
	}
	if err := json.Unmarshal([]byte(ra.stdout), &va); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(rb.stdout), &vb); err != nil {
		t.Fatal(err)
	}
	if va.Latest.CalDate != "2020-09-01" || vb.Latest.CalDate != "2020-09-02" {
		t.Fatalf("最近证书串台：a=%s b=%s", va.Latest.CalDate, vb.Latest.CalDate)
	}
}

// 目标文件没有该器具时，即使另一份台账有同名记录也报编号不存在、不留痕、不建文件。
func TestUseNotFoundScopedAndNoTrace(t *testing.T) {
	dir := chdirTemp(t)
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	if r := runArgs(t, registerArgs(a, "ONLY-A")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// b 尚不存在：申请使用返回编号不存在，且不创建 b。
	r := runArgs(t, "use", "--id", "ONLY-A", "-f", b)
	if r.code != 1 {
		t.Fatalf("目标文件无此器具应退出 1，得到 %d", r.code)
	}
	if fileExists(t, b) {
		t.Fatal("编号不存在时不得创建目标文件或使用记录")
	}

	// b 存在但只有别的器具：仍报不存在，且不新增使用记录。
	if r := runArgs(t, registerArgs(b, "OTHER")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	before := readUsageCount(t, b)
	r = runArgs(t, "use", "--id", "ONLY-A", "-f", b, "--json")
	if r.code != 1 || !strings.Contains(r.stdout, `"recorded": false`) {
		t.Fatalf("应拒绝且 recorded=false，code=%d stdout=%q", r.code, r.stdout)
	}
	if got := readUsageCount(t, b); got != before {
		t.Fatalf("目标文件被写入了使用记录：%d -> %d", before, got)
	}
}

// 申请使用的留痕必须写在本次选中的目标文件中（拒绝也留痕）。
func TestUseDecisionRecordedInTarget(t *testing.T) {
	dir := chdirTemp(t)
	a := filepath.Join(dir, "台账 甲.json")
	b := filepath.Join(dir, "台账 乙.json")
	if r := runArgs(t, registerArgs(a, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if r := runArgs(t, registerArgs(b, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 新登记器具为待校准、无证书：拒绝使用（退出 1），但必须留痕。
	r := runArgs(t, "use", "--id", "M-1", "-f", a)
	if r.code != 1 || !strings.Contains(r.stderr, "拒绝") {
		t.Fatalf("a 应拒绝使用并说明原因，code=%d stderr=%q", r.code, r.stderr)
	}
	if readUsageCount(t, a) != 1 {
		t.Fatal("拒绝留痕没有写入目标文件 a")
	}
	if fileExists(t, b) && readUsageCount(t, b) != 0 {
		t.Fatal("留痕误入另一份台账 b")
	}
}

// 目标文件写入失败时报告目标路径并退出 2。
func TestTargetWriteFailureReported(t *testing.T) {
	dir := chdirTemp(t)
	// blocker 是普通文件，其下无法再创建目录与台账。
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "ledger.json")
	r := runArgs(t, "register", "--id", "M-1", "--name", "n",
		"--allowed", "1", "-f", target)
	if r.code != 2 {
		t.Fatalf("写入失败应退出 2，得到 %d", r.code)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("应明确报告目标文件 %s，stderr=%q", target, r.stderr)
	}
}

// --json 放在子命令之后仍生效，输出字段沿用原命令。
func TestJSONAfterSubcommand(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "data.json")
	reg := runArgs(t, "register", "--id", "M-1", "--name", "万用表",
		"--allowed", "0.5", "--json", "-f", target)
	if reg.code != 0 {
		t.Fatal(reg.stderr)
	}
	var got struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.Unmarshal([]byte(reg.stdout), &got); err != nil || !got.Accepted {
		t.Fatalf("register JSON 异常: %v", err)
	}

	lr := runArgs(t, "list", "-f", target, "--json")
	var listed struct {
		Count       int `json:"count"`
		Instruments []struct {
			ID string `json:"id"`
		} `json:"instruments"`
	}
	if err := json.Unmarshal([]byte(lr.stdout), &listed); err != nil {
		t.Fatalf("list JSON 解析失败: %v\n%s", err, lr.stdout)
	}
	if listed.Count != 1 || len(listed.Instruments) != 1 || listed.Instruments[0].ID != "M-1" {
		t.Fatalf("list JSON 内容不符：%+v", listed)
	}
}

// certJSON 是解析 cert --json 输出用的结构。
type certJSON struct {
	Accepted    bool `json:"accepted"`
	Duplicate   bool `json:"duplicate"`
	Certificate struct {
		Number  string `json:"number"`
		Method  string `json:"method"`
		Summary string `json:"summary"`
	} `json:"certificate"`
}

func parseCertJSON(t *testing.T, stdout string) certJSON {
	t.Helper()
	var got certJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("解析 cert JSON 失败: %v\n%s", err, stdout)
	}
	return got
}

// 证书字段的值即使恰好是 --json，也必须按文字保存：不改变输出格式，
// 不丢失字段，不影响录入成功。
func TestCertFieldValueThatLooksLikeJSONFlag(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// --summary 与 --json 分开传入：--json 是 --summary 的值，不是全局开关。
	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "JJF", "--error", "0.1", "--summary", "--json")
	if r.code != 0 {
		t.Fatalf("摘要为 --json 时应录入成功，code=%d stderr=%s", r.code, r.stderr)
	}
	if strings.HasPrefix(r.stdout, "{") {
		t.Fatal("未另行开启 JSON 输出时应保持普通文字输出")
	}
	if !strings.Contains(r.stdout, "已录入新证书") {
		t.Fatalf("普通文字输出内容异常：%q", r.stdout)
	}

	// 开启 --json 核对：摘要文字必须完整出现在证书内容里。
	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rj.code != 0 {
		t.Fatal(rj.stderr)
	}
	var review struct {
		History []struct {
			Number  string `json:"number"`
			Summary string `json:"summary"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.History) != 1 || review.History[0].Summary != "--json" {
		t.Fatalf("摘要文字未完整保存：%+v", review.History)
	}
}

// 校准方法的值即使恰好是 -f备用.json，也必须按文字保存：不改变目标台账，
// 不创建多余文件。分开传入与等号连传应得到相同的证书内容。
func TestCertFieldValueThatLooksLikeFileFlag(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// 分开传入：--method 的值是 -f备用.json，不得被剥成全局文件参数。
	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "-f备用.json", "--error", "0.1", "--summary", "摘要")
	if r.code != 0 {
		t.Fatalf("方法为 -f备用.json 时应录入成功，code=%d stderr=%s", r.code, r.stderr)
	}
	if fileExists(t, filepath.Join(dir, "备用.json")) {
		t.Fatal("字段值中的 -f 不得改变目标台账，不应创建备用.json")
	}

	// 等号连传：--method=-f备用.json 与分开传入内容一致。
	r2 := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-2",
		"--cal-date", "2026-09-02", "--expiry", "2027-09-02",
		"--method=-f备用.json", "--error", "0.2", "--summary", "摘要")
	if r2.code != 0 {
		t.Fatalf("等号连传应录入成功，code=%d stderr=%s", r2.code, r2.stderr)
	}

	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		History []struct {
			Number string `json:"number"`
			Method string `json:"method"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range review.History {
		got[c.Number] = c.Method
	}
	if got["C-1"] != "-f备用.json" || got["C-2"] != "-f备用.json" {
		t.Fatalf("分开传入与等号连传的方法内容应一致：%+v", got)
	}
}

// 字段值中的中文、内部空格、等号、开头横线都必须原样保留，首尾空白沿用
// 现有处理（trim 后判空、保存 trim 后的内容）。
func TestCertFieldValueTextPreservation(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	method := "JJF 规范 = 等号"
	summary := "--开头 横线 含 空格 = 等号"
	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", method, "--error", "0.1", "--summary", summary)
	if r.code != 0 {
		t.Fatalf("录入失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 首尾空白：传入带首尾空白的值，保存 trim 后的内容。
	r2 := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-2",
		"--cal-date", "2026-09-02", "--expiry", "2027-09-02",
		"--method", "  "+method+"  ", "--error", "0.2",
		"--summary", "  "+summary+"  ")
	if r2.code != 0 {
		t.Fatalf("带首尾空白录入失败 code=%d stderr=%s", r2.code, r2.stderr)
	}

	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		History []struct {
			Number  string `json:"number"`
			Method  string `json:"method"`
			Summary string `json:"summary"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	for _, c := range review.History {
		if c.Number == "C-1" {
			if c.Method != method || c.Summary != summary {
				t.Fatalf("文字未原样保留：method=%q summary=%q", c.Method, c.Summary)
			}
		}
		if c.Number == "C-2" {
			if c.Method != method || c.Summary != summary {
				t.Fatalf("首尾空白应 trim 后保存：method=%q summary=%q", c.Method, c.Summary)
			}
		}
	}
}

// 真正作为命令设置的 -f/--file 和 --json 仍可穿插在已填写完整的证书字段
// 之间，不影响字段值，也不改变目标台账与输出格式。
func TestGenuineGlobalFlagsInterspersedBetweenFields(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// -f 与 --json 穿插在完整字段之间：-f 选中目标，--json 开启 JSON 输出。
	r := runArgs(t, "cert",
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "JJF", "-f", target, "--error", "0.1",
		"--summary", "摘要", "--json")
	if r.code != 0 {
		t.Fatalf("穿插全局参数时录入失败 code=%d stderr=%s", r.code, r.stderr)
	}
	got := parseCertJSON(t, r.stdout)
	if !got.Accepted || got.Certificate.Number != "C-1" {
		t.Fatalf("JSON 输出内容异常：%+v", got)
	}
	if got.Certificate.Method != "JJF" || got.Certificate.Summary != "摘要" {
		t.Fatalf("字段值被全局参数干扰：%+v", got.Certificate)
	}
}

// 需要值的证书参数放在命令末尾却没有值时，明确报参数错误并退出 2，
// 不创建证书或改动台账。
func TestCertFlagMissingValueAtEnd(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "JJF", "--error", "0.1", "--summary")
	if r.code != 2 {
		t.Fatalf("末尾缺值应退出 2，得到 %d", r.code)
	}
	if fileExists(t, filepath.Join(dir, "ledger.json.bak")) {
		t.Fatal("参数错误不应改动台账")
	}
	// 台账中不应出现任何证书。
	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		History []json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.History) != 0 {
		t.Fatal("缺值错误不应创建证书")
	}
}

// 必填字段传入空字符串或只有空白时，明确报参数错误并退出 2。
func TestCertBlankFieldValueRejected(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	cases := [][]string{
		{"--summary", ""},
		{"--summary", "   "},
		{"--method", ""},
		{"--method", "\t"},
	}
	for i, c := range cases {
		args := []string{"cert", "-f", target,
			"--instrument", "M-1", "--number", "C-1",
			"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
			"--method", "JJF", "--error", "0.1", "--summary", "摘要"}
		// 用当前用例替换对应字段的值。
		flag := c[0]
		for j := 0; j < len(args)-1; j++ {
			if args[j] == flag {
				args[j+1] = c[1]
			}
		}
		r := runArgs(t, args...)
		if r.code != 2 {
			t.Fatalf("用例 %d 应退出 2，得到 %d（%v）", i, r.code, c)
		}
	}
}

// 有效的非空字段值即使长得像选项，也不能被判为缺值：--summary --json
// 应录入成功，而不是报“缺少 --summary 的值”。
func TestCertValidValueThatLooksLikeOptionNotMissing(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	// --summary --json：--json 是值，录入成功。
	r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "JJF", "--error", "0.1", "--summary", "--json")
	if r.code != 0 {
		t.Fatalf("长得像选项的有效值不应判为缺值，code=%d stderr=%s", r.code, r.stderr)
	}

	// --method -f备用.json：-f备用.json 是值，录入成功。
	r2 := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-2",
		"--cal-date", "2026-09-02", "--expiry", "2027-09-02",
		"--method", "-f备用.json", "--error", "0.2", "--summary", "摘要")
	if r2.code != 0 {
		t.Fatalf("长得像文件选项的有效值不应判为缺值，code=%d stderr=%s", r2.code, r2.stderr)
	}
}

// 退出后重新打开同一台账，字段中长得像选项的文字保持一致。
func TestCertFieldValuePersistsAcrossReopen(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "-f备用.json", "--error", "0.1", "--summary", "--json")

	// 重新打开：用另一条命令读取同一文件，内容必须一致。
	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		History []struct {
			Number  string `json:"number"`
			Method  string `json:"method"`
			Summary string `json:"summary"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.History) != 1 {
		t.Fatalf("重开后证书数量异常：%d", len(review.History))
	}
	c := review.History[0]
	if c.Method != "-f备用.json" || c.Summary != "--json" {
		t.Fatalf("重开后字段内容不一致：method=%q summary=%q", c.Method, c.Summary)
	}
}

// 同号且业务内容一致时返回原证书，不增加历史；同号内容不同仍拒绝覆盖。
// 字段值中长得像选项的文字不影响这两条判断。
func TestCertIdempotencyAndConflictWithOptionLikeValues(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatal(r.stderr)
	}

	base := []string{
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "-f备用.json", "--error", "0.1", "--summary", "--json",
	}
	args := append([]string{"cert", "-f", target}, base...)
	if r := runArgs(t, args...); r.code != 0 {
		t.Fatalf("首次录入失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 同号同内容：幂等返回原证书，历史不增加。
	args2 := append([]string{"cert", "-f", target, "--json"}, base...)
	r2 := runArgs(t, args2...)
	if r2.code != 0 {
		t.Fatalf("幂等录入失败 code=%d stderr=%s", r2.code, r2.stderr)
	}
	got := parseCertJSON(t, r2.stdout)
	if !got.Duplicate {
		t.Fatal("同号同内容应返回原证书（duplicate=true）")
	}

	// 同号不同内容：冲突拒绝，不改动已有数据。
	conflict := append([]string{"cert", "-f", target},
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-01", "--expiry", "2027-09-01",
		"--method", "不同方法", "--error", "0.1", "--summary", "--json")
	r3 := runArgs(t, conflict...)
	if r3.code != 1 {
		t.Fatalf("同号不同内容应冲突退出 1，得到 %d", r3.code)
	}

	// 历史仍只有一张证书，内容未被改动。
	rj := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		History []struct {
			Number  string `json:"number"`
			Method  string `json:"method"`
			Summary string `json:"summary"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.History) != 1 {
		t.Fatalf("冲突不应改动历史，现有 %d 张", len(review.History))
	}
	c := review.History[0]
	if c.Method != "-f备用.json" || c.Summary != "--json" {
		t.Fatalf("已有证书内容被改动：method=%q summary=%q", c.Method, c.Summary)
	}
}
