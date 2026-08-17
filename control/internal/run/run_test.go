package run

import (
	"log/slog"
	"testing"
	"time"
)

func TestNormalizeExecutor(t *testing.T) {
	cases := []struct {
		in   string
		want Executor
	}{
		{"", ExecutorClaude},
		{"claude", ExecutorClaude},
		{" Claude ", ExecutorClaude},
		{"CODEX", ExecutorCodex},
		{"codex", ExecutorCodex},
		{"gpt", Executor("gpt")}, // preserved but invalid
	}
	for _, c := range cases {
		if got := NormalizeExecutor(c.in); got != c.want {
			t.Errorf("NormalizeExecutor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExecutorValid(t *testing.T) {
	if !ExecutorClaude.Valid() || !ExecutorCodex.Valid() {
		t.Fatal("claude and codex must be valid executors")
	}
	if Executor("gpt").Valid() {
		t.Fatal("unknown executor must be invalid")
	}
	if got := Executors(); len(got) != 2 {
		t.Fatalf("Executors() = %v, want the two known engines", got)
	}
}

func TestNormalizeVerdict(t *testing.T) {
	cases := map[string]Verdict{
		"ok":             VerdictOK,
		"done":           VerdictOK, // pre-ADR-0017 synonym
		"needs-work":     VerdictNeedsWork,
		"partial":        VerdictNeedsWork,
		"blocked":        VerdictBlocked,
		"needs-decision": VerdictBlocked,
		"complete":       VerdictComplete,
		" COMPLETE ":     VerdictComplete,
		"garbage":        Verdict(""),
	}
	for in, want := range cases {
		if got := NormalizeVerdict(in); got != want {
			t.Errorf("NormalizeVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJobTier(t *testing.T) {
	future := time.Now()
	cases := []struct {
		name string
		job  Job
		want Tier
	}{
		{"operator deferred outranks all", Job{Kind: "deferred"}, TierOperator},
		{"profile deadline is a window to protect", Job{Kind: "sweep", Deadline: "01:00"}, TierDeadline},
		{"finish-by is also a deadline tier", Job{Kind: "sweep", FinishBy: &future}, TierDeadline},
		{"open-ended sweep is elastic", Job{Kind: "sweep"}, TierElastic},
	}
	for _, c := range cases {
		if got := c.job.Tier(); got != c.want {
			t.Errorf("%s: Tier() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSortByLaunchOrder(t *testing.T) {
	now := time.Date(2026, 6, 13, 14, 0, 0, 0, time.UTC)
	// Deliberately out of order: elastic-but-earlier, operator-but-later, and a
	// same-time id tiebreak. Want: tier first, then due time, then id.
	jobs := []Job{
		{ID: "z-sweep", Kind: "sweep", At: now.Add(-10 * time.Minute)},
		{ID: "op", Kind: "deferred", At: now.Add(-5 * time.Minute)},
		{ID: "b-sweep", Kind: "sweep", At: now.Add(-3 * time.Minute)},
		{ID: "a-sweep", Kind: "sweep", At: now.Add(-3 * time.Minute)},
	}
	SortByLaunchOrder(jobs)
	want := []string{"op", "z-sweep", "a-sweep", "b-sweep"}
	for i, id := range want {
		if jobs[i].ID != id {
			t.Fatalf("order[%d] = %q, want %q (full: %v)", i, jobs[i].ID, id, ids(jobs))
		}
	}
}

func ids(js []Job) []string {
	out := make([]string, len(js))
	for i, j := range js {
		out[i] = j.ID
	}
	return out
}

func TestJobLogAttrsCarriesTopLevelIdentity(t *testing.T) {
	j := Job{ID: "flow-1", Agent: "playground", Node: "review", Executor: ExecutorCodex}
	pairs := j.LogAttrs()
	if len(pairs)%2 != 0 {
		t.Fatalf("LogAttrs must be key/value pairs, got %d elements", len(pairs))
	}
	attrs := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		val, _ := pairs[i+1].(string)
		attrs[key] = val
	}
	// The keys are top-level (run_id, not a nested job.run) so a night is
	// reconstructable by a single run_id filter across every transition.
	for k, want := range map[string]string{
		"run_id": "flow-1", "agent": "playground", "node": "review", "executor": "codex",
	} {
		if attrs[k] != want {
			t.Errorf("LogAttrs()[%q] = %q, want %q", k, attrs[k], want)
		}
	}
	// An empty executor normalizes to claude so the identity is never blank.
	if got := attrKV(Job{}.LogAttrs())["executor"]; got != "claude" {
		t.Errorf("empty executor = %q, want claude", got)
	}
	// A slog line must be constructable from the attrs without panicking.
	slog.New(slog.NewTextHandler(discard{}, nil)).Info("t", j.LogAttrs()...)
}

func attrKV(pairs []any) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		val, _ := pairs[i+1].(string)
		m[key] = val
	}
	return m
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
