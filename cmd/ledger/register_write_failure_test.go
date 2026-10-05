package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegisterSaveFailureIsFileErrorAndRecoverable 验证命令行登记器具在台账
// 文件无法写入时按文件读写错误报告：普通输出与 --json 都退出码 2、指出台账
// 路径、不输出登记成功或 accepted: true；失败的器具不进入列表、按编号核对
// 报告不存在，也不会被此后其他成功保存的操作顺带写入；恢复可写后重新提交
// 才正常登记，成功后再登记同号才按编号重复拒绝。
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
		t.Fatalf("登记 M-1 失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法登记写盘失败：退出码 2，按文件读写错误报告并指出台账文件，
	// 不输出“已登记器具”。
	failArgs := registerArgs(target, "M-2")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("登记写盘失败应按文件读写错误退出 2，得到 %d（stderr=%s）", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "已登记器具") {
		t.Fatalf("写盘失败不得输出登记成功：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}
	if strings.Contains(r.stderr, "已存在") {
		t.Fatalf("保存失败不能被说成编号重复：stderr=%q", r.stderr)
	}

	// --json 模式同样退出码 2，且不输出成功 JSON。
	jr := runArgs(t, append(append([]string{}, failArgs...), "--json")...)
	if jr.code != 2 {
		t.Fatalf("--json 写盘失败也应退出 2，得到 %d（stderr=%s）", jr.code, jr.stderr)
	}
	if strings.Contains(jr.stdout, `"accepted": true`) {
		t.Fatalf("--json 写盘失败不得输出 accepted: true：%s", jr.stdout)
	}

	// 文件仍不可写时再次提交同号：仍退出 2，而不是被上次失败留下的器具
	// 当成编号重复（退出 1）挡住。
	if r2 := runArgs(t, failArgs...); r2.code != 2 {
		t.Fatalf("再次提交应仍退出 2，得到 %d（stderr=%s）", r2.code, r2.stderr)
	}

	// 与业务拒绝保持区别：负数允许误差即使目录只读也按业务拒绝退出 1，
	// 不触碰写盘。
	biz := runArgs(t, "register", "-f", target, "--id", "M-3", "--name", "负误差表", "--allowed", "-0.1")
	if biz.code != 1 {
		t.Fatalf("负数允许误差应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 目录只读但台账可读：列表只有 M-1，按失败编号核对报告不存在（退出 1）。
	lr := runArgs(t, "list", "-f", target, "--json")
	if lr.code != 0 {
		t.Fatalf("只读目录下列表查询应可进行：%s", lr.stderr)
	}
	var listed struct {
		Instruments []struct {
			ID string `json:"id"`
		} `json:"instruments"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(lr.stdout), &listed); err != nil {
		t.Fatalf("解析 list JSON 失败: %v\n%s", err, lr.stdout)
	}
	if listed.Count != 1 || len(listed.Instruments) != 1 || listed.Instruments[0].ID != "M-1" {
		t.Fatalf("失败的器具不应出现在列表：%s", lr.stdout)
	}
	rv := runArgs(t, "review", "--id", "M-2", "-f", target)
	if rv.code != 1 {
		t.Fatalf("按失败编号核对应报告不存在（退出 1），得到 %d（stderr=%s）", rv.code, rv.stderr)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败登记的器具不能被顺带写入。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "停用"); st.code != 0 {
		t.Fatalf("恢复后正常操作应能保存：%s", st.stderr)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "M-2") {
		t.Fatalf("失败登记的器具被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 无需重新打开台账（每次调用都会打开同一文件）：重新提交同号且内容合法
	// 才正常新增一件待校准器具，普通输出与 --json 都给出成功结果。
	ok := runArgs(t, failArgs...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已登记器具 M-2") ||
		!strings.Contains(ok.stdout, "待校准") {
		t.Fatalf("恢复后重新提交应成功登记：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	okj := runArgs(t, append(append([]string{}, registerArgs(target, "M-3")...), "--json")...)
	if okj.code != 0 || !strings.Contains(okj.stdout, `"accepted": true`) {
		t.Fatalf("M-3 的登记 --json 应成功：code=%d stdout=%q", okj.code, okj.stdout)
	}

	lr2 := runArgs(t, "list", "-f", target, "--json")
	var listed2 struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(lr2.stdout), &listed2); err != nil {
		t.Fatalf("解析 list JSON 失败: %v\n%s", err, lr2.stdout)
	}
	if listed2.Count != 3 {
		t.Fatalf("成功登记后应有 3 件器具：%s", lr2.stdout)
	}

	// 成功登记后再提交同号，才按现有规则以编号重复拒绝（退出 1）。
	dup := runArgs(t, failArgs...)
	if dup.code != 1 || !strings.Contains(dup.stderr, "已存在") {
		t.Fatalf("成功保存后同号应业务拒绝退出 1：code=%d stderr=%q", dup.code, dup.stderr)
	}
}

// TestRegisterSaveFailureOnFreshLedger 验证首次登记新台账的场景：台账文件
// 尚不存在时合法登记遇到目录只读同样退出码 2，恢复可写后重新提交才建立
// 台账并登记成功。
func TestRegisterSaveFailureOnFreshLedger(t *testing.T) {
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

	args := registerArgs(target, "M-1")
	r := runArgs(t, args...)
	if r.code != 2 {
		t.Fatalf("新台账首次登记写盘失败应退出 2，得到 %d（stderr=%s）", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "已登记器具") || !strings.Contains(r.stderr, target) {
		t.Fatalf("首次登记失败不得输出成功且应指出台账路径：stdout=%q stderr=%q", r.stdout, r.stderr)
	}
	if fileExists(t, target) {
		t.Fatal("保存失败不应留下台账文件")
	}

	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, args...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已登记器具 M-1") {
		t.Fatalf("恢复后重新提交应成功登记：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
}
