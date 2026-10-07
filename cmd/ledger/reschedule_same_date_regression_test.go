package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRescheduleSameDateCountedAndReviewedCLI 验证命令行下同日期改期不被当作
// “没有变化”跳过：正常改期后再提交与当前计划日期相同的新日期仍成功并留痕，
// 普通输出中的改期次数包含这些记录；连续两次同日期改期（即使原因相同）各自
// 分别增加一条记录，不合并、不直接返回上次结果；JSON 输出的成功结果与随后
// 核对看到的计划历史一致，前后日期相同的记录在核对中不被隐藏；校准证书、
// 器具状态和历史使用记录不因改期变化。
func TestRescheduleSameDateCountedAndReviewedCLI(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记器具失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换状态失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, certArgs(target, "C-1", "2026-09-01", "比较法", "例行")...); r.code != 0 {
		t.Fatalf("录入证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 留一条历史使用记录（无论允许或拒绝都会留痕）。
	runArgs(t, "use", "--id", "M-1", "-f", target)
	usageBefore := readUsageCount(t, target)

	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
	}
	// 正常改期：2030-10-10 → 2030-10-20。
	r := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "实验室排期冲突")...)
	if r.code != 0 || !strings.Contains(r.stdout, "第 1 次改期") {
		t.Fatalf("正常改期应成功且为第 1 次改期：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}
	// 与当前计划日期完全相同的新日期：仍成功，改期次数包含这条记录。
	r = runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "按原排期再次确认")...)
	if r.code != 0 || !strings.Contains(r.stdout, "已改期为 2030-10-20") ||
		!strings.Contains(r.stdout, "第 2 次改期") {
		t.Fatalf("同日期改期应成功且计为第 2 次改期：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}
	// 连续第二次同日期改期，原因与上一次完全相同：仍分别增加一条记录，
	// 不能合并为一条或直接返回上次结果。
	r = runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "按原排期再次确认")...)
	if r.code != 0 || !strings.Contains(r.stdout, "第 3 次改期") {
		t.Fatalf("第二次同日期改期应计为第 3 次改期：code=%d stdout=%q stderr=%s",
			r.code, r.stdout, r.stderr)
	}

	// JSON 输出的成功结果包含全部三条记录。
	rj := runArgs(t, "reschedule", "-f", target, "--number", "P-1",
		"--date", "2030-10-20", "--reason", "按原排期再次确认", "--json")
	if rj.code != 0 {
		t.Fatalf("JSON 同日期改期应成功：code=%d stderr=%s", rj.code, rj.stderr)
	}
	var resp struct {
		Accepted bool `json:"accepted"`
		Plan     struct {
			PlannedDate  string `json:"planned_date"`
			OriginalDate string `json:"original_date"`
			Status       string `json:"status"`
			Changes      []struct {
				From   string `json:"from"`
				To     string `json:"to"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"plan"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &resp); err != nil {
		t.Fatalf("解析 reschedule JSON 失败: %v\n%s", err, rj.stdout)
	}
	if !resp.Accepted || resp.Plan.PlannedDate != "2030-10-20" ||
		resp.Plan.OriginalDate != "2030-10-10" || resp.Plan.Status != "未完成" {
		t.Fatalf("JSON 成功结果的计划字段异常：%+v", resp)
	}
	if len(resp.Plan.Changes) != 4 {
		t.Fatalf("四次改期应有四条记录，得到 %+v", resp.Plan.Changes)
	}
	// 第一条是正常改期，其余三条前后日期都等于当前计划日期。
	if resp.Plan.Changes[0].From != "2030-10-10" || resp.Plan.Changes[0].To != "2030-10-20" {
		t.Fatalf("第一条记录应为正常改期：%+v", resp.Plan.Changes[0])
	}
	for i, ch := range resp.Plan.Changes[1:] {
		if ch.From != "2030-10-20" || ch.To != "2030-10-20" {
			t.Fatalf("第 %d 条同日期记录前后日期应都等于当前计划日期：%+v", i+2, ch)
		}
		if ch.Reason != "按原排期再次确认" {
			t.Fatalf("第 %d 条记录应保留各自提交的原因：%+v", i+2, ch)
		}
	}

	// 随后核对看到的计划历史与 JSON 成功结果一致；前后日期相同的记录不被隐藏。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("核对失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	var view struct {
		Instrument struct {
			Status string `json:"status"`
		} `json:"instrument"`
		History []struct {
			Number string `json:"number"`
		} `json:"history"`
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
			Changes     []struct {
				From   string `json:"from"`
				To     string `json:"to"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || len(view.Plans[0].Changes) != 4 {
		t.Fatalf("核对中应看到全部四条改期记录，得到 %+v", view.Plans)
	}
	for i, ch := range view.Plans[0].Changes {
		want := resp.Plan.Changes[i]
		if ch.From != want.From || ch.To != want.To || ch.Reason != want.Reason {
			t.Fatalf("核对历史应与 JSON 成功结果一致：第 %d 条 %+v vs %+v", i+1, ch, want)
		}
	}
	if view.Plans[0].PlannedDate != "2030-10-20" || view.Plans[0].Marker != "未到计划日" {
		t.Fatalf("核对中的计划日期与标记异常：%+v", view.Plans[0])
	}
	// 普通文本核对同样展示前后日期相同的记录。
	rp := runArgs(t, "review", "--id", "M-1", "-f", target)
	if rp.code != 0 || !strings.Contains(rp.stdout, "改期 2030-10-20 → 2030-10-20") {
		t.Fatalf("普通文本核对应展示同日期改期记录：code=%d stdout=%q", rp.code, rp.stdout)
	}

	// 计划仍留在待办中，标记由当前计划日期与查询当天的关系决定。
	tv := runArgs(t, "todos", "-f", target, "--json")
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
		todos.Todos[0].PlannedDate != "2030-10-20" || todos.Todos[0].Marker != "未到计划日" {
		t.Fatalf("同日期改期后计划应留在待办：%+v", todos.Todos)
	}

	// 校准证书、器具状态和历史使用记录不因改期变化。
	if view.Instrument.Status != "在用" {
		t.Fatalf("器具状态不应因改期变化，得到 %s", view.Instrument.Status)
	}
	if len(view.History) != 1 || view.History[0].Number != "C-1" {
		t.Fatalf("证书历史不应因改期变化，得到 %+v", view.History)
	}
	if got := readUsageCount(t, target); got != usageBefore {
		t.Fatalf("历史使用记录不应因改期变化：%d -> %d", usageBefore, got)
	}
}

// TestRescheduleSameDateBoundaryAndExitCodesCLI 验证命令行下同日期改期的边界
// 与退出码：日期恰好等于本机今天时同日期改期成功；当前计划日期已早于操作
// 当天时，再提交这个原日期按日期已过业务拒绝（退出码 1），不能因日期没有
// 改变而绕过限制；原因为空白按缺少必填参数拒绝（退出码 2）。失败申请不留下
// 记录，原因不进入核对结果，原计划日期与已有改期历史保持原样。
func TestRescheduleSameDateBoundaryAndExitCodesCLI(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "台账.json")
	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记器具失败 code=%d stderr=%s", r.code, r.stderr)
	}
	today := time.Now().Format("2006-01-02")
	if r := runArgs(t, planArgs(target, "M-1", "P-1", today)...); r.code != 0 {
		t.Fatalf("建立当天计划失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 日期恰好等于本机今天：同日期改期成功。
	r := runArgs(t, rescheduleArgs(target, "P-1", today, "当天再次确认")...)
	if r.code != 0 || !strings.Contains(r.stdout, "第 1 次改期") {
		t.Fatalf("当天同日期改期应成功：code=%d stdout=%q stderr=%s", r.code, r.stdout, r.stderr)
	}

	// 原因为空白：按缺少必填参数拒绝，退出码 2。
	if r := runArgs(t, rescheduleArgs(target, "P-1", today, "   ")...); r.code != 2 {
		t.Fatalf("空白原因应退出 2，得到 %d", r.code)
	}
	if r := runArgs(t, "reschedule", "-f", target, "--number", "P-1", "--date", today); r.code != 2 {
		t.Fatalf("缺少 --reason 应退出 2，得到 %d", r.code)
	}
	// 已过的日期：按业务拒绝，退出码 1。
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2020-01-01", "想改到过去")...); r.code != 1 {
		t.Fatalf("日期已过应业务拒绝退出 1，得到 %d", r.code)
	}

	// 失败申请不留下记录：核对中只有成功的那一条，失败原因不出现。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	if rv.code != 0 {
		t.Fatalf("核对失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	if strings.Contains(rv.stdout, "想改到过去") {
		t.Fatalf("失败申请的原因不应进入核对结果：%s", rv.stdout)
	}
	var view struct {
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Changes     []struct {
				From   string `json:"from"`
				To     string `json:"to"`
				Reason string `json:"reason"`
			} `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].PlannedDate != today ||
		len(view.Plans[0].Changes) != 1 || view.Plans[0].Changes[0].Reason != "当天再次确认" {
		t.Fatalf("失败申请不应改动计划日期与改期历史：%+v", view.Plans)
	}

	// 当前计划日期已早于操作当天：再提交这个原日期必须按日期已过拒绝，
	// 不能因为日期没有改变而绕过限制。直接写一份计划日期在昨天的台账。
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	stale := filepath.Join(dir, "过期.json")
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status": "待校准", "registered_at": yesterday + "T08:00:00Z",
		}},
		"certificates": []any{},
		"usage":        []any{},
		"plans": []map[string]any{{
			"number": "PL-PAST", "instrument_id": "M-1",
			"planned_date": yesterday, "original_date": yesterday,
			"note": "昨天该校准", "created_at": yesterday + "T09:00:00Z",
			"status": "未完成",
		}},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化测试台账: %v", err)
	}
	if err := os.WriteFile(stale, raw, 0o644); err != nil {
		t.Fatalf("写测试台账: %v", err)
	}
	r = runArgs(t, rescheduleArgs(stale, "PL-PAST", yesterday, "还想停在原日期")...)
	if r.code != 1 {
		t.Fatalf("已过的原日期应业务拒绝退出 1，得到 %d（stderr=%s）", r.code, r.stderr)
	}
	// 拒绝后计划日期保持原样，不留下任何改期记录。
	rv = runArgs(t, "review", "--id", "M-1", "-f", stale, "--json")
	if rv.code != 0 {
		t.Fatalf("核对失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	if strings.Contains(rv.stdout, "还想停在原日期") {
		t.Fatalf("失败申请的原因不应进入核对结果：%s", rv.stdout)
	}
	var staleView struct {
		Plans []struct {
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
			Changes     []any  `json:"changes"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &staleView); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	if len(staleView.Plans) != 1 || staleView.Plans[0].PlannedDate != yesterday ||
		len(staleView.Plans[0].Changes) != 0 || staleView.Plans[0].Marker != "逾期" {
		t.Fatalf("被拒绝的同日期申请不应留下记录：%+v", staleView.Plans)
	}
}
