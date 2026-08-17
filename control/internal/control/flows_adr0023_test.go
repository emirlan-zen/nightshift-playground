package control

// ADR-0023 coverage: runtime fan-out (emit-nodes), the judge/eval loop's
// persistence, and harness comparison via member slots + per-member runtime.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// scoreStore gives a test a real (temp) obs store wired into the package's
// obsDB handle, so score writes exercise the actual schema, not a mock.
func scoreStore(t *testing.T) *sql.DB {
	t.Helper()
	prev := obsDB
	if err := os.MkdirAll(nsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := openObsDB()
	if err != nil {
		t.Fatal(err)
	}
	obsDB = db
	t.Cleanup(func() { db.Close(); obsDB = prev })
	return db
}

func emitTestFlow(t *testing.T, id string, stages [][]string, specs []emitterSpec, now time.Time) flow {
	t.Helper()
	f := mintTestFlow(t, id, stages, nil, now)
	f.Emitters = specs
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	return f
}

// driveRun advances real scheduler ticks (reconcile, then the fire loop — the
// order the box uses) and answers each node that launches with the report the
// test scripted for it. A node whose scripted report is EMPTY dies report-less
// (the watchdog releases it, ADR-0019); a node absent from the map keeps
// running. Stops when the run is terminal or the tick budget runs out.
func driveRun(t *testing.T, f flow, start time.Time, reports map[string]string, ticks int) flow {
	t.Helper()
	now := start
	answered := map[string]bool{}
	for range ticks {
		schedTick(now)
		cur, err := loadFlow(f.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch cur.Status {
		case "complete", "blocked", "stopped", "deadline":
			return cur
		}
		for _, n := range cur.Nodes {
			j, ok := findFlowJob(cur, n.JobID)
			if !ok || !j.Started || answered[n.ID] {
				continue
			}
			body, scripted := reports[n.ID]
			if !scripted {
				continue
			}
			answered[n.ID] = true
			if body == "" {
				writeWatchdogMarker(cur.Agent, n.JobID, "unit died report-less")
				continue
			}
			writeFile(t, reportPath(cur.Agent, n.JobID), body)
		}
		now = now.Add(launchGap + time.Minute)
	}
	cur, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

// exploreRun mints the built-in `explore-attempts` shape (plan emits N attempts,
// a judge fans them in, validate accepts) with room to run its group in
// parallel.
func exploreRun(t *testing.T, id string, now time.Time) flow {
	t.Helper()
	f := flow{
		ID: id, Agent: "agent-b", Repo: "example-repo", Goal: "explore three ways",
		Stages:    [][]string{{"plan"}, {"validate"}},
		NodeRoles: []string{"plan", "validate"},
		Emitters:  []emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
		// The emitted group must be able to run at once; cap 1 would serialise it
		// (correct, but it makes the lifecycle test 4 ticks longer per member).
		MaxConcurrent: 4,
		Created:       now, Updated: now, Status: "queued", Batch: "flow-" + id[len(id)-8:], Base: "HEAD",
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	return f
}

// ---- A2: the report grammar -----------------------------------------------------

func TestParseEmitGrammar(t *testing.T) {
	report := "# done\n\n## Emit\n- role: attempt\n  input: |\n    Try approach B:\n      constraint-solver first.\n- role: attempt\n\n## Handoff\n- branch: x\n"
	reqs, err := parseEmit([]byte(report))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("entries = %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Role != "attempt" || reqs[0].Input != "Try approach B:\n  constraint-solver first." {
		t.Fatalf("first entry = %+v", reqs[0])
	}
	if reqs[1].Input != "" {
		t.Fatalf("second entry took the first's input: %+v", reqs[1])
	}
	if _, err := parseEmit([]byte("# no section\n")); err != nil {
		t.Fatalf("absent section must be a silent no-op: %v", err)
	}
	for name, body := range map[string]string{
		"field before entry": "## Emit\n  role: attempt\n",
		"unknown field":      "## Emit\n- role: attempt\n  mood: brave\n",
		"unparseable line":   "## Emit\n- role attempt\n",
		"roleless entry":     "## Emit\n- input: hello\n",
	} {
		if _, err := parseEmit([]byte(body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	big := "## Emit\n- role: attempt\n  input: |\n    " + strings.Repeat("x", emitInputMax+10) + "\n"
	if _, err := parseEmit([]byte(big)); err == nil {
		t.Error("oversized emit input accepted")
	}
}

// ---- A1: declaration validation -------------------------------------------------

func TestValidateEmittersRejectsUnsafeShapes(t *testing.T) {
	flowEnv(t)
	stages := [][]string{{"plan"}, {"validate"}}
	ok := []emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}
	if err := validateEmitters(ok, stages); err != nil {
		t.Fatalf("valid emitter refused: %v", err)
	}
	cases := map[string][]emitterSpec{
		"off-path emitter": {{Node: "amend", Max: 2, Roles: []string{"attempt"}, FanIn: "judge"}},
		"max out of range": {{Node: "plan", Max: 0, Roles: []string{"attempt"}, FanIn: "judge"}},
		"max too wide":     {{Node: "plan", Max: emitMaxWidth + 1, Roles: []string{"attempt"}, FanIn: "judge"}},
		"unknown role":     {{Node: "plan", Max: 2, Roles: []string{"nope"}, FanIn: "judge"}},
		"no roles":         {{Node: "plan", Max: 2, FanIn: "judge"}},
		"unknown fan-in":   {{Node: "plan", Max: 2, Roles: []string{"attempt"}, FanIn: "nope"}},
		"self fan-in":      {{Node: "plan", Max: 2, Roles: []string{"attempt"}, FanIn: "plan"}},
		"duplicate node":   {{Node: "plan", Max: 2, Roles: []string{"attempt"}, FanIn: "judge"}, {Node: "plan", Max: 1, Roles: []string{"attempt"}, FanIn: "judge"}},
		"depth 2": {
			{Node: "plan", Max: 2, Roles: []string{"attempt"}, FanIn: "judge"},
			{Node: "validate", Max: 2, Roles: []string{"plan"}, FanIn: "judge"},
		},
	}
	for name, specs := range cases {
		if err := validateEmitters(specs, stages); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Worst case must fit the lifetime budget at SAVE time, not at 3am.
	wide := make([][]string, 0, flowNodeMax-2)
	for range flowNodeMax - 2 {
		wide = append(wide, []string{"review"})
	}
	if err := validateEmitters(ok, append(wide, []string{"plan"})); err == nil {
		t.Error("emitter that cannot fit the session budget accepted")
	}
}

// ---- A3: application ------------------------------------------------------------

func emitReport(verdict string, roles ...string) string {
	var b strings.Builder
	b.WriteString("---\nverdict: " + verdict + "\n---\n# done\n\n## Emit\n")
	for _, r := range roles {
		b.WriteString("- role: " + r + "\n")
	}
	return b.String()
}

func TestEmissionAppliesChildrenFanInAndReGatesSuccessors(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := emitTestFlow(t, "flow-20260817-1200-aa000001", [][]string{{"plan"}, {"validate"}},
		[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), emitReport("ok", "attempt", "attempt"))

	reconcileFlowsLocked(now)
	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 5 {
		t.Fatalf("nodes = %d, want 5 (plan, validate, 2 children, fan-in): %+v", len(got.Nodes), got.Nodes)
	}
	children := []flowNodeRun{}
	var fanIn flowNodeRun
	for _, n := range got.Nodes {
		if n.EmittedBy == "plan" {
			children = append(children, n)
		}
		if n.Role == "judge" {
			fanIn = n
		}
	}
	if len(children) != 2 {
		t.Fatalf("children = %+v", children)
	}
	for _, c := range children {
		if c.Worktree == "" || c.Branch == "" {
			t.Fatalf("emitted child %s has no member worktree/branch", c.ID)
		}
		if len(c.upstreams()) != 1 || c.upstreams()[0] != "plan" {
			t.Fatalf("child %s gating = %v", c.ID, c.upstreams())
		}
	}
	if fanIn.ID == "" || len(fanIn.upstreams()) != 2 {
		t.Fatalf("fan-in = %+v", fanIn)
	}
	// The emitter's former successor now waits on the fan-in, and its unstarted
	// job was re-gated too (an honest insertion, not a parallel shortcut).
	for _, n := range got.Nodes {
		if n.Role != "validate" {
			continue
		}
		if len(n.upstreams()) != 1 || n.upstreams()[0] != fanIn.ID {
			t.Fatalf("validate gating = %v, want [%s]", n.upstreams(), fanIn.ID)
		}
		j, ok := findFlowJob(got, n.JobID)
		if !ok || len(j.After) != 1 || j.After[0] != fanIn.ID {
			t.Fatalf("validate job gating = %v", j.After)
		}
	}
	// Reconcile twice: VerdictSeen makes emission exactly-once.
	reconcileFlowsLocked(now)
	again, _ := loadFlow(f.ID)
	if len(again.Nodes) != 5 {
		t.Fatalf("second reconcile re-emitted: %d nodes", len(again.Nodes))
	}
}

func TestEmissionRefusals(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	cases := []struct {
		name    string
		specs   []emitterSpec
		report  string
		wantLen int
	}{
		{"undeclared emitter", nil, emitReport("ok", "attempt"), 2},
		{"verdict not ok",
			[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
			emitReport("needs-work", "attempt"), 2},
		{"role not allowed",
			[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
			emitReport("ok", "review"), 2},
		{"over the declared width",
			[]emitterSpec{{Node: "plan", Max: 1, Roles: []string{"attempt"}, FanIn: "judge"}},
			emitReport("ok", "attempt", "attempt"), 2},
		{"malformed section",
			[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
			"---\nverdict: ok\n---\n## Emit\n- role attempt\n", 2},
		{"zero entries",
			[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
			"---\nverdict: ok\n---\n## Emit\n\n", 2},
	}
	for i, tc := range cases {
		id := "flow-20260817-1200-bb0000" + string(rune('a'+i)) + "0"
		f := emitTestFlow(t, id, [][]string{{"plan"}, {"validate"}}, tc.specs, now)
		writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), tc.report)
		reconcileFlowsLocked(now)
		got, err := loadFlow(f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Nodes) != tc.wantLen {
			t.Errorf("%s: nodes = %d, want %d", tc.name, len(got.Nodes), tc.wantLen)
		}
		// Refusal must never stop the chain: the verdict still routed.
		if got.Nodes[0].VerdictSeen == "" {
			t.Errorf("%s: verdict was not routed", tc.name)
		}
	}
}

func TestEmissionRefusedWholeWhenOverBudget(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	stages := [][]string{{"plan"}}
	for range flowNodeMax - 3 {
		stages = append(stages, []string{"review"})
	}
	f := emitTestFlow(t, "flow-20260817-1200-cc000001", stages,
		[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}, now)
	before := len(f.Nodes)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), emitReport("ok", "attempt", "attempt", "attempt"))
	reconcileFlowsLocked(now)
	got, _ := loadFlow(f.ID)
	if len(got.Nodes) != before {
		t.Fatalf("partial emission applied: %d nodes, want %d", len(got.Nodes), before)
	}
}

func TestEmittedChildInputRidesThePromptNotTheWeb(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := emitTestFlow(t, "flow-20260817-1200-dd000001", [][]string{{"plan"}, {"validate"}},
		[]emitterSpec{{Node: "plan", Max: 2, Roles: []string{"attempt"}, FanIn: "judge"}}, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID),
		"---\nverdict: ok\n---\n## Emit\n- role: attempt\n  input: |\n    Solve it with a solver.\n")
	reconcileFlowsLocked(now)
	got, _ := loadFlow(f.ID)
	var child flowNodeRun
	for _, n := range got.Nodes {
		if n.EmittedBy != "" {
			child = n
		}
	}
	if child.EmitInput != "Solve it with a solver." {
		t.Fatalf("child input = %q", child.EmitInput)
	}
	j, ok := findFlowJob(got, child.JobID)
	if !ok || !strings.Contains(j.Prompt, "### Emitted input (from plan") || !strings.Contains(j.Prompt, "Solve it with a solver.") {
		t.Fatalf("emitted input missing from the child prompt")
	}
	for _, nv := range flowToView(got).NodeViews {
		if nv.EmitInput != "" {
			t.Fatalf("emitInput leaked into the web payload for %s", nv.ID)
		}
		if nv.ID == child.ID && nv.EmittedBy != "plan" {
			t.Fatalf("emittedBy missing from the run view: %+v", nv)
		}
	}
}

// The emitter's own prompt must state the cap and the remaining budget: a node
// cannot respect a limit it was never told.
func TestEmitterPromptStatesItsBudget(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := emitTestFlow(t, "flow-20260817-1200-ee000001", [][]string{{"plan"}, {"validate"}}, nil, now)
	f.Emitters = []emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}
	section := flowTaskSection(f, f.Nodes[0])
	if !strings.Contains(section, "`## Emit`") || !strings.Contains(section, "roles: attempt") {
		t.Fatalf("emitter prompt line missing: %s", section)
	}
}

// ---- A3.3: quorum ---------------------------------------------------------------

func TestEmittedGroupFanInLaunchesOnQuorum(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := emitTestFlow(t, "flow-20260817-1200-ff000001", [][]string{{"plan"}, {"validate"}},
		[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), emitReport("ok", "attempt", "attempt"))
	reconcileFlowsLocked(now)
	got, _ := loadFlow(f.ID)
	var children []flowNodeRun
	var fanIn flowNodeRun
	for _, n := range got.Nodes {
		if n.EmittedBy != "" {
			children = append(children, n)
		}
		if n.Role == "judge" {
			fanIn = n
		}
	}
	fanInJob, _ := findFlowJob(got, fanIn.JobID)
	// One child delivers, the other dies without a report (watchdog release).
	writeFile(t, reportPath(f.Agent, children[0].JobID), "---\nverdict: ok\n---\n# attempt\n")
	dead, _ := findFlowJob(got, children[1].JobID)
	dead.Started, dead.StartedAt, dead.Minutes = true, now.Add(-5*time.Hour), 60
	if err := saveJob(dead); err != nil {
		t.Fatal(err)
	}
	st, paths, reason := gatedReadiness(got.Agent, fanInJob, now)
	if st != upSuccess || len(paths) != 1 {
		t.Fatalf("quorum gate: st=%d paths=%v reason=%q", st, paths, reason)
	}
	// Both dead ⇒ the fan-in must skip loudly, never launch with no input.
	if err := os.Remove(reportPath(f.Agent, children[0].JobID)); err != nil {
		t.Fatal(err)
	}
	alsoDead, _ := findFlowJob(got, children[0].JobID)
	alsoDead.Started, alsoDead.StartedAt, alsoDead.Minutes = true, now.Add(-5*time.Hour), 60
	if err := saveJob(alsoDead); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := gatedReadiness(got.Agent, fanInJob, now); st != upFailed {
		t.Fatalf("all-dead group: st=%d, want upFailed", st)
	}
}

// ---- A3.4: the emitted lifecycle through the real tick order --------------------

// The whole point of a fan-out is that the run continues past it. Emission
// appends its group AFTER the successors it re-gates, so reading the acceptance
// node as `f.Nodes[len-1]` made the JUDGE the apparent tail: its honest
// `verdict: ok` blocked the run ("final report missing a complete|needs-work|
// blocked verdict") and validate was reaped without ever launching.
func TestEmittedRunReachesItsAcceptanceNode(t *testing.T) {
	flowEnv(t)
	now := bish(t, "20:00")
	f := exploreRun(t, "flow-20260817-2000-ac000001", now)
	got := driveRun(t, f, now, map[string]string{
		"plan":         emitReport("ok", "attempt", "attempt"),
		"attempt-r1":   "---\nverdict: ok\n---\n# attempt A\n",
		"attempt-2-r1": "---\nverdict: ok\n---\n# attempt B\n",
		"judge-r1":     "---\nverdict: ok\nscores:\n  - subject: attempt-r1\n    dimension: correctness\n    score: 4\n---\n# judged\n",
		"validate":     "---\nverdict: complete\n---\n# accepted\n",
	}, 24)
	if got.Status != "complete" {
		t.Fatalf("run status = %s (%s), want complete", got.Status, got.CleanupMessage)
	}
	if id := acceptanceNodeID(got); id != "validate" {
		t.Fatalf("acceptance node = %s, want validate", id)
	}
	validate, ok := flowNodeByID(got, "validate")
	if !ok {
		t.Fatal("validate was reaped")
	}
	j, ok := findFlowJob(got, validate.JobID)
	if !ok || !j.Started || j.Gated {
		t.Fatalf("validate job = %+v", j)
	}
	// The fan-in decided nothing; only the acceptance node did.
	fanIn, _ := flowNodeByID(got, "judge-r1")
	if fanIn.VerdictSeen != verdictOK {
		t.Fatalf("fan-in verdict = %q", fanIn.VerdictSeen)
	}
}

// A dead member of a runtime fan-out is tolerable — that is what quorum ≥1
// MEANS. reconcile ran before gatedReadiness could ever apply it, so one
// watchdog-released attempt turned the whole run blocked.
func TestEmittedGroupToleratesOneDeadMember(t *testing.T) {
	flowEnv(t)
	now := bish(t, "20:00")
	f := exploreRun(t, "flow-20260817-2000-ad000001", now)
	got := driveRun(t, f, now, map[string]string{
		"plan":       emitReport("ok", "attempt", "attempt"),
		"attempt-r1": "---\nverdict: ok\n---\n# attempt A\n",
		// An empty report = the unit died and the watchdog released it.
		"attempt-2-r1": "",
		"judge-r1":     "---\nverdict: ok\n---\n# judged one of two\n",
		"validate":     "---\nverdict: complete\n---\n# accepted\n",
	}, 30)
	if got.Status != "complete" {
		t.Fatalf("one dead member ⇒ status %s (%s), want complete", got.Status, got.CleanupMessage)
	}
	dead, _ := flowNodeByID(got, "attempt-2-r1")
	if dead.VerdictSeen != "terminal-no-report" {
		t.Fatalf("dead member verdict = %q", dead.VerdictSeen)
	}
	fanIn, _ := flowNodeByID(got, "judge-r1")
	if j, ok := findFlowJob(got, fanIn.JobID); !ok || !j.Started {
		t.Fatalf("fan-in never launched on quorum: %+v", j)
	}
}

// A member that reports `blocked` delivered a verdict — the fan-in reads it and
// decides. Only a STATIC upstream's blocked verdict stops the run.
func TestEmittedBlockedMemberDoesNotBlockTheRun(t *testing.T) {
	flowEnv(t)
	now := bish(t, "20:00")
	f := exploreRun(t, "flow-20260817-2000-ae000001", now)
	got := driveRun(t, f, now, map[string]string{
		"plan":         emitReport("ok", "attempt", "attempt"),
		"attempt-r1":   "---\nverdict: ok\n---\n# attempt A\n",
		"attempt-2-r1": "---\nverdict: blocked\n---\n# this approach cannot work\n",
		"judge-r1":     "---\nverdict: ok\n---\n# A wins; B is a dead end\n",
		"validate":     "---\nverdict: complete\n---\n# accepted\n",
	}, 30)
	if got.Status != "complete" {
		t.Fatalf("blocked member ⇒ status %s (%s), want complete", got.Status, got.CleanupMessage)
	}
	if j, ok := findFlowJob(got, mustNode(t, got, "judge-r1").JobID); !ok || !j.Started {
		t.Fatalf("fan-in held behind a blocked member: %+v", j)
	}
}

// All members dead is NOT tolerable: the fan-in has no input, so it dep-skips
// loudly and the skip cascades to the acceptance node. Never a faked run.
func TestEmittedGroupAllDeadBlocksLoudly(t *testing.T) {
	flowEnv(t)
	now := bish(t, "20:00")
	f := exploreRun(t, "flow-20260817-2000-af000001", now)
	got := driveRun(t, f, now, map[string]string{
		"plan":         emitReport("ok", "attempt", "attempt"),
		"attempt-r1":   "",
		"attempt-2-r1": "",
	}, 30)
	if got.Status != "blocked" {
		t.Fatalf("all-dead group ⇒ status %s, want blocked", got.Status)
	}
	fanIn := mustNode(t, got, "judge-r1")
	if !depSkipMarker(got.Agent, fanIn.JobID) {
		t.Fatal("fan-in was not dep-skipped: the failure was silent")
	}
	if j, _ := findFlowJob(got, fanIn.JobID); j.Started {
		t.Fatal("fan-in launched with no delivered child")
	}
}

func mustNode(t *testing.T, f flow, id string) flowNodeRun {
	t.Helper()
	n, ok := flowNodeByID(f, id)
	if !ok {
		t.Fatalf("run has no node %s: %+v", id, f.Nodes)
	}
	return n
}

// ---- A3.5: emission is atomic on disk -------------------------------------------

// Children are persisted one job at a time. A failure partway through used to
// leave a truncated group for the fan-in to judge plus orphaned job files that
// still launch (their upstream has delivered) with no node to reconcile them.
func TestEmissionRollsBackEveryJobOnPersistenceFailure(t *testing.T) {
	flowEnv(t)
	for _, tc := range []struct {
		name string
		id   string
		fail func(job) bool
	}{
		{"second child", "flow-20260817-1200-a1000001", func(j job) bool { return j.NodeID == "attempt-2-r1" }},
		{"the fan-in", "flow-20260817-1200-a1000002", func(j job) bool { return j.NodeID == "judge-r1" }},
		{"re-gating the successor", "flow-20260817-1200-a1000003", func(j job) bool { return j.NodeID == "validate" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flowEnv(t)
			now := time.Now()
			f := emitTestFlow(t, tc.id, [][]string{{"plan"}, {"validate"}},
				[]emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}, now)
			validateJob, _ := findFlowJob(f, f.Nodes[1].JobID)
			jobsBefore := len(loadJobs(f.Agent))
			jobWriteVeto = func(j job) error {
				if tc.fail(j) {
					return errors.New("disk full")
				}
				return nil
			}
			t.Cleanup(func() { jobWriteVeto = nil })
			writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), emitReport("ok", "attempt", "attempt"))

			reconcileFlowsLocked(now)
			jobWriteVeto = nil
			got, err := loadFlow(f.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Nodes) != 2 {
				t.Fatalf("partial group survived: %+v", got.Nodes)
			}
			if got.Round != 0 {
				t.Fatalf("round advanced on a refused emission: %d", got.Round)
			}
			if n := len(loadJobs(got.Agent)); n != jobsBefore {
				t.Fatalf("jobs on disk = %d, want %d — an orphan child can still launch", n, jobsBefore)
			}
			// The successor still gates on the emitter, not on a fan-in that no
			// longer exists — otherwise it would wait forever.
			after, _ := findFlowJob(got, got.Nodes[1].JobID)
			if len(after.After) != 1 || after.After[0] != "plan" {
				t.Fatalf("successor gate = %v, want [plan] (was %v)", after.After, validateJob.After)
			}
			if got.Nodes[1].upstreams()[0] != "plan" {
				t.Fatalf("successor node gate = %v", got.Nodes[1].upstreams())
			}
			// Refusal must never stop the chain (same rule as a rejected emission).
			if got.Nodes[0].VerdictSeen == "" {
				t.Fatal("verdict was not routed after the rollback")
			}
			if got.Status == "complete" {
				t.Fatal("a rolled-back emission completed the run")
			}
		})
	}
}

// The crash window: jobs are written before the flow records their nodes, so a
// process death in between leaves gated jobs whose upstream has delivered. They
// would launch as phantom sessions with no reconcile owner, and the restarted
// reconcile would emit a SECOND group beside them.
func TestCrashBetweenEmissionAndFlowSaveLeavesNoPhantom(t *testing.T) {
	flowEnv(t)
	now := bish(t, "20:00")
	f := exploreRun(t, "flow-20260817-2000-b0000001", now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), emitReport("ok", "attempt", "attempt"))
	reconcileFlowsLocked(now)
	emitted, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(emitted.Nodes) != 5 {
		t.Fatalf("emission did not apply: %+v", emitted.Nodes)
	}
	// Simulate the crash: the child + fan-in JOBS are on disk, the flow record is
	// the pre-emission one (its own write never happened).
	crashed := emitted
	crashed.Nodes = append([]flowNodeRun(nil), emitted.Nodes[:2]...)
	crashed.Round = 0
	crashed.Nodes[1].AfterIDs = []string{"plan"}
	crashed.Nodes[1].VerdictSeen = ""
	crashed.Nodes[0].VerdictSeen = ""
	if err := saveFlow(crashed); err != nil {
		t.Fatal(err)
	}

	reconcileFlowsLocked(now.Add(time.Minute))
	after, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Nodes) != 5 {
		t.Fatalf("restart did not re-emit exactly one group: %+v", after.Nodes)
	}
	// Exactly one job per node: the orphans from before the crash are gone.
	known := map[string]bool{}
	for _, n := range after.Nodes {
		known[n.JobID] = true
	}
	for _, j := range loadJobs(after.Agent) {
		if j.FlowID == after.ID && !known[j.ID] {
			t.Fatalf("orphan job %s (node %s) survived — it would launch unowned", j.ID, j.NodeID)
		}
	}
}

