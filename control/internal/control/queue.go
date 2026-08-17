package control

// Serialized launch queue (ADR-0019). The fire loop used to launch the first
// due job it happened to iterate onto; this file makes launching a deliberate
// queue decision. Each tick every launchable job becomes a candidate; the
// candidates sort deterministically (priority tier, then due time, then id) so
// a control-plane restart replays the same queue; the head launches only when
// it passes admission — executor health holds (pause, don't amputate), the
// box-wide and per-automation concurrency caps, the launch gap, and a
// launch-time deadline re-clamp so a pause can never push a run past the
// window it was scheduled to respect. Still at most one launch per tick; the
// launcher's auth flock remains the concurrent-refresh guard.
//
// ADR-0020: the DECISIONS above are pure functions in internal/queue over
// internal/run domain values; this file is the adapter that gathers the box's
// live state into immutable snapshots, calls those decisions, applies the side
// effects (persist a skip, probe a unit, raise an alert), and emits the
// structured slog transition. The architecture test locks the boundary — the
// decision packages never import this one.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"nightshift/control/internal/queue"
	"nightshift/control/internal/run"
	"nightshift/control/internal/scheduler"
)

// maxAutomationCap bounds any per-automation maxConcurrent value — re-exported
// from the queue domain so automations.go/profile.go share one ceiling.
const maxAutomationCap = queue.MaxAutomationCap

// capGroupFor scopes a night wave's concurrency to its agent + batch — one
// pipeline run. Flow nodes use the flow id instead (stamped in appendFlowNode).
func capGroupFor(agent, batch string) string { return agent + "/" + batch }

// launchCandidate is one launchable job with its queue placement.
type launchCandidate struct {
	agent string
	j     job
}

// runJob projects the control-plane job onto the scheduling domain value the
// queue decisions reason about. The map is the ONE boundary where persistence
// meets the pure kernel.
func runJob(j job) run.Job {
	return run.Job{
		ID:        j.ID,
		Agent:     j.Agent,
		Node:      j.NodeID,
		Kind:      j.Kind,
		Executor:  run.Executor(j.Executor),
		Label:     j.Label,
		At:        j.At,
		Deadline:  j.Deadline,
		FinishBy:  j.FinishBy,
		Minutes:   j.Minutes,
		StartedAt: j.StartedAt,
		Cap:       j.Cap,
		CapGroup:  j.CapGroup,
		FastStart: j.FastStart,
	}
}

// sortCandidates orders the tick's launch queue deterministically: tier, then
// due time, then id (run.Before). The id tiebreak is what makes a restart
// replay the same queue instead of reshuffling it.
func sortCandidates(cands []launchCandidate) {
	sort.SliceStable(cands, func(a, b int) bool {
		return run.Before(runJob(cands[a].j), runJob(cands[b].j))
	})
}

// jobWorking reports whether a started job is still occupying a session slot:
// launched, not terminal, no report yet, inside its auto-stop window, and not
// released by the watchdog. A delivered-but-idle RC session does NOT count —
// successors ungate on the report that ended the work, and counting the idle
// unit would deadlock them behind it.
func jobWorking(agent string, j job, now time.Time) bool {
	return j.Started && !j.Skipped && runMaybeActive(j, now) &&
		!reportExists(agent, j.ID) && !watchdogMarker(agent, j.ID) &&
		!authFailMarker(agent, j.ID) && !depSkipMarker(agent, j.ID)
}

// queueLoad counts working sessions box-wide and per cap group, derived from
// job files alone (no sudo) so it is exact across restarts.
type queueLoad struct {
	total    int
	byGroup  map[string]int
	perAgent map[string][]job // the tick's job listing, reused by callers
}

func loadQueue(now time.Time) queueLoad {
	l := queueLoad{byGroup: map[string]int{}, perAgent: map[string][]job{}}
	for _, agent := range companies {
		jobs := loadJobs(agent)
		l.perAgent[agent] = jobs
		for _, j := range jobs {
			if jobWorking(agent, j, now) {
				l.total++
				if j.CapGroup != "" {
					l.byGroup[j.CapGroup]++
				}
			}
		}
	}
	return l
}

// ---- launch application service adapters (ADR-0020) ---------------------------
//
// schedTick's launch phase — admit → persist → launch for one candidate — is the
// scheduler.Service (internal/scheduler), so the lifecycle runs against three
// narrow ports (Clock, Store, Launcher) instead of the global rcRun/saveJob/time.
// These adapters bind those ports to the box; the pure admission decisions stay
// in internal/queue and the identity/log schema in internal/run.

