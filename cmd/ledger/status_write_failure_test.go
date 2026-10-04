package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusSaveFailureIsFileErrorAndRecoverable 验证命令行状态切换在台账文件
// 无法写入时按文件读写错误报告、退出码 2（普通输出与 --json 一致）、不输出
// 已切换成功的结果；失败的目标状态不生效，文件仍不可写时再次提交同一状态仍
// 报保存错误，恢复可写后重新提交才真正生效。
func TestStatusSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：随后把子目录改成只读即可挡住写临时文件与改名替换，
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

	// 普通输出：保存失败退出码 2，不输出已切换成功的结果。
	r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用")
	if r.code != 2 {
		t.Fatalf("状态切换写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已切换") {
		t.Fatalf("写盘失败不得输出切换成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// JSON 输出：同样退出码 2，不输出 accepted=true 的成功结果。
	rj := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用", "--json")
	if rj.code != 2 {
		t.Fatalf("JSON 模式写盘失败应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"accepted": true`) {
		t.Fatalf("JSON 模式写盘失败不得输出成功结果：stdout=%q", rj.stdout)
	}

	// 文件仍不可写时再次提交相同目标状态：仍报保存错误，不能当成已生效。
	r2 := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用")
	if r2.code != 2 || strings.Contains(r2.stdout, "已切换") {
		t.Fatalf("再次提交应仍退出 2 且不报成功：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账文件可读：列表确认器具仍是登记时的待校准状态。
	lst := runArgs(t, "list", "-f", target, "--json")
	if lst.code != 0 {
		t.Fatalf("只读目录下查询应仍可进行：%s", lst.stderr)
	}
	var view struct {
		Instruments []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"instruments"`
	}
	if err := json.Unmarshal([]byte(lst.stdout), &view); err != nil {
		t.Fatalf("解析 list JSON 失败: %v\n%s", err, lst.stdout)
	}
	if len(view.Instruments) != 1 || view.Instruments[0].Status != "待校准" {
		t.Fatalf("失败切换不应改变列出的状态：%+v", view.Instruments)
	}

	// 恢复目录可写：重新提交同一切换才生效（无需关闭重开台账之外的任何清理）。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用")
	if ok.code != 0 || !strings.Contains(ok.stdout, "已切换为在用") {
		t.Fatalf("恢复后重新提交应成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "list", "-f", target, "--json")
	if !strings.Contains(after.stdout, "在用") {
		t.Fatalf("成功保存后列表应显示在用：%s", after.stdout)
	}
}
