// calibrate 是本地器具校准台账的命令行入口。
//
// 台账文件路径通过 --ledger 指定，或设置环境变量 CAL_LEDGER。
// 用法：
//
//	calibrate --ledger <台账文件> <命令> [参数]
//
// 命令：
//
//	register   登记器具
//	set-status 切换器具状态（在用|停用|待校准）
//	cert-add   录入校准证书
//	use        申请使用器具
//	check      按器具核对
//	list       列出全部器具
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bengzyyys/calibration-ledger/calibrate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ledgerPath := fs.String("ledger", "", "台账文件路径（也可用环境变量 CAL_LEDGER）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(os.Stderr)
		return fmt.Errorf("缺少命令")
	}
	path := *ledgerPath
	if path == "" {
		path = os.Getenv("CAL_LEDGER")
	}
	if path == "" {
		return fmt.Errorf("请通过 --ledger 指定台账文件路径，或设置环境变量 CAL_LEDGER")
	}
	l, err := calibrate.Open(path)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "register":
		return cmdRegister(l, rest[1:])
	case "set-status":
		return cmdSetStatus(l, rest[1:])
	case "cert-add":
		return cmdCertAdd(l, rest[1:])
	case "use":
		return cmdUse(l, rest[1:])
	case "check":
		return cmdCheck(l, rest[1:])
	case "list":
		return cmdList(l, rest[1:])
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("未知命令 %q", rest[0])
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "用法: calibrate --ledger <台账文件> <命令> [参数]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "命令:")
	fmt.Fprintln(w, "  register    --id <编号> --name <名称> --allowed-error <允许绝对误差>")
	fmt.Fprintln(w, "  set-status  --id <编号> --state <在用|停用|待校准>")
	fmt.Fprintln(w, "  cert-add    --instrument <器具编号> --number <证书编号> \\")
	fmt.Fprintln(w, "              --cal-date <YYYY-MM-DD> --expiry <YYYY-MM-DD> \\")
	fmt.Fprintln(w, "              --method <方法> --measured-error <测得误差> --summary <摘要>")
	fmt.Fprintln(w, "  use         --id <编号>")
	fmt.Fprintln(w, "  check       --id <编号>")
	fmt.Fprintln(w, "  list")
}

func cmdRegister(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	id := fs.String("id", "", "器具编号（唯一，不能留空）")
	name := fs.String("name", "", "器具名称（不能留空）")
	allowed := fs.Float64("allowed-error", 0, "允许绝对误差（有限且不小于零）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := l.RegisterInstrument(*id, *name, *allowed); err != nil {
		return err
	}
	fmt.Printf("已登记器具 %q（%s），状态：待校准\n", strings.TrimSpace(*id), strings.TrimSpace(*name))
	return nil
}

func cmdSetStatus(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("set-status", flag.ContinueOnError)
	id := fs.String("id", "", "器具编号")
	state := fs.String("state", "", "在用 | 停用 | 待校准")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var st calibrate.Status
	switch strings.TrimSpace(*state) {
	case "在用":
		st = calibrate.StatusActive
	case "停用":
		st = calibrate.StatusInactive
	case "待校准":
		st = calibrate.StatusPending
	default:
		return fmt.Errorf("无效状态 %q，应为 在用、停用或待校准", *state)
	}
	if err := l.SetStatus(*id, st); err != nil {
		return err
	}
	fmt.Printf("器具 %q 已切换为：%s\n", strings.TrimSpace(*id), st)
	return nil
}

func cmdCertAdd(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("cert-add", flag.ContinueOnError)
	instrument := fs.String("instrument", "", "器具编号")
	number := fs.String("number", "", "证书编号（台账内唯一）")
	calDate := fs.String("cal-date", "", "校准日期 YYYY-MM-DD")
	expiry := fs.String("expiry", "", "有效期截止日 YYYY-MM-DD")
	method := fs.String("method", "", "校准方法")
	measured := fs.Float64("measured-error", 0, "测得误差（有限数）")
	summary := fs.String("summary", "", "摘要")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cert, created, err := l.AddCertificate(calibrate.Certificate{
		InstrumentID:  *instrument,
		Number:        *number,
		CalDate:       *calDate,
		ExpiryDate:    *expiry,
		Method:        *method,
		MeasuredError: *measured,
		Summary:       *summary,
	})
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("已录入证书 %q：校准日期 %s，有效期至 %s，方法 %s\n", cert.Number, cert.CalDate, cert.ExpiryDate, cert.Method)
	} else {
		fmt.Printf("证书 %q 已存在且业务字段一致，返回原证书，未增加历史记录\n", cert.Number)
	}
	inst := l.Instrument(cert.InstrumentID)
	if inst == nil {
		return fmt.Errorf("内部错误: 器具 %q 不存在", cert.InstrumentID)
	}
	if calibrate.Qualified(inst.AllowedError, cert.MeasuredError) {
		fmt.Printf("结论：合格（|测得误差 %g| 未超过允许误差 %g）\n", cert.MeasuredError, inst.AllowedError)
	} else {
		fmt.Printf("结论：超差（|测得误差 %g| 超过允许误差 %g）\n", cert.MeasuredError, inst.AllowedError)
	}
	return nil
}

