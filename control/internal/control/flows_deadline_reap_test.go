package control

import (
	"testing"
	"time"
)

// A flow that blows its absolute deadline must reap its still-gated/unstarted
// node jobs, exactly like blockFlowLocked and handleFlowStop. Before the fix the
// deadline branch only set Status="deadline" and saved, leaving the gated
// downstream jobs held forever by flowGateHold but never pruned — their JSON +
// sidecars leaked on disk indefinitely.
func TestFlowDeadlineReapsGatedJobs(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	future := now.Add(6 * time.Hour)
	f := flow{
		ID: "flow-20260711-0900-deadbeef", Agent: "agent-b", Repo: "example-repo", Goal: "ship it",
		Template: "refine-adr", NodeRoles: []string{"refine", "validate"}, Deadline: &future,
		Created: now, Updated: now, Status: "running", Batch: "flow-test", Base: "HEAD",
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	// The gated downstream node whose job would leak.
	gatedJobID := f.Nodes[1].JobID
	if j, ok := findFlowJob(f, gatedJobID); !ok || !j.Gated || j.Started {
		t.Fatalf("precondition: node[1] should be a gated, unstarted job: %+v ok=%v", j, ok)
	}

	// The run then blows its deadline.
	past := now.Add(-time.Minute)
	f.Deadline = &past
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}

	reconcileFlowsLocked(now)

	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "deadline" {
		t.Fatalf("status = %q, want deadline", got.Status)
	}
	if _, ok := findFlowJob(got, gatedJobID); ok {
		t.Fatalf("gated job %s should have been reaped, still present", gatedJobID)
	}
	if jobs := loadJobs(f.Agent); len(jobs) != 0 {
		t.Fatalf("unstarted jobs should be reaped on deadline, got %d: %+v", len(jobs), jobs)
	}
}
