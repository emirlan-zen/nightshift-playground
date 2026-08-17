// Package scheduler is the launch application service for the night queue
// (ADR-0020). It owns the admit → persist → launch lifecycle for one queued run
// and reaches the running box only through three narrow ports — Clock, Store,
// Launcher — so the whole sequence is table-testable with fakes and a night is
// reconstructable from one structured log schema.
//
// It sits one ring outside the pure decision packages: it imports the run
// kernel and the queue decisions and orchestrates them, but it never touches the
// filesystem, sudo, HTTP, or the composition root. internal/control wires the
// production adapters (Store → job files, Launcher → nightshift-rc, Clock → wall
// time) and drives the per-tick candidate loop; this service decides and effects
// each candidate. The architecture test locks the boundary: scheduler must not
// import net/http or internal/control.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"nightshift/control/internal/queue"
	"nightshift/control/internal/run"
)

// Clock supplies the launch instant. Injected so the service is deterministic
// under test and shares schedTick's per-tick `now` in production.
type Clock interface {
	Now() time.Time
}

// Launcher dispatches a run and returns the launcher's combined output for
// diagnostics. The production adapter shells nightshift-rc; a fake records calls.
type Launcher interface {
	Launch(ctx context.Context, j run.Job) (output string, err error)
}

// Store persists the three launch-time job transitions the service makes:
//   - Clamp: the deadline re-clamp, written BEFORE dispatch so nightshift-rc arms
//     the auto-stop timer on the trimmed window, not the stale one.
//   - MarkStarted: the started stamp, after a successful dispatch.
//   - Skip: a terminal window-closed skip.
//
// Each is keyed by the run identity carried on j; the adapter maps it back to the
// persisted job. Keeping persistence behind a port is what lets the service be
// tested with an in-memory fake instead of the on-box job files.
type Store interface {
	Clamp(j run.Job, minutes int) error
	MarkStarted(j run.Job, at time.Time) error
	Skip(j run.Job, reason string) error
}

// ProfileClamp applies an automation's timezone-aware "HH:MM" deadline to a
// candidate at launch time, returning the trimmed window (ok=false = the window
// already closed). It is injected because that clamp is a timezone/adapter
// concern the kernel must not know; the pure absolute-deadline re-clamp
// (queue.AbsoluteDeadlineMinutes) is layered on top of it here.
type ProfileClamp func(j run.Job, now time.Time) (minutes int, ok bool)

// Decision classifies what the service did with one candidate this tick.
type Decision int

const (
	Launched      Decision = iota // dispatched; the tick's one launch is spent
	Held                          // executor unhealthy — paused, retry next tick
	SkippedWindow                 // deadline already closed — terminal, persisted
	CapBusy                       // a concurrency cap is full — waiting on a slot
	GapBlocked                    // launch-gap spacing — waiting on the box anchor
	LaunchFailed                  // rc dispatch (or its pre-persist) errored — not started
)

func (d Decision) String() string {
	switch d {
	case Launched:
		return "launched"
	case Held:
		return "held"
	case SkippedWindow:
		return "skipped"
	case CapBusy:
		return "cap-busy"
	case GapBlocked:
		return "gap-blocked"
	default:
		return "launch-failed"
	}
}

// Tick is the immutable snapshot the launch decisions read: the box's session
// occupancy, executor health, caps, and the launch-gap state, gathered once by
// the adapter so every candidate in a tick is decided against one consistent
// view.
type Tick struct {
	Load       queue.Load
	Health     queue.Health
	BoxCap     int          // config.MaxConcurrentSessions, 0 = uncapped
	Floor      int          // launcher minimum window (runMinutesFloor)
	GapBlocked bool         // box-wide launch-gap not yet elapsed
	Clamp      ProfileClamp // profile "HH:MM" deadline (tz-aware, adapter-owned)
}

// Service admits and launches queued runs through its ports.
type Service struct {
	clock    Clock
	store    Store
	launcher Launcher
	logger   *slog.Logger
}

// NewService wires the ports. A nil logger falls back to slog.Default so the
// service always emits the lifecycle schema.
func NewService(clock Clock, store Store, launcher Launcher, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{clock: clock, store: store, launcher: launcher, logger: logger}
}

