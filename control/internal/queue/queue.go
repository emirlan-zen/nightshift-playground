// Package queue holds the launch-admission decisions for the serialized night
// queue (ADR-0019): concurrency caps, executor-health holds, the launch-time
// deadline re-clamp, starvation, and the dead-unit watchdog.
//
// Every decision here is a PURE function over run.Job domain values and
// immutable snapshots (Load, Health) — no filesystem, no sudo, no clock reads,
// no globals. The control adapter (internal/control/queue.go) gathers the
// snapshots from the running box, applies the side effects (persisting a skip,
// probing a unit, raising an alert), and emits the structured slog line. That
// split is what lets these rules be table-tested exhaustively and keeps the
// import boundary the architecture test locks (queue imports run, nothing more).
package queue

import (
	"fmt"
	"time"

	"nightshift/control/internal/run"
)

const (
	// MaxAutomationCap bounds any per-automation maxConcurrent — the same
	// ceiling as the flow session budget, far above the box's real capacity.
	MaxAutomationCap = 16

	// StarvationAfter is how long a due, admissible-but-unlaunched job may wait
	// before it raises a queue-starvation alert (scheduling failures should be
	// as loud as auth failures).
	StarvationAfter = 45 * time.Minute

	// ExecutorHoldMaxAge bounds how stale a health verdict may be and still
	// pause the queue. Past it we fail OPEN — the launcher's own pre-flight is
	// the real gate, and a wedged prober must not hold a healthy night.
	ExecutorHoldMaxAge = 30 * time.Minute

	// Watchdog (ADR-0019): a Started unit that died report-less holds its cap
	// slot until auto-stop. WatchdogMinAge/WatchdogInterval bound how eagerly it
	// is probed (minimum age, per-job interval).
	WatchdogMinAge   = 15 * time.Minute
	WatchdogInterval = 5 * time.Minute
)

// Load is the session occupancy at decision time, box-wide and per cap group,
// derived by the adapter from job files alone (no sudo) so it is exact across
// restarts.
type Load struct {
	Total   int
	ByGroup map[string]int
}

// AdmitCap checks the box-wide and per-automation concurrency caps. A denial
// reason is returned for logging; ok=false means hold the candidate this tick.
func AdmitCap(boxCap int, l Load, j run.Job) (reason string, ok bool) {
	if boxCap > 0 && l.Total >= boxCap {
		return fmt.Sprintf("box cap %d reached", boxCap), false
	}
	if j.Cap > 0 && j.CapGroup != "" && l.ByGroup[j.CapGroup] >= j.Cap {
		return fmt.Sprintf("automation cap %d reached (%s)", j.Cap, j.CapGroup), false
	}
	return "", true
}

// Health is an immutable snapshot of executor auth/limit state at decision time.
// The adapter reads the live probes; the decision here is pure. Timestamps are
// unix seconds (0 = never checked). CodexKnown is false when codex was never
// probed on this box — a box that never ran a codex wave must never hold on it.
// LimitHit/LimitHitLabel describe an open Claude limit-hit alert inside the
// current rolling window; the label is the pre-formatted "15:04" of the hit so
// the message reads the same as before without coupling queue to a timezone.
type Health struct {
	Now             time.Time
	ClaudeOK        bool
	ClaudeDetail    string
	ClaudeCheckedAt int64
	CodexKnown      bool
	CodexOK         bool
	CodexCheckedAt  int64
	LimitHit        bool
	LimitHitLabel   string
}

func (h Health) fresh(checkedAt int64) bool {
	return h.Now.Sub(time.Unix(checkedAt, 0)) < ExecutorHoldMaxAge
}

// ExecutorHold pauses launches while the job's executor is known-unhealthy —
// the pause-don't-amputate half of ADR-0019. The job stays queued and is
// retried every tick; recovery resumes it. Stale or missing verdicts fail OPEN.
// Codex and Claude are isolated: a Claude limit-hit or dead Claude auth never
// pauses a codex job, and vice versa.
func ExecutorHold(h Health, j run.Job) (reason string, held bool) {
	if run.NormalizeExecutor(string(j.Executor)) == run.ExecutorCodex {
		if h.CodexKnown && !h.CodexOK && h.fresh(h.CodexCheckedAt) {
			return "codex auth down — queue paused", true
		}
		return "", false
	}
	if !h.ClaudeOK && h.ClaudeCheckedAt > 0 && h.fresh(h.ClaudeCheckedAt) {
		return "claude auth down — queue paused (" + h.ClaudeDetail + ")", true
	}
	if h.LimitHit {
		return "limit-hit at " + h.LimitHitLabel + " — queue paused until the window rolls", true
	}
	return "", false
}

// AbsoluteDeadlineMinutes applies a flow's finish-by timestamp to one session
// window. nil means no flow-level deadline. A window with less than the
// launcher floor left is terminal (ok=false) — the caller skips loudly.
func AbsoluteDeadlineMinutes(finish *time.Time, minutes int, now time.Time, floor int) (int, bool) {
	if finish == nil {
		return minutes, true
	}
	left := int(finish.Sub(now).Minutes())
	if left < floor {
		return 0, false
	}
	return min(minutes, left), true
}

// Starved reports whether a due candidate has waited past the starvation
// threshold. Waiting on a slot is normal; a pathological wait is an alert.
func Starved(j run.Job, now time.Time) bool {
	return now.Sub(j.At) >= StarvationAfter
}

// WatchdogDue reports whether a working run is old enough and hasn't been probed
// within the interval — i.e. worth one run-status probe this tick. A zero
// lastProbe means never probed.
func WatchdogDue(startedAt, lastProbe, now time.Time) bool {
	if now.Sub(startedAt) < WatchdogMinAge {
		return false
	}
	if !lastProbe.IsZero() && now.Sub(lastProbe) < WatchdogInterval {
		return false
	}
	return true
}

// ReleaseDead reports whether a run-status probe means the unit is provably gone
// and its cap slot should be released. An error is inconclusive (never release);
// an active unit is alive (never release); anything else is dead.
func ReleaseDead(state string, probeErr bool) bool {
	return !probeErr && state != "active"
}
