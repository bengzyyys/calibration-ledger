package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

// 本文件从命令行端到端守住“用证书完成校准计划”的一条跨时区界限：
//
//	证书校准日期不得早于计划最初建立时的本机日历日期，等于该日期即可完成。
//
// 计划建立时间在保存时自带当时的时区偏移；之后即使把本机时区切换到 UTC 或
// 其他时区、在更晚的日期完成，界限仍锚定在“记录偏移所表示的那个本机日历日”
// （2026-10-02），既不能因换算 UTC 落到前一天而放行 10-01 的证书，也不能因
// 换算 UTC 落到后一天而把 10-01 误报成“早于 10-03”或拒绝 10-02 的证书。
//
// 这里把命令行编译成独立子进程，用进程级 TZ 环境变量真实切换本机时区（Go 运行时
// 在进程启动时按 TZ 初始化本地时区），与只在进程内拨时钟不同，能覆盖“建立后切换
// 本机时区再完成”的真实路径。证书预置为“此前已按原有规则合法录入”，故本测试的
// 业务结论不依赖实际运行日期：完成入口只比较证书校准日期与计划建立日本机日。

// tzCompleteView 是 complete --json 中本测试关心的字段。
type tzCompleteView struct {
	Accepted   bool   `json:"accepted"`
	Idempotent bool   `json:"idempotent"`
	Error      string `json:"error"`
	Plan       *struct {
		Number            string `json:"number"`
		Status            string `json:"status"`
		PlannedDate       string `json:"planned_date"`
		CreatedAt         string `json:"created_at"`
		CompletedAt       string `json:"completed_at"`
		CertificateNumber string `json:"certificate_number"`
	} `json:"plan"`
}

// tzReviewView 是 review --json 中本测试关心的计划与证书日期字段。
type tzReviewView struct {
	Plans []struct {
		Number            string `json:"number"`
		Status            string `json:"status"`
		PlannedDate       string `json:"planned_date"`
		CreatedAt         string `json:"created_at"`
		CompletedAt       string `json:"completed_at"`
		CertificateNumber string `json:"certificate_number"`
	} `json:"plans"`
	History []struct {
		Number  string `json:"number"`
		CalDate string `json:"cal_date"`
	} `json:"history"`
}

// buildLedgerBinary 把命令行编译成临时二进制；go test 的工作目录即本包目录，
// 无参数 go build 构建的就是 cmd/ledger。
func buildLedgerBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ledger")
	cmd := exec.Command("go", "build", "-o", bin)
	var buildErr bytes.Buffer
	cmd.Stderr = &buildErr
	if err := cmd.Run(); err != nil {
		t.Fatalf("编译命令行失败: %v\n%s", err, buildErr.String())
	}
	return bin
}

// runLedgerTZ 在指定本机时区下以子进程运行一次命令行，返回退出码与输出。
func runLedgerTZ(t *testing.T, bin, tz string, args ...string) result {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "TZ="+tz)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("运行命令行异常（非退出码错误）: %v", err)
		}
		code = ee.ExitCode()
	}
	return result{code: code, stdout: out.String(), stderr: errBuf.String()}
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("解析 JSON 失败: %v\n%s", err, raw)
	}
	return v
}

