package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rescheduleArgs 构造一条计划改期命令。
func rescheduleArgs(path, number, date, reason string) []string {
	return []string{"reschedule", "-f", path,
		"--number", number, "--date", date, "--reason", reason}
}

// planArgs 构造一条建立校准计划命令（日期取较远的未来，避免依赖运行当天）。
func planArgs(path, instrument, number, date string) []string {
	return []string{"plan", "-f", path,
		"--instrument", instrument, "--number", number,
		"--date", date, "--note", "周期校准"}
}

// TestRescheduleSaveFailureIsFileErrorAndRecoverable 验证命令行改期在台账文件
// 无法写入时按文件读写错误报告、退出码 2、不显示“已改期”；失败后无需重开台账，
// 核对、计划查询与待办仍使用最后成功保存的日期，失败日期不进文件、不被此后的
// 成功写盘夹带。恢复可写后重新提交才生效，只新增一条从最后成功日期出发的记录。
func TestRescheduleSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
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
	// 第一条成功改期：10-10 → 10-20。
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "实验室排期冲突")...); r.code != 0 {
		t.Fatalf("首次改期应成功 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法改期 10-20 → 11-05 写盘失败：退出码 2，按文件读写错误报告，
	// 不显示“已改期”，错误信息指出台账文件。
	failArgs := rescheduleArgs(target, "P-1", "2030-11-05", "实验室改造延期")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("改期写盘失败应按文件读写错误退出 2，得到 %d", r.code)
	}
	if strings.Contains(r.stdout, "已改期") {
		t.Fatalf("写盘失败不得显示已改期：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}

	// 与业务拒绝保持区别：日期不符合要求时即使目录只读也仍按业务拒绝退出 1，
	// 不触碰写盘。
	biz := runArgs(t, rescheduleArgs(target, "P-1", "2020-01-01", "想改到过去")...)
	if biz.code != 1 {
		t.Fatalf("早于当天的改期应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交：仍退出 2，而不是成功。
	if r2 := runArgs(t, failArgs...); r2.code != 2 || strings.Contains(r2.stdout, "已改期") {
		t.Fatalf("再次提交应仍退出 2 且不显示已改期：code=%d stdout=%q", r2.code, r2.stdout)
	}

	// 目录只读但台账可读：核对与待办仍使用最后成功保存的 10-20，看不到 11-05，
	// 待办标记不按失败日期变化（对 2030-10-20 均为“未到计划日”）。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "2030-11-05") || strings.Contains(rv.stdout, "实验室改造延期") {
		t.Fatalf("失败改期出现在核对结果中：%s", rv.stdout)
	}
	var view struct {
		Plans []struct {
			Number      string `json:"number"`
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
			Changes     []struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].PlannedDate != "2030-10-20" ||
		view.Plans[0].Marker != "未到计划日" || len(view.Plans[0].Changes) != 1 ||
		view.Plans[0].Changes[0].To != "2030-10-20" {
		t.Fatalf("核对应停留在最后成功保存的状态：%+v", view.Plans)
	}
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	var todos struct {
		Todos []struct {
			PlannedDate string `json:"planned_date"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv.stdout)
	}
	if len(todos.Todos) != 1 || todos.Todos[0].PlannedDate != "2030-10-20" {
		t.Fatalf("待办应仍按 2030-10-20：%+v", todos.Todos)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "2030-11-05") {
		t.Fatalf("失败日期已存在于台账文件：\n%s", raw)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的改期不能被顺带写入。
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
	if strings.Contains(string(raw), "2030-11-05") || strings.Contains(string(raw), "实验室改造延期") {
		t.Fatalf("失败改期被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 停用器具的计划仍可改期：重新提交到 11-08 才成功，当前日期改为 11-08，
	// 只新增一条 10-20 → 11-08，历史共两条，任何位置都没有 11-05。
	ok := runArgs(t, rescheduleArgs(target, "P-1", "2030-11-08", "改造完成重新排期")...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已改期为 2030-11-08") ||
		!strings.Contains(ok.stdout, "第 2 次改期") {
		t.Fatalf("恢复后改期应成功且为第 2 次改期：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	after := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var final struct {
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Status      string `json:"status"`
			Changes     []struct {
				From   string `json:"from"`
				To     string `json:"to"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(after.stdout), &final); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, after.stdout)
	}
	if len(final.Plans) != 1 {
		t.Fatalf("应只有一项计划：%+v", final.Plans)
	}
	p := final.Plans[0]
	if p.PlannedDate != "2030-11-08" || p.Status != "未完成" || len(p.Changes) != 2 {
		t.Fatalf("成功后计划状态异常：%+v", p)
	}
	if p.Changes[0].From != "2030-10-10" || p.Changes[0].To != "2030-10-20" {
		t.Fatalf("第一条改期记录不能变化：%+v", p.Changes[0])
	}
	if p.Changes[1].From != "2030-10-20" || p.Changes[1].To != "2030-11-08" ||
		p.Changes[1].Reason != "改造完成重新排期" {
		t.Fatalf("第二条应从最后成功日期 10-20 改到 11-08：%+v", p.Changes[1])
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "2030-11-05") {
		t.Fatalf("失败日期 2030-11-05 最终仍被写入文件：\n%s", raw)
	}
}
