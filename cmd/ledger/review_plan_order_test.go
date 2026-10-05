package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// seedOffsetLedger 直接写入一份含跨时区建立时间计划的台账文件：命令行建立
// 计划总是盖本机当前时间，不同系统时区留下的历史只能通过台账文件本身模拟。
func seedOffsetLedger(t *testing.T, path string) {
	t.Helper()
	data := struct {
		Version      int                     `json:"version"`
		Instruments  []calibrate.Instrument  `json:"instruments"`
		Certificates []calibrate.Certificate `json:"certificates"`
		Usage        []calibrate.UsageRecord `json:"usage"`
		Plans        []calibrate.Plan        `json:"plans"`
	}{
		Version: 1,
		Instruments: []calibrate.Instrument{
			{
				ID: "M-1", Name: "万用表", AllowedError: 0.5,
				Status: calibrate.StatusInUse, RegisteredAt: "2026-09-01T00:00:00Z",
			},
			{
				ID: "M-2", Name: "示波器", AllowedError: 1.0,
				Status: calibrate.StatusInUse, RegisteredAt: "2026-09-01T00:00:00Z",
			},
			{
				ID: "M-3", Name: "无计划器具", AllowedError: 0.2,
				Status: calibrate.StatusPending, RegisteredAt: "2026-09-01T00:00:00Z",
			},
		},
		// 存储顺序按错误的字符串/入库顺序写入：
		//	PL-A 2026-10-06T02:00:00Z       = 10-06 02:00Z（实际最晚，已取消）
		//	PL-B 2026-10-06T09:30:00+08:00  = 10-06 01:30Z（实际早 A 半小时，已完成）
		//	PL-C 2026-10-06T00:15:00+08:00  = 10-05 16:15Z（实际最早，未完成）
		// 因此按建立实际时刻的正确顺序是 C、B、A，三种状态混在一起。
		Plans: []calibrate.Plan{
			{
				Number: "PL-A", InstrumentID: "M-1",
				PlannedDate: "2026-12-01", OriginalDate: "2026-12-01", Note: "最晚建立",
				CreatedAt: "2026-10-06T02:00:00Z", Status: calibrate.PlanStatusCanceled,
				CanceledAt: "2026-10-06T05:00:00Z", CancelReason: "暂停送检",
			},
			{
				Number: "PL-B", InstrumentID: "M-1",
				PlannedDate: "2026-11-20", OriginalDate: "2026-11-18", Note: "带偏移的较早计划",
				CreatedAt: "2026-10-06T09:30:00+08:00", Status: calibrate.PlanStatusDone,
				Changes: []calibrate.PlanChange{{
					From: "2026-11-18", To: "2026-11-20",
					ChangedAt: "2026-10-10T09:00:00+08:00", Reason: "实验室排期冲突",
				}},
				CompletedAt: "2026-11-20T10:00:00+08:00", CertificateNumber: "C-1",
			},
			{
				Number: "PL-C", InstrumentID: "M-1",
				PlannedDate: "2026-11-10", OriginalDate: "2026-11-10", Note: "跨日实际最早",
				CreatedAt: "2026-10-06T00:15:00+08:00", Status: calibrate.PlanStatusOpen,
			},
			// M-2 的未结束计划：建立实际时刻比 PL-C 更早，但计划日期更晚，
			// 用来守住待办仍按计划日期排列，而不是按建立时刻。
			{
				Number: "PL-D", InstrumentID: "M-2",
				PlannedDate: "2026-12-20", OriginalDate: "2026-12-20", Note: "待办日期更晚",
				CreatedAt: "2026-10-01T00:00:00Z", Status: calibrate.PlanStatusOpen,
			},
		},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化测试台账失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写入测试台账失败: %v", err)
	}
}

var planHeadRE = regexp.MustCompile(`(?m)^    - (PL-\S+) `)

// reviewPlanOrder 从 review 普通文本中按出现行序提取计划编号。
func reviewPlanOrder(t *testing.T, out string) []string {
	t.Helper()
	matches := planHeadRE.FindAllStringSubmatch(out, -1)
	got := make([]string, len(matches))
	for i, m := range matches {
		got[i] = m[1]
	}
	return got
}

type cliReviewPlans struct {
	Found bool `json:"found"`
	Plans []struct {
		Number            string `json:"number"`
		CreatedAt         string `json:"created_at"`
		Status            string `json:"status"`
		CertificateNumber string `json:"certificate_number"`
		CancelReason      string `json:"cancel_reason"`
	} `json:"plans"`
}

