package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// currentDay 返回本机今天的 YYYY-MM-DD，用于录入“不晚于今天且不早于计划
// 建立日期”的证书，避免命令行测试依赖固定运行日期。
func currentDay() string { return time.Now().Format("2006-01-02") }

// completeArgs 构造一条用证书完成计划的命令。
func completeArgs(path, number, certificate string) []string {
	return []string{"complete", "-f", path,
		"--number", number, "--certificate", certificate}
}

// TestCompleteSaveFailureIsFileErrorAndRecoverable 验证命令行完成计划在台账文件
// 无法写入时按文件读写错误报告、退出码 2；普通输出与 --json 模式都不输出
// “已完成”或返回原完成结果的成功信息。失败后核对、计划查询与待办仍显示计划
// 未完成，完成时间与关联证书保持空；再次提交仍退出 2 而不是幂等成功。恢复可写
// 后先做其他正常保存不会顺带写入失败的完成；重新提交才真正完成并从待办移除。
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
	// 证书校准日期取运行当天：不晚于本机今天，且不早于计划建立的本机日期。
	today := currentDay()
	certArgs := []string{"cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", today, "--expiry", "2030-09-01",
		"--method", "规范A", "--error", "0.1", "--summary", "例行校准"}
	if r := runArgs(t, certArgs...); r.code != 0 {
		t.Fatalf("录入证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
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
	if strings.Contains(r.stdout, "已完成") {
		t.Fatalf("写盘失败不得显示已完成：stdout=%q", r.stdout)
	}
	if r.stdout != "" {
		t.Fatalf("普通模式写盘失败不应有成功标准输出：%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出 2，且不能输出 accepted:true 的成功结果或原完成结果。
	rj := runArgs(t, append(append([]string{}, failArgs...), "--json")...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, "已完成") || strings.Contains(rj.stdout, `"accepted": true`) ||
		strings.Contains(rj.stdout, `"idempotent": true`) {
		t.Fatalf("--json 写盘失败不得输出成功完成信息：stdout=%q", rj.stdout)
	}

	// 与业务拒绝保持区别：证书编号不存在时即使目录只读也仍按业务拒绝退出 1，
	// 不触碰写盘。
	biz := runArgs(t, completeArgs(target, "P-1", "C-NO")...)
	if biz.code != 1 {
		t.Fatalf("未知证书应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交同一计划和同一证书：仍退出 2，而不是幂等成功。
	if r2 := runArgs(t, failArgs...); r2.code != 2 ||
		strings.Contains(r2.stdout, "已完成") || r2.stdout != "" {
		t.Fatalf("再次提交应仍退出 2 且无成功输出：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账可读：核对显示计划仍未完成、无完成时间与关联证书；
	// 待办中仍有该计划。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	var view struct {
		Plans []struct {
			Number            string `json:"number"`
			Status            string `json:"status"`
			Marker            string `json:"marker"`
			CompletedAt       string `json:"completed_at"`
			CertificateNumber string `json:"certificate_number"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != "未完成" ||
		view.Plans[0].CompletedAt != "" || view.Plans[0].CertificateNumber != "" {
		t.Fatalf("核对应显示计划仍未完成且无完成信息：%+v", view.Plans)
	}
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	var todos struct {
		Count int `json:"count"`
		Todos []struct {
			PlanNumber string `json:"plan_number"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv.stdout)
	}
	if todos.Count != 1 || len(todos.Todos) != 1 || todos.Todos[0].PlanNumber != "P-1" {
		t.Fatalf("失败后计划不应从待办消失：%+v", todos.Todos)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "completed_at") ||
		strings.Contains(string(raw), `"certificate_number"`) ||
		strings.Contains(string(raw), "已完成") {
		t.Fatalf("失败的完成已存在于台账文件：\n%s", raw)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的完成不能被顺带写入。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "停用"); st.code != 0 {
		t.Fatalf("恢复后正常操作应能保存：%s", st.stderr)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "completed_at") ||
		strings.Contains(string(raw), `"certificate_number"`) ||
		strings.Contains(string(raw), "已完成") {
		t.Fatalf("失败的完成被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 停用器具的计划仍可完成：重新提交才真正成功。
	ok := runArgs(t, failArgs...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成") ||
		!strings.Contains(ok.stdout, "P-1") || !strings.Contains(ok.stdout, "C-1") {
		t.Fatalf("恢复后完成应成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Plans []struct {
			Status            string `json:"status"`
			CertificateNumber string `json:"certificate_number"`
			CompletedAt       string `json:"completed_at"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if len(final.Plans) != 1 || final.Plans[0].Status != "已完成" ||
		final.Plans[0].CertificateNumber != "C-1" || final.Plans[0].CompletedAt == "" {
		t.Fatalf("成功后核对应显示已完成、关联证书与完成时间：%+v", final.Plans)
	}
	emptyTodos := runArgs(t, "todos", "-f", target, "--json")
	var et struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(emptyTodos.stdout), &et); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, emptyTodos.stdout)
	}
	if et.Count != 0 {
		t.Fatalf("成功后计划应从待办移除，仍有 %d 项", et.Count)
	}

	// 真正保存成功后，再用同一证书提交：返回原结果（幂等），退出码 0。
	dup := runArgs(t, failArgs...)
	if dup.code != 0 || !strings.Contains(dup.stdout, "返回原结果") {
		t.Fatalf("保存成功后同证书重复完成应返回原结果：code=%d stdout=%q", dup.code, dup.stdout)
	}
}