// Admit runs one candidate through the admission gates in the ADR-0019 order and,
// when it passes, persists the clamp and dispatches it. It performs at most one
// launch, so the caller loops candidates in launch order (run.SortByLaunchOrder)
// and stops effecting once it sees Launched. Every state transition is emitted
// through the one lifecycle schema (run.Job.LogAttrs), so a night is
// reconstructable from the journal by a single run_id filter.
//
// The returned reason is a human string for the caller's observability side
// effects (a hold-dedup log, a dep-skip marker); the launch/skip transitions are
// already logged here.
func (s *Service) Admit(ctx context.Context, j run.Job, t Tick) (Decision, string) {
	now := s.clock.Now()

	// Launch-time deadline re-clamp: a queue pause (auth down, limit-hit, cap
	// busy) delayed us, and a late launch must still end when the schedule said
	// it would. Profile HH:MM first (tz-aware, injected), then the pure absolute
	// finish-by re-clamp.
	m, ok := t.Clamp(j, now)
	if ok {
		m, ok = queue.AbsoluteDeadlineMinutes(j.FinishBy, m, now, t.Floor)
	}
	if !ok {
		const reason = "window closed before launch (queue pause or backlog)"
		if err := s.store.Skip(j, reason); err != nil {
			s.log(slog.LevelError, "run.skip_persist_failed", j, "reason", reason, "error", err)
		}
		s.log(slog.LevelWarn, "run.skipped", j, "decision", "skip", "reason", reason)
		return SkippedWindow, reason
	}

	// Executor health: pause, don't amputate. The job stays queued and retries
	// every tick; recovery resumes it. Stale/missing verdicts fail OPEN.
	if reason, held := queue.ExecutorHold(t.Health, j); held {
		return Held, reason
	}
	// Concurrency caps: box-wide and per-automation.
	if reason, admit := queue.AdmitCap(t.BoxCap, t.Load, j); !admit {
		return CapBusy, reason
	}
	// Launch-gap spacing (the shared-creds concurrent-refresh guard). A
	// fast-handoff job bypasses the wait, not the one-launch-per-tick rule.
	if t.GapBlocked && !j.FastStart {
		return GapBlocked, "launch gap not elapsed"
	}

	// Persist the clamp BEFORE dispatch: nightshift-rc arms the auto-stop timer
	// from the .stop sidecar the instant it starts the unit, so an unsaved clamp
	// would run the session on the stale, longer window.
	if m != j.Minutes {
		if err := s.store.Clamp(j, m); err != nil {
			s.log(slog.LevelError, "run.clamp_persist_failed", j, "minutes", m, "error", err)
			return LaunchFailed, "clamp persist failed"
		}
		j.Minutes = m
	}

	out, err := s.launcher.Launch(ctx, j)
	if err != nil {
		s.log(slog.LevelError, "run.start_failed", j, "decision", "start-failed", "error", err, "output", out)
		return LaunchFailed, "rc dispatch failed"
	}
	// The unit is up. Persist the started stamp; a failure to persist is loud but
	// must not suppress the started transition — the session is really running.
	if err := s.store.MarkStarted(j, now); err != nil {
		s.log(slog.LevelError, "run.persist_started_failed", j, "error", err)
	}
	s.log(slog.LevelInfo, "run.started", j, "decision", "start", "kind", j.Kind, "minutes", j.Minutes)
	return Launched, ""
}

// log emits one lifecycle transition with the stable top-level identity.
func (s *Service) log(level slog.Level, event string, j run.Job, attrs ...any) {
	Log(s.logger, level, event, j, attrs...)
}

// Log emits one scheduler lifecycle transition with the stable top-level domain
// identity (run_id, node, agent, executor) every scheduler event shares — start,
// start-failure, skip, health hold/resume, watchdog release. The control adapter
// uses it for the transitions it owns (hold/resume/watchdog) so the whole
// scheduler speaks one query schema. A nil logger falls back to slog.Default.
func Log(logger *slog.Logger, level slog.Level, event string, j run.Job, attrs ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, event, append(j.LogAttrs(), attrs...)...)
}