// TestReviewPlansOrderedByActualInstantTextAndJSON 验证 review 的普通文本与
// --json 结果中，同一件器具的计划都按建立的实际时刻从早到晚排列，且顺序一致：
// 已取消、已完成、未完成计划一起排列，不先按状态分组。
func TestReviewPlansOrderedByActualInstantTextAndJSON(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "ledger.json")
	seedOffsetLedger(t, path)
	want := []string{"PL-C", "PL-B", "PL-A"}

	// 普通文本：按建立实际时刻排列，且仍展示改期、完成证书与取消信息。
	human := runArgs(t, "review", "--id", "M-1", "-f", path)
	if human.code != 0 {
		t.Fatalf("review 普通文本失败 code=%d stderr=%s", human.code, human.stderr)
	}
	if got := reviewPlanOrder(t, human.stdout); !equalStrings(got, want) {
		t.Fatalf("普通文本计划应按实际时刻 C、B、A 排列，得到 %v", got)
	}
	if !contains(human.stdout, "证书 C-1") || !contains(human.stdout, "暂停送检") {
		t.Fatalf("普通文本应保留关联证书与取消信息：%q", human.stdout)
	}

	// --json：同样的顺序，且建立时间文字与偏移原样保留。
	jr := runArgs(t, "review", "--id", "M-1", "-f", path, "--json")
	if jr.code != 0 {
		t.Fatalf("review --json 失败 code=%d stderr=%s", jr.code, jr.stderr)
	}
	var rv cliReviewPlans
	if err := json.Unmarshal([]byte(jr.stdout), &rv); err != nil {
		t.Fatalf("解析 review JSON 失败: %v\n%s", err, jr.stdout)
	}
	if len(rv.Plans) != 3 {
		t.Fatalf("JSON 应列出三项计划，得到 %d", len(rv.Plans))
	}
	got := make([]string, len(rv.Plans))
	for i, p := range rv.Plans {
		got[i] = p.Number
	}
	if !equalStrings(got, want) {
		t.Fatalf("JSON 计划顺序应与普通文本一致为 C、B、A，得到 %v", got)
	}
	if rv.Plans[0].CreatedAt != "2026-10-06T00:15:00+08:00" ||
		rv.Plans[1].CreatedAt != "2026-10-06T09:30:00+08:00" ||
		rv.Plans[2].CreatedAt != "2026-10-06T02:00:00Z" {
		t.Fatalf("建立时间文字与偏移必须原样保留：%+v", rv.Plans)
	}
	if rv.Plans[1].CertificateNumber != "C-1" || rv.Plans[2].CancelReason != "暂停送检" {
		t.Fatalf("关联证书或取消信息丢失：%+v", rv.Plans)
	}

	// 查询只调整展示顺序，台账存储顺序不变（仍为 A、B、C、D）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回台账失败: %v", err)
	}
	var stored struct {
		Plans []struct {
			Number string `json:"number"`
		} `json:"plans"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("解析台账失败: %v", err)
	}
	if len(stored.Plans) != 4 ||
		stored.Plans[0].Number != "PL-A" || stored.Plans[1].Number != "PL-B" ||
		stored.Plans[2].Number != "PL-C" || stored.Plans[3].Number != "PL-D" {
		t.Fatalf("查询不应重排台账存储记录：%+v", stored.Plans)
	}
}

// TestTodosStillOrderedByPlannedDateAfterPlanOrderFix 守住待办规则不受本次
// 排序修正影响：待办继续按计划日期排列，未结束计划的日期标记照常给出。
func TestTodosStillOrderedByPlannedDateAfterPlanOrderFix(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "ledger.json")
	seedOffsetLedger(t, path)

	r := runArgs(t, "todos", "-f", path, "--json")
	if r.code != 0 {
		t.Fatalf("todos 失败 code=%d stderr=%s", r.code, r.stderr)
	}
	var v struct {
		Todos []struct {
			PlanNumber  string `json:"plan_number"`
			PlannedDate string `json:"planned_date"`
			Marker      string `json:"marker"`
		} `json:"todos"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatalf("解析 todos JSON 失败: %v\n%s", err, r.stdout)
	}
	// 已取消 PL-A、已完成 PL-B 不列入待办；PL-C（11-10）虽比 PL-D（12-20）
	// 建立得晚，仍因计划日期更早排在前面。
	if v.Count != 2 || v.Todos[0].PlanNumber != "PL-C" || v.Todos[1].PlanNumber != "PL-D" {
		t.Fatalf("待办应只含未结束计划并按计划日期排列：%+v", v.Todos)
	}
	if v.Todos[0].Marker == "" {
		t.Fatalf("未结束计划应继续给出按查询当天判断的日期标记：%+v", v.Todos[0])
	}
}

// TestReviewPlansEmptyAndUnknown 守住无计划器具返回空结果、未知器具明确
// 报告不存在且不产生使用记录。
func TestReviewPlansEmptyAndUnknown(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "ledger.json")
	seedOffsetLedger(t, path)

	empty := runArgs(t, "review", "--id", "M-3", "-f", path)
	if empty.code != 0 || !contains(empty.stdout, "校准计划：无") {
		t.Fatalf("无计划器具应明确显示无计划：code=%d stdout=%q stderr=%s",
			empty.code, empty.stdout, empty.stderr)
	}
	ej := runArgs(t, "review", "--id", "M-3", "-f", path, "--json")
	if ej.code != 0 || !contains(ej.stdout, `"plans": []`) {
		t.Fatalf("无计划器具 JSON 应返回空计划数组：code=%d stdout=%q", ej.code, ej.stdout)
	}

	missing := runArgs(t, "review", "--id", "NOPE", "-f", path, "--json")
	if missing.code != 1 || !contains(missing.stdout, `"found": false`) {
		t.Fatalf("未知器具应退出码 1 且 found=false：code=%d stdout=%q stderr=%s",
			missing.code, missing.stdout, missing.stderr)
	}

	var data struct {
		Usage []json.RawMessage `json:"usage"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回台账失败: %v", err)
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("解析台账失败: %v", err)
	}
	if len(data.Usage) != 0 {
		t.Fatalf("核对查询不应产生使用记录，得到 %d 条", len(data.Usage))
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
