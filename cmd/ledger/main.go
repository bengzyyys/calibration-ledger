// Command ledger 是本地器具校准台账的命令行入口。
//
// 用法：
//
//	ledger -f 台账文件.json <子命令> [参数]
//
// 数据只保存在 -f 指定的本机文件中；不指定时使用当前目录下的 ledger.json。
// 不同文件是互不混入的不同台账，退出后再次打开同一文件可继续查询和操作。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

const usageText = `本地器具校准台账（仅本机，不连接真实实验室）

用法：
  ledger -f <台账文件> [--json] <子命令> [参数]

子命令：
  register   登记器具：--id 编号 --name 名称 --allowed 允许绝对误差
  status     切换状态：--id 编号 --status 在用|停用|待校准
  cert       录入证书：--instrument 器具编号 --number 证书编号 \
             --cal-date YYYY-MM-DD --expiry YYYY-MM-DD \
             --method 方法 --error 测得误差 --summary 摘要
  use        申请使用器具：--id 编号（允许与拒绝都会留痕）
  review     按器具核对：--id 编号
  list       列出全部登记器具
  plan       建立校准计划：--instrument 器具编号 --number 计划编号 \
             --date YYYY-MM-DD --note 非空说明
  reschedule 计划改期：--number 计划编号 --date 新日期 --reason 非空原因
  cancel     取消计划：--number 计划编号 --reason 非空原因
  complete   用证书完成计划：--number 计划编号 --certificate 证书编号
  todos      校准待办：[--instrument 器具编号]（默认全部未结束计划）

通用参数：
  -f, --file   台账文件路径（默认 ledger.json）
      --json   以 JSON 输出结果

退出码：0 成功；1 业务拒绝或未找到；2 参数或读写错误。
`

// options 是所有子命令共用的全局参数。
type options struct {
	file   string
	asJSON bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var opts options
	// 手工解析位于子命令前后的全局参数，flag 包不支持子命令前的散落标志。
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		a := rest[0]
		switch {
		case a == "-f" || a == "--file":
			if len(rest) < 2 {
				fmt.Fprintf(stderr, "%s 需要一个值\n", a)
				return 2
			}
			opts.file = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(a, "--file="):
			opts.file = strings.TrimPrefix(a, "--file=")
			rest = rest[1:]
		case strings.HasPrefix(a, "-f") && len(a) > 2:
			opts.file = a[2:]
			rest = rest[1:]
		case a == "--json":
			opts.asJSON = true
			rest = rest[1:]
		default:
			fmt.Fprintf(stderr, "未知全局参数 %s\n\n%s", a, usageText)
			return 2
		}
	}
	if opts.file == "" {
		opts.file = "ledger.json"
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Fprint(stdout, usageText)
		return 0
	}

	l, err := calibrate.Open(opts.file)
	if err != nil {
		fmt.Fprintln(stderr, "打开台账失败：", err)
		return 2
	}

	switch cmd {
	case "register":
		return cmdRegister(l, cmdArgs, opts, stdout, stderr)
	case "status":
		return cmdStatus(l, cmdArgs, opts, stdout, stderr)
	case "cert":
		return cmdCert(l, cmdArgs, opts, stdout, stderr)
	case "use":
		return cmdUse(l, cmdArgs, opts, stdout, stderr)
	case "review":
		return cmdReview(l, cmdArgs, opts, stdout, stderr)
	case "list":
		return cmdList(l, cmdArgs, opts, stdout, stderr)
	case "plan":
		return cmdPlan(l, cmdArgs, opts, stdout, stderr)
	case "reschedule":
		return cmdReschedule(l, cmdArgs, opts, stdout, stderr)
	case "cancel":
		return cmdCancel(l, cmdArgs, opts, stdout, stderr)
	case "complete":
		return cmdComplete(l, cmdArgs, opts, stdout, stderr)
	case "todos":
		return cmdTodos(l, cmdArgs, opts, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n\n%s", cmd, usageText)
		return 2
	}
}

