package control

// ADR-0019: the serialized launch queue — tiers, caps, holds, launch-time
// deadline re-clamp, and the dead-unit watchdog.

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// queueEnv is nightrunEnv plus resets for the queue's process-lifetime state.
func queueEnv(t *testing.T, agents ...string) *[]string {
	t.Helper()
	calls := nightrunEnv(t, agents...)
	watchdogChecked = map[string]time.Time{}
	queueHoldMsg = ""
	authMu.Lock()
	authCur = authHealth{}
	authMu.Unlock()
	t.Cleanup(func() {
		authMu.Lock()
		authCur = authHealth{}
		authMu.Unlock()
	})
	return calls
}

func mustSave(t *testing.T, j job) {
	t.Helper()
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
}

func launchesOf(calls *[]string) []string {
	var out []string
	for _, c := range *calls {
		if strings.HasPrefix(c, "run ") {
			out = append(out, c)
		}
	}
	return out
}

func TestQueueTierOrderOperatorFirst(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	// The elastic sweep job is due EARLIER, but the operator's deferred job
	// outranks it — tiers beat due times.
	mustSave(t, job{ID: "20260613-1350-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep", At: now.Add(-10 * time.Minute), Created: now})
	mustSave(t, job{ID: "20260613-1355-run-bbbb", Agent: "agent-b", Prompt: "p", Kind: "deferred", At: now.Add(-5 * time.Minute), Created: now})
	schedTick(now)
	got := launchesOf(calls)
	if len(got) != 1 || !strings.Contains(got[0], "run-bbbb") {
		t.Fatalf("launches = %v, want the deferred job first", got)
	}
}

func TestQueueDeterministicIDTiebreak(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	at := now.Add(-5 * time.Minute)
	mustSave(t, job{ID: "20260613-1355-sweep-bbbb", Agent: "agent-b", Prompt: "p", Kind: "sweep", At: at, Created: now})
	mustSave(t, job{ID: "20260613-1355-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep", At: at, Created: now})
	schedTick(now)
	got := launchesOf(calls)
	if len(got) != 1 || !strings.Contains(got[0], "sweep-aaaa") {
		t.Fatalf("launches = %v, want the lexicographically first id", got)
	}
}

