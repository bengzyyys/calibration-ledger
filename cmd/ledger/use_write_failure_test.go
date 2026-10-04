package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useArgs 构造一条使用申请命令。
func useArgs(path, id string, asJSON bool) []string {
	args := []string{"use", "-f", path, "--id", id}
	if asJSON {
		args = append(args, "--json")
	}
	return args
}

// reviewRejections 读取按器具核对 JSON 中的被拒绝使用记录条数。
func reviewRejections(t *testing.T, path, id string) int {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var view struct {
		Rejections []json.RawMessage `json:"rejections"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, r.stdout)
	}
	return len(view.Rejections)
}

// TestUseSaveFailureIsFileErrorAndRecoverable 验证命令行使用申请在台账文件
// 无法写入或替换时按文件读写错误报告：普通输出与 --json 都退出码 2，不宣称
// 已记录本次申请，也不输出成已留痕的业务拒绝；同一台账随后的查询与核对只看
// 到此前成功保存的记录。恢复可写后重新申请才真正留痕。
func TestUseSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：把子目录改成只读即可挡住写临时文件与改名替换，
	// 已存在的台账文件本身仍可读，查询不受影响。
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")

	// M-1：在用 + 合格未到期证书（业务上获准）；M-2：停用 + 合格未到期证书
	// （业务上拒绝）。两种业务结论在保存失败时都必须报文件读写错误。
	for _, id := range []string{"M-1", "M-2"} {
		if r := runArgs(t, registerArgs(target, id)...); r.code != 0 {
			t.Fatalf("登记 %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
		}
		if r := runArgs(t, certForArgs(target, id, "C-"+id, "2026-09-01")...); r.code != 0 {
			t.Fatalf("%s 录证书失败 code=%d stderr=%s", id, r.code, r.stderr)
		}
	}
	if r := runArgs(t, statusArgs(target, "M-1", "在用", false)...); r.code != 0 {
		t.Fatalf("M-1 切换在用应成功 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, statusArgs(target, "M-2", "在用", false)...); r.code != 0 {
		t.Fatalf("M-2 切换在用应成功 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, statusArgs(target, "M-2", "停用", false)...); r.code != 0 {
		t.Fatalf("M-2 停用应成功 code=%d stderr=%s", r.code, r.stderr)
	}

	// 先留一条成功保存的获准记录，作为失败申请不得改动的既有历史。
	if r := runArgs(t, useArgs(target, "M-1", false)...); r.code != 0 {
		t.Fatalf("M-1 首次申请应成功 code=%d stderr=%s", r.code, r.stderr)
	}
	if got := readUsageCount(t, target); got != 1 {
		t.Fatalf("既有历史应有 1 条记录，得到 %d", got)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// M-1 业务上获准但写盘失败：退出码 2，不输出已记录信息。
	r := runArgs(t, useArgs(target, "M-1", false)...)
	if r.code != 2 {
		t.Fatalf("使用申请写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已记录") {
		t.Fatalf("写盘失败不得宣称已记录本次申请：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出码 2，且不输出 recorded:true 的成功结果。
	rj := runArgs(t, useArgs(target, "M-1", true)...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败也应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"recorded": true`) ||
		strings.Contains(rj.stdout, `"allowed": true`) {
		t.Fatalf("--json 写盘失败不得输出已提交成功的结果：stdout=%q", rj.stdout)
	}

	// M-2 业务上拒绝（停用）但写盘失败：同样退出 2，不能输出成已留痕的
	// 业务拒绝（退出码 1 + 已记录拒绝）。
	rr := runArgs(t, useArgs(target, "M-2", false)...)
	if rr.code != 2 {
		t.Fatalf("业务拒绝的器具在写盘失败时仍应退出 2，得到 %d", rr.code)
	}
	if strings.Contains(rr.stdout, "已记录") || strings.Contains(rr.stderr, "已记录") {
		t.Fatalf("写盘失败不得宣称已记录本次拒绝：stdout=%q stderr=%q", rr.stdout, rr.stderr)
	}
	rrj := runArgs(t, useArgs(target, "M-2", true)...)
	if rrj.code != 2 {
		t.Fatalf("--json 模式下业务拒绝的器具写盘失败也应退出 2，得到 %d", rrj.code)
	}
	if strings.Contains(rrj.stdout, `"recorded": true`) {
		t.Fatalf("--json 写盘失败不得输出已留痕的拒绝结果：stdout=%q", rrj.stdout)
	}

	// 只读目录下台账文件仍可读：既有历史保持 1 条，M-2 没有被拒绝记录。
	if got := readUsageCount(t, target); got != 1 {
		t.Fatalf("失败的申请进入了文件历史（应仍为 1 条），得到 %d", got)
	}
	if got := reviewRejections(t, target, "M-1"); got != 0 {
		t.Fatalf("M-1 不应出现被拒绝记录，得到 %d 条", got)
	}
	if got := reviewRejections(t, target, "M-2"); got != 0 {
		t.Fatalf("M-2 失败的拒绝申请不应留痕，得到 %d 条", got)
	}

	// 恢复目录可写：重新申请才真正留痕，各新增一条。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, useArgs(target, "M-1", false)...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已记录") {
		t.Fatalf("恢复后 M-1 申请应成功留痕：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	rej := runArgs(t, useArgs(target, "M-2", false)...)
	if rej.code != 1 || !strings.Contains(rej.stderr, "已记录本次拒绝") {
		t.Fatalf("恢复后 M-2 申请应按业务拒绝退出 1 并留痕：code=%d stdout=%q stderr=%s",
			rej.code, rej.stdout, rej.stderr)
	}
	if got := readUsageCount(t, target); got != 3 {
		t.Fatalf("恢复后应只有 3 条成功保存的记录，得到 %d", got)
	}
	if got := reviewRejections(t, target, "M-2"); got != 1 {
		t.Fatalf("M-2 恢复后应只有重新申请的一条拒绝记录，得到 %d 条", got)
	}
}
