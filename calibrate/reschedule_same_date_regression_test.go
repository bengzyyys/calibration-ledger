package calibrate

import (
	"testing"
	"time"
)

// TestRescheduleSameDateAfterNormalChange 验证同日期改期不被当作“没有变化”跳过：
// 计划先正常改期（最初日期与当前日期已经不同），再提交与当前计划日期完全相同的
// 新日期，只要日期不早于本机今天且原因非空，这次申请就成功保存一条改期记录。
// 新增记录的前后日期都等于当前计划日期（不是把最初日期当作修改前日期），原因和
// 操作时间属于这次申请；已有改期记录、当前计划日期、最初日期、说明与未完成状态
// 都保持原样，计划仍留在待办中，核对能看到前后日期相同的新记录。
func TestRescheduleSameDateAfterNormalChange(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "年度送检"})

	// 先有一次正常改期：2026-10-10 → 2026-10-20，最初日期与当前日期不同。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-20", "实验室排期冲突")
	if err != nil {
		t.Fatalf("正常改期应成功: %v", err)
	}
	if len(first.Changes) != 1 {
		t.Fatalf("正常改期后应有一条记录，得到 %+v", first.Changes)
	}

	// 再提交与当前计划日期完全相同的新日期：日期相同不等于未发生操作。
	clock.t = mustDate(t, "2026-10-05")
	second, err := l.ReschedulePlan("P-1", "2026-10-20", "按原排期再次确认")
	if err != nil {
		t.Fatalf("与当前日期相同的改期应成功保存记录，得到 %v", err)
	}
	// 计划的当前日期、最初日期、说明与未完成状态不因这次申请改变。
	if second.PlannedDate != "2026-10-20" || second.OriginalDate != "2026-10-10" ||
		second.Note != "年度送检" || second.Status != PlanStatusOpen {
		t.Fatalf("同日期改期不应改动计划本身字段：%+v", second)
	}
	if len(second.Changes) != 2 {
		t.Fatalf("同日期改期应新增一条记录，共两条，得到 %+v", second.Changes)
	}
	// 已有改期记录的日期、原因和时间保持原样。
	if second.Changes[0] != first.Changes[0] {
		t.Fatalf("已有改期记录应保持原样：原 %+v 现 %+v", first.Changes[0], second.Changes[0])
	}
	// 新增记录的前后日期都等于当前计划日期，而不是把最初日期当作修改前日期。
	added := second.Changes[1]
	if added.From != "2026-10-20" || added.To != "2026-10-20" {
		t.Fatalf("同日期记录的前后日期应都是当前计划日期 2026-10-20，得到 %+v", added)
	}
	// 原因和操作时间属于这次申请。
	if added.Reason != "按原排期再次确认" {
		t.Fatalf("新记录应保留本次提交的原因，得到 %q", added.Reason)
	}
	if want := clock.t.Format(time.RFC3339); added.ChangedAt != want {
		t.Fatalf("新记录的操作时间应取本次申请时刻 %s，得到 %s", want, added.ChangedAt)
	}
	if added.ChangedAt == first.Changes[0].ChangedAt {
		t.Fatal("新记录的操作时间不能复用上一条记录的时间")
	}

	// 台账内保存的正式记录与返回结果一致。
	saved := l.findPlan("P-1")
	if saved == nil || len(saved.Changes) != 2 || saved.Changes[1] != added ||
		saved.PlannedDate != "2026-10-20" || saved.OriginalDate != "2026-10-10" {
		t.Fatalf("台账内计划应与返回结果一致，得到 %+v", saved)
	}

	// 计划仍留在待办中，待办标记继续由当前计划日期与查询当天的关系决定：
	// 10-05 查询为“未到计划日”，到 10-20 当天重新判断为“今天需校准”。
	todos, err := l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].PlannedDate != "2026-10-20" || todos[0].Marker != TodoFuture {
		t.Fatalf("同日期改期后计划应留在待办且标记为 %q，得到 %+v", TodoFuture, todos)
	}
	clock.t = mustDate(t, "2026-10-20")
	todos, err = l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoToday {
		t.Fatalf("计划日当天查询应标记为 %q，得到 %+v", TodoToday, todos)
	}

	// 按器具核对能看到新增记录：前后日期相同也不能被隐藏。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 {
		t.Fatalf("核对应展示当前计划，得到 %+v", r.Plans)
	}
	view := r.Plans[0]
	if len(view.Changes) != 2 {
		t.Fatalf("核对应展示全部两条改期记录，得到 %+v", view.Changes)
	}
	if view.Changes[1].From != "2026-10-20" || view.Changes[1].To != "2026-10-20" ||
		view.Changes[1].Reason != "按原排期再次确认" {
		t.Fatalf("核对中前后日期相同的记录不能被隐藏，得到 %+v", view.Changes[1])
	}
	if view.PlannedDate != "2026-10-20" || view.OriginalDate != "2026-10-10" ||
		view.Marker != TodoToday {
		t.Fatalf("核对中的计划视图异常：%+v", view)
	}
}

