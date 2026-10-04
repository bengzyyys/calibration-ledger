package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUseSaveFailureIsFileErrorAndRecoverable 验证命令行申请使用在台账文件无法
// 写入时按文件读写错误报告：普通输出与 --json 模式都退出 2，不宣称已记录本次
// 申请，也不把这种情况输出成已留痕的业务拒绝（即使器具停用、无证书等拒绝原因
// 已经得出）。恢复可写后重新申请才留痕，且只新增重新提交的那一条记录。
func TestUseSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
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
	// M-1 满足可用条件（在用 + 合格未到期证书）；M-2 保持待校准、无证书，
	// 属于业务判断拒绝使用的情形。
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换状态失败: %s", r.stderr)
	}
	if r := runArgs(t, certArgs(target, "C-1", "2026-09-01", "规范A", "例行校准")...); r.code != 0 {
		t.Fatalf("录入证书失败: %s", r.stderr)
	}
	if r := runArgs(t, registerArgs(target, "M-2")...); r.code != 0 {
		t.Fatalf("登记 M-2 失败: %s", r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 本可获准的申请：保存失败退出 2，说明文件读写失败，不能宣称已记录。
	r := runArgs(t, "use", "--id", "M-1", "-f", target)
	if r.code != 2 {
		t.Fatalf("申请写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, "已记录") {
		t.Fatalf("写盘失败不得宣称已记录本次申请：stdout=%q stderr=%q", r.stdout, r.stderr)
	}

	// --json 模式同样退出 2，不能输出已提交成功或已留痕的结果。
	rj := runArgs(t, "use", "--id", "M-1", "-f", target, "--json")
	if rj.code != 2 {
		t.Fatalf("--json 写盘失败应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"recorded": true`) || strings.Contains(rj.stdout, `"allowed"`) {
		t.Fatalf("--json 不得输出已留痕的申请结果：%s", rj.stdout)
	}

	// 业务判断拒绝使用的情形（待校准且无证书）：即使拒绝原因已经得出，
	// 保存失败仍退出 2，不能输出成已留痕的业务拒绝（退出 1）。
	rb := runArgs(t, "use", "--id", "M-2", "-f", target)
	if rb.code != 2 {
		t.Fatalf("拒绝情形的保存失败应退出 2 而非业务拒绝，得到 %d", rb.code)
	}
	if strings.Contains(rb.stdout+rb.stderr, "已记录") {
		t.Fatalf("保存失败不得输出成已留痕的拒绝：stdout=%q stderr=%q", rb.stdout, rb.stderr)
	}
	rbj := runArgs(t, "use", "--id", "M-2", "-f", target, "--json")
	if rbj.code != 2 || strings.Contains(rbj.stdout, `"recorded": true`) {
		t.Fatalf("--json 拒绝情形的保存失败应退出 2 且不输出留痕结果：code=%d stdout=%q",
			rbj.code, rbj.stdout)
	}

	// 失败的申请没有进入文件。
	if got := readUsageCount(t, target); got != 0 {
		t.Fatalf("失败申请不应写入文件，得到 %d 条使用记录", got)
	}

	// 恢复目录可写：先完成一次正常的状态切换，失败申请不被顺带写入。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "停用"); r.code != 0 {
		t.Fatalf("恢复后正常操作应能保存: %s", r.stderr)
	}
	if got := readUsageCount(t, target); got != 0 {
		t.Fatalf("失败申请被随后成功的操作顺带写入：%d 条", got)
	}

	// 重新申请：器具已停用，按新的实际情况拒绝并留痕（退出 1），只新增一条。
	rej := runArgs(t, "use", "--id", "M-1", "-f", target)
	if rej.code != 1 || !strings.Contains(rej.stderr, "停用") {
		t.Fatalf("重新申请应按当前状态拒绝并留痕：code=%d stderr=%q", rej.code, rej.stderr)
	}
	if got := readUsageCount(t, target); got != 1 {
		t.Fatalf("重新申请应只新增一条记录，得到 %d 条", got)
	}

	// 恢复在用后重新申请获准：退出 0 且再增加一条。
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("恢复在用失败: %s", r.stderr)
	}
	ok := runArgs(t, "use", "--id", "M-1", "-f", target)
	if ok.code != 0 || !strings.Contains(ok.stdout, "允许使用") {
		t.Fatalf("恢复后获准申请应退出 0：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if got := readUsageCount(t, target); got != 2 {
		t.Fatalf("获准申请应再新增一条记录，得到 %d 条", got)
	}
}