// newFlagSet 构造子命令自己的标志集，全局参数仍可写在子命令之后。
func newFlagSet(name string, opts *options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.file, "f", opts.file, "台账文件路径")
	fs.StringVar(&opts.file, "file", opts.file, "台账文件路径")
	fs.BoolVar(&opts.asJSON, "json", opts.asJSON, "以 JSON 输出")
	return fs
}

func parseFloat(stderr io.Writer, raw, field string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		fmt.Fprintf(stderr, "%s 必须是有限数字\n", field)
		return 0, false
	}
	return v, true
}

func emit(opts options, stdout, stderr io.Writer, human string, v any) int {
	if opts.asJSON {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, "输出 JSON 失败：", err)
			return 2
		}
		fmt.Fprintln(stdout, string(raw))
		return 0
	}
	fmt.Fprint(stdout, human)
	return 0
}

// failBusiness 处理业务拒绝：退出码 1，原记录未被改动。
func failBusiness(opts options, stdout, stderr io.Writer, err error, rejected any) int {
	if opts.asJSON {
		raw, _ := json.MarshalIndent(rejected, "", "  ")
		fmt.Fprintln(stdout, string(raw))
	}
	fmt.Fprintln(stderr, "已拒绝：", err)
	return 1
}

func failIO(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "操作失败：", err)
	return 2
}

func cmdRegister(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("register", &opts)
	id := fs.String("id", "", "器具唯一编号")
	name := fs.String("name", "", "器具名称")
	allowed := fs.String("allowed", "", "允许的绝对误差（不小于零的有限数）")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "register 参数错误：", err)
		return 2
	}
	if *id == "" || *name == "" || *allowed == "" {
		fmt.Fprintln(stderr, "register 必须提供 --id、--name、--allowed")
		return 2
	}
	allowedErr, ok := parseFloat(stderr, *allowed, "允许的绝对误差")
	if !ok {
		return 2
	}
	inst, err := l.Register(calibrate.RegisterInput{
		ID: *id, Name: *name, AllowedError: allowedErr,
	})
	if err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"accepted": false, "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	human := fmt.Sprintf("已登记器具 %s（%s），允许绝对误差 %g，状态：%s\n",
		inst.ID, inst.Name, inst.AllowedError, inst.Status)
	return emit(opts, stdout, stderr, human, map[string]any{"accepted": true, "instrument": inst})
}

func cmdStatus(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("status", &opts)
	id := fs.String("id", "", "器具编号")
	status := fs.String("status", "", "在用|停用|待校准")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "status 参数错误：", err)
		return 2
	}
	if *id == "" || *status == "" {
		fmt.Fprintln(stderr, "status 必须提供 --id、--status")
		return 2
	}
	st, err := calibrate.ParseStatus(*status)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := l.SetStatus(*id, st); err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"accepted": false, "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	human := fmt.Sprintf("器具 %s 已切换为%s\n", *id, st)
	return emit(opts, stdout, stderr, human,
		map[string]any{"accepted": true, "id": *id, "status": st})
}

