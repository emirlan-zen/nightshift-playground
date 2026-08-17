package control

// ADR-0019: unified scheduled automations — structured handoff, the agent
// spool, recurring templates, terminal-without-report routing, and the
// extended worktree janitor.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- handoff -------------------------------------------------------------------

func TestExtractHandoff(t *testing.T) {
	report := "verdict: ok\n\n# Work\n\nstuff\n\n## Handoff\n\nBranch nightshift/x is pushed.\nOpen question: rename?\n\n## Appendix\n\nnoise\n"
	got := extractHandoff(report)
	if got != "Branch nightshift/x is pushed.\nOpen question: rename?" {
		t.Fatalf("handoff = %q", got)
	}
	if extractHandoff("no section here") != "" {
		t.Fatal("absent handoff must be empty, never an error")
	}
	huge := "## Handoff\n" + strings.Repeat("x", handoffMax+100)
	if got := extractHandoff(huge); len(got) > handoffMax+64 || !strings.HasSuffix(got, "…(handoff truncated)") {
		t.Fatalf("oversized handoff not truncated visibly (len %d)", len(got))
	}
}

func TestUpstreamPromptSectionCarriesHandoff(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "20260613-2300-sweep-aaaa.md")
	without := filepath.Join(dir, "20260613-2310-sweep-bbbb.md")
	if err := os.WriteFile(with, []byte("report\n\n## Handoff\n\nUse branch X.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(without, []byte("plain report"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := upstreamPromptSection([]string{with, without})
	if !strings.Contains(got, "- `"+with+"`") || !strings.Contains(got, "- `"+without+"`") {
		t.Fatalf("paths missing: %s", got)
	}
	if !strings.Contains(got, "Handoff from 20260613-2300-sweep-aaaa") || !strings.Contains(got, "Use branch X.") {
		t.Fatalf("handoff block missing: %s", got)
	}
	if strings.Contains(got, "Handoff from 20260613-2310-sweep-bbbb") {
		t.Fatal("a report without a handoff must not grow an empty block")
	}
}

// ---- terminal-without-report ---------------------------------------------------

func TestReconcileBlocksRunOnTerminalNoReport(t *testing.T) {
	flowEnv(t)
	watchdogChecked = map[string]time.Time{}
	now := time.Now()
	f := mintFastTestFlow(t, "flow-20260613-1400-dead0001", false, now.Add(-10*time.Hour))
	byNode := flowJobsByNode(t, f)
	j := byNode["refine"]
	j.Started, j.StartedAt, j.Minutes = true, now.Add(-9*time.Hour), 60
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	reconcileFlowsLocked(now)
	nsMu.Unlock()
	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "blocked" || !strings.Contains(got.CleanupMessage, "ended without a report") {
		t.Fatalf("flow = %s (%q), want blocked with a terminal-no-report reason", got.Status, got.CleanupMessage)
	}
}

// ---- agent spool ---------------------------------------------------------------

func TestSpoolCreateRun(t *testing.T) {
	flowEnv(t)
	dir := flowUpdatesDir("agent-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"repo":"example-repo","template":"refine-adr","goal":"spooled goal","agent":"playground","maxConcurrent":2}`
	if err := os.WriteFile(filepath.Join(dir, "create-123.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	applyFlowSpoolLocked(time.Now())
	nsMu.Unlock()
	flows := loadFlows()
	if len(flows) != 1 {
		t.Fatalf("flows = %d, want 1", len(flows))
	}
	f := flows[0]
	// The payload tried to claim agent=playground; the cwd scope wins.
	if f.Agent != "agent-b" || f.Goal != "spooled goal" || f.MaxConcurrent != 2 {
		t.Fatalf("flow = %+v", f)
	}
	res, err := os.ReadFile(filepath.Join(dir, "create-123.result"))
	if err != nil || !strings.HasPrefix(string(res), "ok "+f.ID) {
		t.Fatalf("result = %q, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "create-123.json")); !os.IsNotExist(err) {
		t.Fatal("request file must be consumed")
	}
}

func TestSpoolCreateRejectedLeavesReason(t *testing.T) {
	flowEnv(t)
	dir := flowUpdatesDir("agent-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "create-9.json"), []byte(`{"repo":"example-repo","template":"nope","goal":"g"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	applyFlowSpoolLocked(time.Now())
	nsMu.Unlock()
	if n := len(loadFlows()); n != 0 {
		t.Fatalf("flows = %d, want 0", n)
	}
	res, _ := os.ReadFile(filepath.Join(dir, "create-9.result"))
	if !strings.HasPrefix(string(res), "rejected:") {
		t.Fatalf("result = %q, want a rejection reason", res)
	}
}

func TestSpoolAppendAndGuidance(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintFastTestFlow(t, "flow-20260613-1400-ab010001", false, now)
	dir := flowUpdatesDir("agent-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Nonce'd request names (PR #57 review): concurrent sessions must not
	// clobber each other's request or consume the other's verdict.
	if err := os.WriteFile(filepath.Join(dir, f.ID+".append-111-1.json"), []byte(`{"roles":["preview"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.ID+".guidance-111-1"), []byte("fresh guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	applyFlowSpoolLocked(now)
	nsMu.Unlock()
	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 3 || got.Nodes[2].Role != "preview" {
		t.Fatalf("nodes = %+v, want an appended preview", got.Nodes)
	}
	if ups := got.Nodes[2].upstreams(); len(ups) != 1 || ups[0] != got.Nodes[1].ID {
		t.Fatalf("appended node gated on %v, want the previous tail", ups)
	}
	if got.Guidance != "fresh guidance" {
		t.Fatalf("guidance = %q", got.Guidance)
	}
	res, _ := os.ReadFile(filepath.Join(dir, f.ID+".append-111-1.result"))
	if !strings.HasPrefix(string(res), "ok ") {
		t.Fatalf("append result = %q", res)
	}
	if _, err := os.Stat(filepath.Join(dir, f.ID+".guidance-111-1.result")); err != nil {
		t.Fatal("guidance verdict must land under the nonce'd result name")
	}
}

func TestSpoolAppendRespectsBudgetAndTerminalRuns(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintFastTestFlow(t, "flow-20260613-1400-ab020002", false, now)
	f.Status = "blocked"
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	dir := flowUpdatesDir("agent-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.ID+".append.json"), []byte(`{"roles":["preview"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	applyFlowSpoolLocked(now)
	nsMu.Unlock()
	res, _ := os.ReadFile(filepath.Join(dir, f.ID+".append.result"))
	if !strings.Contains(string(res), "terminal") {
		t.Fatalf("append into a terminal run = %q, want rejection", res)
	}
}

func TestFlowViewShowsWatchdogReleasedAsSkipped(t *testing.T) {
	flowEnv(t)
	watchdogChecked = map[string]time.Time{}
	now := time.Now()
	f := mintFastTestFlow(t, "flow-20260613-1400-dead0002", false, now)
	byNode := flowJobsByNode(t, f)
	j := byNode["refine"]
	j.Started, j.StartedAt, j.Minutes = true, now.Add(-30*time.Minute), 240
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	writeWatchdogMarker("agent-b", j.ID, "unit gone")
	v := flowToView(f)
	if v.NodeViews[0].State != "skipped" {
		t.Fatalf("state = %q, want skipped — the window is still open but the unit is provably gone", v.NodeViews[0].State)
	}
}

// ---- recurrence ----------------------------------------------------------------

func writeScheduledTemplate(t *testing.T, id, hhmm string) {
	t.Helper()
	tpl := flowTemplate{
		ID: id, Name: "Nightly check", Description: "d",
		Nodes: []string{"investigate", "validate"},
		Schedule: &templateSchedule{
			Agent: "agent-b", Repo: "example-repo", Goal: "nightly investigation",
			Time: hhmm, DeadlineMinutes: 300, MaxConcurrent: 1,
		},
	}
	if err := os.MkdirAll(flowTemplatesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(tpl)
	if err := os.WriteFile(flowTemplatePath(id), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledAutomationMintsOncePerDay(t *testing.T) {
	flowEnv(t)
	writeScheduledTemplate(t, "nightly-check", "03:00")
	now := bishD(t, "2026-06-14", "03:02")
	cfg := loadConfig()
	dirty := false
	mintScheduledAutomations(&cfg, now, &dirty)
	mintScheduledAutomations(&cfg, now.Add(time.Minute), &dirty)
	flows := loadFlows()
	if len(flows) != 1 {
		t.Fatalf("flows = %d, want 1 (daily dedup)", len(flows))
	}
	f := flows[0]
	if f.Template != "nightly-check" || f.Agent != "agent-b" || f.Deadline == nil || f.MaxConcurrent != 1 {
		t.Fatalf("flow = %+v", f)
	}
	if want := now.Truncate(time.Minute).Add(-2 * time.Minute).Add(300 * time.Minute); !f.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want due+300m = %v", f.Deadline, want)
	}
	if !dirty {
		t.Fatal("mint must mark the config dirty (dedup key)")
	}
}

func TestScheduledAutomationOverlapSkips(t *testing.T) {
	flowEnv(t)
	writeScheduledTemplate(t, "nightly-check", "03:00")
	cfg := loadConfig()
	dirty := false
	mintScheduledAutomations(&cfg, bishD(t, "2026-06-14", "03:02"), &dirty)
	if n := len(loadFlows()); n != 1 {
		t.Fatalf("flows = %d, want 1", n)
	}
	// Next night: yesterday's run is still queued/running -> overlap-skip.
	mintScheduledAutomations(&cfg, bishD(t, "2026-06-15", "03:02"), &dirty)
	if n := len(loadFlows()); n != 1 {
		t.Fatalf("flows = %d, want 1 (overlap-skip)", n)
	}
	// Once it terminates, the next firing mints again.
	f := loadFlows()[0]
	f.Status = "complete"
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	mintScheduledAutomations(&cfg, bishD(t, "2026-06-16", "03:02"), &dirty)
	if n := len(loadFlows()); n != 2 {
		t.Fatalf("flows = %d, want 2 after the previous run terminated", n)
	}
}

func TestScheduledAutomationRespectsSweepOff(t *testing.T) {
	flowEnv(t)
	writeScheduledTemplate(t, "nightly-check", "03:00")
	cfg := loadConfig()
	cfg.SweepOff["agent-b"] = true
	dirty := false
	mintScheduledAutomations(&cfg, bishD(t, "2026-06-14", "03:02"), &dirty)
	if n := len(loadFlows()); n != 0 {
		t.Fatalf("flows = %d, want 0 with the agent's night switched off", n)
	}
}

func TestValidateTemplateScheduleRejectsBadFields(t *testing.T) {
	flowEnv(t)
	cases := []templateSchedule{
		{Agent: "nope", Repo: "r", Goal: "g", Time: "03:00"},
		{Agent: "agent-b", Repo: "r", Goal: "g", Time: "27:00"},
		{Agent: "agent-b", Repo: "r", Goal: "", Time: "03:00"},
		{Agent: "agent-b", Repo: "r", Goal: "g", Time: "03:00", MaxConcurrent: 99},
		{Agent: "agent-b", Repo: "r", Goal: "g", Time: "03:00", DeadlineMinutes: -1},
	}
	for i, s := range cases {
		if err := validateTemplateSchedule(&s); err == nil {
			t.Fatalf("case %d: schedule %+v validated, want error", i, s)
		}
	}
	ok := templateSchedule{Agent: "agent-b", Repo: "example-repo", Goal: "g", Time: "03:00"}
	if err := validateTemplateSchedule(&ok); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
}

// ---- janitor -------------------------------------------------------------------

func TestJanitorEligibleCoversTerminalStates(t *testing.T) {
	now := time.Now()
	mk := func(status string, updatedAgo time.Duration) flow {
		return flow{Status: status, WorktreeState: "active", Updated: now.Add(-updatedAgo)}
	}
	if !janitorEligible(mk("complete", 0), now) || !janitorEligible(mk("stopped", 0), now) || !janitorEligible(mk("deadline", 0), now) {
		t.Fatal("terminal states must be cleanup candidates")
	}
	if janitorEligible(mk("running", 0), now) || janitorEligible(mk("queued", 0), now) {
		t.Fatal("live runs must never be candidates")
	}
	if janitorEligible(mk("blocked", time.Hour), now) {
		t.Fatal("a fresh blocked run keeps its worktree for the operator")
	}
	if !janitorEligible(mk("blocked", 30*time.Hour), now) {
		t.Fatal("an old blocked run is a candidate")
	}
	done := mk("complete", 0)
	done.WorktreeState = "cleaned"
	if janitorEligible(done, now) {
		t.Fatal("cleaned runs are done")
	}
}

func TestJanitorEscalatesLongRetainedWorktree(t *testing.T) {
	db := obsEnv(t, "agent-b")
	obsDB = db
	t.Cleanup(func() { obsDB = nil })
	flowEnv(t)
	// Non-dev path with git stubbed dirty so cleanup retains, on a flow whose
	// Updated is already past the escalation horizon.
	devMode = false
	prevGit := runGit
	runGit = func(args ...string) (string, error) {
		if len(args) >= 2 && args[2] == "status" {
			return "M dirty.txt", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runGit = prevGit })
	f := flow{
		ID: "flow-20260610-1400-aaaa0001", Agent: "agent-b", Repo: "example-repo", Goal: "g",
		NodeRoles: []string{"refine"}, Nodes: []flowNodeRun{},
		Status: "complete", WorktreeState: "retained", CleanupMessage: "worktree is dirty or unreadable",
		SourceRepo: filepath.Join(agentWorkspace("agent-b"), "example-repo"),
		Worktree:   filepath.Join(agentWorkspace("agent-b"), ".nightshift-worktrees", "flow-20260610-1400-aaaa0001", "example-repo"),
		Created:    time.Now().Add(-80 * time.Hour), Updated: time.Now().Add(-72 * time.Hour),
	}
	if err := os.MkdirAll(f.Worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(flowsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f) // direct write keeps the old Updated (saveFlow would bump it)
	if err := os.WriteFile(flowPath(f.ID), b, 0o644); err != nil {
		t.Fatal(err)
	}
	flowJanitorTick()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='worktree-retained' AND cleared=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("worktree-retained alerts = %d, want 1", n)
	}
}

// ---- profile cap ---------------------------------------------------------------

func TestParseProfileMaxConcurrent(t *testing.T) {
	base := `{"name":"strict","maxConcurrent":%d,"waves":[{"name":"medic","time":"23:00","prompt":"m.md","minutes":50}]}`
	if _, err := parseProfile([]byte(strings.Replace(base, "%d", "99", 1))); err == nil {
		t.Fatal("maxConcurrent 99 validated, want error")
	}
	p, err := parseProfile([]byte(strings.Replace(base, "%d", "1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	slots := profileToSlots("playground", p)
	if len(slots) != 1 || slots[0].cap != 1 {
		t.Fatalf("slots = %+v, want cap carried through", slots)
	}
}
