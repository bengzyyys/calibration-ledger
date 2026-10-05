package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registerCmd 构造一条器具登记命令。
func registerCmd(path, id, name, allowed string, asJSON bool) []string {
	args := []string{"register", "-f", path,
		"--id", id, "--name", name, "--allowed", allowed}
	if asJSON {
		args = append(args, "--json")
	}
	return args
}

// listIDs 通过 list --json 读取当前台账中的全部器具编号。
func listIDs(t *testing.T, path string) []string {
	t.Helper()
	r := runArgs(t, "list", "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("list 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	var got struct {
		Instruments []struct {
			ID string `json:"id"`
		} `json:"instruments"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("解析 list JSON 失败: %v\n%s", err, r.stdout)
	}
	ids := make([]string, 0, len(got.Instruments))
	for _, in := range got.Instruments {
		ids = append(ids, in.ID)
	}
	return ids
}

// TestRegisterSaveFailureIsFileErrorAndRecoverable 验证命令行登记在台账文件
// 无法写入或替换时按文件读写错误报告：普通输出与 --json 模式都退出码 2，
// 不输出登记成功或 accepted:true；失败器具不在列表中、按编号核对报不存在，
// 再次提交同号继续报保存错误（不会当成编号重复）。恢复可写后重新提交合法
// 内容才真正新增，且成功后同号仍被拒绝。
func TestRegisterSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：把子目录改成只读即可挡住写临时文件与改名替换，
	// 已存在的台账文件本身仍可读，查询不受影响。
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("准备台账失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法登记写盘失败：退出码 2，错误指出台账路径，不输出登记成功。
	r := runArgs(t, registerCmd(target, "M-2", "示波器", "0.25", false)...)
	if r.code != 2 {
		t.Fatalf("登记写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已登记器具") {
		t.Fatalf("写盘失败不得输出登记成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出码 2，且不输出 accepted:true。
	rj := runArgs(t, registerCmd(target, "M-2", "示波器", "0.25", true)...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败也应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"accepted": true`) ||
		strings.Contains(rj.stdout, `"instrument"`) {
		t.Fatalf("--json 写盘失败不得输出成功结果：stdout=%q", rj.stdout)
	}

	// 文件仍不可写时再次提交同号合法内容：仍退出 2，而不是当成编号重复退出 1。
	r2 := runArgs(t, registerCmd(target, "M-2", "示波器", "0.25", false)...)
	if r2.code != 2 {
		t.Fatalf("再次提交失败登记应仍退出 2，得到 %d", r2.code)
	}

	// 文件不可写时，原本不合法的申请仍按原有业务或参数错误处理，不是保存错误。
	// 已保存编号不能覆盖：业务拒绝退出 1。
	dup := runArgs(t, registerCmd(target, "M-1", "覆盖者", "9", true)...)
	if dup.code != 1 || !strings.Contains(dup.stdout, `"accepted": false`) {
		t.Fatalf("已保存编号应业务拒绝退出 1：code=%d stdout=%q stderr=%s",
			dup.code, dup.stdout, dup.stderr)
	}
	// 负误差属于业务校验失败：退出 1。
	neg := runArgs(t, registerCmd(target, "M-3", "负误差", "-0.01", false)...)
	if neg.code != 1 {
		t.Fatalf("负误差应按业务拒绝退出 1，得到 %d（stderr=%s）", neg.code, neg.stderr)
	}
	// 非有限数字属于参数错误：退出 2。
	nan := runArgs(t, registerCmd(target, "M-3", "非有限", "NaN", false)...)
	if nan.code != 2 {
		t.Fatalf("非有限数字应按参数错误退出 2，得到 %d", nan.code)
	}
	// 缺少必填参数属于参数错误：退出 2。
	miss := runArgs(t, "register", "-f", target, "--id", "M-3", "--allowed", "1")
	if miss.code != 2 {
		t.Fatalf("缺少参数应退出 2，得到 %d", miss.code)
	}

	// 失败后继续查询同一台账：列表与登记前一致，按失败编号核对报不存在。
	if ids := listIDs(t, target); len(ids) != 1 || ids[0] != "M-1" {
		t.Fatalf("列表应与登记前一致（只有 M-1）：%v", ids)
	}
	rv := runArgs(t, "review", "--id", "M-2", "-f", target, "--json")
	if rv.code != 1 || !strings.Contains(rv.stdout, `"found": false`) {
		t.Fatalf("失败编号核对应报器具不存在（退出 1）：code=%d stdout=%q", rv.code, rv.stdout)
	}
	// 失败器具不能申请使用：仍是编号不存在的业务错误（退出 1），且不留使用记录。
	before := readUsageCount(t, target)
	ru := runArgs(t, "use", "--id", "M-2", "-f", target)
	if ru.code != 1 {
		t.Fatalf("失败器具不能申请使用，应报不存在退出 1，得到 %d", ru.code)
	}
	if got := readUsageCount(t, target); got != before {
		t.Fatalf("对失败器具的使用申请不得留痕：%d -> %d", before, got)
	}

	// 恢复目录可写：先完成一次正常的状态切换，失败器具不能被顺带写入。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "停用"); r.code != 0 {
		t.Fatalf("恢复后正常操作应能保存: %s", r.stderr)
	}
	if ids := listIDs(t, target); len(ids) != 1 || ids[0] != "M-1" {
		t.Fatalf("正常保存不得夹带失败器具：%v", ids)
	}

	// 重新提交该编号且内容合法：才真正新增一件待校准器具；名称与允许误差取
	// 本次提交（与失败那次不同）。
	ok := runArgs(t, registerCmd(target, "M-2", "新示波器", "0.75", false)...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已登记器具 M-2") ||
		!strings.Contains(ok.stdout, "待校准") {
		t.Fatalf("恢复后登记应成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	rev := runArgs(t, "review", "--id", "M-2", "-f", target, "--json")
	if rev.code != 0 {
		t.Fatalf("成功登记后应能核对：code=%d stderr=%s", rev.code, rev.stderr)
	}
	var view struct {
		Instrument struct {
			Name         string  `json:"name"`
			AllowedError float64 `json:"allowed_error"`
			Status       string  `json:"status"`
		} `json:"instrument"`
	}
	if err := json.Unmarshal([]byte(rev.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rev.stdout)
	}
	if view.Instrument.Name != "新示波器" || view.Instrument.AllowedError != 0.75 ||
		view.Instrument.Status != "待校准" {
		t.Fatalf("成功登记内容应取本次提交：%+v", view.Instrument)
	}

	// 成功后再次登记同号仍按现有规则拒绝（退出 1）。
	again := runArgs(t, registerCmd(target, " M-2 ", "再一个", "1", true)...)
	if again.code != 1 || !strings.Contains(again.stdout, `"accepted": false`) {
		t.Fatalf("成功登记后同号应被拒绝退出 1：code=%d stdout=%q stderr=%s",
			again.code, again.stdout, again.stderr)
	}
	if ids := listIDs(t, target); len(ids) != 2 {
		t.Fatalf("重复登记不应新增器具：%v", ids)
	}
}

// TestRegisterSaveFailureOnBrandNewLedger 验证首次登记一份尚不存在的台账时
// 写盘失败：失败后查询仍是空台账、不创建文件；恢复后重新提交才创建文件。
func TestRegisterSaveFailureOnBrandNewLedger(t *testing.T) {
	dir := chdirTemp(t)
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")

	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	r := runArgs(t, registerCmd(target, "M-1", "万用表", "0.5", false)...)
	if r.code != 2 || !strings.Contains(r.stderr, target) {
		t.Fatalf("首次登记写盘失败应退出 2 并指出台账路径：code=%d stderr=%q",
			r.code, r.stderr)
	}
	rj := runArgs(t, registerCmd(target, "M-1", "万用表", "0.5", true)...)
	if rj.code != 2 || strings.Contains(rj.stdout, `"accepted": true`) {
		t.Fatalf("--json 首次登记失败应退出 2 且无成功结果：code=%d stdout=%q",
			rj.code, rj.stdout)
	}

	// 只读目录下台账文件未被创建；查询按空台账处理，不创建文件。
	if fileExists(t, target) {
		t.Fatal("登记失败不应创建台账文件")
	}
	lr := runArgs(t, "list", "-f", target)
	if lr.code != 0 || !strings.Contains(lr.stdout, "还没有器具") {
		t.Fatalf("失败后查询应为空台账：code=%d stdout=%q", lr.code, lr.stdout)
	}
	if fileExists(t, target) {
		t.Fatal("查询空台账不应创建文件")
	}
	// 文件仍不可写时再次提交同号：继续报保存错误，不能变成编号重复。
	if r2 := runArgs(t, registerCmd(target, "M-1", "万用表", "0.5", false)...); r2.code != 2 {
		t.Fatalf("再次提交应仍退出 2，得到 %d", r2.code)
	}

	// 恢复可写后重新提交：文件被创建，器具登记成功。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, registerCmd(target, "M-1", "万用表", "0", true)...)
	if ok.code != 0 || !strings.Contains(ok.stdout, `"accepted": true`) {
		t.Fatalf("恢复后零误差登记应成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if !fileExists(t, target) {
		t.Fatal("成功登记后应创建台账文件")
	}
	if ids := listIDs(t, target); len(ids) != 1 || ids[0] != "M-1" {
		t.Fatalf("成功登记后列表应只有 M-1：%v", ids)
	}
}
