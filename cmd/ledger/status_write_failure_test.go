package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statusArgs 构造一条状态切换命令。
func statusArgs(path, id, status string, asJSON bool) []string {
	args := []string{"status", "-f", path, "--id", id, "--status", status}
	if asJSON {
		args = append(args, "--json")
	}
	return args
}

// certForArgs 为指定器具构造一条合格且未到期的证书录入命令。
func certForArgs(path, id, number, calDate string) []string {
	return []string{"cert", "-f", path,
		"--instrument", id, "--number", number,
		"--cal-date", calDate, "--expiry", "2027-09-01",
		"--method", "规范A", "--error", "0.1", "--summary", "例行校准"}
}

// reviewStatus 读取按器具核对 JSON 中的当前状态、能否使用与原因。
func reviewStatus(t *testing.T, path, id string) (status string, canUse bool, reasons []string) {
	t.Helper()
	r := runArgs(t, "review", "--id", id, "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("review %s 失败 code=%d stderr=%s", id, r.code, r.stderr)
	}
	var view struct {
		Instrument struct {
			Status string `json:"status"`
		} `json:"instrument"`
		CanUse  bool     `json:"can_use"`
		Reasons []string `json:"reasons"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, r.stdout)
	}
	return view.Instrument.Status, view.CanUse, view.Reasons
}

func reasonContains(reasons []string, sub string) bool {
	for _, r := range reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

// TestStatusSaveFailureIsFileErrorAndRecoverable 验证命令行状态切换在台账文件
// 无法写入或替换时按文件读写错误报告：普通输出与 --json 都退出码 2，不输出
// 已切换成功的结果；同一台账随后的核对仍按最后成功保存的状态判断，再次提交
// 相同目标状态也继续报保存错误。恢复可写后无需重开，重新提交成功才生效。
func TestStatusSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：把子目录改成只读即可挡住写临时文件与改名替换，
	// 已存在的台账文件本身仍可读，查询不受影响。
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")

	// M-1：登记（待校准）+ 合格未到期证书，始终未切换为在用。
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记 M-1 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, certForArgs(target, "M-1", "C-1", "2026-09-01")...); r.code != 0 {
		t.Fatalf("M-1 录证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// M-2：登记 + 合格未到期证书 + 成功切换为在用。
	if r := runArgs(t, registerArgs(target, "M-2")...); r.code != 0 {
		t.Fatalf("登记 M-2 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, certForArgs(target, "M-2", "C-2", "2026-09-02")...); r.code != 0 {
		t.Fatalf("M-2 录证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, statusArgs(target, "M-2", "在用", false)...); r.code != 0 {
		t.Fatalf("M-2 切换在用应成功 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// M-1 待校准 → 在用：写盘失败，退出码 2，不输出切换成功信息。
	r := runArgs(t, statusArgs(target, "M-1", "在用", false)...)
	if r.code != 2 {
		t.Fatalf("状态写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已切换为") {
		t.Fatalf("写盘失败不得输出切换成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出码 2，且不输出 accepted:true 的成功结果。
	rj := runArgs(t, statusArgs(target, "M-1", "在用", true)...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败也应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"accepted": true`) ||
		strings.Contains(rj.stdout, "已切换为") {
		t.Fatalf("--json 写盘失败不得输出成功结果：stdout=%q", rj.stdout)
	}

	// 文件仍不可写时再次提交相同目标状态：仍退出 2，而不是当成“状态未变”
	// 直接成功。
	r2 := runArgs(t, statusArgs(target, "M-1", "在用", false)...)
	if r2.code != 2 {
		t.Fatalf("再次提交失败状态应仍退出 2，得到 %d", r2.code)
	}

	// M-2 在用 → 停用同样写盘失败，不能提前按停用限制使用。
	rr := runArgs(t, statusArgs(target, "M-2", "停用", false)...)
	if rr.code != 2 || strings.Contains(rr.stdout, "已切换为") {
		t.Fatalf("M-2 停用写盘失败应退出 2 且无成功输出：code=%d stdout=%q", rr.code, rr.stdout)
	}
	if rr2 := runArgs(t, statusArgs(target, "M-2", "停用", false)...); rr2.code != 2 {
		t.Fatalf("再次提交停用应仍退出 2，得到 %d", rr2.code)
	}

	// 只读目录下台账文件仍可读：核对按最后成功保存的状态判断。
	st1, can1, reasons1 := reviewStatus(t, target, "M-1")
	if st1 != "待校准" || can1 || !reasonContains(reasons1, "待校准") {
		t.Fatalf("M-1 应仍为待校准且不可使用：status=%s canUse=%v reasons=%v",
			st1, can1, reasons1)
	}
	st2, can2, reasons2 := reviewStatus(t, target, "M-2")
	if st2 != "在用" || !can2 || len(reasons2) != 0 {
		t.Fatalf("M-2 应仍为在用且可使用，不能提前受限：status=%s canUse=%v reasons=%v",
			st2, can2, reasons2)
	}

	// 恢复目录可写：无需删除任何残留，重新提交才真正生效。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, statusArgs(target, "M-1", "在用", false)...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已切换为在用") {
		t.Fatalf("恢复后 M-1 切换在用应成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	off := runArgs(t, statusArgs(target, "M-2", "停用", false)...)
	if off.code != 0 || !strings.Contains(off.stdout, "已切换为停用") {
		t.Fatalf("恢复后 M-2 停用应成功：code=%d stdout=%q stderr=%s",
			off.code, off.stdout, off.stderr)
	}

	// 成功保存后核对按新状态判断。
	st1, can1, reasons1 = reviewStatus(t, target, "M-1")
	if st1 != "在用" || !can1 {
		t.Fatalf("M-1 成功切换后应为在用且可使用：status=%s canUse=%v reasons=%v",
			st1, can1, reasons1)
	}
	st2, can2, reasons2 = reviewStatus(t, target, "M-2")
	if st2 != "停用" || can2 || !reasonContains(reasons2, "停用") {
		t.Fatalf("M-2 成功停用后应不可使用且原因含停用：status=%s canUse=%v reasons=%v",
			st2, can2, reasons2)
	}

	// 请求状态等于最后成功保存的状态时直接成功，不要求再次写盘：目录重新
	// 只读也应返回成功。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	same := runArgs(t, statusArgs(target, "M-1", "在用", true)...)
	if same.code != 0 || !strings.Contains(same.stdout, `"accepted": true`) {
		t.Fatalf("重复提交当前状态应直接成功且不写盘：code=%d stdout=%q stderr=%s",
			same.code, same.stdout, same.stderr)
	}
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// 重新打开台账文件（新进程方式）能读到成功切换的状态。
	st1, can1, _ = reviewStatus(t, target, "M-1")
	st2, _, _ = reviewStatus(t, target, "M-2")
	if st1 != "在用" || st2 != "停用" {
		t.Fatalf("重开核对状态异常：M-1=%s M-2=%s", st1, st2)
	}
}
