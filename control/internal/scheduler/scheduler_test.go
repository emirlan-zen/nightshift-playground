package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"nightshift/control/internal/queue"
	"nightshift/control/internal/run"
)

// --- fakes ---------------------------------------------------------------------

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

type launchCall struct {
	id      string
	minutes int
}

type fakeLauncher struct {
	err   error
	out   string
	calls []launchCall
}

func (l *fakeLauncher) Launch(_ context.Context, j run.Job) (string, error) {
	l.calls = append(l.calls, launchCall{j.ID, j.Minutes})
	return l.out, l.err
}

type fakeStore struct {
	clampErr   error
	startedErr error
	skipErr    error
	clamped    map[string]int
	started    map[string]time.Time
	skipped    map[string]string
}

func newStore() *fakeStore {
	return &fakeStore{clamped: map[string]int{}, started: map[string]time.Time{}, skipped: map[string]string{}}
}

func (s *fakeStore) Clamp(j run.Job, minutes int) error {
	if s.clampErr != nil {
		return s.clampErr
	}
	s.clamped[j.ID] = minutes
	return nil
}

func (s *fakeStore) MarkStarted(j run.Job, at time.Time) error {
	if s.startedErr != nil {
		return s.startedErr
	}
	s.started[j.ID] = at
	return nil
}

func (s *fakeStore) Skip(j run.Job, reason string) error {
	if s.skipErr != nil {
		return s.skipErr
	}
	s.skipped[j.ID] = reason
	return nil
}

// capturingHandler records each record's message and its flattened attrs, so a
// test can assert the stable lifecycle log schema (Finding 2 regression guard).
type capturingHandler struct {
	records []capturedRecord
}

