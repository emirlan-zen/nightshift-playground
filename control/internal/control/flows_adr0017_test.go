package control

// ADR-0017 coverage: verdict vocabulary, exception-edge routing, the lifetime
// node budget, parallel stages with per-member worktrees, the verdict gate
// hold, the open node catalog, the executor seam, the run ledger, and the
// widened proposal inbox.

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVerdictVocabulary(t *testing.T) {
	cases := map[string]string{
		"ok": verdictOK, "done": verdictOK,
		"needs-work": verdictNeedsWork, "partial": verdictNeedsWork, " Needs-Work ": verdictNeedsWork,
		"blocked": verdictBlocked, "needs-decision": verdictBlocked,
		"complete": verdictComplete,
		"perfect":  "", "": "",
	}
	for in, want := range cases {
		if got := normalizeVerdict(in); got != want {
			t.Errorf("normalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReportVerdictReadsBothKeys(t *testing.T) {
	flowEnv(t)
	writeFile(t, reportPath("agent-b", "r1"), "---\nverdict: needs-work\n---\n# r\n")
	writeFile(t, reportPath("agent-b", "r2"), "---\nflow_status: complete\n---\n# r\n")
	if got := reportVerdict("agent-b", "r1"); got != verdictNeedsWork {
		t.Fatalf("verdict key = %q", got)
	}
	if got := reportVerdict("agent-b", "r2"); got != verdictComplete {
		t.Fatalf("flow_status alias = %q", got)
	}
}

// mintTestFlow builds and mints a flow with the given stages/edges.
func mintTestFlow(t *testing.T, id string, stages [][]string, edges []routeEdge, now time.Time) flow {
	t.Helper()
	f := flow{
		ID: id, Agent: "agent-b", Repo: "example-repo", Goal: "ship it",
		NodeRoles: flattenStages(stages), Stages: stages, Edges: edges,
		Created: now, Updated: now, Status: "running", Batch: "flow-" + id[len(id)-8:], Base: "HEAD",
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

func TestLifetimeNodeBudgetBlocksRunawayRemediation(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-b0d9e700", [][]string{{"refine"}, {"validate"}}, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "# refined\n")
	// Every validate round keeps saying needs-work; without the budget this
	// loops forever (the pre-ADR-0017 hole).
	for round := range 20 {
		got, err := loadFlow(f.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "blocked" {
			if len(got.Nodes) != flowNodeMax {
				t.Fatalf("blocked at %d nodes, want %d", len(got.Nodes), flowNodeMax)
			}
			if !strings.Contains(got.CleanupMessage, "node budget exhausted") {
				t.Fatalf("cleanup message = %q", got.CleanupMessage)
			}
			return
		}
		for _, n := range got.Nodes {
			if !reportExists(got.Agent, n.JobID) {
				verdict := "ok"
				if n.Role == "validate" {
					verdict = "needs-work"
				}
				writeFile(t, reportPath(got.Agent, n.JobID), "---\nverdict: "+verdict+"\n---\n# done\n")
			}
		}
		reconcileFlowsLocked(now.Add(time.Duration(round+1) * time.Minute))
	}
	t.Fatal("run never hit the lifetime node budget")
}

func TestMidChainBlockedVerdictBlocksRunAndDeletesQueuedJobs(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-b10cced0", [][]string{{"plan"}, {"implement"}, {"validate"}}, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: blocked\n---\n# stuck on an operator decision\n")
	reconcileFlowsLocked(now.Add(time.Minute))
	got, _ := loadFlow(f.ID)
	if got.Status != "blocked" || !strings.Contains(got.CleanupMessage, "plan") {
		t.Fatalf("flow = %s %q", got.Status, got.CleanupMessage)
	}
	if len(loadJobs(f.Agent)) != 1 { // only the delivered plan job survives
		t.Fatalf("queued jobs not deleted: %d", len(loadJobs(f.Agent)))
	}
}

func TestDeclaredEdgeInsertsAndRegatesSuccessor(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	edges := []routeEdge{{Node: "preview", Verdict: "needs-work", Append: []string{"implement"}}}
	f := mintTestFlow(t, "flow-20260710-1200-ed6e0001", [][]string{{"preview"}, {"review"}}, edges, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: needs-work\n---\n# found a blocker\n")

	// Before reconcile acts, the successor must hold even though the upstream
	// report exists — the next step is the routed insertion, not review.
	reviewJob, _ := findFlowJob(f, f.Nodes[1].JobID)
	if !flowGateHold(f.Agent, reviewJob) {
		t.Fatal("successor did not hold on an unprocessed verdict")
	}

	reconcileFlowsLocked(now.Add(time.Minute))
	got, _ := loadFlow(f.ID)
	if len(got.Nodes) != 3 || got.Nodes[2].Role != "implement" {
		t.Fatalf("nodes after edge = %+v", got.Nodes)
	}
	inserted := got.Nodes[2]
	if !slices.Equal(inserted.upstreams(), []string{"preview"}) {
		t.Fatalf("inserted upstreams = %v", inserted.upstreams())
	}
	review := got.Nodes[1]
	if !slices.Equal(review.upstreams(), []string{inserted.ID}) {
		t.Fatalf("review not re-gated onto inserted tail: %v", review.upstreams())
	}
	reviewJob, _ = findFlowJob(got, review.JobID)
	if !slices.Equal(reviewJob.After, []string{inserted.ID}) {
		t.Fatalf("review job After = %v", reviewJob.After)
	}
	// Processed verdict: the hold must clear (the gate now waits on the
	// inserted node's report, which doesn't exist yet).
	if flowGateHold(got.Agent, reviewJob) {
		t.Fatal("hold did not clear after reconcile processed the verdict")
	}
	if got.Status != "running" {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestMidChainNeedsWorkWithoutEdgeProceeds(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-9d0cee00", [][]string{{"review"}, {"amend"}}, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: needs-work\n---\n# findings\n")
	reconcileFlowsLocked(now.Add(time.Minute))
	got, _ := loadFlow(f.ID)
	if len(got.Nodes) != 2 || got.Status != "running" {
		t.Fatalf("undeclared mid-chain needs-work must proceed: %+v", got)
	}
	amendJob, _ := findFlowJob(got, got.Nodes[1].JobID)
	if flowGateHold(got.Agent, amendJob) {
		t.Fatal("amend held although the happy path is the handler")
	}
}

func TestParallelStageMintsMemberWorktreesAndIntegrateBranches(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	stages := [][]string{{"plan"}, {"implement", "implement"}, {"integrate"}, {"validate"}}
	f := mintTestFlow(t, "flow-20260710-1200-fa60c700", stages, nil, now)
	if len(f.Nodes) != 5 {
		t.Fatalf("nodes = %d", len(f.Nodes))
	}
	m1, m2, integ := f.Nodes[1], f.Nodes[2], f.Nodes[3]
	if m1.Worktree == "" || m2.Worktree == "" || m1.Worktree == m2.Worktree {
		t.Fatalf("members lack distinct worktrees: %q %q", m1.Worktree, m2.Worktree)
	}
	if m1.Branch == m2.Branch || !strings.HasPrefix(m1.Branch, f.Branch+"--") {
		t.Fatalf("member branches = %q %q", m1.Branch, m2.Branch)
	}
	if !slices.Equal(integ.upstreams(), []string{m1.ID, m2.ID}) {
		t.Fatalf("integrate upstreams = %v", integ.upstreams())
	}
	ij, _ := findFlowJob(f, integ.JobID)
	if !strings.Contains(ij.Prompt, m1.Branch) || !strings.Contains(ij.Prompt, m2.Branch) {
		t.Fatal("integrate prompt does not list the group branches")
	}
	if ij.Workdir != f.Worktree {
		t.Fatalf("integrate must run in the run worktree, got %q", ij.Workdir)
	}
	mj, _ := findFlowJob(f, m1.JobID)
	if mj.Workdir != m1.Worktree {
		t.Fatalf("member workdir = %q, want its own worktree", mj.Workdir)
	}
	// A member's dev worktree is created lazily at ungate time.
	if err := ensureFlowNodeWorktree(mj); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m1.Worktree); err != nil {
		t.Fatalf("member worktree not created: %v", err)
	}
}

// prepareFlowWorktree must fetch origin before resolving the base ref: nothing
// else on the box fetches the source clones, so a run would silently branch
// from stale code (2026-08-17: the factory run designed against a month-old
// main). A fetch failure stays non-fatal — stale beats no run.
func TestPrepareFlowWorktreeFetchesBeforeBranching(t *testing.T) {
	flowEnv(t)
	prevDev := devMode
	devMode = false
	t.Cleanup(func() { devMode = prevDev })
	src := filepath.Join(home, "workspace", "agent-b", "example-repo")
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	prevGit := runGit
	runGit = func(args ...string) (string, error) {
		calls = append(calls, args)
		if len(args) > 2 && args[2] == "fetch" {
			return "fatal: could not read from remote", errors.New("exit 128")
		}
		if len(args) > 2 && args[2] == "symbolic-ref" {
			return "origin/main", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runGit = prevGit })
	f := flow{ID: "flow-20260817-1400-fe7c4000", Agent: "agent-b", Repo: "example-repo"}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatalf("fetch failure must be non-fatal: %v", err)
	}
	if len(calls) == 0 || calls[0][2] != "fetch" || calls[0][1] != src {
		t.Fatalf("first git call is not a fetch of the source clone: %v", calls)
	}
	last := calls[len(calls)-1]
	if last[2] != "worktree" {
		t.Fatalf("branching did not happen after the fetch: %v", calls)
	}
}

// A verdict-less report from a LIVE session means "still writing" (the contract
// tells sessions to keep the report current as they work) — reconcile must not
// route it, or the successor launches on half-written input (2026-08-17 factory
// run: design started two minutes in, reading draft research).
func TestVerdictlessReportFromLiveSessionDoesNotRoute(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260817-1400-11feed00", [][]string{{"refine"}, {"validate"}}, nil, now)
	j, _ := findFlowJob(f, f.Nodes[0].JobID)
	j.Started, j.StartedAt, j.Minutes = true, now, 50 // live: well inside its window
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "# drafting\n")
	reconcileFlowsLocked(now.Add(time.Minute))
	got, _ := loadFlow(f.ID)
	if got.Nodes[0].VerdictSeen != "" {
		t.Fatalf("verdict-less report from a live session routed: %q", got.Nodes[0].VerdictSeen)
	}
	vj, _ := findFlowJob(got, got.Nodes[1].JobID)
	if !flowGateHold(got.Agent, vj) {
		t.Fatal("successor not held while the upstream is still writing")
	}
	// The final verdict appears — now it routes.
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: ok\n---\n# done\n")
	reconcileFlowsLocked(now.Add(2 * time.Minute))
	got, _ = loadFlow(f.ID)
	if got.Nodes[0].VerdictSeen != verdictOK {
		t.Fatalf("final verdict did not route: %q", got.Nodes[0].VerdictSeen)
	}
}

// Once the session is definitively over, a verdict-less report falls back to
// routing as "none" (the pre-existing behavior) — a session that never wrote a
// verdict must not wedge the chain forever.
func TestVerdictlessReportRoutesOnceSessionOver(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260817-1400-0dead0e5", [][]string{{"refine"}, {"validate"}}, nil, now)
	j, _ := findFlowJob(f, f.Nodes[0].JobID)
	j.Started, j.StartedAt, j.Minutes = true, now.Add(-2*time.Hour), 50 // window long past
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "# ended without a verdict\n")
	reconcileFlowsLocked(now)
	got, _ := loadFlow(f.ID)
	if got.Nodes[0].VerdictSeen != "none" {
		t.Fatalf("session-over verdict-less report did not route as none: %q", got.Nodes[0].VerdictSeen)
	}
}

// First-stage parallel members are minted UNGATED, so the gated path's
// fire-time worktree creation never runs for them — the launch path must
// create the member checkout itself. Regression for the 2026-08-15 factory
// run: both stage-1 members launched into missing worktrees, the watchdog
// marked them terminal-no-report, and the whole run blocked without a single
// session running.
func TestFirstStageParallelMembersGetWorktreesAtLaunch(t *testing.T) {
	flowEnv(t)
	now := bish(t, "22:00") // before any sweep slot: only flow jobs fire
	f := flow{
		ID: "flow-20260815-2200-0f175a6e", Agent: "agent-b", Repo: "example-repo", Goal: "parallel first stage",
		NodeRoles:     []string{"implement", "implement", "integrate", "validate"},
		Stages:        [][]string{{"implement", "implement"}, {"integrate"}, {"validate"}},
		MaxConcurrent: 2,
		Created:       now, Updated: now, Status: "queued", Batch: "flow-0f175a6e", Base: "HEAD",
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
	m1, m2 := f.Nodes[0], f.Nodes[1]
	if m1.Worktree == "" || m2.Worktree == "" {
		t.Fatalf("members lack worktrees: %+v %+v", m1, m2)
	}
	schedTick(now)                              // one launch per tick
	schedTick(now.Add(launchGap + time.Minute)) // past the box-wide gap
	jobs := flowJobsByNode(t, f)
	for _, n := range []flowNodeRun{m1, m2} {
		j := jobs[n.ID]
		if !j.Started {
			t.Fatalf("member %s did not launch: %+v", n.ID, j)
		}
		if _, err := os.Stat(n.Worktree); err != nil {
			t.Fatalf("member %s launched without its worktree: %v", n.ID, err)
		}
	}
}

func TestParallelBlockedMemberBlocksRun(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	stages := [][]string{{"implement", "implement"}, {"integrate"}, {"validate"}}
	f := mintTestFlow(t, "flow-20260710-1200-a66b1000", stages, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: ok\n---\n# half done\n")
	writeFile(t, reportPath(f.Agent, f.Nodes[1].JobID), "---\nverdict: blocked\n---\n# needs the operator\n")
	reconcileFlowsLocked(now.Add(time.Minute))
	got, _ := loadFlow(f.ID)
	if got.Status != "blocked" {
		t.Fatalf("blocked > ok aggregation failed: %s", got.Status)
	}
}

func TestFlowJobsExemptFromTriggeredMaxWait(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-77a17000", [][]string{{"plan"}, {"validate"}}, nil, now)
	vj, _ := findFlowJob(f, f.Nodes[1].JobID)
	st, _, _ := gatedReadiness(f.Agent, vj, now.Add(triggeredMaxWait+2*time.Hour))
	if st != upPending {
		t.Fatalf("flow gate = %d, want pending past the night window", st)
	}
	vj.FlowID = "" // the same job as a profile wave times out
	st, _, _ = gatedReadiness(f.Agent, vj, now.Add(triggeredMaxWait+2*time.Hour))
	if st != upFailed {
		t.Fatalf("profile gate = %d, want failed", st)
	}
}

func TestCustomNodeDefLifecycleAndFlowUse(t *testing.T) {
	flowEnv(t)
	body := `{"id":"security-audit","name":"Security audit","description":"Adversarial security pass.","effort":"xhigh","minutes":120,"output":"Findings report","prompt":"Audit the diff for vulnerabilities and report severity-ranked findings."}`
	rr := httptest.NewRecorder()
	handleNodeCreate(rr, httptest.NewRequest("POST", "/api/nodes", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	d, ok := nodeDefinitionByID("security-audit")
	if !ok || d.Minutes != 120 || effectiveExecutor(d) != "claude" {
		t.Fatalf("custom def = %+v ok=%v", d, ok)
	}
	if got := nodePrompt("security-audit"); !strings.Contains(got, "vulnerabilities") {
		t.Fatalf("custom prompt = %q", got)
	}
	// The custom role composes into a real flow.
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-c0570de0", [][]string{{"security-audit"}, {"validate"}}, nil, now)
	j, _ := findFlowJob(f, f.Nodes[0].JobID)
	if !strings.Contains(j.Prompt, "vulnerabilities") || j.Effort != "xhigh" || j.Executor != "claude" {
		t.Fatalf("custom node job = effort %q executor %q", j.Effort, j.Executor)
	}
	// Referenced by a template → delete refused.
	tpl := flowTemplate{ID: "audit", Name: "Audit", Nodes: []string{"security-audit", "validate"}}
	if err := saveFlowTemplate(tpl); err != nil {
		t.Fatal(err)
	}
	if err := deleteCustomNodeDef("security-audit"); err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("delete while referenced = %v", err)
	}
	if err := os.Remove(flowTemplatePath("audit")); err != nil {
		t.Fatal(err)
	}
	if err := deleteCustomNodeDef("security-audit"); err == nil || !strings.Contains(err.Error(), "active flow") {
		t.Fatalf("delete while active flow references node = %v", err)
	}
	f.Status = "complete"
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	if err := deleteCustomNodeDef("security-audit"); err != nil {
		t.Fatalf("delete after unreference: %v", err)
	}
}

func TestCustomNodeDefRejectsBuiltinShadowAndBadExecutor(t *testing.T) {
	flowEnv(t)
	if err := validateNodeDefinition(nodeDefinition{ID: "review", Name: "x", Effort: "high", Minutes: 60}); err == nil {
		t.Fatal("shadowing a built-in must be rejected")
	}
	// codex is allowlisted since ADR-0018; anything else stays rejected until
	// its own ADR lands (launcher dispatch, auth probe, credentials).
	if err := validateNodeDefinition(nodeDefinition{ID: "codex-node", Name: "x", Effort: "high", Minutes: 60, Executor: "codex"}); err != nil {
		t.Fatalf("codex executor must be accepted (ADR-0018): %v", err)
	}
	if err := validateNodeDefinition(nodeDefinition{ID: "gemini-node", Name: "x", Effort: "high", Minutes: 60, Executor: "gemini"}); err == nil {
		t.Fatal("unknown executor must be rejected until its ADR lands")
	}
	if err := saveNodeRuntime("review", nodeRuntimeOverride{Effort: "high", Minutes: 60, Executor: "gemini"}); err == nil {
		t.Fatal("runtime override with unknown executor must be rejected")
	}
	if err := saveNodeRuntime("review", nodeRuntimeOverride{Effort: "high", Minutes: 60, Executor: "codex"}); err != nil {
		t.Fatalf("runtime override with codex executor must be accepted (ADR-0018): %v", err)
	}
}

func TestTemplateStagesAndEdgesValidation(t *testing.T) {
	flowEnv(t)
	good := flowTemplate{
		ID: "fanout-build", Name: "Fan-out build",
		Stages: [][]string{{"plan"}, {"implement", "implement"}, {"integrate"}, {"validate"}},
		Edges:  []routeEdge{{Node: "plan", Verdict: "needs-work", Append: []string{"refine"}}},
	}
	if err := saveFlowTemplate(good); err != nil {
		t.Fatalf("valid stages template rejected: %v", err)
	}
	saved, ok := templateByID("fanout-build")
	if !ok || len(saved.Stages) != 4 || len(saved.Edges) != 1 {
		t.Fatalf("template round-trip = %+v", saved)
	}
	bad := good
	bad.ID, bad.Edges = "bad-verdict", []routeEdge{{Node: "plan", Verdict: "ok", Append: []string{"refine"}}}
	if err := saveFlowTemplate(bad); err == nil {
		t.Fatal("ok-verdict edge must be rejected")
	}
	wide := good
	wide.ID, wide.Stages = "too-wide", [][]string{{"implement", "implement", "implement", "implement", "implement"}}
	if err := saveFlowTemplate(wide); err == nil {
		t.Fatalf("stage wider than %d must be rejected", flowStageMax)
	}
}

func TestLedgerDerivesVerdictsRoundsAndExecutor(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-1ed6e400", [][]string{{"refine"}, {"validate"}}, nil, now)
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "---\nverdict: ok\n---\n# refined\n")
	writeFile(t, reportPath(f.Agent, f.Nodes[1].JobID), "---\nverdict: needs-work\n---\n# gaps\n")
	reconcileFlowsLocked(now.Add(time.Minute))
	runs := ledgerRuns()
	var run ledgerRun
	for _, r := range runs {
		if r.ID == f.ID {
			run = r
		}
	}
	if run.ID == "" || run.Rounds != 1 || len(run.Nodes) != 4 {
		t.Fatalf("ledger run = %+v", run)
	}
	if run.Nodes[0].Verdict != verdictOK || run.Nodes[1].Verdict != verdictNeedsWork {
		t.Fatalf("ledger verdicts = %q %q", run.Nodes[0].Verdict, run.Nodes[1].Verdict)
	}
	if run.Nodes[0].Executor != "claude" {
		t.Fatalf("ledger executor = %q", run.Nodes[0].Executor)
	}
	for _, n := range run.Nodes {
		if strings.Contains(n.Role, "#") { // sanity: metadata only, no content fields
			t.Fatalf("unexpected content in ledger: %+v", n)
		}
	}
}

func TestChangeProposalRoundTrip(t *testing.T) {
	flowEnv(t)
	writeFile(t, filepath.Join(nodesDir(), "review.md"), "# Node · Review\n\nOld review prompt.\n")
	prop := changeProposal{
		Type: "node-prompt", Target: "review",
		Why:  "review sessions rubber-stamp: 0 findings on 9 of 11 PRs with known bugs",
		Body: "# Node · Review\n\nHarder adversarial review prompt.\n",
	}
	b, _ := json.Marshal(prop)
	writeFile(t, proposalPath("harden-review"), string(b))

	rr := httptest.NewRecorder()
	handleChangeProposals(rr, httptest.NewRequest("GET", "/api/proposals", nil))
	var listed []changeProposalView
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].Valid || !strings.Contains(listed[0].Current, "Old review prompt") {
		t.Fatalf("proposal list = %+v", listed)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/proposals/harden-review/apply", nil)
	req.SetPathValue("name", "harden-review")
	handleChangeProposalApply(rr, req)
	if rr.Code != 200 {
		t.Fatalf("apply: %d %s", rr.Code, rr.Body.String())
	}
	if got := nodePrompt("review"); !strings.Contains(got, "Harder adversarial") {
		t.Fatalf("prompt after apply = %q", got)
	}
	// Applied through the versioned path: the old body is in prompt-history.
	if versions := listPromptVersions("node-review"); len(versions) == 0 {
		t.Fatal("apply did not record prompt history")
	}
	if _, err := os.Stat(proposalPath("harden-review")); !os.IsNotExist(err) {
		t.Fatal("proposal not consumed")
	}
}

func TestInvalidProposalListedWithErrorAndNeverApplied(t *testing.T) {
	flowEnv(t)
	writeFile(t, proposalPath("junk"), `{"type":"node-prompt","target":"nope","why":"x","body":"y"}`)
	rr := httptest.NewRecorder()
	handleChangeProposals(rr, httptest.NewRequest("GET", "/api/proposals", nil))
	var listed []changeProposalView
	_ = json.Unmarshal(rr.Body.Bytes(), &listed)
	if len(listed) != 1 || listed[0].Valid || listed[0].Error == "" {
		t.Fatalf("junk proposal view = %+v", listed)
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/proposals/junk/apply", nil)
	req.SetPathValue("name", "junk")
	handleChangeProposalApply(rr, req)
	if rr.Code == 200 {
		t.Fatal("junk proposal must not apply")
	}
}

func TestFlowCreateWithStagesAndEdgesPinsThem(t *testing.T) {
	flowEnv(t)
	deadline := time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"agent":"agent-b","repo":"example-repo","goal":"parallel build","stages":[["plan"],["implement","implement"],["integrate"],["validate"]],"edges":[{"node":"plan","verdict":"needs-work","append":["refine"]}],"deadline":"` + deadline + `"}`
	rr := httptest.NewRecorder()
	handleFlowCreate(rr, httptest.NewRequest("POST", "/api/flows", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var got flowView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Stages) != 4 || len(got.Edges) != 1 || len(got.Nodes) != 5 {
		t.Fatalf("flow = stages %d edges %d nodes %d", len(got.Stages), len(got.Edges), len(got.Nodes))
	}
}

func TestNodeModelPinning(t *testing.T) {
	flowEnv(t)
	// Definition-level validation: a plain model id is accepted, anything the
	// launcher's .model sidecar regex would drop is rejected at save.
	if err := validateNodeDefinition(nodeDefinition{ID: "fable-node", Name: "x", Effort: "high", Minutes: 60, Model: "claude-fable-5"}); err != nil {
		t.Fatalf("model id must be accepted: %v", err)
	}
	if err := validateNodeDefinition(nodeDefinition{ID: "bad-node", Name: "x", Effort: "high", Minutes: 60, Model: "claude fable"}); err == nil {
		t.Fatal("model id with a space must be rejected")
	}
	// A custom def's model pins the minted job; a codex def without one keeps
	// the executor default.
	body := `{"id":"implement-fable","name":"Implement (Fable)","description":"Implement on Claude Fable 5.","effort":"xhigh","minutes":240,"output":"Tested branch","model":"claude-fable-5","prompt":"Implement the goal completely."}`
	rr := httptest.NewRecorder()
	handleNodeCreate(rr, httptest.NewRequest("POST", "/api/nodes", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	now := time.Now()
	f := mintTestFlow(t, "flow-20260713-2100-ab12cd34", [][]string{{"implement-fable"}, {"review"}, {"validate"}}, nil, now)
	j, _ := findFlowJob(f, f.Nodes[0].JobID)
	if j.Model != "claude-fable-5" || j.Executor != "claude" {
		t.Fatalf("pinned job = model %q executor %q", j.Model, j.Executor)
	}
	if rj, _ := findFlowJob(f, f.Nodes[1].JobID); rj.Model != codexModel || rj.Executor != "codex" {
		t.Fatalf("review job keeps executor default = model %q executor %q", rj.Model, rj.Executor)
	}
	if v := flowToView(f); v.NodeViews[0].Model != "claude-fable-5" {
		t.Fatalf("node view model = %q", v.NodeViews[0].Model)
	}
	// Runtime-override path for built-ins: model applies through
	// effectiveNodeDefinition, and an invalid one is rejected on save and
	// ignored (loudly, falling back to built-in) on load.
	if err := saveNodeRuntime("implement", nodeRuntimeOverride{Effort: "xhigh", Minutes: 300, Model: "claude-fable-5"}); err != nil {
		t.Fatalf("runtime model override: %v", err)
	}
	if def, src := effectiveNodeDefinition("implement"); def.Model != "claude-fable-5" || src != "custom" {
		t.Fatalf("effective def = model %q source %q", def.Model, src)
	}
	if err := saveNodeRuntime("implement", nodeRuntimeOverride{Effort: "xhigh", Minutes: 300, Model: "not a model"}); err == nil {
		t.Fatal("bad runtime model must be rejected")
	}
	writeFile(t, nodeRuntimePath("implement"), `{"effort":"xhigh","minutes":300,"model":"not a model"}`)
	if def, src := effectiveNodeDefinition("implement"); def.Model != "" || src != "built-in" {
		t.Fatalf("invalid on-disk override must fall back to built-in: model %q source %q", def.Model, src)
	}
}
