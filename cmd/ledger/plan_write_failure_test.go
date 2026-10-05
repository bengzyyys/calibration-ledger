package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planCreateArgs 构造一条指定日期与说明的新建计划命令。
func planCreateArgs(path, instrument, number, date, note string) []string {
	return []string{"plan", "-f", path,
		"--instrument", instrument, "--number", number,
		"--date", date, "--note", note}
}

// TestPlanSaveFailureIsFileErrorAndRecoverable 验证命令行新建校准计划在台账文件
// 无法写入时按文件读写错误报告、退出码 2（普通输出与 --json 一致）、不输出已建立成功；
// 失败的计划不占用编号、不占用该器具唯一的未结束计划名额，待办与核对都看不到它。
// 文件仍不可写时再次提交相同编号内容仍退出 2，而不是被当成编号重复或器具已有计划（退出 1）。
// 恢复可写后重新提交才建立并出现在待办与核对中；此前保存的计划信息保持原样；未通过业务
// 检查的申请仍按原方式拒绝，不能因台账不可写被混成保存失败。
func TestPlanSaveFailureIsFileErrorAndRecoverable(t *testing.T) {
	dir := chdirTemp(t)
	// 台账放在子目录里：把子目录改成只读即可挡住写临时文件与改名替换，
	// 已存在的台账文件本身仍可读，查询不受影响。
	sub := filepath.Join(dir, "只读目录")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sub, "ledger.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记 M-1 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, registerArgs(target, "M-2")...); r.code != 0 {
		t.Fatalf("登记 M-2 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 先成功建立一项计划 P-SAVED：失败申请不得改动它，也不应让它在待办或核对中消失。
	if r := runArgs(t, planCreateArgs(target, "M-1", "P-SAVED", "2030-10-10", "已保存的计划")...); r.code != 0 {
		t.Fatalf("建立已保存计划失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法新建（M-2 已登记且没有未结束计划）写盘失败：退出码 2，按文件读写错误报告，
	// 不显示“已为器具…建立”，错误信息指出台账文件，也不能说成编号重复或器具已有计划。
	legalFail := planCreateArgs(target, "M-2", "P-NEW", "2030-10-20", "失败的新计划")
	r := runArgs(t, legalFail...)
	if r.code != 2 {
		t.Fatalf("新建写盘失败应按文件读写错误退出 2，得到 %d（stderr=%s）", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "已为器具") || strings.Contains(r.stdout, "建立校准计划") {
		t.Fatalf("写盘失败不得显示建立成功：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}
	if strings.Contains(r.stderr, "已存在") || strings.Contains(r.stderr, "最多一项未结束计划") {
		t.Fatalf("写盘失败不能被说成编号重复或器具已有计划：stderr=%q", r.stderr)
	}

	// --json 模式同样退出码 2，不输出建立成功的结果。
	rj := runArgs(t, append(append([]string{}, legalFail...), "--json")...)
	if rj.code != 2 {
		t.Fatalf("--json 模式写盘失败应退出 2，得到 %d", rj.code)
	}
	if strings.Contains(rj.stdout, "已为器具") || strings.Contains(rj.stdout, "\"accepted\": true") ||
		strings.Contains(rj.stdout, "\"accepted\":true") {
		t.Fatalf("--json 模式不得声称建立成功：stdout=%q", rj.stdout)
	}

	// 与业务拒绝保持区别：过去日期、复用已保存编号、器具已有未结束计划、未知器具均按业务
	// 拒绝退出 1（不触碰写盘）。
	if biz := runArgs(t, planCreateArgs(target, "M-2", "P-NEW", "2020-01-01", "过去日期")...); biz.code != 1 {
		t.Fatalf("过去日期应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}
	if biz := runArgs(t, planCreateArgs(target, "M-2", "P-SAVED", "2030-10-20", "复用编号")...); biz.code != 1 {
		t.Fatalf("复用已保存编号应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}
	if biz := runArgs(t, planCreateArgs(target, "M-1", "P-OTHER", "2030-10-20", "占用名额")...); biz.code != 1 {
		t.Fatalf("器具有未结束计划应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}
	if biz := runArgs(t, planCreateArgs(target, "NOPE", "P-X", "2030-10-20", "未知器具")...); biz.code != 1 {
		t.Fatalf("未知器具应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 文件仍不可写时再次提交相同编号与内容：仍退出 2，而不是被上次失败留下的计划挡住。
	if r2 := runArgs(t, legalFail...); r2.code != 2 || strings.Contains(r2.stdout, "已为器具") {
		t.Fatalf("再次提交应仍退出 2 且不显示成功：code=%d stdout=%q stderr=%s", r2.code, r2.stdout, r2.stderr)
	}

	// 目录只读但台账可读：待办只包含此前已保存的 P-SAVED，M-2 的待办为空；
	// 核对中 M-2 没有任何计划。
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
	if len(todos.Todos) != 1 || todos.Todos[0].PlanNumber != "P-SAVED" {
		t.Fatalf("待办应只能看到此前已保存的计划：%+v", todos.Todos)
	}
	tv2 := runArgs(t, "todos", "--instrument", "M-2", "-f", target, "--json")
	if tv2.code != 0 || strings.Contains(tv2.stdout, "P-NEW") {
		t.Fatalf("M-2 待办不应包含失败计划：code=%d stdout=%s", tv2.code, tv2.stdout)
	}
	rv := runArgs(t, "review", "--id", "M-2", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "P-NEW") || strings.Contains(rv.stdout, "失败的新计划") {
		t.Fatalf("失败计划出现在核对结果中：%s", rv.stdout)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "P-NEW") || strings.Contains(string(raw), "失败的新计划") {
		t.Fatalf("失败的新计划已存在于台账文件：\n%s", raw)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，失败的计划不能被
	// 顺带写入。
	if err := os.Chmod(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := runArgs(t, "status", "-f", target, "--id", "M-2", "--status", "停用"); st.code != 0 {
		t.Fatalf("恢复后正常操作应能保存：%s", st.stderr)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取台账文件: %v", err)
	}
	if strings.Contains(string(raw), "P-NEW") || strings.Contains(string(raw), "失败的新计划") {
		t.Fatalf("失败计划被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 无需重新打开台账：重新提交合法申请才成功建立（停用器具的计划名额同样不受限制），
	// 出现在 M-2 待办与核对中。
	ok := runArgs(t, legalFail...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已为器具 M-2 建立校准计划 P-NEW") {
		t.Fatalf("恢复后重新提交应成功：code=%d stdout=%q stderr=%s", ok.code, ok.stdout, ok.stderr)
	}
	tv3 := runArgs(t, "todos", "--instrument", "M-2", "-f", target, "--json")
	var todos2 struct {
		Todos []struct {
			PlanNumber string `json:"plan_number"`
			Marker     string `json:"marker"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv3.stdout), &todos2); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv3.stdout)
	}
	if len(todos2.Todos) != 1 || todos2.Todos[0].PlanNumber != "P-NEW" ||
		todos2.Todos[0].Marker != "未到计划日" {
		t.Fatalf("成功建立后待办应包含新计划：%+v", todos2.Todos)
	}
	rv2 := runArgs(t, "review", "--id", "M-2", "-f", target, "--json")
	var view struct {
		Plans []struct {
			Number string `json:"number"`
			Status string `json:"status"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv2.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv2.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Number != "P-NEW" || view.Plans[0].Status != "未完成" {
		t.Fatalf("核对应展示新建的未完成计划：%+v", view.Plans)
	}

	// 已保存的编号不能复用；该器具有了未结束计划，再建被业务拒绝（退出 1）。
	if dup := runArgs(t, legalFail...); dup.code != 1 {
		t.Fatalf("已保存编号再次新建应业务拒绝退出 1，得到 %d（stderr=%s）", dup.code, dup.stderr)
	}
	if slot := runArgs(t, planCreateArgs(target, "M-2", "P-NEW2", "2030-10-20", "再建一项")...); slot.code != 1 {
		t.Fatalf("占用名额应业务拒绝退出 1，得到 %d（stderr=%s）", slot.code, slot.stderr)
	}

	// 此前保存的计划 P-SAVED 信息保持原样，与新计划一起都在待办中。
	tv4 := runArgs(t, "todos", "-f", target, "--json")
	var allTodos struct {
		Todos []struct {
			PlanNumber string `json:"plan_number"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv4.stdout), &allTodos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv4.stdout)
	}
	got := map[string]bool{}
	for _, it := range allTodos.Todos {
		got[it.PlanNumber] = true
	}
	if !got["P-SAVED"] || !got["P-NEW"] || len(allTodos.Todos) != 2 {
		t.Fatalf("待办应同时包含此前保存的 P-SAVED 与新建的 P-NEW：%+v", allTodos.Todos)
	}
}
