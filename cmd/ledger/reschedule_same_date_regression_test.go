package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// planChangeJSON 是核对/改期 JSON 中一条改期记录的字段镜像。
type planChangeJSON struct {
	From      string `json:"from"`
	To        string `json:"to"`
	ChangedAt string `json:"changed_at"`
	Reason    string `json:"reason"`
}

// planJSON 是核对结果中计划部分的字段镜像。
type planJSON struct {
	Number       string           `json:"number"`
	InstrumentID string           `json:"instrument_id"`
	PlannedDate  string           `json:"planned_date"`
	OriginalDate string           `json:"original_date"`
	Note         string           `json:"note"`
	Status       string           `json:"status"`
	Marker       string           `json:"marker"`
	Changes      []planChangeJSON `json:"changes"`
}

func reviewPlans(t *testing.T, path string) []planJSON {
	t.Helper()
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if rv.code != 0 {
		t.Fatalf("review 失败 code=%d stderr=%s", rv.code, rv.stderr)
	}
	var view struct {
		Instrument struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"instrument"`
		Latest *struct {
			Number string `json:"number"`
		} `json:"latest"`
		History    []json.RawMessage `json:"history"`
		Rejections []json.RawMessage `json:"rejections"`
		Plans      []planJSON        `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, rv.stdout)
	}
	return view.Plans
}