func cmdCert(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("cert", &opts)
	instrument := fs.String("instrument", "", "器具编号")
	number := fs.String("number", "", "证书编号（全台账唯一）")
	calDate := fs.String("cal-date", "", "校准日期 YYYY-MM-DD")
	expiry := fs.String("expiry", "", "有效期截止日 YYYY-MM-DD")
	method := fs.String("method", "", "校准方法")
	measured := fs.String("error", "", "测得误差（有限数）")
	summary := fs.String("summary", "", "证书摘要")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "cert 参数错误：", err)
		return 2
	}
	missing := []string{}
	for k, v := range map[string]string{
		"--instrument": *instrument, "--number": *number, "--cal-date": *calDate,
		"--expiry": *expiry, "--method": *method, "--error": *measured, "--summary": *summary,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintln(stderr, "cert 缺少必填参数：", strings.Join(missing, "、"))
		return 2
	}
	measuredErr, ok := parseFloat(stderr, *measured, "测得误差")
	if !ok {
		return 2
	}
	cert, duplicate, err := l.AddCertificate(calibrate.CertificateInput{
		InstrumentID: *instrument,
		Number:       *number,
		CalDate:      *calDate,
		Expiry:       *expiry,
		Method:       *method,
		Error:        measuredErr,
		Summary:      *summary,
	})
	if err != nil {
		if calibrate.IsValidation(err) || calibrate.IsConflict(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"accepted": false, "conflict": calibrate.IsConflict(err), "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	note := "已录入新证书"
	if duplicate {
		note = "同号证书且内容一致，返回原证书，未增加历史记录"
	}
	verdict := "合格"
	if math.Abs(cert.Error) > allowedErrorOf(l, cert.InstrumentID) {
		verdict = "超差"
	}
	human := fmt.Sprintf("%s：%s（器具 %s，校准日期 %s，截止日 %s，测得误差 %g，判定：%s）\n",
		note, cert.Number, cert.InstrumentID, cert.CalDate, cert.Expiry, cert.Error, verdict)
	return emit(opts, stdout, stderr, human,
		map[string]any{"accepted": true, "duplicate": duplicate, "verdict": verdict, "certificate": cert})
}

func allowedErrorOf(l *calibrate.Ledger, id string) float64 {
	r, err := l.Review(id)
	if err != nil {
		return 0
	}
	return r.Instrument.AllowedError
}

func cmdUse(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("use", &opts)
	id := fs.String("id", "", "器具编号")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "use 参数错误：", err)
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(stderr, "use 必须提供 --id")
		return 2
	}
	d, err := l.RequestUse(*id)
	if err != nil {
		if errors.Is(err, calibrate.ErrNotFound) || calibrate.IsValidation(err) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"accepted": false, "recorded": false, "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	if !d.Allowed {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("拒绝使用器具 %s，已记录本次拒绝及原因：\n", *id))
		for _, r := range d.Reasons {
			sb.WriteString("  - " + r + "\n")
		}
		if opts.asJSON {
			raw, _ := json.MarshalIndent(map[string]any{
				"allowed": false, "recorded": true, "decision": d,
			}, "", "  ")
			fmt.Fprintln(stdout, string(raw))
		}
		fmt.Fprint(stderr, sb.String())
		return 1
	}
	human := fmt.Sprintf("允许使用器具 %s，已记录本次申请。\n", *id)
	return emit(opts, stdout, stderr, human,
		map[string]any{"allowed": true, "recorded": true, "decision": d})
}