// TestRescheduleSameDateTwiceNotMerged 验证同一计划连续两次合法的同日期改期各
// 自分别增加一条记录，按提交顺序保留各自的原因和操作时间；即使两次原因完全
// 相同，也不能合并为一条或直接返回上次结果。改期不影响校准证书、器具状态和
// 历史使用记录。
func TestRescheduleSameDateTwiceNotMerged(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	if err := l.SetStatus("M-1", StatusInUse); err != nil {
		t.Fatalf("status: %v", err)
	}
	mustAddCert(t, l, CertificateInput{
		InstrumentID: "M-1", Number: "C-1", CalDate: "2026-10-02",
		Expiry: "2027-10-02", Method: "m", Error: 0.1, Summary: "s",
	})
	if _, err := l.RequestUse("M-1"); err != nil {
		t.Fatalf("request use: %v", err)
	}
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-10", Note: "周期"})

	// 第一次同日期改期。
	clock.t = mustDate(t, "2026-10-03")
	first, err := l.ReschedulePlan("P-1", "2026-10-10", "再次确认排期")
	if err != nil {
		t.Fatalf("第一次同日期改期应成功: %v", err)
	}
	if len(first.Changes) != 1 || first.Changes[0].From != "2026-10-10" ||
		first.Changes[0].To != "2026-10-10" {
		t.Fatalf("第一次同日期改期记录异常：%+v", first.Changes)
	}

	// 第二次同日期改期，原因与第一次完全相同：仍应新增一条记录，不能合并
	// 为一条或直接返回上次结果。
	clock.t = mustDate(t, "2026-10-04")
	second, err := l.ReschedulePlan("P-1", "2026-10-10", "再次确认排期")
	if err != nil {
		t.Fatalf("第二次同日期改期应成功: %v", err)
	}
	if len(second.Changes) != 2 {
		t.Fatalf("两次同日期改期应各留一条记录，得到 %+v", second.Changes)
	}
	// 按提交顺序保留各自的原因和时间。
	if second.Changes[0] != first.Changes[0] {
		t.Fatalf("第一条记录应保持原样：原 %+v 现 %+v", first.Changes[0], second.Changes[0])
	}
	if second.Changes[1].From != "2026-10-10" || second.Changes[1].To != "2026-10-10" ||
		second.Changes[1].Reason != "再次确认排期" {
		t.Fatalf("第二条记录异常：%+v", second.Changes[1])
	}
	if want := clock.t.Format(time.RFC3339); second.Changes[1].ChangedAt != want {
		t.Fatalf("第二条记录时间应取第二次申请时刻 %s，得到 %s", want, second.Changes[1].ChangedAt)
	}
	if second.Changes[1].ChangedAt == second.Changes[0].ChangedAt {
		t.Fatal("两次申请的操作时间应分别保留，不能复用")
	}
	if second.PlannedDate != "2026-10-10" || second.OriginalDate != "2026-10-10" ||
		second.Status != PlanStatusOpen {
		t.Fatalf("计划字段不应被同日期改期改动：%+v", second)
	}

	// 校准证书、器具状态和历史使用记录不因改期变化。
	inst, err := l.Instrument("M-1")
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	if inst.Status != StatusInUse {
		t.Fatalf("器具状态不应因改期变化，得到 %s", inst.Status)
	}
	latest := l.LatestCertificate("M-1")
	if latest == nil || latest.Number != "C-1" || latest.CalDate != "2026-10-02" {
		t.Fatalf("证书不应因改期变化，得到 %+v", latest)
	}
	usage := l.UsageRecords()
	if len(usage) != 1 || !usage[0].Allowed {
		t.Fatalf("历史使用记录不应因改期变化，得到 %+v", usage)
	}

	// 核对中两条同日期记录都可见，使用拒绝记录与证书历史不受影响。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || len(r.Plans[0].Changes) != 2 {
		t.Fatalf("核对应展示两条同日期改期记录，得到 %+v", r.Plans)
	}
	if len(r.History) != 1 || r.History[0].Number != "C-1" {
		t.Fatalf("证书历史不应因改期变化，得到 %+v", r.History)
	}
}