// rescheduleJSON 解析一次 --json 改期输出中的 plan。
func rescheduleJSON(t *testing.T, r result) planJSON {
	t.Helper()
	var resp struct {
		Accepted bool     `json:"accepted"`
		Plan     planJSON `json:"plan"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &resp); err != nil {
		t.Fatalf("解析改期 JSON 失败: %v\n%s", err, r.stdout)
	}
	if !resp.Accepted {
		t.Fatalf("改期 JSON 应为 accepted=true：%s", r.stdout)
	}
	return resp.Plan
}

// seedPastPlanLedger 直接写一份台账文件：一件器具、一张证书、一次使用留痕，
// 以及一项当前计划日期早已过去（且已有过一次正常改期）的未结束计划。
// 命令行无法建立过去日期的计划，只能通过既有台账文件构造这一场景。
func seedPastPlanLedger(t *testing.T, path string) {
	t.Helper()
	raw := `{
  "version": 1,
  "instruments": [
    {"id": "M-1", "name": "万用表", "allowed_error": 0.5, "status": "在用",
     "registered_at": "2020-01-01T00:00:00Z"}
  ],
  "certificates": [
    {"number": "C-OLD", "instrument_id": "M-1", "cal_date": "2020-01-05",
     "expiry": "2021-01-05", "method": "规范A", "error": 0.1, "summary": "旧证书",
     "created_at": "2020-01-06T00:00:00Z"}
  ],
  "usage": [
    {"instrument_id": "M-1", "requested_at": "2020-01-07T00:00:00Z",
     "allowed": true, "reasons": []}
  ],
  "plans": [
    {"number": "P-OLD", "instrument_id": "M-1",
     "planned_date": "2020-02-10", "original_date": "2020-02-01",
     "note": "遗留周期校准", "created_at": "2020-01-02T00:00:00Z",
     "status": "未完成",
     "changes": [
       {"from": "2020-02-01", "to": "2020-02-10",
        "changed_at": "2020-01-10T00:00:00Z", "reason": "旧实验室排期冲突"}
     ]}
  ]
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("写入种子台账: %v", err)
	}
}

// TestCLIRescheduleSameDateStillRecorded 从命令行守护同日期改期行为：
// 已有一次正常改期后，连续提交与当前计划日期相同的新日期必须逐次成功并各增加
// 一条改期记录；普通输出中的“第 N 次改期”次数包含这些记录；--json 成功结果
// 与随后核对看到的计划历史一致（含各自的原因与操作时间），普通核对输出也展示
// “D → D”的记录；计划的当前/最初日期、说明、未完成状态与待办标记不因申请改变；
// 校准证书、器具状态和历史使用记录不发生变化。
func TestCLIRescheduleSameDateStillRecorded(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "ledger.json")

	if r := runArgs(t, registerArgs(target, "M-1")...); r.code != 0 {
		t.Fatalf("登记器具失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, "cert", "-f", target,
		"--instrument", "M-1", "--number", "C-1",
		"--cal-date", "2026-09-15", "--expiry", "2027-09-15",
		"--method", "规范A", "--error", "0.1", "--summary", "例行校准",
	); r.code != 0 {
		t.Fatalf("录入证书失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, "status", "-f", target, "--id", "M-1", "--status", "在用"); r.code != 0 {
		t.Fatalf("切换在用失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, "use", "-f", target, "--id", "M-1"); r.code != 0 {
		t.Fatalf("申请使用失败 code=%d stderr=%s", r.code, r.stderr)
	}
	usageBefore := readUsageCount(t, target)

	// 建立未来计划并做一次正常改期：10-10 → 2030-10-20。
	if r := runArgs(t, planArgs(target, "M-1", "P-1", "2030-10-10")...); r.code != 0 {
		t.Fatalf("建立计划失败 code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "实验室排期冲突")...); r.code != 0 {
		t.Fatalf("正常改期失败 code=%d stderr=%s", r.code, r.stderr)
	}

	// 第一次同日期改期（普通输出）：次数必须计入这条记录，显示“第 2 次改期”。
	firstSame := runArgs(t, rescheduleArgs(target, "P-1", "2030-10-20", "确认维持原排期")...)
	if firstSame.code != 0 {
		t.Fatalf("同日期改期应成功，code=%d stderr=%s", firstSame.code, firstSame.stderr)
	}
	if !strings.Contains(firstSame.stdout, "已改期为 2030-10-20") ||
		!strings.Contains(firstSame.stdout, "第 2 次改期") {
		t.Fatalf("普通输出应计入同日期改期次数：stdout=%q", firstSame.stdout)
	}

	// 第二次同日期改期，原因与第一次不同，使用 --json：应再增加一条。
	secondSame := runArgs(t, "reschedule", "-f", target, "--json",
		"--number", "P-1", "--date", "2030-10-20", "--reason", "客户再次确认")
	if secondSame.code != 0 {
		t.Fatalf("第二次同日期改期应成功，code=%d stderr=%s",
			secondSame.code, secondSame.stderr)
	}
	got := rescheduleJSON(t, secondSame)
	if got.PlannedDate != "2030-10-20" || got.OriginalDate != "2030-10-10" ||
		got.Status != "未完成" || got.Note != "周期校准" {
		t.Fatalf("计划的当前日期、最初日期、说明和状态应保持原样：%+v", got)
	}
	if len(got.Changes) != 3 {
		t.Fatalf("JSON 成功结果应有 3 条改期记录，得到 %+v", got.Changes)
	}
	wantChanges := []planChangeJSON{
		{From: "2030-10-10", To: "2030-10-20", Reason: "实验室排期冲突"},
		{From: "2030-10-20", To: "2030-10-20", Reason: "确认维持原排期"},
		{From: "2030-10-20", To: "2030-10-20", Reason: "客户再次确认"},
	}
	for i, w := range wantChanges {
		if got.Changes[i].From != w.From || got.Changes[i].To != w.To ||
			got.Changes[i].Reason != w.Reason || got.Changes[i].ChangedAt == "" {
			t.Fatalf("JSON 中第 %d 条改期记录异常：应 %+v，得到 %+v",
				i, w, got.Changes[i])
		}
	}
	// 命令行连续调用可能落在同一秒：各自的操作时间只需完整保留、可解析，
	// 记录仍须按提交顺序逐条存在，不能因时间或日期相同而合并。
	for i, ch := range got.Changes {
		if _, err := time.Parse(time.RFC3339, ch.ChangedAt); err != nil {
			t.Fatalf("第 %d 条改期记录的操作时间应完整可解析：%v（%+v）", i, err, ch)
		}
	}

	// --json 成功结果与随后核对看到的计划历史完全一致。
	plans := reviewPlans(t, target)
	if len(plans) != 1 {
		t.Fatalf("核对中应只有一项计划：%+v", plans)
	}
	if !reflect.DeepEqual(plans[0].Changes, got.Changes) {
		t.Fatalf("核对历史与改期 JSON 结果不一致：\n核对=%+v\n改期=%+v",
			plans[0].Changes, got.Changes)
	}
	if plans[0].PlannedDate != "2030-10-20" || plans[0].OriginalDate != "2030-10-10" ||
		plans[0].Status != "未完成" || plans[0].Marker != "未到计划日" {
		t.Fatalf("核对中的计划字段与标记异常：%+v", plans[0])
	}

	// 普通核对输出必须展示前后日期相同的改期记录（两条 2030-10-20 → 2030-10-20）。
	humanReview := runArgs(t, "review", "--id", "M-1", "-f", target)
	if humanReview.code != 0 {
		t.Fatalf("普通核对失败 code=%d", humanReview.code)
	}
	if n := strings.Count(humanReview.stdout, "改期 2030-10-20 → 2030-10-20"); n != 2 {
		t.Fatalf("普通核对应展示 2 条同日期改期记录，实际 %d：%s", n, humanReview.stdout)
	}
	if !strings.Contains(humanReview.stdout, "确认维持原排期") ||
		!strings.Contains(humanReview.stdout, "客户再次确认") {
		t.Fatalf("普通核对应保留各次同日期改期的原因：%s", humanReview.stdout)
	}

	// 待办标记仍由当前计划日期与查询当天的关系决定，计划仍在待办中。
	tv := runArgs(t, "todos", "-f", target, "--json")
	if tv.code != 0 {
		t.Fatalf("待办查询失败 code=%d", tv.code)
	}
	var todos struct {
		Count int `json:"count"`
		Todos []struct {
			PlanNumber  string `json:"plan_number"`
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(tv.stdout), &todos); err != nil {
		t.Fatalf("解析 todos JSON: %v\n%s", err, tv.stdout)
	}
	if todos.Count != 1 || len(todos.Todos) != 1 ||
		todos.Todos[0].PlanNumber != "P-1" ||
		todos.Todos[0].PlannedDate != "2030-10-20" ||
		todos.Todos[0].Marker != "未到计划日" {
		t.Fatalf("计划应仍在待办且按当前日期标记：%+v", todos)
	}

	// 校准证书、器具状态与历史使用记录不因改期变化。
	rv := runArgs(t, "review", "--id", "M-1", "-f", target, "--json")
	var review struct {
		Instrument struct {
			Status string `json:"status"`
		} `json:"instrument"`
		Latest *struct {
			Number string `json:"number"`
		} `json:"latest"`
		History []json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &review); err != nil {
		t.Fatalf("解析 review JSON: %v", err)
	}
	if review.Instrument.Status != "在用" {
		t.Fatalf("器具状态不应变化，得到 %q", review.Instrument.Status)
	}
	if review.Latest == nil || review.Latest.Number != "C-1" || len(review.History) != 1 {
		t.Fatalf("校准证书不应因改期变化：latest=%+v history=%d",
			review.Latest, len(review.History))
	}
	if n := readUsageCount(t, target); n != usageBefore {
		t.Fatalf("历史使用记录不应因改期变化：改期前 %d 条，现在 %d 条", usageBefore, n)
	}
}

// TestCLIRescheduleSameDateRejections 覆盖命令行的两类失败及其退出码：
// 当前计划日期已早于操作当天时，再提交该原日期必须按业务拒绝退出 1；
// 原因缺失或为空白按缺少必填参数退出 2。两类失败都保留原计划日期与全部
// 改期历史，失败申请的原因和时间不进入核对。
func TestCLIRescheduleSameDateRejections(t *testing.T) {
	dir := chdirTemp(t)
	target := filepath.Join(dir, "past.json")
	seedPastPlanLedger(t, target)

	// 该计划当前日期 2020-02-10 相对运行当天已逾期，仍出现在待办中。
	tv := runArgs(t, "todos", "-f", target)
	if tv.code != 0 || !strings.Contains(tv.stdout, "P-OLD") ||
		!strings.Contains(tv.stdout, "逾期") {
		t.Fatalf("逾期的未结束计划应仍在待办并标逾期：code=%d stdout=%q", tv.code, tv.stdout)
	}

	// 提交与当前日期完全相同的过去日期：必须按日期已过业务拒绝（退出 1），
	// 不能因为日期没有改变而绕过限制。
	expired := runArgs(t, rescheduleArgs(target, "P-OLD", "2020-02-10", "日期没变重申一次")...)
	if expired.code != 1 {
		t.Fatalf("过去日期的同日期改期应业务拒绝退出 1，得到 %d（stdout=%q stderr=%s）",
			expired.code, expired.stdout, expired.stderr)
	}
	if !strings.Contains(expired.stderr, "已拒绝") ||
		!strings.Contains(expired.stderr, "不能早于本机今天") {
		t.Fatalf("应明确按日期已过拒绝：stderr=%q", expired.stderr)
	}
	if strings.Contains(expired.stdout, "已改期") {
		t.Fatalf("业务拒绝不得输出改期成功：stdout=%q", expired.stdout)
	}

	// 日期合法但原因缺失/空白：按缺少必填参数退出 2。
	noReason := runArgs(t, "reschedule", "-f", target,
		"--number", "P-OLD", "--date", "2030-03-01")
	if noReason.code != 2 || !strings.Contains(noReason.stderr, "缺少必填参数") {
		t.Fatalf("缺少 --reason 应退出 2 并提示缺少必填参数：code=%d stderr=%q",
			noReason.code, noReason.stderr)
	}
	blankReason := runArgs(t, "reschedule", "-f", target,
		"--number", "P-OLD", "--date", "2030-03-01", "--reason", "   ")
	if blankReason.code != 2 || !strings.Contains(blankReason.stderr, "缺少必填参数") {
		t.Fatalf("空白 --reason 应退出 2 并提示缺少必填参数：code=%d stderr=%q",
			blankReason.code, blankReason.stderr)
	}

	// 两类失败都不改变计划日期、改期历史、待办标记；失败原因不进入核对。
	plans := reviewPlans(t, target)
	if len(plans) != 1 {
		t.Fatalf("应仍只有一项计划：%+v", plans)
	}
	p := plans[0]
	if p.PlannedDate != "2020-02-10" || p.OriginalDate != "2020-02-01" ||
		p.Status != "未完成" || p.Marker != "逾期" {
		t.Fatalf("失败后计划应保持原样：%+v", p)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("失败申请不能进入改期历史：%+v", p.Changes)
	}
	wantOld := planChangeJSON{
		From: "2020-02-01", To: "2020-02-10",
		ChangedAt: "2020-01-10T00:00:00Z", Reason: "旧实验室排期冲突",
	}
	if p.Changes[0] != wantOld {
		t.Fatalf("已有改期记录应保持原样：%+v", p.Changes[0])
	}
	rv := runArgs(t, "review", "--id", "M-1", "-f", target)
	if strings.Contains(rv.stdout, "日期没变重申一次") {
		t.Fatalf("失败申请的原因不能出现在核对结果中：%s", rv.stdout)
	}

	// 改到合法的未来日期仍可成功，且只新增一条从最后成功保存的日期出发的记录。
	ok := runArgs(t, rescheduleArgs(target, "P-OLD", "2030-03-01", "重新安排送检")...)
	if ok.code != 0 || !strings.Contains(ok.stdout, "第 2 次改期") {
		t.Fatalf("合法改期应成功且为第 2 次改期：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	plans = reviewPlans(t, target)
	if len(plans) != 1 || plans[0].PlannedDate != "2030-03-01" ||
		plans[0].OriginalDate != "2020-02-01" || len(plans[0].Changes) != 2 {
		t.Fatalf("合法改期后计划状态异常：%+v", plans)
	}
	if plans[0].Changes[0] != wantOld {
		t.Fatalf("原改期记录不能变化：%+v", plans[0].Changes[0])
	}
	if plans[0].Changes[1].From != "2020-02-10" ||
		plans[0].Changes[1].To != "2030-03-01" ||
		plans[0].Changes[1].Reason != "重新安排送检" ||
		plans[0].Changes[1].ChangedAt == "" {
		t.Fatalf("新记录应从原当前日期 2020-02-10 改到 2030-03-01：%+v",
			plans[0].Changes[1])
	}
}