func cmdReview(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("review", &opts)
	id := fs.String("id", "", "器具编号")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "review 参数错误：", err)
		return 2
	}
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(stderr, "review 必须提供 --id")
		return 2
	}
	r, err := l.Review(*id)
	if err != nil {
		if errors.Is(err, calibrate.ErrNotFound) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"found": false, "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "器具 %s（%s）\n", r.Instrument.ID, r.Instrument.Name)
	fmt.Fprintf(&sb, "  允许绝对误差：%g；当前状态：%s\n", r.Instrument.AllowedError, r.Instrument.Status)
	if r.CanUse {
		sb.WriteString("  当前能否使用：可以\n")
	} else {
		sb.WriteString("  当前能否使用：不可以，原因：\n")
		for _, reason := range r.Reasons {
			sb.WriteString("    - " + reason + "\n")
		}
	}
	if r.Latest == nil {
		sb.WriteString("  最近证书：无\n")
	} else {
		fmt.Fprintf(&sb, "  最近证书：%s（校准日期 %s，截止日 %s，判定：%s，%s）\n",
			r.Latest.Number, r.Latest.CalDate, r.Latest.Expiry,
			verdictText(r.Latest.Pass), expiryText(r.Latest.Expired))
	}
	if len(r.History) == 0 {
		sb.WriteString("  历史证书：无\n")
	} else {
		sb.WriteString("  历史证书（按校准日期由近到远）：\n")
		for _, c := range r.History {
			fmt.Fprintf(&sb, "    - %s %s 截止 %s 误差 %g %s %s（%s）\n",
				c.Number, c.CalDate, c.Expiry, c.Error,
				verdictText(c.Pass), expiryText(c.Expired), c.Method)
		}
	}
	if len(r.Rejections) == 0 {
		sb.WriteString("  被拒绝的使用记录：无\n")
	} else {
		sb.WriteString("  被拒绝的使用记录（原因为申请当时冻结）：\n")
		for _, u := range r.Rejections {
			sb.WriteString("    - " + u.RequestedAt + "\n")
			for _, reason := range u.Reasons {
				sb.WriteString("        - " + reason + "\n")
			}
		}
	}
	if len(r.Plans) == 0 {
		sb.WriteString("  校准计划：无\n")
	} else {
		sb.WriteString("  校准计划（含当前及已结束计划、改期记录与关联证书）：\n")
		for _, p := range r.Plans {
			head := fmt.Sprintf("    - %s %s 状态：%s", p.Number, p.PlannedDate, p.Status)
			if p.Open() {
				head += "（" + p.Marker + "）"
			}
			if p.OriginalDate != p.PlannedDate {
				head += fmt.Sprintf("，最初计划日期 %s", p.OriginalDate)
			}
			head += "，说明：" + p.Note + "\n"
			sb.WriteString(head)
			for _, ch := range p.Changes {
				fmt.Fprintf(&sb, "        改期 %s → %s（%s），原因：%s\n",
					ch.From, ch.To, ch.ChangedAt, ch.Reason)
			}
			if p.Status == calibrate.PlanStatusCanceled {
				fmt.Fprintf(&sb, "        取消于 %s，原因：%s\n", p.CanceledAt, p.CancelReason)
			}
			if p.Status == calibrate.PlanStatusDone {
				fmt.Fprintf(&sb, "        完成于 %s，证书 %s\n", p.CompletedAt, p.CertificateNumber)
			}
		}
	}
	return emit(opts, stdout, stderr, sb.String(), r)
}

func verdictText(pass bool) string {
	if pass {
		return "合格"
	}
	return "超差"
}

func expiryText(expired bool) string {
	if expired {
		return "已到期"
	}
	return "未到期"
}

func cmdList(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("list", &opts)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "list 参数错误：", err)
		return 2
	}
	instruments := l.Instruments()
	var sb strings.Builder
	if len(instruments) == 0 {
		sb.WriteString("台账中还没有器具。\n")
	}
	for _, in := range instruments {
		fmt.Fprintf(&sb, "%s\t%s\t允许误差 %g\t%s\n", in.ID, in.Name, in.AllowedError, in.Status)
	}
	return emit(opts, stdout, stderr, sb.String(),
		map[string]any{"instruments": instruments, "count": len(instruments)})
}

// failPlanBusiness 是计划类命令共用的业务拒绝输出（退出码 1）。
func failPlanBusiness(opts options, stdout, stderr io.Writer, err error, extra map[string]any) int {
	resp := map[string]any{"accepted": false, "error": err.Error()}
	for k, v := range extra {
		resp[k] = v
	}
	return failBusiness(opts, stdout, stderr, err, resp)
}

func cmdPlan(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("plan", &opts)
	instrument := fs.String("instrument", "", "器具编号")
	number := fs.String("number", "", "计划编号（全台账唯一，不可复用）")
	date := fs.String("date", "", "计划日期 YYYY-MM-DD（不早于今天）")
	note := fs.String("note", "", "非空计划说明")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "plan 参数错误：", err)
		return 2
	}
	missing := []string{}
	for k, v := range map[string]string{
		"--instrument": *instrument, "--number": *number, "--date": *date, "--note": *note,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintln(stderr, "plan 缺少必填参数：", strings.Join(missing, "、"))
		return 2
	}
	p, err := l.CreatePlan(calibrate.PlanInput{
		Number: *number, InstrumentID: *instrument, Date: *date, Note: *note,
	})
	if err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failPlanBusiness(opts, stdout, stderr, err, nil)
		}
		return failIO(stderr, err)
	}
	human := fmt.Sprintf("已为器具 %s 建立校准计划 %s，计划日期 %s，说明：%s\n",
		p.InstrumentID, p.Number, p.PlannedDate, p.Note)
	return emit(opts, stdout, stderr, human, map[string]any{"accepted": true, "plan": p})
}