func cmdUse(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	id := fs.String("id", "", "器具编号")
	if err := fs.Parse(args); err != nil {
		return err
	}
	approved, reasons, err := l.ApplyUse(*id)
	if err != nil {
		return err
	}
	if approved {
		fmt.Printf("批准使用器具 %q\n", strings.TrimSpace(*id))
		return nil
	}
	fmt.Printf("拒绝使用器具 %q，原因：\n", strings.TrimSpace(*id))
	for _, r := range reasons {
		fmt.Printf("  - %s\n", r)
	}
	return nil
}

func cmdCheck(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	id := fs.String("id", "", "器具编号")
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := l.Check(*id)
	if err != nil {
		return err
	}
	inst := res.Instrument
	fmt.Printf("器具核对：%s（%s）\n", inst.ID, inst.Name)
	fmt.Printf("  允许误差：%g\n", inst.AllowedError)
	fmt.Printf("  状态：%s\n", inst.Status)
	if res.Approved {
		fmt.Println("  当前能否使用：能")
	} else {
		fmt.Println("  当前能否使用：不能")
		for _, r := range res.Reasons {
			fmt.Printf("    - %s\n", r)
		}
	}
	if res.LatestCertificate != nil {
		c := res.LatestCertificate
		fmt.Printf("  最近证书：%s（校准日期 %s，有效期至 %s，方法 %s）\n", c.Number, c.CalDate, c.ExpiryDate, c.Method)
		if calibrate.Qualified(inst.AllowedError, c.MeasuredError) {
			fmt.Printf("    证书结论：合格（|测得误差 %g| <= 允许误差 %g）\n", c.MeasuredError, inst.AllowedError)
		} else {
			fmt.Printf("    证书结论：超差（|测得误差 %g| > 允许误差 %g）\n", c.MeasuredError, inst.AllowedError)
		}
		if calibrate.Expired(c.ExpiryDate, time.Now()) {
			fmt.Println("    证书状态：已到期（截止日当天起算到期）")
		} else {
			fmt.Println("    证书状态：有效")
		}
	} else {
		fmt.Println("  最近证书：无")
	}
	fmt.Printf("  全部历史证书（%d 份，按校准日期排序）：\n", len(res.Certificates))
	for i, c := range res.Certificates {
		verdict := "合格"
		if !calibrate.Qualified(inst.AllowedError, c.MeasuredError) {
			verdict = "超差"
		}
		fmt.Printf("    %d. 编号 %s，校准日期 %s，有效期至 %s，方法 %s，测得误差 %g（%s），摘要 %s\n",
			i+1, c.Number, c.CalDate, c.ExpiryDate, c.Method, c.MeasuredError, verdict, c.Summary)
	}
	fmt.Printf("  使用申请记录（%d 条）：\n", len(res.UsageRecords))
	for i, u := range res.UsageRecords {
		result := "批准"
		if !u.Approved {
			result = "拒绝"
		}
		reasons := "无"
		if len(u.Reasons) > 0 {
			reasons = strings.Join(u.Reasons, "；")
		}
		fmt.Printf("    %d. %s  %s  原因：%s\n", i+1, u.Time.Format("2006-01-02 15:04:05"), result, reasons)
	}
	return nil
}

func cmdList(l *calibrate.Ledger, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	instruments := l.ListInstruments()
	if len(instruments) == 0 {
		fmt.Println("台账中还没有器具")
		return nil
	}
	fmt.Printf("编号\t名称\t状态\t允许误差\n")
	for _, inst := range instruments {
		fmt.Printf("%s\t%s\t%s\t%g\n", inst.ID, inst.Name, inst.Status, inst.AllowedError)
	}
	return nil
}
