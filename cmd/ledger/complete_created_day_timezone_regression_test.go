package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件从命令行端到端守住“计划建立的本机日期不因切换时区而改变”：
//
// 命令行使用真实本机时钟，无法把计划建立时刻倒拨到过去，因此这里直接预置
// 带时区偏移建立时间的台账文件（与真实台账保存格式一致），再通过真实命令
// 录入证书、完成计划。建立的本机日历日期与证书校准日期都相对运行当天动态
// 计算，且特意选取偏移使建立时刻换算 UTC 落到另一个日历日期——验证完成校验
// 只认建立时刻自带偏移的日历日期，而不是换算后的日期。
//
//  1. 建立于本机日期 D-2 的 00:30（+08:00），换算 UTC 落在 D-3：校准日期
//     D-3 的证书必须被拒绝（早于建立日期 D-2），D-2 的证书可以完成。
//  2. 建立于本机日期 D-2 的 23:30（-07:00），换算 UTC 已到 D-1：校准日期
//     D-2（与建立同日，证书无时分秒）的证书仍应完成，D-3 的证书被拒绝。

// seedPlanWithCreatedAt 在 path 写入一件在用器具和一项 CreatedAt 为指定带
// 偏移时间的未完成计划；证书留空，由测试随后用真实命令录入。
func seedPlanWithCreatedAt(t *testing.T, path, createdAt string) {
	t.Helper()
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status":        string(calibrate.StatusInUse),
			"registered_at": createdAt,
		}},
		"certificates": []any{},
		"usage":        []any{},
		"plans": []map[string]any{{
			"number": "PL-1", "instrument_id": "M-1",
			"planned_date": "2030-10-20", "original_date": "2030-10-20",
			"note": "年度例行校准", "created_at": createdAt, "status": calibrate.PlanStatusOpen,
		}},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化预置台账: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入预置台账 %s: %v", path, err)
	}
}

// localDayMinusOffset 返回“运行当天的 n 天前”这一本机日历日的指定时分，
// 放在 loc 偏移下的带偏移时间字符串（RFC3339），使换算 UTC 可落到另一天。
func localDayMinusOffset(t *testing.T, n int, hour, min int, loc *time.Location) string {
	t.Helper()
	base := time.Now().AddDate(0, 0, -n)
	return time.Date(base.Year(), base.Month(), base.Day(), hour, min, 0, 0, loc).
		Format(time.RFC3339)
}

// readPlanJSON 直接从台账文件读取计划，核对落盘内容未被操作时区改写。
func readPlanJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取台账 %s: %v", path, err)
	}
	var data struct {
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账 %s: %v", path, err)
	}
	if len(data.Plans) != 1 {
		t.Fatalf("应只有一项计划，得到 %d", len(data.Plans))
	}
	return data.Plans[0]
}

func boundaryCertArgs(path, number, calDate string) []string {
	return certFullArgs(path, "M-1", number, calDate, "2037-12-31", "0.1")
}

