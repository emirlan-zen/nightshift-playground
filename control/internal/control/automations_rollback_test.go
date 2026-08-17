package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A createFlowLocked that fails at saveFlow (after mintFlow already persisted
// every node job) must delete those jobs. Otherwise the ungated first-stage job
// orphans on disk and schedTick launches a real session for a flow whose record
// was never written — a phantom run with no reconcile owner.
func TestCreateFlowRollsBackJobsOnSaveFailure(t *testing.T) {
	flowEnv(t)
	now := time.Now()

	// Force saveFlow to fail without touching jobsDir: drop a regular file where
	// the flows dir must be, so saveFlow's MkdirAll errors. mintFlow writes
	// jobsDir (a different path) and still succeeds first.
	if err := os.MkdirAll(filepath.Dir(flowsDir()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flowsDir(), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}

	future := now.Add(8 * time.Hour)
	in := flowSpec{
		Agent: "agent-b", Repo: "example-repo", Goal: "x", Acceptance: []string{"done"},
		Template: "refine-adr", Base: "HEAD", Deadline: &future,
	}
	if _, err := createFlowLocked(in, now); err == nil {
		t.Fatal("expected createFlowLocked to fail at saveFlow")
	}
	if jobs := loadJobs("agent-b"); len(jobs) != 0 {
		t.Fatalf("failed create orphaned %d node jobs: %+v", len(jobs), jobs)
	}
}
