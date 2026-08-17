package control

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// saveTerminalFlow persists a minimal flow in the given terminal status and
// returns its round-tripped Updated timestamp (so callers can prove a rejected
// mutation never re-saved it — saveFlow bumps Updated, which resets the janitor
// cleanup clocks).
func saveTerminalFlow(t *testing.T, id, status string) time.Time {
	t.Helper()
	now := time.Now()
	f := flow{ID: id, Agent: "agent-b", Repo: "example-repo", Status: status, Created: now, Updated: now}
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	got, err := loadFlow(id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Updated
}

// Stopping an already-terminal flow must not clobber its outcome (a "complete"
// run reported as operator-cancelled) or bump Updated.
func TestFlowStopRejectsTerminalRun(t *testing.T) {
	flowEnv(t)
	id := "flow-20260711-0900-ab010001"
	updated := saveTerminalFlow(t, id, "complete")

	req := httptest.NewRequest("POST", "/api/flows/"+id+"/stop", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handleFlowStop(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("stop terminal flow: code = %d, want 409; body %s", rr.Code, rr.Body.String())
	}
	got, err := loadFlow(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "complete" {
		t.Fatalf("status = %q, want complete (outcome clobbered)", got.Status)
	}
	if !got.Updated.Equal(updated) {
		t.Fatalf("Updated was bumped on a rejected stop (%v -> %v)", updated, got.Updated)
	}
}

// Setting a deadline on a terminal flow is a no-op that must not re-save it.
func TestFlowDeadlineRejectsTerminalRun(t *testing.T) {
	flowEnv(t)
	id := "flow-20260711-0900-ab010002"
	updated := saveTerminalFlow(t, id, "blocked")

	req := httptest.NewRequest("POST", "/api/flows/"+id+"/deadline", strings.NewReader(`{"deadline":""}`))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handleFlowDeadline(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("deadline on terminal flow: code = %d, want 409", rr.Code)
	}
	got, _ := loadFlow(id)
	if got.Status != "blocked" || !got.Updated.Equal(updated) {
		t.Fatalf("terminal flow mutated: status=%q updated %v->%v", got.Status, updated, got.Updated)
	}
}

// Guidance on a terminal flow is a no-op that must not re-save it (an agent
// re-spooling guidance on a blocked run could otherwise keep its worktree from
// ever being reaped by resetting the janitor clock).
func TestFlowGuidanceRejectsTerminalRun(t *testing.T) {
	flowEnv(t)
	id := "flow-20260711-0900-ab010003"
	updated := saveTerminalFlow(t, id, "blocked")

	req := httptest.NewRequest("POST", "/api/flows/"+id+"/guidance", strings.NewReader(`{"guidance":"do X"}`))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	handleFlowGuidance(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("guidance on terminal flow: code = %d, want 409", rr.Code)
	}
	got, _ := loadFlow(id)
	if got.Guidance != "" || !got.Updated.Equal(updated) {
		t.Fatalf("terminal flow mutated: guidance=%q updated %v->%v", got.Guidance, updated, got.Updated)
	}
}

// The agent spool's guidance path must reject a terminal run just like its
// append path already does (the inconsistency this PR closes).
func TestSpoolGuidanceRejectsTerminalRun(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	id := "flow-20260711-0900-ab010004"
	updated := saveTerminalFlow(t, id, "blocked")

	dir := flowUpdatesDir("agent-b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".guidance-9-1"), []byte("late steer"), 0o644); err != nil {
		t.Fatal(err)
	}
	nsMu.Lock()
	applyFlowSpoolLocked(now)
	nsMu.Unlock()

	res, err := os.ReadFile(filepath.Join(dir, id+".guidance-9-1.result"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), "rejected: run is terminal") {
		t.Fatalf("spool guidance result = %q, want a terminal rejection", res)
	}
	got, _ := loadFlow(id)
	if got.Guidance != "" || !got.Updated.Equal(updated) {
		t.Fatalf("terminal flow mutated via spool: guidance=%q updated %v->%v", got.Guidance, updated, got.Updated)
	}
}