// ---- A4: the session budget is per AUTOMATION -----------------------------------

func TestWorstCaseBudgetSumsEveryEmitterOccurrence(t *testing.T) {
	flowEnv(t)
	stages := [][]string{{"plan"}, {"design"}, {"validate"}}
	// Two emitters that each fit ALONE (3 + 8 + 1 = 12 ≤ 16) but not together.
	specs := []emitterSpec{
		{Node: "plan", Max: 8, Roles: []string{"attempt"}, FanIn: "judge"},
		{Node: "design", Max: 8, Roles: []string{"attempt"}, FanIn: "judge"},
	}
	if got := worstCaseSessions(specs[:1], stages); got != 12 {
		t.Fatalf("single-emitter worst case = %d, want 12", got)
	}
	if got := worstCaseSessions(specs, stages); got != 21 {
		t.Fatalf("aggregate worst case = %d, want 21", got)
	}
	if err := validateEmitters(specs, stages); err == nil {
		t.Fatal("two emitters whose aggregate exceeds the budget accepted")
	}
	// A ROLE-level spec governs every member of that role, so each occurrence can
	// emit its own group (emitterFor matches by role).
	members := [][]string{{"attempt", "attempt#2"}, {"judge"}, {"validate"}}
	wide := []emitterSpec{{Node: "attempt", Max: 6, Roles: []string{"review"}, FanIn: "integrate"}}
	if got := worstCaseSessions(wide, members); got != 4+2*7 {
		t.Fatalf("role-level spec worst case = %d, want %d", got, 4+2*7)
	}
	if err := validateEmitters(wide, members); err == nil {
		t.Fatal("a role-level emitter over two members ignored the second occurrence")
	}
	// A member-slot spec governs only that member.
	pinned := []emitterSpec{{Node: "attempt#2", Max: 6, Roles: []string{"review"}, FanIn: "integrate"}}
	if got := worstCaseSessions(pinned, members); got != 4+7 {
		t.Fatalf("member-slot worst case = %d, want %d", got, 4+7)
	}
	if err := validateEmitters(pinned, members); err != nil {
		t.Fatalf("a fitting member-slot emitter refused: %v", err)
	}
}