// TestRescheduleSameDateBoundaryAndRejections 验证同日期改期的边界与拒绝规则：
// 日期恰好等于本机今天时同日期改期仍可成功；当前计划日期已经早于操作当天时，
// 再提交这个原日期必须按日期已过明确拒绝，不能因为日期没有改变而绕过限制；
// 日期合法但原因为空白时同样拒绝，不生成没有原因的历史记录。两类失败都保留
// 原来的计划日期及全部改期历史，失败申请的原因和时间不进入核对结果。
func TestRescheduleSameDateBoundaryAndRejections(t *testing.T) {
	clock := &fakeClock{t: mustDate(t, "2026-10-02")}
	l := newTestLedger(t, clock)
	mustRegister(t, l, "M-1", "万用表", 0.5)
	// 计划日期就定在本机今天。
	mustPlan(t, l, PlanInput{Number: "P-1", InstrumentID: "M-1", Date: "2026-10-02", Note: "当天校准"})

	// 日期恰好等于本机今天：同日期改期成功，前后日期都是今天。
	today, err := l.ReschedulePlan("P-1", "2026-10-02", "当天再次确认")
	if err != nil {
		t.Fatalf("日期等于本机今天的同日期改期应成功，得到 %v", err)
	}
	if len(today.Changes) != 1 || today.Changes[0].From != "2026-10-02" ||
		today.Changes[0].To != "2026-10-02" || today.Changes[0].Reason != "当天再次确认" {
		t.Fatalf("当天同日期改期记录异常：%+v", today.Changes)
	}
	if today.PlannedDate != "2026-10-02" || today.OriginalDate != "2026-10-02" {
		t.Fatalf("当天同日期改期不应改动计划日期：%+v", today)
	}

	// 时钟前进一天：当前计划日期 2026-10-02 已经早于操作当天。再提交这个
	// 原日期必须按日期已过明确拒绝，不能因为日期没有改变而绕过限制。
	clock.t = mustDate(t, "2026-10-03")
	if _, err := l.ReschedulePlan("P-1", "2026-10-02", "还想停在原日期"); !IsValidation(err) {
		t.Fatalf("已过的原日期应按日期已过拒绝，得到 %v", err)
	}
	// 日期合法（今天）但原因为空白：同样拒绝，不生成没有原因的历史记录。
	if _, err := l.ReschedulePlan("P-1", "2026-10-03", "   "); !IsValidation(err) {
		t.Fatalf("空白原因应拒绝，得到 %v", err)
	}

	// 两类失败都保留原来的计划日期及全部改期历史。
	saved := l.findPlan("P-1")
	if saved.PlannedDate != "2026-10-02" || saved.OriginalDate != "2026-10-02" ||
		saved.Status != PlanStatusOpen {
		t.Fatalf("失败的申请不应改动计划，得到 %+v", saved)
	}
	if len(saved.Changes) != 1 || saved.Changes[0] != today.Changes[0] {
		t.Fatalf("失败的申请不应留下记录，已有记录应保持原样：%+v", saved.Changes)
	}

	// 失败申请的原因和时间不进入核对结果；计划仍在待办，标记按查询当天
	// 重新判断（10-03 查询 10-02 的计划为“逾期”）。
	r, err := l.Review("M-1")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(r.Plans) != 1 || len(r.Plans[0].Changes) != 1 {
		t.Fatalf("核对中不应出现失败申请的记录，得到 %+v", r.Plans)
	}
	for _, ch := range r.Plans[0].Changes {
		if ch.Reason == "还想停在原日期" || ch.Reason == "" {
			t.Fatalf("失败申请的原因不应进入核对结果：%+v", ch)
		}
	}
	if r.Plans[0].Marker != TodoOverdue {
		t.Fatalf("已过的计划日期在次日核对中应标记为 %q，得到 %q", TodoOverdue, r.Plans[0].Marker)
	}
	todos, err := l.Todos("M-1")
	if err != nil {
		t.Fatalf("todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Marker != TodoOverdue {
		t.Fatalf("已过的计划日期在次日待办中应标记为 %q，得到 %+v", TodoOverdue, todos)
	}

	// 失败后合法改期仍可进行：从最后成功保存的日期接到新日期。
	clock.t = mustDate(t, "2026-10-04")
	ok, err := l.ReschedulePlan("P-1", "2026-10-10", "重新排期")
	if err != nil {
		t.Fatalf("失败后的合法改期应成功: %v", err)
	}
	if len(ok.Changes) != 2 || ok.Changes[1].From != "2026-10-02" ||
		ok.Changes[1].To != "2026-10-10" || ok.PlannedDate != "2026-10-10" {
		t.Fatalf("失败后的合法改期应从最后保存的日期接起：%+v", ok.Changes)
	}
}
