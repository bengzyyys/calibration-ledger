package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cancelArgs 构造一条取消计划命令。
func cancelArgs(path, number, reason string) []string {
	return []string{"cancel", "-f", path, "--number", number, "--reason", reason}
}

// TestCancelSaveFailureIsFileErrorAndRecoverable 验证命令行取消计划在台账文件
// 无法写入时按文件读写错误报告、退出码 2（普通输出与 --json 一致）、不显示
// “已取消”；失败后同一台账里计划仍为未完成并留在待办，取消时间和原因不进文件、
// 不被此后的成功写盘夹带。恢复可写后重新提交才生效，取消时间取本次成功提交。
func TestCancelSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
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
	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法取消写盘失败：退出码 2，按文件读写错误报告，不显示“已取消”，
	// 错误信息指出台账文件。
	failArgs := cancelArgs(target, "P-1", "暂停送检")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("取消写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已取消") || strings.Contains(r.stdout, "不再列入待办") {
		t.Fatalf("写盘失败不得显示已取消：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// --json 模式同样退出码 2，不输出取消成功的结果。
	rj := runArgs(t, append(failArgs, "--json")...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, "已取消") || strings.Contains(rj.stdout, "\"accepted\": true") ||
		strings.Contains(rj.stdout, "\"accepted\":true") {
		t.Fatalf("--json 模式不得声称取消成功：stdout=%q", rj.stdout)
	}

	// 与业务拒绝保持区别：原因为空白仍按参数错误退出 2（未触碰写盘）；
	// 不存在的计划按业务拒绝退出 1。
	if biz := runArgs(t, cancelArgs(target, "NOPE", "x")...); biz.code != 1 {
		t.Fatalf("未知计划应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交：仍退出 2，而不是按“已结束”拒绝（退出 1）或成功。
	if r2 := runArgs(t, failArgs...); r2.code != 2 || strings.Contains(r2.stdout, "已取消") {
		t.Fatalf("再次提交应仍退出 2 且不显示已取消：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账可读：待办仍包含该计划，核对中的计划仍为未完成，
	// 没有取消时间和原因。
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	var todos struct {
		Todos []struct {
			PlanNumber  string `json:"plan_number"`
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv.stdout)
	}
	if len(todos.Todos) != 1 || todos.Todos[0].PlanNumber != "P-1" ||
		todos.Todos[0].PlannedDate != "2030-10-10" || todos.Todos[0].Marker != "未到计划日" {
		t.Fatalf("待办应仍包含未取消的计划：%+v", todos.Todos)
	}
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "暂停送检") {
		t.Fatalf("失败取消的原因出现在核对结果中：%s", rv.stdout)
	}
	var view struct {
		Plans []struct {
			Status       string `json:"status"`
			CanceledAt   string `json:"canceled_at"`
			CancelReason string `json:"cancel_reason"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != "未完成" ||
		view.Plans[0].CanceledAt != "" || view.Plans[0].CancelReason != "" {
		t.Fatalf("核对应展示尚未取消的计划：%+v", view.Plans)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "暂停送检") || strings.Contains(string(raw), "已取消") {
		t.Fatalf("失败取消已存在于台账文件：\n%s", raw)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的取消内容不能被顺带写入。
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
	if strings.Contains(string(raw), "暂停送检") || strings.Contains(string(raw), "已取消") {
		t.Fatalf("失败取消被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 停用器具的未结束计划仍允许取消：重新提交才成功，核对展示取消时间和原因，
	// 待办不再包含该计划；已取消计划不能再次取消或改期。
	ok := runArgs(t, cancelArgs(target, "P-1", "实验室长期停用")...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "取消") ||
		!strings.Contains(ok.stdout, "不再列入待办") {
		t.Fatalf("恢复后取消应成功：code=%d stdout=%q stderr=%s", ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Plans []struct {
			Status       string `json:"status"`
			CanceledAt   string `json:"canceled_at"`
			CancelReason string `json:"cancel_reason"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if len(final.Plans) != 1 || final.Plans[0].Status != "已取消" ||
		final.Plans[0].CanceledAt == "" || final.Plans[0].CancelReason != "实验室长期停用" {
		t.Fatalf("核对应展示本次成功的取消时间和原因：%+v", final.Plans)
	}
	tv2 := runArgs(t, "todos", "-f", target, "--json")
	if strings.Contains(tv2.stdout, "P-1") {
		t.Fatalf("成功取消后待办不应再包含该计划：%s", tv2.stdout)
	}
	if again := runArgs(t, cancelArgs(target, "P-1", "再次取消")...); again.code != 1 {
		t.Fatalf("已取消计划再次取消应业务拒绝退出 1，得到 %d", again.code)
	}
	if rs := runArgs(t, rescheduleArgs(target, "P-1", "2030-11-01", "还想改期")...); rs.code != 1 {
		t.Fatalf("已取消计划改期应业务拒绝退出 1，得到 %d", rs.code)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "暂停送检") {
		t.Fatalf("失败申请的原因最终仍被写入文件：\n%s", raw)
	}
}
