package control

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// captureLog installs a recording slog handler as the default for the duration
// of a test and returns the store. schedTick constructs the launch service with
// slog.Default() and the adapter emitters call scheduler.Log(nil,…), so this
// captures the real wired lifecycle transitions, not a helper in isolation.
type logRec struct {
	msg   string
	attrs map[string]string
}

type logCapture struct{ recs *[]logRec }

func (c logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c logCapture) WithGroup(string) slog.Handler            { return c }
func (c logCapture) Handle(_ context.Context, r slog.Record) error {
	rec := logRec{msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	*c.recs = append(*c.recs, rec)
	return nil
}

func captureLog(t *testing.T) *[]logRec {
	t.Helper()
	recs := &[]logRec{}
	prev := slog.Default()
	slog.SetDefault(slog.New(logCapture{recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return recs
}

func findRec(recs *[]logRec, msg string) (logRec, bool) {
	for _, r := range *recs {
		if r.msg == msg {
			return r, true
		}
	}
	return logRec{}, false
}

// assertIdentity checks that a lifecycle transition carries the ONE stable schema
// (top-level run_id/node/agent/executor) every scheduler event shares, so a night
// is reconstructable from the journal by a single run_id filter.
func assertIdentity(t *testing.T, recs *[]logRec, msg, runID, node string) {
	t.Helper()
	rec, ok := findRec(recs, msg)
	if !ok {
		t.Fatalf("no %q lifecycle event emitted; got %+v", msg, *recs)
	}
	for k, want := range map[string]string{"run_id": runID, "node": node, "agent": "playground", "executor": "claude"} {
		if rec.attrs[k] != want {
			t.Errorf("%s[%q] = %q, want %q (schema: %+v)", msg, k, rec.attrs[k], want, rec.attrs)
		}
	}
}

// TestSchedTickEmitsStartedWithStableIdentity drives a real due job through the
// wired launch service and asserts the run.started transition carries the schema.
func TestSchedTickEmitsStartedWithStableIdentity(t *testing.T) {
	nightrunEnv(t, "playground")
	recs := captureLog(t)
	now := bish(t, "15:00") // daytime: no night wave is due, so only our job launches

	j := job{
		ID: "flow-life-1", Agent: "playground", NodeID: "exec", Executor: "claude",
		Kind: "deferred", Prompt: "x", Label: "exec", Minutes: 60,
		At: now.Add(-time.Minute), Created: now.Add(-time.Minute),
	}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}

	schedTick(now)

	if got := loadJobs("playground"); len(got) != 1 || !got[0].Started {
		t.Fatalf("job did not launch: %+v", got)
	}
	assertIdentity(t, recs, "run.started", "flow-life-1", "exec")
}

// TestSchedTickEmitsSkipWithStableIdentity covers a previously-unlogged skip
// branch (Finding 2): a job whose absolute deadline is already inside the
// launcher floor is skipped, and that terminal transition must be logged with the
// same top-level identity as a start.
func TestSchedTickEmitsSkipWithStableIdentity(t *testing.T) {
	nightrunEnv(t, "playground")
	recs := captureLog(t)
	now := bish(t, "15:00")
	soon := now.Add(3 * time.Minute) // inside the 10m launcher floor → terminal

	j := job{
		ID: "flow-life-skip", Agent: "playground", NodeID: "synth", Executor: "claude",
		Kind: "deferred", Prompt: "x", Label: "synth", Minutes: 30,
		At: now.Add(-time.Minute), Created: now.Add(-time.Minute), FinishBy: &soon,
	}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}

	schedTick(now)

	got := loadJobs("playground")
	if len(got) != 1 || !got[0].Skipped {
		t.Fatalf("job should be skipped, not launched: %+v", got)
	}
	assertIdentity(t, recs, "run.skipped", "flow-life-skip", "synth")
}
