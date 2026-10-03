package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCertSaveFailureIsFileErrorAndRecoverable 验证命令行证书录入在台账文件
// 无法写入时按文件读写错误报告、退出码 2、不输出录入成功信息；失败的证书既
// 不进文件，再次提交也不会被当成重复证书跳过保存。目录恢复可写后，同号同内容
// 重新提交作为新证书正常录入，此后才恢复正常的幂等重复判定。
func TestCertSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
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

	args := certArgs(target, "C-1", "2026-09-01", "规范A", "例行校准")
	r := runArgs(t, args...)
	if r.code != 2 {
		t.Fatalf("证书写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已录入新证书") ||
		strings.Contains(r.stdout, "未增加历史") {
		t.Fatalf("写盘失败不得输出录入成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// 文件仍不可写时再次提交相同内容：仍报保存失败，而不是重复证书成功。
	r2 := runArgs(t, args...)
	if r2.code != 2 || strings.Contains(r2.stdout, "未增加历史") {
		t.Fatalf("再次提交应仍退出 2 且不是重复证书：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账文件可读：核对确认失败证书没有进入历史。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下查询应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "C-1") {
		t.Fatalf("失败证书出现在核对结果中：%s", rv.stdout)
	}

	// 恢复目录可写：同号同内容重新提交，作为新证书正常录入（无需任何额外清理）。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, args...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已录入新证书") {
		t.Fatalf("恢复后应作为新证书录入：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}

	// 真正保存成功后，才恢复原有的幂等重复规则，且历史只有一张。
	dup := runArgs(t, args...)
	if dup.code != 0 || !strings.Contains(dup.stdout, "未增加历史") {
		t.Fatalf("保存成功后同号同内容应幂等返回原证书：code=%d stdout=%q", dup.code, dup.stdout)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var view struct {
		History []struct {
			Number string `json:"number"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if len(view.History) != 1 || view.History[0].Number != "C-1" {
		t.Fatalf("历史中应只有一张 C-1：%+v", view.History)
	}
}