// seedCreationTimezoneLedger 预置一件在用器具、两张此前已合法录入的证书
// （校准日期分别为 10-01、10-02）和一项建立时间为 createdAt 的未完成计划。
func seedCreationTimezoneLedger(t *testing.T, path, createdAt string) {
	t.Helper()
	data := map[string]any{
		"version": 1,
		"instruments": []map[string]any{{
			"id": "M-1", "name": "万用表", "allowed_error": 0.5,
			"status":        string(calibrate.StatusInUse),
			"registered_at": createdAt,
		}},
		"certificates": []map[string]any{
			{
				"number": "C-PREV", "instrument_id": "M-1", "cal_date": "2026-10-01",
				"expiry": "2027-10-01", "method": "m", "error": 0.1,
				"summary": "建立前一天", "created_at": createdAt,
			},
			{
				"number": "C-DAY", "instrument_id": "M-1", "cal_date": "2026-10-02",
				"expiry": "2027-10-02", "method": "m", "error": 0.1,
				"summary": "建立当天", "created_at": createdAt,
			},
		},
		"usage": []any{},
		"plans": []map[string]any{{
			"number": "PL-1", "instrument_id": "M-1",
			"planned_date": "2030-10-20", "original_date": "2030-10-20",
			"note": "跨时区周期校准", "created_at": createdAt,
			"status": calibrate.PlanStatusOpen,
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

// TestCompletePlanCreationDateBoundBySavedOffsetAcrossTZ 端到端覆盖两个出错方向。
func TestCompletePlanCreationDateBoundBySavedOffsetAcrossTZ(t *testing.T) {
	bin := buildLedgerBinary(t)

	cases := []struct {
		name      string
		createdAt string
		// 完成后用“对立时区”做幂等复查与核对，证明换时区不改写结果。
		recheckTZ string
	}{
		{
			// 换算 UTC 为 2026-10-01T16:30:00Z：不能因此接受 10-01 的证书。
			name:      "东八区凌晨建立_切UTC完成",
			createdAt: "2026-10-02T00:30:00+08:00",
			recheckTZ: "America/Los_Angeles",
		},
		{
			// 换算 UTC 为 2026-10-03T06:30:00Z：不能因此把界限推到 10-03，
			// 10-02 的证书必须仍能完成。
			name:      "西七区深夜建立_切UTC完成",
			createdAt: "2026-10-02T23:30:00-07:00",
			recheckTZ: "Asia/Shanghai",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ledger.json")
			seedCreationTimezoneLedger(t, path, tc.createdAt)

			// 在更晚的日期、本机时区已切换为 UTC 后尝试用 10-01 的证书完成：
			// 必须按业务拒绝（退出码 1），原因明确指出证书 10-01 早于计划的
			// 10-02 建立日期。stderr 带“已拒绝”，沿用既有业务拒绝返回含义。
			rejected := runLedgerTZ(t, bin, "UTC",
				"complete", "-f", path, "--number", "PL-1",
				"--certificate", "C-PREV", "--json")
			if rejected.code != 1 {
				t.Fatalf("10-01 证书应业务拒绝（退出码 1），得到 code=%d stdout=%s stderr=%s",
					rejected.code, rejected.stdout, rejected.stderr)
			}
			if !strings.Contains(rejected.stderr, "已拒绝") {
				t.Fatalf("业务拒绝应在 stderr 标注“已拒绝”，得到 %q", rejected.stderr)
			}
			rv := decodeJSON[tzCompleteView](t, rejected.stdout)
			if rv.Accepted {
				t.Fatalf("被拒绝时 accepted 必须为 false：%s", rejected.stdout)
			}
			for _, want := range []string{"C-PREV", "2026-10-01", "PL-1", "2026-10-02", "早于", "建立日期"} {
				if !strings.Contains(rv.Error, want) {
					t.Fatalf("拒绝原因应包含 %q（界限必须是建立日本机日 10-02），得到 %q",
						want, rv.Error)
				}
			}

			// 拒绝后计划仍未完成并留在待办：待办计数为 1。
			todos := runLedgerTZ(t, bin, "UTC", "todos", "-f", path, "--json")
			if todos.code != 0 {
				t.Fatalf("todos 查询失败 code=%d stderr=%s", todos.code, todos.stderr)
			}
			tv := decodeJSON[struct {
				Count int `json:"count"`
				Todos []struct {
					PlanNumber string `json:"plan_number"`
				} `json:"todos"`
			}](t, todos.stdout)
			if tv.Count != 1 || len(tv.Todos) != 1 || tv.Todos[0].PlanNumber != "PL-1" {
				t.Fatalf("被拒绝的计划应继续留在待办，得到 %s", todos.stdout)
			}

			// 用建立当天（10-02）的证书完成：正常成功（退出码 0），不必等到
			// 原定的 2030-10-20 计划日；保存证书编号与本次完成时间。
			done := runLedgerTZ(t, bin, "UTC",
				"complete", "-f", path, "--number", "PL-1",
				"--certificate", "C-DAY", "--json")
			if done.code != 0 {
				t.Fatalf("10-02（建立日本机日）的证书应完成计划，code=%d stderr=%s",
					done.code, done.stderr)
			}
			dv := decodeJSON[tzCompleteView](t, done.stdout)
			if !dv.Accepted || dv.Idempotent || dv.Plan == nil {
				t.Fatalf("完成应 accepted=true、idempotent=false，得到 %s", done.stdout)
			}
			if dv.Plan.Status != calibrate.PlanStatusDone ||
				dv.Plan.CertificateNumber != "C-DAY" || dv.Plan.CompletedAt == "" {
				t.Fatalf("完成结果应保存已完成状态、C-DAY 与完成时间：%+v", dv.Plan)
			}
			if dv.Plan.CreatedAt != tc.createdAt {
				t.Fatalf("完成结果中的建立时间应保留原偏移串 %s，得到 %s",
					tc.createdAt, dv.Plan.CreatedAt)
			}
			if dv.Plan.PlannedDate != "2030-10-20" {
				t.Fatalf("计划日期不应被完成动作改动，得到 %s", dv.Plan.PlannedDate)
			}
			completedAt := dv.Plan.CompletedAt

			// 完成后计划从待办移除。
			todosAfter := runLedgerTZ(t, bin, "UTC", "todos", "-f", path, "--json")
			after := decodeJSON[struct {
				Count int `json:"count"`
			}](t, todosAfter.stdout)
			if after.Count != 0 {
				t.Fatalf("完成后待办应为空，得到 %s", todosAfter.stdout)
			}

			// 切换到对立时区、更晚日期：同证书重复完成幂等返回原结果，完成时间
			// 串逐字符不变（不刷新、不按新时区重新表示）。
			again := runLedgerTZ(t, bin, tc.recheckTZ,
				"complete", "-f", path, "--number", "PL-1",
				"--certificate", "C-DAY", "--json")
			if again.code != 0 {
				t.Fatalf("换时区后同证书重复完成应幂等成功，code=%d stderr=%s",
					again.code, again.stderr)
			}
			av := decodeJSON[tzCompleteView](t, again.stdout)
			if !av.Accepted || !av.Idempotent || av.Plan == nil {
				t.Fatalf("重复完成应 accepted=true、idempotent=true，得到 %s", again.stdout)
			}
			if av.Plan.Status != calibrate.PlanStatusDone ||
				av.Plan.CertificateNumber != "C-DAY" ||
				av.Plan.CompletedAt != completedAt {
				t.Fatalf("换时区幂等返回应原样保留 C-DAY 与原完成时间（%s），得到 %+v",
					completedAt, av.Plan)
			}

			// 在对立时区按器具核对：计划显示已完成、关联 C-DAY、完成时间为原值；
			// 建立时间保留保存时的原偏移串，两张证书校准日期保持原内容。
			review := runLedgerTZ(t, bin, tc.recheckTZ,
				"review", "-f", path, "--id", "M-1", "--json")
			if review.code != 0 {
				t.Fatalf("核对失败 code=%d stderr=%s", review.code, review.stderr)
			}
			rv2 := decodeJSON[tzReviewView](t, review.stdout)
			if len(rv2.Plans) != 1 {
				t.Fatalf("应只有一项计划，得到 %d", len(rv2.Plans))
			}
			pl := rv2.Plans[0]
			if pl.Status != calibrate.PlanStatusDone || pl.CertificateNumber != "C-DAY" ||
				pl.CompletedAt != completedAt {
				t.Fatalf("核对中计划应已完成、关联 C-DAY 且完成时间不变：%+v", pl)
			}
			if pl.CreatedAt != tc.createdAt {
				t.Fatalf("建立时间串应保留原偏移 %s，操作时区不应改写，得到 %s",
					tc.createdAt, pl.CreatedAt)
			}
			if pl.PlannedDate != "2030-10-20" {
				t.Fatalf("核对中计划日期应保留 2030-10-20，得到 %s", pl.PlannedDate)
			}
			calDates := map[string]string{}
			for _, h := range rv2.History {
				calDates[h.Number] = h.CalDate
			}
			if calDates["C-DAY"] != "2026-10-02" || calDates["C-PREV"] != "2026-10-01" {
				t.Fatalf("证书校准日期应保留原内容，得到 %+v", calDates)
			}
		})
	}
}
