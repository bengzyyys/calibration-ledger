package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// completeArgs 构造一条用证书完成计划的命令。
func completeArgs(path, number, certificate string) []string {
	return []string{"complete", "-f", path,
		"--number", number, "--certificate", certificate}
}

// certArgsFor 构造指定字段的录入证书命令；calDate 由用例给出，
// 有效期截止日固定取很远的未来以避开到期边界。
func certArgsFor(path, instrument, number, calDate, summary string) []string {
	return []string{"cert", "-f", path,
		"--instrument", instrument, "--number", number,
		"--cal-date", calDate, "--expiry", "2031-09-01",
		"--method", "规范A", "--error", "0.1", "--summary", summary}
}

// TestCompleteSaveFailureIsFileErrorAndRecoverable 验证命令行 complete 在台账
// 文件无法写入时按文件读写错误报告、退出码 2（普通输出与 --json 模式一致）、
// 不输出“已完成”或幂等返回原结果的成功信息；计划在文件、核对与待办中仍为
// 未完成。恢复可写后重新提交才真正完成并移出待办，完成时间取本次成功操作；
// 此后同证书重复完成才幂等返回原结果。
func TestCompleteSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：把子目录改成只读即可挡住写临时文件与改名替换，
	// 已存在的台账文件本身仍可读，查询不受影响。
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记器具失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, registerArgs(target, "M-2")...); r.code != 0 {
		t.Fatalf("登记第二件器具失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 证书校准日期取本机今天：完成规则要求不早于计划建立的本机日期，
	// 取今天保证完成请求能通过全部业务校验、真正走到写盘。
	today := time.Now().Format("2006-01-02")
	if r := runArgs(t, certArgsFor(target, "M-1", "C-1", today, "例行")...); r.code != 0 {
		t.Fatalf("录入证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 他器具证书用于验证只读目录下业务拒绝仍退出 1（校准日期不晚于今天即可）。
	if r := runArgs(t, certArgsFor(target, "M-2", "C-OTHER", today, "他器具")...); r.code != 0 {
		t.Fatalf("录入他器具证书失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法完成写盘失败：退出码 2，按文件读写错误报告，不显示“已完成”，
	// 错误信息指出台账文件。
	failArgs := completeArgs(target, "P-1", "C-1")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("完成写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已完成") ||
		strings.Contains(r.stdout, "返回原结果") {
		t.Fatalf("写盘失败不得输出完成成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出 2，且不能在标准输出给出 accepted 成功结果。
	rj := runArgs(t, append(append([]string{}, failArgs...), "--json")...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败也应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, `"accepted": true`) ||
		strings.Contains(rj.stdout, "已完成") {
		t.Fatalf("--json 写盘失败不得输出成功结果：stdout=%q", rj.stdout)
	}

	// 与业务拒绝保持区别：他器具证书即使目录只读也仍按业务拒绝退出 1，
	// 不触碰写盘；未知证书同样退出 1。
	if biz := runArgs(t, completeArgs(target, "P-1", "C-OTHER")...); biz.code != 1 {
		t.Fatalf("他器具证书应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}
	if biz := runArgs(t, completeArgs(target, "P-1", "C-NO")...); biz.code != 1 {
		t.Fatalf("未知证书应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交：仍退出 2，而不是幂等成功。
	if r2 := runArgs(t, failArgs...); r2.code != 2 ||
		strings.Contains(r2.stdout, "返回原结果") ||
		strings.Contains(r2.stdout, "已完成") {
		t.Fatalf("再次提交应仍退出 2 且不返回成功：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账可读：核对与待办仍显示计划未完成、无关联证书与完成时间。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	var view struct {
		Plans []struct {
			Number            string `json:"number"`
			Status            string `json:"status"`
			PlannedDate       string `json:"planned_date"`
			Marker            string `json:"marker"`
			CompletedAt       string `json:"completed_at"`
			CertificateNumber string `json:"certificate_number"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != "未完成" ||
		view.Plans[0].PlannedDate != "2030-10-10" ||
		view.Plans[0].Marker != "未到计划日" ||
		view.Plans[0].CompletedAt != "" || view.Plans[0].CertificateNumber != "" {
		t.Fatalf("核应对停留在完成前状态：%+v", view.Plans)
	}
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	var todos struct {
		Todos []struct {
			PlanNumber string `json:"plan_number"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv.stdout)
	}
	if len(todos.Todos) != 1 || todos.Todos[0].PlanNumber != "P-1" {
		t.Fatalf("失败完成后计划应仍在待办：%+v", todos.Todos)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的完成信息不能被顺带写入。
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
	if strings.Contains(string(raw), "completed_at") {
		t.Fatalf("失败完成被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 重新提交才真正完成：普通输出显示“已完成”，计划移出待办，核对展示
	// 关联证书与本次成功操作的完成时间。
	ok := runArgs(t, failArgs...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成：计划 P-1") ||
		strings.Contains(ok.stdout, "返回原结果") {
		t.Fatalf("恢复后完成应真正成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Plans []struct {
			Status            string `json:"status"`
			CompletedAt       string `json:"completed_at"`
			CertificateNumber string `json:"certificate_number"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if len(final.Plans) != 1 || final.Plans[0].Status != "已完成" ||
		final.Plans[0].CertificateNumber != "C-1" || final.Plans[0].CompletedAt == "" {
		t.Fatalf("核对应显示真正完成的信息：%+v", final.Plans)
	}
	successAt := final.Plans[0].CompletedAt
	if empty := runArgs(t, "todos", "-f", target, "--json"); !strings.Contains(empty.stdout, `"count": 0`) {
		t.Fatalf("完成后待办应为空：%s", empty.stdout)
	}

	// 真正保存成功后，同证书重复完成才幂等返回原结果，完成时间不变。
	dup := runArgs(t, append(append([]string{}, failArgs...), "--json")...)
	if dup.code != 0 || !strings.Contains(dup.stdout, `"idempotent": true`) {
		t.Fatalf("成功后同证书重复完成应幂等：code=%d stdout=%q", dup.code, dup.stdout)
	}
	var dupView struct {
		Plan struct {
			CompletedAt string `json:"completed_at"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(dup.stdout), &dupView); err != nil {
		t.Fatalf("解析幂等完成 JSON 失败: %v\n%s", err, dup.stdout)
	}
	if dupView.Plan.CompletedAt != successAt {
		t.Fatalf("重复完成刷新了完成时间：原 %s 现 %s", successAt, dupView.Plan.CompletedAt)
	}
}