// TestCLICompleteCreatedDayEastOfUTC 命令行方向 1：建立时刻 +08:00 的 00:30
// 换算 UTC 是前一天。早于建立本机日期一天的证书在普通输出与 --json 下都按
// 既有业务方式拒绝（退出 1），计划保持未完成、留在待办；建立当天的证书
// 正常完成并移出待办，落盘的建立时间与偏移、证书校准日期保持原文。
func TestCLICompleteCreatedDayEastOfUTC(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账-东八区.json")
	loc := time.FixedZone("UTC+8", 8*3600)
	createdAt := localDayMinusOffset(t, 2, 0, 30, loc)
	createdDay := daysAgo(t, 2)
	prevDay := daysAgo(t, 3)
	seedPlanWithCreatedAt(t, path, createdAt)

	// 两张证书：一张校准日期是建立本机日期的前一天（换算 UTC 恰好与建立
	// 时刻同一天），一张是建立当天。
	if r := runArgs(t, boundaryCertArgs(path, "C-PREV", prevDay)...); r.code != 0 {
		t.Fatalf("前一天的证书本身应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, boundaryCertArgs(path, "C-DAY", createdDay)...); r.code != 0 {
		t.Fatalf("建立当天的证书应能录入：code=%d stderr=%s", r.code, r.stderr)
	}

	// 用换算后“看似不早”的前一天证书完成：仍须按业务拒绝，原因指明
	// 证书日期早于计划建立的本机日期。
	human := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-PREV")
	if human.code != 1 {
		t.Fatalf("早于建立本机日期应业务拒绝退出 1，得到 %d", human.code)
	}
	if !strings.Contains(human.stderr, "已拒绝") ||
		!strings.Contains(human.stderr, prevDay) ||
		!strings.Contains(human.stderr, "建立日期 "+createdDay) {
		t.Fatalf("普通输出应指出 %s 早于建立日期 %s：stderr=%q",
			prevDay, createdDay, human.stderr)
	}
	js := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-PREV")
	if js.code != 1 || !strings.Contains(js.stdout, `"accepted": false`) ||
		!strings.Contains(js.stdout, "建立日期 "+createdDay) {
		t.Fatalf("--json 应退出 1 且 accepted=false 并说明建立日期：code=%d stdout=%q",
			js.code, js.stdout)
	}
	assertPlanOpenInCLI(t, path)

	// 落盘内容原样保留：带偏移建立时间、计划日期、说明都不被拒绝动作改写。
	if p := readPlanJSON(t, path); p["created_at"] != createdAt ||
		p["planned_date"] != "2030-10-20" || p["note"] != "年度例行校准" ||
		p["status"] != calibrate.PlanStatusOpen {
		t.Fatalf("拒绝后落盘计划被改动：%+v", p)
	}

	// 建立当天的证书正常完成，不必等到 2030-10-20 的计划日。
	ok := runArgs(t, "complete", "-f", path, "--number", "PL-1", "--certificate", "C-DAY")
	if ok.code != 0 || !strings.Contains(ok.stdout, "已完成：计划 PL-1，证书 C-DAY") {
		t.Fatalf("建立当天的证书应完成成功：code=%d stdout=%q stderr=%s",
			ok.code, ok.stdout, ok.stderr)
	}
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("完成后应移出待办：%s", tv.stdout)
	}

	// 核对中显示已完成并保留建立时间原文、证书编号与非空完成时间。
	rv := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	var view struct {
		Plans []struct {
			Status            string `json:"status"`
			CertificateNumber string `json:"certificate_number"`
			CompletedAt       string `json:"completed_at"`
			CreatedAt         string `json:"created_at"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rv.stdout), &view); err != nil {
		t.Fatalf("review JSON 解析失败: %v\n%s", err, rv.stdout)
	}
	if len(view.Plans) != 1 || view.Plans[0].Status != calibrate.PlanStatusDone ||
		view.Plans[0].CertificateNumber != "C-DAY" ||
		view.Plans[0].CompletedAt == "" ||
		view.Plans[0].CreatedAt != createdAt {
		t.Fatalf("核对中完成信息异常或建立时间被换算改写：%+v", view.Plans)
	}

	// 同证书重复完成幂等返回原完成时间。
	first := view.Plans[0].CompletedAt
	again := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-DAY")
	var av completeJSONView
	if err := json.Unmarshal([]byte(again.stdout), &av); err != nil {
		t.Fatalf("幂等完成 JSON 解析失败: %v\n%s", err, again.stdout)
	}
	if again.code != 0 || !av.Idempotent || av.Plan == nil || av.Plan.CompletedAt != first {
		t.Fatalf("重复完成应幂等返回原完成时间：code=%d %+v", again.code, av)
	}
}

// TestCLICompleteCreatedDayWestOfUTC 命令行方向 2：建立时刻 -07:00 的 23:30
// 换算 UTC 已是次日。与建立本机日期同日的证书仍满足日期条件；早一天的证书
// 被拒绝。
func TestCLICompleteCreatedDayWestOfUTC(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "台账-西七区.json")
	loc := time.FixedZone("UTC-7", -7*3600)
	createdAt := localDayMinusOffset(t, 2, 23, 30, loc)
	createdDay := daysAgo(t, 2)
	prevDay := daysAgo(t, 3)
	seedPlanWithCreatedAt(t, path, createdAt)

	if r := runArgs(t, boundaryCertArgs(path, "C-PREV", prevDay)...); r.code != 0 {
		t.Fatalf("前一天的证书本身应能录入：code=%d stderr=%s", r.code, r.stderr)
	}
	if r := runArgs(t, boundaryCertArgs(path, "C-DAY", createdDay)...); r.code != 0 {
		t.Fatalf("建立当天的证书应能录入：code=%d stderr=%s", r.code, r.stderr)
	}

	// 早一天的证书在两个方向都被拒绝，拒绝后计划仍未完成并留在待办。
	rj := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-PREV")
	if rj.code != 1 || !strings.Contains(rj.stdout, `"accepted": false`) ||
		!strings.Contains(rj.stdout, "建立日期 "+createdDay) {
		t.Fatalf("前一天的证书应退出 1 且说明建立日期 %s：code=%d stdout=%q",
			createdDay, rj.code, rj.stdout)
	}
	assertPlanOpenInCLI(t, path)

	// 与建立本机日期同日的证书：即使建立时刻换算 UTC 已到次日，也应完成。
	ok := runArgs(t, "complete", "-f", path, "--json",
		"--number", "PL-1", "--certificate", "C-DAY")
	var v completeJSONView
	if err := json.Unmarshal([]byte(ok.stdout), &v); err != nil {
		t.Fatalf("完成 JSON 解析失败: %v\n%s", err, ok.stdout)
	}
	if ok.code != 0 || !v.Accepted || v.Idempotent || v.Plan == nil ||
		v.Plan.Status != calibrate.PlanStatusDone || v.Plan.CertificateNumber != "C-DAY" ||
		v.Plan.CompletedAt == "" {
		t.Fatalf("建立当天（无时分秒）的证书应正常完成：code=%d %+v", ok.code, v)
	}
	if tv := runArgs(t, "todos", "-f", path, "--json"); !strings.Contains(tv.stdout, `"count": 0`) {
		t.Fatalf("完成后应移出待办：%s", tv.stdout)
	}
	// 落盘的建立时间保留 -07:00 原文，不被换算成次日。
	if p := readPlanJSON(t, path); p["created_at"] != createdAt ||
		p["certificate_number"] != "C-DAY" {
		t.Fatalf("落盘建立时间与关联证书异常：%+v", p)
	}
}