// launchClock returns the tick's instant so the service is deterministic with
// schedTick's injected `now`.
type launchClock struct{ now time.Time }

func (c launchClock) Now() time.Time { return c.now }

// rcLauncher dispatches a run through the scoped nightshift-rc wrapper.
type rcLauncher struct{}

func (rcLauncher) Launch(_ context.Context, j run.Job) (string, error) {
	return rcRun("run", j.Agent, j.ID)
}

// launchStore persists the service's job transitions. It holds the tick's
// candidate jobs by run id so a run.Job transition maps back to the persisted
// job. Guarded by nsMu (held by schedTick); not safe for concurrent use.
type launchStore struct {
	jobs map[string]*job
}

func newLaunchStore(cands []launchCandidate) *launchStore {
	s := &launchStore{jobs: make(map[string]*job, len(cands))}
	for i := range cands {
		jc := cands[i].j
		s.jobs[jc.ID] = &jc
	}
	return s
}

func (s *launchStore) lookup(id string) (*job, error) {
	j, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("launch store: unknown run %s", id)
	}
	return j, nil
}

func (s *launchStore) Clamp(rj run.Job, minutes int) error {
	j, err := s.lookup(rj.ID)
	if err != nil {
		return err
	}
	j.Minutes = minutes
	return saveJob(*j)
}

func (s *launchStore) MarkStarted(rj run.Job, at time.Time) error {
	j, err := s.lookup(rj.ID)
	if err != nil {
		return err
	}
	j.Started, j.StartedAt = true, at
	return saveJob(*j)
}

func (s *launchStore) Skip(rj run.Job, _ string) error {
	j, err := s.lookup(rj.ID)
	if err != nil {
		return err
	}
	j.Gated, j.Skipped = false, true
	return saveJob(*j)
}

// tickHealth gathers the executor-health snapshot once per tick: auth verdicts
// plus any active limit-hit. Gathering the limit-hit unconditionally is safe —
// the pure ExecutorHold decision reads it only on the claude branch, so a codex
// job is decided identically to before.
func tickHealth(now time.Time) queue.Health {
	h := queue.Health{Now: now}
	h.ClaudeOK, h.ClaudeDetail, h.ClaudeCheckedAt = claudeAuthVerdict()
	if cs := codexHealthStatus(); cs != nil {
		h.CodexKnown, h.CodexOK, h.CodexCheckedAt = true, cs.OK, cs.CheckedAt
	}
	if hitAt, active := limitHitActive(now); active {
		h.LimitHit = true
		h.LimitHitLabel = time.Unix(hitAt, 0).In(bishkek).Format("15:04")
	}
	return h
}

// newLaunchTick assembles the immutable per-tick snapshot the launch service
// reads. The profile "HH:MM" clamp is injected as a closure because it is
// timezone-aware (an adapter concern); the pure absolute finish-by re-clamp is
// applied by the service on top of it.
func newLaunchTick(cfg nightConfig, load queueLoad, now time.Time, gapBlocked bool) scheduler.Tick {
	return scheduler.Tick{
		Load:       queue.Load{Total: load.total, ByGroup: load.byGroup},
		Health:     tickHealth(now),
		BoxCap:     cfg.MaxConcurrentSessions,
		Floor:      runMinutesFloor,
		GapBlocked: gapBlocked,
		Clamp: func(rj run.Job, t time.Time) (int, bool) {
			return deadlineClampedMinutes(rj.Deadline, rj.Minutes, t)
		},
	}
}

// queueHoldMsg dedups the hold log line — a standing pause must not spam the
// journal every 30s tick. Guarded by nsMu like the scheduler state around it.
var queueHoldMsg string

func noteQueueHold(j job, reason string) {
	if reason != queueHoldMsg {
		logf("queue: holding launches — %s", reason)
		scheduler.Log(nil, slog.LevelWarn, "queue.held", runJob(j), "decision", "hold", "reason", reason)
		queueHoldMsg = reason
	}
}

func clearQueueHold(j job) {
	if queueHoldMsg != "" {
		logf("queue: resumed after hold (%s)", queueHoldMsg)
		scheduler.Log(nil, slog.LevelInfo, "queue.resumed", runJob(j), "decision", "resume", "was", queueHoldMsg)
		queueHoldMsg = ""
	}
}

