package control

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func flowEnv(t *testing.T) {
	t.Helper()
	nightrunEnv(t, "agent-b", "playground")
	prevDev := devMode
	devMode = true
	t.Cleanup(func() { devMode = prevDev })
}

func TestFlowCreateMintsIsolatedSequentialNodes(t *testing.T) {
	flowEnv(t)
	deadline := time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"agent":"agent-b","repo":"example-repo","goal":"Refine the latest ADR","acceptance":["all decisions resolved"],"template":"refine-adr","deadline":"` + deadline + `"}`
	rr := httptest.NewRecorder()
	handleFlowCreate(rr, httptest.NewRequest("POST", "/api/flows", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var got flowView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" || len(got.Nodes) != 4 || got.Nodes[0].Role != "refine" || got.Nodes[3].Role != "validate" {
		t.Fatalf("flow = %+v", got.flow)
	}
	if got.AutomationRevision == "" || got.NodeViews[0].PromptRevision == "" || got.NodeViews[0].Output == "" {
		t.Fatalf("run did not pin automation metadata: %+v", got)
	}
	if !strings.Contains(got.Worktree, filepath.Join("workspace", "agent-b", ".nightshift-worktrees", got.ID)) {
		t.Fatalf("worktree not isolated under agent workspace: %s", got.Worktree)
	}
	jobs := loadJobs("agent-b")
	if len(jobs) != 4 {
		t.Fatalf("jobs = %d, want 4", len(jobs))
	}
	byNode := map[string]job{}
	for _, j := range jobs {
		byNode[j.NodeID] = j
		if j.FlowID != got.ID || j.Workdir != got.Worktree || j.FinishBy == nil {
			t.Fatalf("flow job missing correlation/worktree/deadline: %+v", j)
		}
		b, err := os.ReadFile(filepath.Join(jobsDir("agent-b"), j.ID+".workdir"))
		if err != nil || string(b) != got.Worktree {
			t.Fatalf("workdir sidecar = %q, %v", b, err)
		}
	}
	if byNode["refine"].Gated || !byNode["review"].Gated || byNode["review"].After[0] != "refine" {
		t.Fatalf("unexpected gating: refine=%+v review=%+v", byNode["refine"], byNode["review"])
	}
	if !strings.Contains(byNode["refine"].Prompt, "Refine the latest ADR") || !strings.Contains(byNode["refine"].Prompt, "all decisions resolved") {
		t.Fatal("composed prompt missing task brief or acceptance criteria")
	}
}

func TestDiscoverReposFindsFlatAndProductLayouts(t *testing.T) {
	flowEnv(t)
	writeFile(t, filepath.Join(agentWorkspace("agent-b"), "example-repo", ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(agentWorkspace("agent-b"), "product", "nested", ".git", "HEAD"), "ref: refs/heads/main\n")
	got := discoverRepos("agent-b")
	if len(got) != 2 || got[0].Path != "example-repo" || got[1].Path != "product/nested" {
		t.Fatalf("repos = %+v", got)
	}
}

func TestFlowValidationLoopsUntilComplete(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	deadline := now.Add(6 * time.Hour)
	f := flow{
		ID: "flow-20260710-1200-a1b2c3d4", Agent: "agent-b", Repo: "example-repo", Goal: "ship it",
		Template: "refine-adr", NodeRoles: []string{"refine", "validate"}, Deadline: &deadline,
		Created: now, Updated: now, Status: "running", Batch: "flow-test", Base: "HEAD",
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	writeFile(t, reportPath(f.Agent, f.Nodes[0].JobID), "# refined\n")
	writeFile(t, reportPath(f.Agent, f.Nodes[1].JobID), "---\nflow_status: needs-work\n---\n# validation\n")
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	reconcileFlowsLocked(now.Add(time.Minute))
	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || got.Round != 1 || len(got.Nodes) != 4 || got.Nodes[2].Role != "amend" || got.Nodes[3].Role != "validate" {
		t.Fatalf("after needs-work = %+v", got)
	}
	writeFile(t, reportPath(got.Agent, got.Nodes[2].JobID), "# amended\n")
	writeFile(t, reportPath(got.Agent, got.Nodes[3].JobID), "---\nflow_status: complete\n---\n# accepted\n")
	reconcileFlowsLocked(now.Add(2 * time.Minute))
	got, _ = loadFlow(f.ID)
	if got.Status != "complete" {
		t.Fatalf("status = %s, want complete", got.Status)
	}
}

func TestFlowDeadlineStopsNewLaunches(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	past := now.Add(5 * time.Minute)
	j := job{ID: "20260710-1200-flow-ab12", Agent: "agent-b", Prompt: "x", At: now, Kind: "flow", Created: now, FinishBy: &past}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	schedTick(now)
	got := loadJobs("agent-b")[0]
	if !got.Skipped || got.Started {
		t.Fatalf("past-deadline job = %+v", got)
	}
}

func TestCompletedDevFlowCleansWorktree(t *testing.T) {
	flowEnv(t)
	f := flow{ID: "flow-20260710-1200-deadbeef", Agent: "playground", Repo: "demo", Status: "complete", Base: "HEAD"}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Worktree); err != nil {
		t.Fatal(err)
	}
	if err := tryCleanupWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if f.WorktreeState != "cleaned" {
		t.Fatalf("state = %s", f.WorktreeState)
	}
	if _, err := os.Stat(f.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

// TestCleanupAbsentWorktreeMarksCleaned guards the janitor bug where a worktree
// dir that is already gone (manually removed / lost to a crash) was wrongly
// retained forever — `git status` errors on the missing dir, so the tree wedged
// in "retained" and escalated to a bogus worktree-retained alert at 48h. Runs in
// the non-dev path (devMode's RemoveAll masks the bug) with a Worktree that does
// not exist on disk.
func TestCleanupAbsentWorktreeMarksCleaned(t *testing.T) {
	flowEnv(t)
	devMode = false // flowEnv set it true; the bug only manifests in the git path
	f := flow{
		ID: "flow-20260710-1200-c0ffee01", Agent: "playground", Status: "complete",
		WorktreeState: "active",
		SourceRepo:    filepath.Join(agentWorkspace("playground"), "demo"),
		Worktree:      filepath.Join(agentWorkspace("playground"), ".nightshift-worktrees", "flow-20260710-1200-c0ffee01", "demo"),
	}
	if _, err := os.Stat(f.Worktree); !os.IsNotExist(err) {
		t.Fatalf("precondition: worktree should be absent, stat err = %v", err)
	}
	if err := tryCleanupWorktree(&f); err != nil {
		t.Fatalf("cleanup of an absent worktree must not error: %v", err)
	}
	if f.WorktreeState != "cleaned" {
		t.Fatalf("state = %q, want cleaned (an absent worktree must not retain forever)", f.WorktreeState)
	}
}

func TestAgentDeadlineProposalUpdatesFlowAndQueuedJobs(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := flow{ID: "flow-20260710-1200-cafefeed", Agent: "agent-b", Repo: "example-repo", Goal: "x", Status: "queued", Batch: "flow-deadline", NodeRoles: []string{"refine"}, Created: now}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	want := now.Add(4 * time.Hour).UTC().Truncate(time.Second)
	writeFile(t, filepath.Join(flowUpdatesDir("agent-b"), f.ID+".deadline"), want.Format(time.RFC3339))
	reconcileFlowsLocked(now)
	got, _ := loadFlow(f.ID)
	if got.Deadline == nil || !got.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", got.Deadline, want)
	}
	j, ok := findFlowJob(got, got.Nodes[0].JobID)
	if !ok || j.FinishBy == nil || !j.FinishBy.Equal(want) {
		t.Fatalf("queued job finishBy = %v", j.FinishBy)
	}
	if _, err := os.Stat(filepath.Join(flowUpdatesDir("agent-b"), f.ID+".deadline")); !os.IsNotExist(err) {
		t.Fatalf("proposal not consumed: %v", err)
	}
}

func TestFlowGuidanceRewritesOnlyUnstartedJobs(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := flow{
		ID: "flow-20260710-1200-acde1234", Agent: "agent-b", Repo: "example-repo", Goal: "ship",
		Status: "queued", Batch: "flow-guidance", NodeRoles: []string{"plan", "review"}, Created: now,
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	first, _ := findFlowJob(f, f.Nodes[0].JobID)
	secondBefore, _ := findFlowJob(f, f.Nodes[1].JobID)
	pinnedInstructions, _, _ := strings.Cut(secondBefore.Prompt, "\n\n## Flow\n")
	first.Started = true
	if err := saveJob(first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nodesDir(), "review.md"), "# Node · Review\n\nA newer prompt that this run must not adopt.\n")
	setFlowGuidanceLocked(&f, "Prefer the public API and preserve compatibility.")
	firstAfter, _ := findFlowJob(f, f.Nodes[0].JobID)
	secondAfter, _ := findFlowJob(f, f.Nodes[1].JobID)
	if strings.Contains(firstAfter.Prompt, "Prefer the public API") {
		t.Fatal("started job prompt was mutated")
	}
	if !strings.Contains(secondAfter.Prompt, "Prefer the public API") {
		t.Fatal("queued job prompt did not receive updated guidance")
	}
	if !strings.HasPrefix(secondAfter.Prompt, pinnedInstructions+"\n\n## Flow\n") {
		t.Fatal("guidance update changed the queued job's pinned node prompt")
	}
}

// A spool-created run (nightshift-flow create, ADR-0019) can persist
// "acceptance": null; loaders must normalize it to [] or the SPA's run-detail
// page crashes on .length (seen live 2026-07-28, flow-20260728-1715-7abc6be0).
func TestLoadFlowNormalizesNullAcceptance(t *testing.T) {
	flowEnv(t)
	id := "flow-20260728-0001-deadbeef"
	writeFile(t, flowPath(id), `{"id":"`+id+`","agent":"playground","repo":"essayist","goal":"g","acceptance":null,"nodeRoles":["plan"],"nodes":[],"status":"running","created":"2026-07-28T10:00:00Z","updated":"2026-07-28T10:00:00Z"}`)

	f, err := loadFlow(id)
	if err != nil {
		t.Fatal(err)
	}
	if f.Acceptance == nil {
		t.Fatal("loadFlow left acceptance nil — SPA sees null and crashes")
	}
	all := loadFlows()
	for _, lf := range all {
		if lf.ID == id && lf.Acceptance == nil {
			t.Fatal("loadFlows left acceptance nil")
		}
	}
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), `"acceptance":null`) {
		t.Fatal("flow marshals acceptance as null")
	}
}
