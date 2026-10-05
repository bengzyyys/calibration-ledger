package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanSaveFailureIsFileErrorAndRecoverable 验证命令行新建计划在台账文件
// 无法写入时按文件读写错误报告：普通输出与 --json 都退出码 2、不输出成功结果，
// 也不能把失败说成编号重复或器具已有计划。失败的新计划不占用编号与该器具唯一
// 的未结束计划名额，待办与核对都看不到它；恢复可写后重新提交才建立。
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

	// 目录只读：save 无法创建临时文件；测试结束后恢复权限便于清理。
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	// 合法新建写盘失败：退出码 2，按文件读写错误报告，不显示“已为器具…建立”，
	// 错误信息指出台账文件。
	failArgs := planArgs(target, "M-1", "P-1", "2030-10-10")
	r := runArgs(t, failArgs...)
	if r.code != 2 {
		t.Fatalf("新建写盘失败应按文件读写错误退出 2，得到 %d（stderr=%s）", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "已为器具") || strings.Contains(r.stdout, "建立校准计划") {
		t.Fatalf("写盘失败不得输出成功结果：stdout=%q", r.stdout)
	}
	if !strings.Contains(r.stderr, target) {
		t.Fatalf("错误应指出台账文件 %s，stderr=%q", target, r.stderr)
	}
	if strings.Contains(r.stderr, "已存在") || strings.Contains(r.stderr, "已有未结束") {
		t.Fatalf("保存失败不能被说成编号重复或器具已有计划：stderr=%q", r.stderr)
	}

	// --json 模式同样退出码 2，且不输出成功 JSON。
	jr := runArgs(t, append(append([]string{}, failArgs...), "--json")...)
	if jr.code != 2 {
		t.Fatalf("--json 写盘失败也应退出 2，得到 %d（stderr=%s）", jr.code, jr.stderr)
	}
	if strings.Contains(jr.stdout, `"accepted": true`) {
		t.Fatalf("--json 写盘失败不得输出成功结果：%s", jr.stdout)
	}

	// 文件仍不可写时再次提交相同编号和内容：仍退出 2，而不是被上次失败留下的
	// 计划当成编号重复（退出 1）挡住。
	if r2 := runArgs(t, failArgs...); r2.code != 2 {
		t.Fatalf("再次提交应仍退出 2，得到 %d（stderr=%s）", r2.code, r2.stderr)
	}
	// 同一器具换编号、别的器具用同一编号也都应走到保存并退出 2：失败计划既不
	// 占用该器具唯一的未结束计划名额，也不占用编号。
	if r2 := runArgs(t, planArgs(target, "M-1", "P-OTHER", "2030-10-11")...); r2.code != 2 {
		t.Fatalf("同器具换编号也应退出 2，得到 %d（stderr=%s）", r2.code, r2.stderr)
	}
	if r2 := runArgs(t, planArgs(target, "M-2", "P-1", "2030-10-10")...); r2.code != 2 {
		t.Fatalf("别的器具用失败编号也应退出 2，得到 %d（stderr=%s）", r2.code, r2.stderr)
	}

	// 与业务拒绝保持区别：日期早于本机今天即使目录只读也按业务拒绝退出 1，
	// 不触碰写盘。
	biz := runArgs(t, planArgs(target, "M-1", "P-PAST", "2020-01-01")...)
	if biz.code != 1 {
		t.Fatalf("过去日期应业务拒绝退出 1，得到 %d（stderr=%s）", biz.code, biz.stderr)
	}

	// 目录只读但台账可读：待办为空，M-1 核对中没有任何计划。
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("只读目录下待办查询应可进行：%s", tv.stderr)
	}
	var todos struct {
		Todos []json.RawMessage `json:"todos"`
		Count int               `json:"count"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv.stdout)
	}
	if todos.Count != 0 || len(todos.Todos) != 0 {
		t.Fatalf("失败的新计划不应出现在待办：%s", tv.stdout)
	}
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("只读目录下核对应仍可进行：%s", rv.stderr)
	}
	if strings.Contains(rv.stdout, "P-1") || strings.Contains(rv.stdout, "P-OTHER") {
		t.Fatalf("失败的新计划出现在核对结果中：%s", rv.stdout)
	}

	// 恢复目录可写：先做一次无关的正常操作（器具停用）并保存成功，
	// 失败的新计划不能被顺带写入。
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
	if strings.Contains(string(raw), "P-1") || strings.Contains(string(raw), "P-OTHER") {
		t.Fatalf("失败的新计划被随后成功的写盘顺带写入文件：\n%s", raw)
	}

	// 无需重新打开台账（每次调用都会打开同一文件）：重新提交相同编号和内容才
	// 真正建立，普通输出与 --json 都给出成功结果，计划进入待办与核对。
	ok := runArgs(t, failArgs...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "已为器具 M-1 建立校准计划 P-1") {
		t.Fatalf("恢复后重新提交应成功建立：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	okj := runArgs(t, append(append([]string{}, planArgs(target, "M-2", "P-2", "2030-10-20")...), "--json")...)
	if okj.code != 0 || !strings.Contains(okj.stdout, `"accepted": true`) {
		t.Fatalf("M-2 的新计划 --json 应成功：code=%d stdout=%q", okj.code, okj.stdout)
	}

	tv2 := runArgs(t, "todos", "-f", target, "--json")
	var gotTodos struct {
		Todos []struct {
			PlanNumber string `json:"plan_number"`
		} `json:"todos"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(tv2.stdout), &gotTodos); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, tv2.stdout)
	}
	if gotTodos.Count != 2 {
		t.Fatalf("成功建立后应有 2 项待办：%s", tv2.stdout)
	}
	nums := map[string]bool{}
	for _, it := range gotTodos.Todos {
		nums[it.PlanNumber] = true
	}
	if !nums["P-1"] || !nums["P-2"] {
		t.Fatalf("待办应包含 P-1 与 P-2：%v", nums)
	}

	rv2 := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var view struct {
		Plans []struct {
			Number string `json:"number"`
			Status string `json:"status"`
			Note   string `json:"note"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv2.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv2.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Number != "P-1" ||
		view.Plans[0].Status != "未完成" || view.Plans[0].Note != "周期校准" {
		t.Fatalf("M-1 核对应只含成功建立的 P-1：%+v", view.Plans)
	}

	// 成功建立后再提交相同编号，才按业务规则以编号重复拒绝（退出 1）。
	dup := runArgs(t, failArgs...)
	if dup.code != 1 || !strings.Contains(dup.stderr, "已存在") {
		t.Fatalf("成功保存后同号应业务拒绝退出 1：code=%d stderr=%q", dup.code, dup.stderr)
	}
}