// A static parallel stage keeps the strict gate — its members are not
// interchangeable, so a dead member is a dep-skip, not a quorum.
func TestStaticParallelStageKeepsStrictGate(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260817-1200-11000001", [][]string{{"implement", "review"}, {"integrate"}}, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: ok\n---\n# one\n")
	dead, _ := findFlowJob(f, f.Nodes[1].JobID)
	dead.Started, dead.StartedAt, dead.Minutes = true, now.Add(-5*time.Hour), 60
	if err := saveJob(dead); err != nil {
		t.Fatal(err)
	}
	fanIn, _ := findFlowJob(f, f.Nodes[2].JobID)
	if st, _, _ := gatedReadiness(f.Agent, fanIn, now); st != upFailed {
		t.Fatalf("static stage with a dead member: st=%d, want upFailed", st)
	}
}

// ---- C: member slots + per-member runtime ---------------------------------------

func TestMemberSlotValidation(t *testing.T) {
	flowEnv(t)
	if err := validateStages([][]string{{"attempt", "attempt#2"}, {"judge"}}); err != nil {
		t.Fatalf("two members of one role in one stage refused: %v", err)
	}
	for name, stages := range map[string][][]string{
		"slot 1":          {{"attempt", "attempt#1"}},
		"slot 5":          {{"attempt", "attempt#5"}},
		"unknown base":    {{"nope#2"}},
		"cross-stage dup": {{"attempt", "attempt#2"}, {"attempt#2"}},
	} {
		if err := validateStages(stages); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMemberRuntimePrecedenceAtMint(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	if err := saveNodeRuntime("attempt", nodeRuntimeOverride{Effort: "low", Minutes: 30}); err != nil {
		t.Fatal(err)
	}
	stages := [][]string{{"attempt", "attempt#2"}, {"judge"}}
	f := flow{
		ID: "flow-20260817-1200-22000001", Agent: "agent-b", Repo: "example-repo", Goal: "compare",
		NodeRoles: flattenStages(stages), Stages: stages,
		MemberRuntime: map[string]nodeRuntimeOverride{
			"attempt#2": {Executor: "codex", Model: "gpt-5.6-sol", Effort: "xhigh"},
		},
		Created: now, Updated: now, Status: "running", Batch: "flow-22000001", Base: "HEAD",
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	byID := map[string]job{}
	for _, n := range f.Nodes {
		j, ok := findFlowJob(f, n.JobID)
		if !ok {
			t.Fatalf("node %s has no job", n.ID)
		}
		byID[n.ID] = j
		if n.Role != baseRole(n.ID) {
			t.Fatalf("node %s resolved to role %q", n.ID, n.Role)
		}
	}
	base, slot := byID["attempt"], byID["attempt#2"]
	// Same pinned prompt — that identity is what makes it a comparison.
	if strings.Split(base.Prompt, "\n\n## Flow\n")[0] != strings.Split(slot.Prompt, "\n\n## Flow\n")[0] {
		t.Fatal("member slots did not pin the same node prompt")
	}
	if base.Executor != "claude" || base.Effort != "low" || base.Minutes != 30 {
		t.Fatalf("role runtime override lost: %+v", base)
	}
	if slot.Executor != "codex" || slot.Model != "gpt-5.6-sol" || slot.Effort != "xhigh" {
		t.Fatalf("member runtime not applied: executor=%s model=%s effort=%s", slot.Executor, slot.Model, slot.Effort)
	}
	if slot.Minutes != 30 {
		t.Fatalf("member runtime should inherit unset fields: minutes=%d", slot.Minutes)
	}
	if f.Nodes[1].Branch == f.Nodes[0].Branch {
		t.Fatal("member slots share a branch")
	}
}

func TestCompareHarnessTemplateValidates(t *testing.T) {
	flowEnv(t)
	tpl, ok := templateByID("compare-harness")
	if !ok {
		t.Fatal("compare-harness built-in missing")
	}
	if err := validateFlowTemplate(tpl); err != nil {
		t.Fatalf("built-in compare-harness invalid: %v", err)
	}
	if tpl.MemberRuntime["attempt#2"].Executor != "codex" {
		t.Fatalf("compare-harness member runtime = %+v", tpl.MemberRuntime)
	}
	explore, ok := templateByID("explore-attempts")
	if !ok {
		t.Fatal("explore-attempts built-in missing")
	}
	if err := validateFlowTemplate(explore); err != nil {
		t.Fatalf("built-in explore-attempts invalid: %v", err)
	}
	// A run created from the automation PINS both, so a later edit cannot
	// widen a live run's permissions.
	f, err := resolveFlowSpec(flowSpec{Agent: "agent-b", Repo: "example-repo", Goal: "g", Template: "explore-attempts"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Emitters) != 1 || f.Emitters[0].FanIn != "judge" {
		t.Fatalf("emitters not pinned onto the run: %+v", f.Emitters)
	}
	c, err := resolveFlowSpec(flowSpec{Agent: "agent-b", Repo: "example-repo", Goal: "g", Template: "compare-harness"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if c.MemberRuntime["attempt#2"].Model != "gpt-5.6-sol" {
		t.Fatalf("memberRuntime not pinned onto the run: %+v", c.MemberRuntime)
	}
}

func TestTemplateValidationRejectsBadMemberRuntime(t *testing.T) {
	flowEnv(t)
	tpl := flowTemplate{ID: "bad-member", Name: "Bad", Stages: [][]string{{"attempt", "attempt#2"}},
		MemberRuntime: map[string]nodeRuntimeOverride{"attempt#3": {Executor: "codex"}}}
	if err := validateFlowTemplate(tpl); err == nil {
		t.Fatal("memberRuntime key naming no member accepted")
	}
	tpl.MemberRuntime = map[string]nodeRuntimeOverride{"attempt#2": {Executor: "grok"}}
	if err := validateFlowTemplate(tpl); err == nil {
		t.Fatal("unknown executor accepted")
	}
	tpl.MemberRuntime = map[string]nodeRuntimeOverride{"attempt#2": {Minutes: 9999}}
	if err := validateFlowTemplate(tpl); err == nil {
		t.Fatal("out-of-range minutes accepted")
	}
}

// ---- B: scores persisted by reconcile -------------------------------------------

func TestReconcilePersistsScoresExactlyOnce(t *testing.T) {
	flowEnv(t)
	db := scoreStore(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260817-1200-33000001", [][]string{{"attempt", "attempt#2"}, {"judge"}}, nil, now)
	report := "---\nverdict: ok\nscores:\n  - subject: attempt\n    dimension: correctness\n    score: 4\n    max: 5\n    rationale: \"tests pass\"\n  - subject: attempt#2\n    dimension: correctness\n    score: unknown\n---\n# judged\n"
	// Judge is the last node; give the attempts reports first so the chain routes.
	for _, n := range f.Nodes[:2] {
		writeFile(t, reportPath(f.Agent, n.JobID), "---\nverdict: ok\n---\n# attempt\n")
	}
	writeFile(t, reportPath(f.Agent, f.Nodes[2].JobID), report)
	reconcileFlowsLocked(now)
	reconcileFlowsLocked(now)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM scores WHERE run_id=?`, f.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("score rows = %d, want 2", n)
	}
	var score sql.NullInt64
	var subjectExec, promptRev string
	if err := db.QueryRow(
		`SELECT score, COALESCE(subject_executor,''), COALESCE(subject_prompt_rev,'') FROM scores WHERE run_id=? AND subject='attempt#2'`,
		f.ID).Scan(&score, &subjectExec, &promptRev); err != nil {
		t.Fatal(err)
	}
	if score.Valid {
		t.Fatalf("unknown persisted as %v, want NULL", score.Int64)
	}
	if subjectExec == "" || promptRev == "" {
		t.Fatalf("subject identity not denormalised: exec=%q rev=%q", subjectExec, promptRev)
	}
	views := queryNodeScores(db, f.ID, "judge")
	if len(views) != 2 {
		t.Fatalf("ledger scores = %+v", views)
	}
}

// The run payload must state stage 0 explicitly. With `omitempty` the SPA read a
// two-member FIRST stage as stages 0 and 1 and drew a comparison pair
// sequentially — the one shape harness comparison exists to render.
func TestRunPayloadSerializesStageZero(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260817-1200-44000001", [][]string{{"attempt", "attempt#2"}, {"judge"}}, nil, now)
	b, err := json.Marshal(flowToView(f))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		NodeViews []struct {
			ID    string `json:"id"`
			Stage *int   `json:"stage"`
		} `json:"nodeViews"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.NodeViews) != 3 {
		t.Fatalf("nodes = %+v", payload.NodeViews)
	}
	for _, n := range payload.NodeViews[:2] {
		if n.Stage == nil {
			t.Fatalf("node %s omitted its stage: the client cannot tell parallel from sequential", n.ID)
		}
		if *n.Stage != 0 {
			t.Fatalf("node %s stage = %d, want 0", n.ID, *n.Stage)
		}
	}
}

// A revision that only hashed the role list called a rewired graph, a new
// emitter, and a member's model swap "the same automation" — so the eval trends
// averaged two different experiments under one identity.
func TestAutomationRevisionCoversTheWholeGraph(t *testing.T) {
	flowEnv(t)
	base := flow{
		NodeRoles: []string{"attempt", "attempt#2", "judge"},
		Stages:    [][]string{{"attempt", "attempt#2"}, {"judge"}},
		MemberRuntime: map[string]nodeRuntimeOverride{
			"attempt#2": {Executor: "codex", Model: "gpt-5.6-sol"},
		},
	}
	rev := flowAutomationRevision(base)
	if rev == "" {
		t.Fatal("no revision")
	}
	// Same graph, same pins ⇒ same revision (the trend must not split on a re-read).
	if again := flowAutomationRevision(base); again != rev {
		t.Fatalf("revision unstable: %s vs %s", rev, again)
	}
	variants := map[string]flow{}
	modelSwap := base
	modelSwap.MemberRuntime = map[string]nodeRuntimeOverride{
		"attempt#2": {Executor: "codex", Model: "gpt-5.6-terra"},
	}
	variants["member model"] = modelSwap
	rewired := base
	rewired.Stages = [][]string{{"attempt"}, {"attempt#2"}, {"judge"}}
	variants["stage shape"] = rewired
	routed := base
	routed.Edges = []routeEdge{{Node: "judge", Verdict: verdictNeedsWork, Append: []string{"amend"}}}
	variants["a route"] = routed
	emitting := base
	emitting.Emitters = []emitterSpec{{Node: "attempt", Max: 2, Roles: []string{"review"}, FanIn: "integrate"}}
	variants["an emitter"] = emitting
	for name, v := range variants {
		if got := flowAutomationRevision(v); got == rev {
			t.Errorf("%s changed nothing in the revision", name)
		}
	}
}

// A judge that changes scale is recording the same quality, not a regression.
// Raw-integer averaging turned an equivalent 4/5 and 8/10 into 6/10.
func TestScoreTrendsNormaliseScalesAndSplitRuntimes(t *testing.T) {
	flowEnv(t)
	db := scoreStore(t)
	now := time.Now().Unix()
	rows := []scoreRow{
		{Night: "2026-08-16", RunID: "flow-20260816-2300-cccccccc", JudgeNode: "judge", Subject: "attempt",
			Dimension: "correctness", Score: intPtr(4), Max: 5, AutomationID: "compare-harness",
			SubjectPromptRev: "rev1", SubjectExecutor: "claude", CreatedAt: now - 86400},
		{Night: "2026-08-17", RunID: "flow-20260817-2300-dddddddd", JudgeNode: "judge", Subject: "attempt",
			Dimension: "correctness", Score: intPtr(8), Max: 10, AutomationID: "compare-harness",
			SubjectPromptRev: "rev1", SubjectExecutor: "claude", CreatedAt: now},
		// Same prompt, different engine: its own trend, never folded into the above.
		{Night: "2026-08-17", RunID: "flow-20260817-2300-dddddddd", JudgeNode: "judge", Subject: "attempt#2",
			Dimension: "correctness", Score: intPtr(2), Max: 5, AutomationID: "compare-harness",
			SubjectPromptRev: "rev1", SubjectExecutor: "codex", SubjectModel: "gpt-5.6-sol", CreatedAt: now},
	}
	for _, r := range rows {
		if err := insertScore(db, r); err != nil {
			t.Fatal(err)
		}
	}
	groups := queryScoreGroups(db, "automation_id", "compare-harness")
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want one per (subject, runtime)", groups)
	}
	var claude, codex *scoreGroup
	for i := range groups {
		switch groups[i].Executor {
		case "codex":
			codex = &groups[i]
		default:
			claude = &groups[i]
		}
	}
	if claude == nil || codex == nil {
		t.Fatalf("runtime identity missing from the trend groups: %+v", groups)
	}
	if claude.N != 2 || claude.Pct == nil || *claude.Pct != 80 {
		t.Fatalf("equivalent 4/5 and 8/10 did not average to 80%%: %+v", claude)
	}
	if claude.Max != 10 || claude.Avg == nil || *claude.Avg != 8 {
		t.Fatalf("average not expressed on the newest scale: %+v", claude)
	}
	if codex.Model != "gpt-5.6-sol" || codex.Pct == nil || *codex.Pct != 40 {
		t.Fatalf("codex trend = %+v", codex)
	}
}

func TestScoresAPIShapes(t *testing.T) {
	flowEnv(t)
	db := scoreStore(t)
	now := time.Now().Unix()
	rows := []scoreRow{
		{Night: "2026-08-16", RunID: "flow-20260816-2300-aaaaaaaa", JudgeNode: "judge", Subject: "implement",
			Dimension: "correctness", Score: intPtr(4), Max: 5, Rationale: "solid", AutomationID: "build-feature",
			SubjectPromptRev: "rev1", JudgeExecutor: "claude", SubjectExecutor: "claude", CreatedAt: now - 86400},
		{Night: "2026-08-17", RunID: "flow-20260817-2300-bbbbbbbb", JudgeNode: "judge", Subject: "implement",
			Dimension: "correctness", Score: intPtr(5), Max: 5, Rationale: "better", AutomationID: "build-feature",
			SubjectPromptRev: "rev2", JudgeExecutor: "claude", SubjectExecutor: "codex", CreatedAt: now},
		{Night: "2026-08-17", RunID: "flow-20260817-2300-bbbbbbbb", JudgeNode: "judge", Subject: "implement",
			Dimension: "clarity", Max: 5, AutomationID: "build-feature", SubjectPromptRev: "rev2", CreatedAt: now},
	}
	for _, r := range rows {
		if err := insertScore(db, r); err != nil {
			t.Fatal(err)
		}
	}
	// Idempotent: the same row twice is one row, not a doubled average.
	if err := insertScore(db, rows[0]); err != nil {
		t.Fatal(err)
	}
	groups := queryScoreGroups(db, "automation_id", "build-feature")
	if len(groups) != 3 {
		t.Fatalf("groups = %+v", groups)
	}
	if groups[0].PromptRev != "rev2" {
		t.Fatalf("groups not newest-revision-first: %+v", groups)
	}
	for _, g := range groups {
		if g.Dimension == "clarity" && g.Avg != nil {
			t.Fatalf("unknown-only group has an average: %+v", g)
		}
		if g.Dimension == "correctness" && g.PromptRev == "rev1" && (g.Avg == nil || *g.Avg != 4) {
			t.Fatalf("rev1 average = %+v", g)
		}
	}
	got := queryScoreRows(db, "flow-20260817-2300-bbbbbbbb")
	if len(got) != 2 {
		t.Fatalf("rows = %+v", got)
	}
	for _, r := range got {
		if r.Dimension == "correctness" && r.SubjectExecutor != "codex" {
			t.Fatalf("row identity lost: %+v", r)
		}
	}
}

func intPtr(n int) *int { return &n }