func cmdReschedule(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("reschedule", &opts)
	number := fs.String("number", "", "计划编号")
	date := fs.String("date", "", "新计划日期 YYYY-MM-DD（不早于今天）")
	reason := fs.String("reason", "", "非空改期原因")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "reschedule 参数错误：", err)
		return 2
	}
	missing := []string{}
	for k, v := range map[string]string{
		"--number": *number, "--date": *date, "--reason": *reason,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintln(stderr, "reschedule 缺少必填参数：", strings.Join(missing, "、"))
		return 2
	}
	p, err := l.ReschedulePlan(*number, *date, *reason)
	if err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failPlanBusiness(opts, stdout, stderr, err, nil)
		}
		return failIO(stderr, err)
	}
	human := fmt.Sprintf("计划 %s 已改期为 %s（第 %d 次改期）。\n",
		p.Number, p.PlannedDate, len(p.Changes))
	return emit(opts, stdout, stderr, human, map[string]any{"accepted": true, "plan": p})
}

func cmdCancel(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("cancel", &opts)
	number := fs.String("number", "", "计划编号")
	reason := fs.String("reason", "", "非空取消原因")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "cancel 参数错误：", err)
		return 2
	}
	if strings.TrimSpace(*number) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "cancel 必须提供 --number、--reason")
		return 2
	}
	p, err := l.CancelPlan(*number, *reason)
	if err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failPlanBusiness(opts, stdout, stderr, err, nil)
		}
		return failIO(stderr, err)
	}
	human := fmt.Sprintf("计划 %s 已于 %s 取消，原因：%s；不再列入待办。\n",
		p.Number, p.CanceledAt, p.CancelReason)
	return emit(opts, stdout, stderr, human, map[string]any{"accepted": true, "plan": p})
}

func cmdComplete(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("complete", &opts)
	number := fs.String("number", "", "计划编号")
	certificate := fs.String("certificate", "", "证书编号")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "complete 参数错误：", err)
		return 2
	}
	if strings.TrimSpace(*number) == "" || strings.TrimSpace(*certificate) == "" {
		fmt.Fprintln(stderr, "complete 必须提供 --number、--certificate")
		return 2
	}
	p, idempotent, err := l.CompletePlan(*number, *certificate)
	if err != nil {
		if calibrate.IsValidation(err) || errors.Is(err, calibrate.ErrNotFound) {
			return failPlanBusiness(opts, stdout, stderr, err, nil)
		}
		return failIO(stderr, err)
	}
	note := "已完成"
	if idempotent {
		note = "该计划已由此证书完成，返回原结果，未刷新完成时间"
	}
	human := fmt.Sprintf("%s：计划 %s，证书 %s，完成时间 %s。\n",
		note, p.Number, p.CertificateNumber, p.CompletedAt)
	return emit(opts, stdout, stderr, human,
		map[string]any{"accepted": true, "idempotent": idempotent, "plan": p})
}

func cmdTodos(l *calibrate.Ledger, args []string, opts options, stdout, stderr io.Writer) int {
	fs := newFlagSet("todos", &opts)
	instrument := fs.String("instrument", "", "只看该器具编号的待办")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "todos 参数错误：", err)
		return 2
	}
	items, err := l.Todos(*instrument)
	if err != nil {
		if errors.Is(err, calibrate.ErrNotFound) {
			return failBusiness(opts, stdout, stderr, err,
				map[string]any{"found": false, "error": err.Error()})
		}
		return failIO(stderr, err)
	}
	var sb strings.Builder
	if len(items) == 0 {
		sb.WriteString("无待办。\n")
	}
	for _, it := range items {
		fmt.Fprintf(&sb, "%s\t%s（%s，%s）\t%s\t%s\t%s\n",
			it.PlanNumber, it.InstrumentID, it.InstrumentName, it.Status,
			it.PlannedDate, it.Marker, it.Note)
	}
	return emit(opts, stdout, stderr, sb.String(),
		map[string]any{"todos": items, "count": len(items)})
}