// limitHitActive reports an open limit-hit alert younger than one 5h rolling
// window: launching more Claude sessions into an enforced limit only converts
// queued work into error storms. Past the window the limit has rolled — resume.
func limitHitActive(now time.Time) (int64, bool) {
	if obsDB == nil {
		return 0, false
	}
	var ts int64
	err := obsDB.QueryRow(
		`SELECT COALESCE(MAX(COALESCE(NULLIF(last_ts,0), ts)),0) FROM alerts
		  WHERE kind='limit-hit' AND cleared=0`).Scan(&ts)
	if err != nil || ts == 0 {
		return 0, false
	}
	if now.Sub(time.Unix(ts, 0)) < 5*time.Hour {
		return ts, true
	}
	return 0, false
}

// noteSkip records a terminal skip (a window that closed before the job could
// launch, a past deadline, a failed worktree, or a dead upstream) through the
// one lifecycle schema, so a hollowed sub-tree is reconstructable from the
// journal by the same top-level run_id every other transition carries. Launch-
// time window skips are emitted by the scheduler service; the scheduler-gating
// branches in schedTick call this directly.
func noteSkip(j job, reason string) {
	scheduler.Log(nil, slog.LevelWarn, "run.skipped", runJob(j), "decision", "skip", "reason", reason)
}

// noteStarvation raises a queue-starvation alert for a candidate that has been
// due for a while without launching. raiseAlert dedups on (kind,agent,run_id),
// so a standing condition refreshes one row instead of spamming.
func noteStarvation(agent string, j job, now time.Time) {
	if obsDB == nil || !queue.Starved(runJob(j), now) {
		return
	}
	raiseAlert(obsDB, agent, j.ID, "", "queue-starvation",
		fmt.Sprintf("%s due %s, still queued %dm later", j.Label,
			j.At.In(bishkek).Format("15:04"), int(now.Sub(j.At).Minutes())),
		j.At.Unix(), now.Unix())
}

// queueBacklogged reports whether an agent still has LAUNCHABLE unstarted work
// — due, unskipped, and not gated. The governor holds top-ups while the queue
// holds work it could start; a gated wave waiting on its upstreams is a
// dependency, not spare demand, and must not block pacing all night.
func queueBacklogged(agent string, now time.Time) bool {
	for _, j := range loadJobs(agent) {
		if !j.Started && !j.Skipped && !j.Gated && !j.At.After(now) {
			return true
		}
	}
	return false
}

// ---- watchdog ------------------------------------------------------------------

func watchdogPath(agent, id string) string {
	return filepath.Join(reportsDir(agent), id+".watchdog")
}

func watchdogMarker(agent, id string) bool {
	_, err := os.Stat(watchdogPath(agent, id))
	return err == nil
}

func writeWatchdogMarker(agent, id, reason string) {
	if err := os.MkdirAll(reportsDir(agent), 0o755); err != nil {
		logf("watchdog: mkdir %s: %v", agent, err)
		return
	}
	if err := os.WriteFile(watchdogPath(agent, id), []byte(reason), 0o644); err != nil {
		logf("watchdog: write %s/%s: %v", agent, id, err)
	}
}

// watchdogChecked bounds run-status probing to one sudo call per job per
// queue.WatchdogInterval. Guarded by nsMu; entries are dropped once the job is.
var watchdogChecked = map[string]time.Time{}

// runWatchdog releases cap slots held by dead sessions: a Started run whose
// systemd unit is gone but which never wrote a report would otherwise occupy
// its slot until the auto-stop window expired — under a cap of 1, the whole
// box. Active-but-idle sessions stay the obs stall detector's job (alert, not
// auto-stop); this only reaps units that are provably gone. Caller holds nsMu.
func runWatchdog(now time.Time, load queueLoad) {
	for _, agent := range companies {
		for _, j := range load.perAgent[agent] {
			if !jobWorking(agent, j, now) || !queue.WatchdogDue(j.StartedAt, watchdogChecked[j.ID], now) {
				continue
			}
			watchdogChecked[j.ID] = now
			state, err := rcRun("run-status", agent, j.ID)
			if !queue.ReleaseDead(state, err != nil) {
				continue
			}
			reason := fmt.Sprintf("session unit %s with no report %dm after start — queue slot released",
				state, int(now.Sub(j.StartedAt).Minutes()))
			writeWatchdogMarker(agent, j.ID, reason)
			logf("watchdog %s/%s (%s): %s", agent, j.ID, j.Label, reason)
			scheduler.Log(nil, slog.LevelWarn, "run.watchdog_released", runJob(j),
				"decision", "release", "state", state, "reason", reason)
			if obsDB != nil {
				raiseAlert(obsDB, agent, j.ID, "", "watchdog-release", reason, j.StartedAt.Unix(), now.Unix())
			}
		}
	}
	for id, last := range watchdogChecked {
		if now.Sub(last) > 24*time.Hour {
			delete(watchdogChecked, id)
		}
	}
}
