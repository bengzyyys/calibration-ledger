package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMixedOffsetLedger 直接写一份台账：同一器具的两项计划在不同系统时区
// 偏移下建立，且实际较晚的（02:00Z）在文件中排在前面。
// 09:30+08:00 == 01:30Z，实际早于 02:00Z 半小时。
func writeMixedOffsetLedger(t *testing.T, path string) {
	t.Helper()
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status": "待校准", "registered_at": "2026-10-01T09:00:00+08:00",
		}},
		"certificates": []any{},
		"usage":        []any{},
		"plans": []map[string]any{
			{
				"number": "PL-LATE", "instrument_id": "M-1",
				"planned_date": "2026-10-25", "original_date": "2026-10-25",
				"note": "较晚建立", "created_at": "2026-10-06T02:00:00Z",
				"status": "未完成",
			},
			{
				"number": "PL-EARLY", "instrument_id": "M-1",
				"planned_date": "2026-10-20", "original_date": "2026-10-20",
				"note": "较早建立", "created_at": "2026-10-06T09:30:00+08:00",
				"status":       "已完成",
				"completed_at": "2026-10-08T02:00:00Z", "certificate_number": "C-1",
			},
		},
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("序列化测试台账: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写测试台账: %v", err)
	}
}

// TestReviewPlansOrderPlainAndJSON 验证 review 普通文本与 --json 结果都按
// 建立实际时刻排列（PL-EARLY 在 PL-LATE 前），且两种入口顺序一致。
func TestReviewPlansOrderPlainAndJSON(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "ledger.json")
	writeMixedOffsetLedger(t, path)

	// 普通文本：较早建立的计划应先出现，建立时间不影响显示文字。
	r := runArgs(t, "review", "-f", path, "--id", "M-1")
	if r.code != 0 {
		t.Fatalf("review 退出码 %d，stderr=%s", r.code, r.stderr)
	}
	idxEarly := strings.Index(r.stdout, "PL-EARLY")
	idxLate := strings.Index(r.stdout, "PL-LATE")
	if idxEarly < 0 || idxLate < 0 {
		t.Fatalf("两项计划都应显示：\n%s", r.stdout)
	}
	if idxEarly > idxLate {
		t.Fatalf("普通文本应按建立实际时刻排列，PL-EARLY 应在 PL-LATE 前：\n%s", r.stdout)
	}

	// --json：plans 数组顺序应一致，且保留各自原始建立时间文字与偏移。
	rj := runArgs(t, "review", "-f", path, "--id", "M-1", "--json")
	if rj.code != 0 {
		t.Fatalf("review --json 退出码 %d，stderr=%s", rj.code, rj.stderr)
	}
	var resp struct {
		Plans []struct {
			Number      string `json:"number"`
			CreatedAt   string `json:"created_at"`
			Status      string `json:"status"`
			PlannedDate string `json:"planned_date"`
			CertNumber  string `json:"certificate_number"`
			CompletedAt string `json:"completed_at"`
		} `json:"plans"`
	}
	if err := json.Unmarshal([]byte(rj.stdout), &resp); err != nil {
		t.Fatalf("解析 JSON 输出: %v\n%s", err, rj.stdout)
	}
	if len(resp.Plans) != 2 {
		t.Fatalf("应有两项计划，得到 %d", len(resp.Plans))
	}
	if resp.Plans[0].Number != "PL-EARLY" || resp.Plans[1].Number != "PL-LATE" {
		t.Fatalf("JSON 应按建立实际时刻排列，得到 %s、%s",
			resp.Plans[0].Number, resp.Plans[1].Number)
	}
	if resp.Plans[0].CreatedAt != "2026-10-06T09:30:00+08:00" ||
		resp.Plans[1].CreatedAt != "2026-10-06T02:00:00Z" {
		t.Fatalf("建立时间原文字与偏移被改动：%+v", resp.Plans)
	}
	// 完成信息与计划日期保持保存时内容，未因排序丢失。
	if resp.Plans[0].CertNumber != "C-1" ||
		resp.Plans[0].CompletedAt != "2026-10-08T02:00:00Z" ||
		resp.Plans[0].PlannedDate != "2026-10-20" {
		t.Fatalf("已完成计划的关联信息丢失：%+v", resp.Plans[0])
	}

	// 查询不得重排台账文件内的存储记录：PL-LATE 仍在 PL-EARLY 前。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回台账: %v", err)
	}
	text := string(raw)
	if strings.Index(text, "PL-LATE") > strings.Index(text, "PL-EARLY") {
		t.Fatalf("查询不应重排台账内存储记录")
	}
}

// TestReviewUnknownInstrumentStillNotFound 验证排序修正后未知器具仍明确
// 报告不存在（普通与 --json 均退出码 1）。
func TestReviewUnknownInstrumentStillNotFound(t *testing.T) {
	dir := chdirTemp(t)
	path := filepath.Join(dir, "ledger.json")
	writeMixedOffsetLedger(t, path)

	r := runArgs(t, "review", "-f", path, "--id", "NOPE")
	if r.code != 1 {
		t.Fatalf("未知器具普通输出应退出码 1，得到 %d", r.code)
	}
	rj := runArgs(t, "review", "-f", path, "--id", "NOPE", "--json")
	if rj.code != 1 {
		t.Fatalf("未知器具 --json 应退出码 1，得到 %d", rj.code)
	}
	if !strings.Contains(rj.stdout, `"found": false`) {
		t.Fatalf("--json 应报告 found:false：%s", rj.stdout)
	}
}