func TestGroupCapHoldsSecondSession(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	group := "agent-b/night-x"
	// One session of the capped automation is already WORKING (started, no
	// report, inside its window) — the second must wait for the slot.
	mustSave(t, job{ID: "20260613-1300-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-time.Hour), Created: now, Started: true, StartedAt: now.Add(-30 * time.Minute), Minutes: 120,
		Cap: 1, CapGroup: group})
	mustSave(t, job{ID: "20260613-1355-sweep-bbbb", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-5 * time.Minute), Created: now, Cap: 1, CapGroup: group})
	schedTick(now)
	if got := launchesOf(calls); len(got) != 0 {
		t.Fatalf("launches = %v, want none while the cap slot is busy", got)
	}
	// The report lands -> the slot frees -> the successor launches.
	if err := os.MkdirAll(reportsDir("agent-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath("agent-b", "20260613-1300-sweep-aaaa"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	schedTick(now.Add(time.Minute))
	if got := launchesOf(calls); len(got) != 1 || !strings.Contains(got[0], "sweep-bbbb") {
		t.Fatalf("launches = %v, want the queued session once the slot freed", got)
	}
}

func TestGlobalCapCountsAcrossAgents(t *testing.T) {
	calls := queueEnv(t, "agent-b", "agent-a")
	now := bish(t, "14:00")
	cfg := loadConfig()
	cfg.MaxConcurrentSessions = 1
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	mustSave(t, job{ID: "20260613-1300-sweep-aaaa", Agent: "agent-a", Prompt: "p", Kind: "sweep",
		At: now.Add(-time.Hour), Created: now, Started: true, StartedAt: now.Add(-30 * time.Minute), Minutes: 120})
	mustSave(t, job{ID: "20260613-1355-run-bbbb", Agent: "agent-b", Prompt: "p", Kind: "deferred",
		At: now.Add(-5 * time.Minute), Created: now})
	schedTick(now)
	if got := launchesOf(calls); len(got) != 0 {
		t.Fatalf("launches = %v, want none under the box-wide cap", got)
	}
}

func TestQueueHoldsWhileClaudeAuthDown(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	authMu.Lock()
	authCur = authHealth{OK: false, Detail: "Not logged in", CheckedAt: now.Add(-2 * time.Minute).Unix()}
	authMu.Unlock()
	mustSave(t, job{ID: "20260613-1355-run-bbbb", Agent: "agent-b", Prompt: "p", Kind: "deferred",
		At: now.Add(-5 * time.Minute), Created: now})
	schedTick(now)
	if got := launchesOf(calls); len(got) != 0 {
		t.Fatalf("launches = %v, want a paused queue while auth is down", got)
	}
	for _, j := range loadJobs("agent-b") {
		if j.Skipped {
			t.Fatal("a paused job must stay queued, not be skipped")
		}
	}
	// Recovery (operator /login, next probe OK) resumes the queue.
	authMu.Lock()
	authCur = authHealth{OK: true, Detail: "ok", CheckedAt: now.Unix()}
	authMu.Unlock()
	schedTick(now.Add(time.Minute))
	if got := launchesOf(calls); len(got) != 1 {
		t.Fatalf("launches = %v, want the held job after auth recovered", got)
	}
}

func TestQueueAuthHoldFailsOpenWhenStale(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	authMu.Lock()
	authCur = authHealth{OK: false, Detail: "Not logged in", CheckedAt: now.Add(-2 * time.Hour).Unix()}
	authMu.Unlock()
	mustSave(t, job{ID: "20260613-1355-run-bbbb", Agent: "agent-b", Prompt: "p", Kind: "deferred",
		At: now.Add(-5 * time.Minute), Created: now})
	schedTick(now)
	if got := launchesOf(calls); len(got) != 1 {
		t.Fatalf("launches = %v, want fail-open on a stale verdict (launcher pre-flight is the gate)", got)
	}
}

func TestLaunchClampSkipsClosedWindowLoudly(t *testing.T) {
	calls := queueEnv(t, "agent-b")
	// 02:00: a wave with a 01:00 profile deadline was held past its window (a
	// pause, a backlog). It must skip loudly, never run silently late.
	now := bishD(t, "2026-06-14", "02:00")
	mustSave(t, job{ID: "20260613-2330-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-2 * time.Hour), Created: now.Add(-2 * time.Hour), Deadline: "01:00", Minutes: 60})
	schedTick(now)
	if got := launchesOf(calls); len(got) != 0 {
		t.Fatalf("launches = %v, want none past the deadline", got)
	}
	jobs := loadJobs("agent-b")
	if len(jobs) != 1 || !jobs[0].Skipped {
		t.Fatalf("job = %+v, want a loud skip", jobs)
	}
	if !depSkipMarker("agent-b", "20260613-2330-sweep-aaaa") {
		t.Fatal("want a .depskip marker for the closed window")
	}
}

func TestLaunchPersistsClampedStopBeforeRun(t *testing.T) {
	queueEnv(t, "agent-b")
	// A wave minted for 240m whose 03:00 deadline now leaves only ~60: the
	// clamp must land in the .stop sidecar BEFORE rc starts the unit —
	// nightshift-rc arms the auto-stop timer from that file at start time
	// (PR #57 review).
	now := bishD(t, "2026-06-14", "02:00")
	mustSave(t, job{ID: "20260613-2330-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-2 * time.Hour), Created: now.Add(-2 * time.Hour), Deadline: "03:00", Minutes: 240})
	stopAtRun := ""
	prevExec := execCommand
	execCommand = func(name string, args ...string) (string, error) {
		if len(args) >= 2 && args[1] == "run" {
			b, _ := os.ReadFile(filepath.Join(jobsDir("agent-b"), "20260613-2330-sweep-aaaa.stop"))
			stopAtRun = strings.TrimSpace(string(b))
		}
		return "active", nil
	}
	t.Cleanup(func() { execCommand = prevExec })
	schedTick(now)
	v, err := strconv.Atoi(stopAtRun)
	if err != nil || v > 60 || v < runMinutesFloor {
		t.Fatalf(".stop at run time = %q, want the clamped ≤60m window persisted before launch", stopAtRun)
	}
}

func TestWatchdogReleasesDeadUnit(t *testing.T) {
	queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	prevExec := execCommand
	execCommand = func(name string, args ...string) (string, error) { return "inactive", nil }
	t.Cleanup(func() { execCommand = prevExec })
	j := job{ID: "20260613-1300-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-time.Hour), Created: now, Started: true, StartedAt: now.Add(-30 * time.Minute), Minutes: 240}
	mustSave(t, j)
	if !jobWorking("agent-b", j, now) {
		t.Fatal("precondition: job should read as working")
	}
	schedTick(now)
	if !watchdogMarker("agent-b", j.ID) {
		t.Fatal("want a .watchdog marker for the dead unit")
	}
	if jobWorking("agent-b", j, now) {
		t.Fatal("watchdog release must free the cap slot")
	}
	if !jobTerminalNoReport("agent-b", j, now) {
		t.Fatal("a watchdog-released run is terminal-without-report")
	}
}

func TestWatchdogLeavesActiveAndYoungRunsAlone(t *testing.T) {
	queueEnv(t, "agent-b")
	now := bish(t, "14:00")
	probes := 0
	prevExec := execCommand
	execCommand = func(name string, args ...string) (string, error) {
		if len(args) > 0 && args[1] == "run-status" {
			probes++
		}
		return "active", nil
	}
	t.Cleanup(func() { execCommand = prevExec })
	// Too young: never probed.
	mustSave(t, job{ID: "20260613-1355-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-10 * time.Minute), Created: now, Started: true, StartedAt: now.Add(-5 * time.Minute), Minutes: 240})
	// Old enough but ACTIVE: probed, not released; the per-job interval keeps
	// the second tick probe-free.
	mustSave(t, job{ID: "20260613-1300-sweep-bbbb", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-time.Hour), Created: now, Started: true, StartedAt: now.Add(-30 * time.Minute), Minutes: 240})
	schedTick(now)
	schedTick(now.Add(time.Minute))
	if probes != 1 {
		t.Fatalf("run-status probes = %d, want 1 (age gate + per-job interval)", probes)
	}
	if watchdogMarker("agent-b", "20260613-1300-sweep-bbbb") || watchdogMarker("agent-b", "20260613-1355-sweep-aaaa") {
		t.Fatal("no marker for live/young runs")
	}
}

func TestQueueStarvationAlertRaised(t *testing.T) {
	db := obsEnv(t, "agent-b")
	obsDB = db
	t.Cleanup(func() { obsDB = nil })
	calls := queueEnv(t, "agent-b")
	_ = calls
	now := bish(t, "14:00")
	// A working capped session + a same-group candidate stuck past the
	// starvation threshold.
	group := "agent-b/night-x"
	mustSave(t, job{ID: "20260613-1200-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-2 * time.Hour), Created: now, Started: true, StartedAt: now.Add(-90 * time.Minute), Minutes: 240,
		Cap: 1, CapGroup: group})
	mustSave(t, job{ID: "20260613-1250-sweep-bbbb", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-70 * time.Minute), Created: now, Cap: 1, CapGroup: group})
	schedTick(now)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='queue-starvation' AND cleared=0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queue-starvation alerts = %d, want 1", n)
	}
}

// A closed-window skip must emit a structured, greppable transition carrying the
// run's domain identity (run id + executor), so a hollowed sub-tree is
// reconstructable from the journal — the ADR-0020 observability goal, proven by
// capturing the real slog output rather than asserting it.
func TestQueueSkipEmitsStructuredEvent(t *testing.T) {
	queueEnv(t, "agent-b")
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	// 02:00 with a 01:00 profile deadline already gone → a codex wave skips loudly.
	now := bishD(t, "2026-06-14", "02:00")
	mustSave(t, job{ID: "20260613-2330-sweep-aaaa", Agent: "agent-b", Prompt: "p", Kind: "sweep",
		At: now.Add(-2 * time.Hour), Created: now.Add(-2 * time.Hour), Deadline: "01:00", Minutes: 60, Executor: "codex"})
	schedTick(now)
	out := buf.String()
	if !strings.Contains(out, `"msg":"run.skipped"`) || !strings.Contains(out, "20260613-2330-sweep-aaaa") {
		t.Fatalf("want a structured run.skipped event with the run id, got: %s", out)
	}
	if !strings.Contains(out, `"executor":"codex"`) {
		t.Fatalf("skip event must carry the executor for reconstruction, got: %s", out)
	}
}

func TestFlowCapDefaultsToOneAndSerializesParallelGroup(t *testing.T) {
	flowEnv(t)
	watchdogChecked = map[string]time.Time{}
	now := time.Now().Add(-time.Minute)
	f := flow{
		ID: "flow-20260613-1400-abcdef01", Agent: "agent-b", Repo: "example-repo", Goal: "wide work",
		NodeRoles: []string{"refine", "amend", "preview", "validate"},
		Stages:    [][]string{{"refine"}, {"amend", "preview"}, {"validate"}},
		Created:   now, Updated: now, Status: "queued", Batch: "flow-abcdef01", Base: "HEAD",
	}
	if err := prepareFlowWorktree(&f); err != nil {
		t.Fatal(err)
	}
	if err := mintFlow(&f, now); err != nil {
		t.Fatal(err)
	}
	if err := saveFlow(f); err != nil {
		t.Fatal(err)
	}
	byNode := flowJobsByNode(t, f)
	if byNode["refine"].Cap != 1 || byNode["amend"].CapGroup != f.ID {
		t.Fatalf("flow jobs must carry the strict default cap: %+v", byNode["refine"])
	}

	// Deliver refine -> both members ungate; the cap admits ONE.
	schedTick(time.Now()) // launches refine
	writeFlowReport(t, f.Agent, byNode["refine"].ID, "ok")
	tick2 := time.Now().Add(5 * time.Minute)
	schedTick(tick2)
	schedTick(tick2.Add(4 * time.Minute)) // second member's turn — must be held by the cap
	started := 0
	for _, j := range flowJobsByNode(t, f) {
		if (j.NodeID == "amend" || j.NodeID == "preview") && j.Started {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("parallel members started = %d, want 1 under the default cap", started)
	}
}

// writeFlowReport drops a node report with the given verdict.
func writeFlowReport(t *testing.T, agent, jobID, verdict string) {
	t.Helper()
	if err := os.MkdirAll(reportsDir(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "verdict: " + verdict + "\n\nDone.\n"
	if err := os.WriteFile(reportPath(agent, jobID), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
