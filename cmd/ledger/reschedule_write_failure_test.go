package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rescheduleArgs 构造一条计划改期命令的完整参数。
func rescheduleArgs(target, number, date, reason string) []string {
	return []string{"reschedule", "-f", target,
		"--number", number, "--date", date, "--reason", reason}
}

// planArgs 构造一条建立校准计划命令的完整参数。
func planArgs(target, instrument, number, date, note string) []string {
	return []string{"plan", "-f", target,
		"--instrument", instrument, "--number", number,
		"--date", date, "--note", note}
}

// TestRescheduleSaveFailureIsFileErrorAndRecoverable 验证命令行改期在台账
// 文件无法写入或替换时按文件读写错误报告、退出码 2、不输出“已改期”；失败的
// 改期既不进文件也不改变随后只读查询看到的计划日期与待办标记，并与日期、
// 原因不合规的业务拒绝（退出码 1）区分开。目录恢复可写后无需额外清理，重新
// 提交合法日期才改期成功，且只新增一条从最后成功保存日期起算的记录。
func TestRescheduleSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
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
	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10", "年度校准")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 先有一次成功改期：10-10 → 10-20。
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "实验室排期冲突")...); r.code != 0 {
		t.Fatalf("首次改期应成功 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法新日期与原因，但文件无法写入：按文件读写错误退出 2。
	args := rescheduleArgs(target, "P-1", "2030-11-05", "车间占用")
	r := runArgs(t, args...)
	if r.code != 2 {
		t.Fatalf("改期写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已改期") {
		t.Fatalf("写盘失败不得输出改期成功信息：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// 文件仍不可写时再次提交：仍退出 2。
	if r2 := runArgs(t, args...); r2.code != 2 {
		t.Fatalf("再次提交应仍退出 2，得到 %d", r2.code)
	}

	// 与业务拒绝区分：日期早于操作当天是业务校验错误，退出 1，而非 2。
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2000-01-01", "过去日期")...); r.code != 1 {
		t.Fatalf("早于当天的日期应业务拒绝退出 1，得到 %d（stderr=%s）", r.code, r.stderr)
	}

	// 目录只读但台账文件可读：按器具核对与待办查询仍只看到 10-20，
	// 失败的 11-05 不出现在任何位置。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下查询应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "2030-11-05") || strings.Contains(rv.stdout, "车间占用") {
		t.Fatalf("失败改期出现在核对结果中：%s", rv.stdout)
	}
	var view struct {
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Changes     []struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].PlannedDate != "2030-10-20" ||
		len(view.Plans[0].Changes) != 1 ||
		view.Plans[0].Changes[0].From != "2030-10-10" ||
		view.Plans[0].Changes[0].To != "2030-10-20" {
		t.Fatalf("核对中的计划应保持最后成功保存的状态：%+v", view.Plans)
	}
	tv := runArgs(t, "todos", "--instrument", "M-1", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	if strings.Contains(tv.stdout, "2030-11-05") {
		t.Fatalf("待办不得按失败提交的新日期变化：%s", tv.stdout)
	}

	// 恢复目录可写：重新提交合法改期到 11-08，成功改期（无需任何额外清理），
	// 只新增一条 10-20 → 11-08，历史中不得出现 11-05。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	ok := runArgs(t, rescheduleArgs(target, "P-1", "2030-11-08", "重新排期")...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已改期") ||
		!strings.Contains(ok.stdout, "第 2 次改期") {
		t.Fatalf("恢复后改期应成功且为第 2 次：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if strings.Contains(ok.stdout, "2030-11-05") {
		t.Fatalf("成功响应中不得夹带失败时提交的 11-05：%s", ok.stdout)
	}

	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Original    string `json:"original_date"`
			Status      string `json:"status"`
			Changes     []struct {
				From   string `json:"from"`
				To     string `json:"to"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析最终 review JSON: %v\n%s", err, after.stdout)
	}
	if len(final.Plans) != 1 {
		t.Fatalf("应仍只有一项计划：%+v", final.Plans)
	}
	p := final.Plans[0]
	if p.PlannedDate != "2030-11-08" || p.Original != "2030-10-10" || p.Status != "未完成" {
		t.Fatalf("成功改期后计划字段异常：%+v", p)
	}
	if len(p.Changes) != 2 ||
		p.Changes[0].From != "2030-10-10" || p.Changes[0].To != "2030-10-20" ||
		p.Changes[1].From != "2030-10-20" || p.Changes[1].To != "2030-11-08" ||
		p.Changes[1].Reason != "重新排期" {
		t.Fatalf("改期历史应保留第一条并新增 10-20→11-08：%+v", p.Changes)
	}
	for _, c := range p.Changes {
		if c.To == "2030-11-05" || c.From == "2030-11-05" {
			t.Fatalf("历史中不得出现失败时提交的 11-05：%+v", p.Changes)
		}
	}
}