type capturedRecord struct {
	msg   string
	level slog.Level
	attrs map[string]string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler            { return h }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{msg: r.Message, level: r.Level, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.records = append(h.records, rec)
	return nil
}

func (h *capturingHandler) byMsg(msg string) (capturedRecord, bool) {
	for _, r := range h.records {
		if r.msg == msg {
			return r, true
		}
	}
	return capturedRecord{}, false
}

// harness builds a service over fresh fakes with an always-healthy tick.
func harness(t *testing.T, now time.Time) (*Service, *fakeStore, *fakeLauncher, *capturingHandler) {
	t.Helper()
	store := newStore()
	launcher := &fakeLauncher{}
	cap := &capturingHandler{}
	svc := NewService(fakeClock{now}, store, launcher, slog.New(cap))
	return svc, store, launcher, cap
}

// okTick is a snapshot that admits any healthy claude job with a passthrough clamp.
func okTick() Tick {
	return Tick{
		Load:   queue.Load{ByGroup: map[string]int{}},
		Health: queue.Health{ClaudeOK: true},
		Floor:  10,
		Clamp:  func(j run.Job, _ time.Time) (int, bool) { return j.Minutes, true },
	}
}

// --- tests ---------------------------------------------------------------------

func TestAdmitLaunchesAndPersistsHappyPath(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, cap := harness(t, now)
	j := run.Job{ID: "flow-1", Agent: "playground", Node: "exec", Executor: run.ExecutorClaude, Minutes: 120}

	dec, _ := svc.Admit(context.Background(), j, okTick())
	if dec != Launched {
		t.Fatalf("decision = %v, want Launched", dec)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].id != "flow-1" {
		t.Fatalf("launcher calls = %v, want one for flow-1", launcher.calls)
	}
	if _, ok := store.started["flow-1"]; !ok {
		t.Fatal("started stamp not persisted")
	}
	if at := store.started["flow-1"]; !at.Equal(now) {
		t.Fatalf("started at %v, want %v", at, now)
	}
	// run.started must carry the stable top-level identity.
	rec, ok := cap.byMsg("run.started")
	if !ok {
		t.Fatal("no run.started emitted")
	}
	for k, want := range map[string]string{"run_id": "flow-1", "node": "exec", "agent": "playground", "executor": "claude"} {
		if rec.attrs[k] != want {
			t.Errorf("run.started[%q] = %q, want %q", k, rec.attrs[k], want)
		}
	}
}

func TestAdmitPersistsClampBeforeLaunch(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, _ := harness(t, now)
	j := run.Job{ID: "r1", Agent: "playground", Node: "review", Minutes: 120}
	tick := okTick()
	tick.Clamp = func(j run.Job, _ time.Time) (int, bool) { return 45, true } // window trimmed to 45m

	if dec, _ := svc.Admit(context.Background(), j, tick); dec != Launched {
		t.Fatalf("decision = %v, want Launched", dec)
	}
	if store.clamped["r1"] != 45 {
		t.Fatalf("clamp persisted = %d, want 45", store.clamped["r1"])
	}
	// The launcher must see the trimmed window, not the stale 120.
	if launcher.calls[0].minutes != 45 {
		t.Fatalf("launched with %dm, want the clamped 45", launcher.calls[0].minutes)
	}
}

func TestAdmitSkipsClosedWindow(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, cap := harness(t, now)
	j := run.Job{ID: "late", Agent: "playground", Node: "synth", Minutes: 30}
	tick := okTick()
	tick.Clamp = func(j run.Job, _ time.Time) (int, bool) { return 0, false } // deadline already closed

	dec, reason := svc.Admit(context.Background(), j, tick)
	if dec != SkippedWindow {
		t.Fatalf("decision = %v, want SkippedWindow", dec)
	}
	if reason == "" {
		t.Fatal("skip must return a reason for the caller's dep-skip marker")
	}
	if len(launcher.calls) != 0 {
		t.Fatal("a closed window must not launch")
	}
	if _, ok := store.skipped["late"]; !ok {
		t.Fatal("skip not persisted")
	}
	rec, ok := cap.byMsg("run.skipped")
	if !ok {
		t.Fatal("no run.skipped emitted")
	}
	if rec.attrs["run_id"] != "late" || rec.attrs["node"] != "synth" {
		t.Errorf("run.skipped identity = %v, want top-level run_id/node", rec.attrs)
	}
}

func TestAdmitAbsoluteDeadlineClosesWindow(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, _, launcher, _ := harness(t, now)
	past := now.Add(3 * time.Minute) // less than the 10m floor away
	j := run.Job{ID: "fb", Agent: "playground", Node: "exec", Minutes: 120, FinishBy: &past}

	if dec, _ := svc.Admit(context.Background(), j, okTick()); dec != SkippedWindow {
		t.Fatalf("decision = %v, want SkippedWindow (finish-by inside floor)", dec)
	}
	if len(launcher.calls) != 0 {
		t.Fatal("a finish-by inside the floor must not launch")
	}
}

func TestAdmitHoldsOnDeadExecutor(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, _ := harness(t, now)
	j := run.Job{ID: "h1", Agent: "playground", Node: "exec", Minutes: 60}
	tick := okTick()
	tick.Health = queue.Health{Now: now, ClaudeOK: false, ClaudeCheckedAt: now.Unix(), ClaudeDetail: "not logged in"}

	dec, reason := svc.Admit(context.Background(), j, tick)
	if dec != Held {
		t.Fatalf("decision = %v, want Held", dec)
	}
	if reason == "" {
		t.Fatal("hold must surface a reason for the dedup log")
	}
	if len(launcher.calls) != 0 || len(store.started) != 0 {
		t.Fatal("a held job must neither launch nor persist a start")
	}
}

func TestAdmitCodexIgnoresClaudeHealth(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, _, launcher, _ := harness(t, now)
	j := run.Job{ID: "c1", Agent: "playground", Node: "review", Executor: run.ExecutorCodex, Minutes: 90}
	tick := okTick()
	// Claude down + limit-hit, but codex is healthy: the codex job must launch.
	tick.Health = queue.Health{Now: now, ClaudeOK: false, ClaudeCheckedAt: now.Unix(), LimitHit: true, CodexKnown: true, CodexOK: true, CodexCheckedAt: now.Unix()}

	if dec, _ := svc.Admit(context.Background(), j, tick); dec != Launched {
		t.Fatalf("decision = %v, want Launched (codex isolated from claude health)", dec)
	}
	if len(launcher.calls) != 1 {
		t.Fatal("codex job should have launched")
	}
}

func TestAdmitCapBusy(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, _, launcher, _ := harness(t, now)
	j := run.Job{ID: "cap", Agent: "playground", Node: "exec", Minutes: 60, Cap: 1, CapGroup: "flow-x"}
	tick := okTick()
	tick.Load = queue.Load{Total: 1, ByGroup: map[string]int{"flow-x": 1}}

	if dec, _ := svc.Admit(context.Background(), j, tick); dec != CapBusy {
		t.Fatalf("decision = %v, want CapBusy", dec)
	}
	if len(launcher.calls) != 0 {
		t.Fatal("a full cap must not launch")
	}
}

func TestAdmitGapBlockedUnlessFast(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, _, launcher, _ := harness(t, now)
	tick := okTick()
	tick.GapBlocked = true

	slow := run.Job{ID: "slow", Agent: "playground", Node: "exec", Minutes: 60}
	if dec, _ := svc.Admit(context.Background(), slow, tick); dec != GapBlocked {
		t.Fatalf("slow decision = %v, want GapBlocked", dec)
	}
	fast := run.Job{ID: "fast", Agent: "playground", Node: "exec", Minutes: 60, FastStart: true}
	if dec, _ := svc.Admit(context.Background(), fast, tick); dec != Launched {
		t.Fatalf("fast decision = %v, want Launched (FastStart bypasses the gap)", dec)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].id != "fast" {
		t.Fatalf("only the fast job should launch, got %v", launcher.calls)
	}
}

func TestAdmitClampPersistFailureBlocksLaunch(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, cap := harness(t, now)
	store.clampErr = errors.New("disk full")
	j := run.Job{ID: "cf", Agent: "playground", Node: "exec", Minutes: 120}
	tick := okTick()
	tick.Clamp = func(j run.Job, _ time.Time) (int, bool) { return 40, true }

	if dec, _ := svc.Admit(context.Background(), j, tick); dec != LaunchFailed {
		t.Fatalf("decision = %v, want LaunchFailed", dec)
	}
	if len(launcher.calls) != 0 {
		t.Fatal("must not launch when the clamp could not be persisted (stale auto-stop window)")
	}
	if _, ok := cap.byMsg("run.clamp_persist_failed"); !ok {
		t.Fatal("clamp persist failure must be logged")
	}
}

func TestAdmitLaunchFailureIsLoggedWithIdentity(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, cap := harness(t, now)
	launcher.err = errors.New("unit failed")
	launcher.out = "boom"
	j := run.Job{ID: "boom", Agent: "playground", Node: "medic", Executor: run.ExecutorClaude, Minutes: 60}

	if dec, _ := svc.Admit(context.Background(), j, okTick()); dec != LaunchFailed {
		t.Fatalf("decision = %v, want LaunchFailed", dec)
	}
	if len(store.started) != 0 {
		t.Fatal("a failed dispatch must not persist a start")
	}
	rec, ok := cap.byMsg("run.start_failed")
	if !ok {
		t.Fatal("no run.start_failed emitted")
	}
	// Finding 2: start_failed previously omitted node — it must be present now.
	if rec.attrs["node"] != "medic" || rec.attrs["run_id"] != "boom" {
		t.Errorf("run.start_failed identity = %v, want node=medic run_id=boom", rec.attrs)
	}
}

func TestAdmitStartedPersistFailureStillLogsStarted(t *testing.T) {
	now := time.Date(2026, 7, 12, 1, 0, 0, 0, time.UTC)
	svc, store, launcher, cap := harness(t, now)
	store.startedErr = errors.New("write failed")
	j := run.Job{ID: "ps", Agent: "playground", Node: "exec", Minutes: 60}

	// The unit is really up (launch succeeded), so started must still be emitted
	// even though persistence failed — otherwise the journal would deny a running
	// session exists.
	if dec, _ := svc.Admit(context.Background(), j, okTick()); dec != Launched {
		t.Fatalf("decision = %v, want Launched", dec)
	}
	if len(launcher.calls) != 1 {
		t.Fatal("launch should have happened")
	}
	if _, ok := cap.byMsg("run.persist_started_failed"); !ok {
		t.Fatal("persist failure must be logged")
	}
	if _, ok := cap.byMsg("run.started"); !ok {
		t.Fatal("run.started must still be emitted after a persist failure")
	}
}

func TestLogCarriesStableIdentityForAdapterEvents(t *testing.T) {
	cap := &capturingHandler{}
	j := run.Job{ID: "w1", Agent: "playground", Node: "harvest", Executor: run.ExecutorCodex}
	// The adapter uses the exported Log for the transitions it owns.
	Log(slog.New(cap), slog.LevelWarn, "queue.held", j, "reason", "codex auth down")
	Log(slog.New(cap), slog.LevelWarn, "run.watchdog_released", j, "state", "failed")

	for _, msg := range []string{"queue.held", "run.watchdog_released"} {
		rec, ok := cap.byMsg(msg)
		if !ok {
			t.Fatalf("no %s emitted", msg)
		}
		if rec.attrs["run_id"] != "w1" || rec.attrs["node"] != "harvest" || rec.attrs["executor"] != "codex" {
			t.Errorf("%s identity = %v, want top-level run_id/node/executor", msg, rec.attrs)
		}
	}
}
