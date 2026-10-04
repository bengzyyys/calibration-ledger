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
	return []string{"cancel", "-f", path,
		"--number", number, "--reason", reason}
}

// TestCancelSaveFailureIsFileErrorAndRecoverable 验证命令行 cancel 在台账文件
// 无法写入时按文件读写错误报告、退出码 2（普通输出与 --json 模式一致）、
// 不显示“已取消”或声称计划已移出待办；失败后核对与待办中计划仍为未完成，
// 取消时间与原因不留痕、不被此后的成功写盘夹带。恢复可写后重新提交才真正
// 取消并移出待办，取消时间取本次成功操作。
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
	// 先成功改期一次 10-10 → 10-20：失败取消不能改动计划日期与改期历史。
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "实验室排期冲突")...); r.code != 0 {
		t.Fatalf("首次改期应成功 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法取消写盘失败：退出码 2，按文件读写错误报告，不显示“已取消”或
	// “不再列入待办”，错误信息指出台账文件。
	failArgs := cancelArgs(target, "P-1", "暂停送检")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("取消写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已取消") ||
		strings.Contains(r.stdout, "不再列入待办") {
		t.Fatalf("写盘失败不得显示取消成功信息：stdout=%q", r.stdout)
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
		strings.Contains(rj.stdout, "已取消") {
		t.Fatalf("--json 写盘失败不得输出成功结果：stdout=%q", rj.stdout)
	}

	// 与业务拒绝保持区别：空白原因在命令行参数层即退出 2（必填参数缺失），
	// 未知计划、已结束计划即使目录只读也仍按业务拒绝退出 1，不触碰写盘。
	if biz := runArgs(t, cancelArgs(target, "P-NO", "某原因")...); biz.code != 1 {
		t.Fatalf("未知计划应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交：仍退出 2，而不是因上一次失败把计划当成已结束。
	if r2 := runArgs(t, failArgs...); r2.code != 2 ||
		strings.Contains(r2.stdout, "已取消") ||
		strings.Contains(r2.stdout, "不再列入待办") {
		t.Fatalf("再次提交应仍退出 2 且不返回成功：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账可读：核对与待办仍显示计划未完成、无取消时间与原因，
	// 当前计划日期仍是最后成功保存的 2030-10-20、改期历史仍只有一条。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "暂停送检") {
		t.Fatalf("失败取消原因出现在核对结果中：%s", rv.stdout)
	}
	var view struct {
		Plans []struct {
			Number       string `json:"number"`
			Status       string `json:"status"`
			PlannedDate  string `json:"planned_date"`
			OriginalDate string `json:"original_date"`
			Marker       string `json:"marker"`
			CanceledAt   string `json:"canceled_at"`
			CancelReason string `json:"cancel_reason"`
			Changes      []struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != "未完成" ||
		view.Plans[0].PlannedDate != "2030-10-20" ||
		view.Plans[0].OriginalDate != "2030-10-10" ||
		view.Plans[0].Marker != "未到计划日" ||
		view.Plans[0].CanceledAt != "" || view.Plans[0].CancelReason != "" ||
		len(view.Plans[0].Changes) != 1 || view.Plans[0].Changes[0].To != "2030-10-20" {
		t.Fatalf("核对应停留在最后成功保存的状态：%+v", view.Plans)
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
		t.Fatalf("失败取消后计划应仍在待办：%+v", todos.Todos)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "暂停送检") ||
		strings.Contains(string(raw), "canceled_at") {
		t.Fatalf("失败取消信息已存在于台账文件：\n%s", raw)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的取消不能被顺带写入；停用器具的未结束计划仍允许取消。
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
	if strings.Contains(string(raw), "暂停送检") ||
		strings.Contains(string(raw), "canceled_at") ||
		strings.Contains(string(raw), `"status": "已取消"`) {
		t.Fatalf("失败取消被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 重新提交才真正取消：普通输出显示“已取消”，计划移出待办，核对展示
	// 本次成功操作的取消时间与原因；器具仍为停用（取消不改变器具状态）。
	ok := runArgs(t, failArgs...)
	if ok.code != 0 ||
		!strings.Contains(ok.stdout, "计划 P-1 已于") ||
		!strings.Contains(ok.stdout, "取消，原因：暂停送检") ||
		!strings.Contains(ok.stdout, "不再列入待办") {
		t.Fatalf("恢复后取消应真正成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Instrument struct {
			Status string `json:"status"`
		} `json:"instrument"`
		Plans []struct {
			Status       string `json:"status"`
			PlannedDate  string `json:"planned_date"`
			OriginalDate string `json:"original_date"`
			CanceledAt   string `json:"canceled_at"`
			CancelReason string `json:"cancel_reason"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if final.Instrument.Status != "停用" {
		t.Fatalf("取消不应改变器具状态：%s", final.Instrument.Status)
	}
	if len(final.Plans) != 1 || final.Plans[0].Status != "已取消" ||
		final.Plans[0].CancelReason != "暂停送检" ||
		final.Plans[0].CanceledAt == "" ||
		final.Plans[0].PlannedDate != "2030-10-20" ||
		final.Plans[0].OriginalDate != "2030-10-10" {
		t.Fatalf("核对应显示真正取消且保留原计划：%+v", final.Plans)
	}
	successAt := final.Plans[0].CanceledAt
	if empty := runArgs(t, "todos", "-f", target, "--json"); !strings.Contains(empty.stdout, `"count": 0`) {
		t.Fatalf("取消后待办应为空：%s", empty.stdout)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if !strings.Contains(string(raw), successAt) ||
		!strings.Contains(string(raw), "暂停送检") {
		t.Fatalf("文件应含成功取消的时间与原因：\n%s", raw)
	}

	// 真正保存成功后，再次取消按业务拒绝退出 1；计划编号也不能复用。
	again := runArgs(t, cancelArgs(target, "P-1", "再次取消")...)
	if again.code != 1 {
		t.Fatalf("已取消计划再次取消应业务拒绝退出 1，得到 %d（stdout=%q stderr=%s）",
			again.code, again.stdout, again.stderr)
	}
	reuse := runArgs(t, planArgs(target, "M-1", "P-1", "2031-01-01")...)
	if reuse.code != 1 {
		t.Fatalf("已取消计划编号不能复用，应业务拒绝退出 1，得到 %d（stderr=%s）",
			reuse.code, reuse.stderr)
	}
}
