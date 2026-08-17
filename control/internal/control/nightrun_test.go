package control

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// nightrunEnv points the package globals at a temp home with a fake rc and
// silenced logs; returns the recorded rc calls.
func nightrunEnv(t *testing.T, agents ...string) *[]string {
	t.Helper()
	home = t.TempDir()
	companies = agents
	mintedMem = map[string]string{} // the belt is process-lifetime state; isolate tests
	pipelineWarned = ""             // warn-dedup is process-lifetime state too
	calls := &[]string{}
	prevExec, prevLogf := execCommand, logf
	execCommand = func(name string, args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args[1:], " ")) // drop wrapper path
		return "active", nil
	}
	logf = func(string, ...any) {}
	t.Cleanup(func() { execCommand, logf = prevExec, prevLogf })
	return calls
}

func bish(t *testing.T, hhmm string) time.Time {
	t.Helper()
	return bishD(t, "2026-06-13", hhmm)
}

// bishD is bish with an explicit date — the night pipeline straddles midnight,
// so several tests need consecutive calendar days.
func bishD(t *testing.T, day, hhmm string) time.Time {
	t.Helper()
	ts, err := time.ParseInLocation("2006-01-02 15:04", day+" "+hhmm, bishkek)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestNewRunIDShape(t *testing.T) {
	id := newRunID("sweep", bish(t, "05:00"))
	if !runIDRe.MatchString(id) {
		t.Fatalf("id %q does not match %s", id, runIDRe)
	}
	if !strings.HasPrefix(id, "20260613-0500-sweep-") {
		t.Fatalf("id %q lacks expected prefix", id)
	}
}

func TestJobStoreRoundtrip(t *testing.T) {
	nightrunEnv(t, "agent-a")
	j := job{ID: "20260613-0230-run-ab12", Agent: "agent-a", Prompt: "do things",
		Effort: "medium", Minutes: 45, At: bish(t, "02:30"), Kind: "deferred", Created: bish(t, "01:00")}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	// the launcher-readable prompt file exists and carries the prompt verbatim
	b, err := os.ReadFile(filepath.Join(jobsDir("agent-a"), j.ID+".prompt"))
	if err != nil || string(b) != "do things" {
		t.Fatalf("prompt file = %q, %v", b, err)
	}
	// the rc-readable stop sidecar carries the auto-stop minutes
	b, err = os.ReadFile(filepath.Join(jobsDir("agent-a"), j.ID+".stop"))
	if err != nil || string(b) != "45" {
		t.Fatalf("stop sidecar = %q, %v", b, err)
	}
	// the launcher-readable effort sidecar carries the reasoning effort
	b, err = os.ReadFile(filepath.Join(jobsDir("agent-a"), j.ID+".effort"))
	if err != nil || string(b) != "medium" {
		t.Fatalf("effort sidecar = %q, %v", b, err)
	}
	got := loadJobs("agent-a")
	if len(got) != 1 || got[0].ID != j.ID || !got[0].At.Equal(j.At) || got[0].Minutes != 45 {
		t.Fatalf("loadJobs = %+v", got)
	}
	// zero minutes / empty effort = default: sidecars removed
	j.Minutes = 0
	j.Effort = ""
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(jobsDir("agent-a"), j.ID+".stop")); !os.IsNotExist(err) {
		t.Fatalf("stop sidecar should be gone for Minutes=0: %v", err)
	}
	if _, err := os.Stat(filepath.Join(jobsDir("agent-a"), j.ID+".effort")); !os.IsNotExist(err) {
		t.Fatalf("effort sidecar should be gone for Effort='': %v", err)
	}
	deleteJob("agent-a", j.ID)
	if got := loadJobs("agent-a"); len(got) != 0 {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestSchedTickSweepMintAndStagger(t *testing.T) {
	calls := nightrunEnv(t, "agent-a", "agent-b")
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range companies {
		if err := os.WriteFile(filepath.Join(nsDir(), "sweep", a+".md"), []byte("sweep "+a), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 23:25: agent-a (due 23:20) mints + fires; agent-b (due 23:30) not yet
	schedTick(bish(t, "23:25"))
	if n := len(loadJobs("agent-a")); n != 1 {
		t.Fatalf("agent-a jobs = %d, want 1", n)
	}
	if n := len(loadJobs("agent-b")); n != 0 {
		t.Fatalf("agent-b jobs = %d, want 0 before its stagger slot", n)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "run agent-a ") {
		t.Fatalf("rc calls = %v", *calls)
	}
	if j := loadJobs("agent-a")[0]; !j.Started || j.Kind != "sweep" || j.Prompt != "sweep agent-a" || j.Model != "" || j.Minutes != defaultRunMinutes {
		t.Fatalf("sweep job = %+v", j)
	}

	// same tick again: no duplicate mint, no re-fire
	schedTick(bish(t, "23:26"))
	if n := len(loadJobs("agent-a")); n != 1 {
		t.Fatalf("duplicate sweep minted: %d jobs", n)
	}
	if len(*calls) != 1 {
		t.Fatalf("re-fired started job: %v", *calls)
	}

	// 23:35: agent-b's slot
	schedTick(bish(t, "23:35"))
	if n := len(loadJobs("agent-b")); n != 1 {
		t.Fatalf("agent-b jobs = %d, want 1", n)
	}
}

// playground runs the two-lane night pipeline (ADR-0008): medic, steward, scout
// before midnight, the two planners around it, exec workers after, review/synth/
// retro in the morning. Every wave is Opus; reasoning effort is the per-wave
// lever, carried to the launcher as an <id>.effort sidecar.
func TestSchedTickPlaygroundSwarmWaves(t *testing.T) {
	nightrunEnv(t, "agent-a", "agent-b", "playground")
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"agent-a.md", "agent-b.md", "playground-medic.md", "playground-steward.md",
		"playground-scout.md", "playground-plan-projects.md", "playground-plan-products.md",
		"playground-exec.md", "playground-review.md", "playground-harvest.md", "playground-synth.md",
		"playground-retro.md"} {
		if err := os.WriteFile(filepath.Join(nsDir(), "sweep", f), []byte("preamble "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	find := func(label string) *job {
		for _, j := range loadJobs("playground") {
			if j.Label == label {
				return &j
			}
		}
		return nil
	}

	// 23:05: only the medic pre-flight is due (opus, medium effort, 50m stop);
	// the rest of the pipeline and the company sweeps at 23:20/23:30 come later.
	schedTick(bish(t, "23:05"))
	pj := loadJobs("playground")
	if len(pj) != 1 || pj[0].Label != "medic" || pj[0].Model != opusModel || pj[0].Minutes != 50 ||
		pj[0].Effort != "medium" || pj[0].Prompt != "preamble playground-medic.md" {
		t.Fatalf("medic job = %+v", pj)
	}
	if b, err := os.ReadFile(filepath.Join(jobsDir("playground"), pj[0].ID+".stop")); err != nil || string(b) != "50" {
		t.Fatalf("medic stop sidecar = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(jobsDir("playground"), pj[0].ID+".effort")); err != nil || string(b) != "medium" {
		t.Fatalf("medic effort sidecar = %q, %v", b, err)
	}

	// 23:12: steward (Lane B reconciler; opus, high, 45m). It gets the open-tickets
	// section — the board is empty here so none is appended.
	schedTick(bish(t, "23:12"))
	if j := find("steward"); j == nil || j.Model != opusModel || j.Effort != "high" || j.Minutes != 45 {
		t.Fatalf("steward job = %+v", j)
	}

	// 23:20: scout — the one xhigh strategy wave (opus, xhigh, 90m), no tickets.
	// Its opus model still writes a launcher-readable model sidecar.
	schedTick(bish(t, "23:20"))
	scoutJob := find("scout")
	if scoutJob == nil || scoutJob.Model != opusModel || scoutJob.Effort != "xhigh" ||
		scoutJob.Prompt != "preamble playground-scout.md" || scoutJob.Minutes != 90 {
		t.Fatalf("scout job = %+v", scoutJob)
	}
	if b, err := os.ReadFile(filepath.Join(jobsDir("playground"), scoutJob.ID+".model")); err != nil || string(b) != opusModel {
		t.Fatalf("model sidecar = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(jobsDir("playground"), scoutJob.ID+".effort")); err != nil || string(b) != "xhigh" {
		t.Fatalf("scout effort sidecar = %q, %v", b, err)
	}

	// 23:58: plan-projects (Lane B planner; opus, high, 60m)
	schedTick(bish(t, "23:58"))
	if j := find("plan-B"); j == nil || j.Model != opusModel || j.Effort != "high" || j.Minutes != 60 ||
		j.Prompt != "preamble playground-plan-projects.md" {
		t.Fatalf("plan-B job = %+v", j)
	}
	if cfg := loadConfig(); cfg.LastSweep["playground/plan-projects"] != "2026-06-13" {
		t.Fatalf("lastSweep after plan-projects = %+v", cfg.LastSweep)
	}

	// past midnight: 00:05 next day — plan-products (Lane A planner; opus, high,
	// 60m, no ticket section). (The 23:05 tick already grace-marked the
	// post-midnight slots for the 13th, so they fire fresh on the 14th.)
	schedTick(bishD(t, "2026-06-14", "00:05"))
	if j := find("plan-A"); j == nil || j.Model != opusModel || j.Effort != "high" || j.Minutes != 60 ||
		j.Prompt != "preamble playground-plan-products.md" {
		t.Fatalf("plan-A job = %+v", j)
	}
	if cfg := loadConfig(); cfg.LastSweep["playground/plan-products"] != "2026-06-14" {
		t.Fatalf("lastSweep after plan-products = %+v", cfg.LastSweep)
	}

	// the plan-B wave filed two open improve tickets (the handoff to the exec wave)
	for _, tk := range []ticket{
		{ID: "20260613-2355-tkt-aa11", Agent: "playground", Title: "big feature", Body: "do the thing", Status: "open", Lane: "improve", CreatedBy: "playground", Created: bish(t, "23:55"), Updated: bish(t, "23:55")},
		{ID: "20260613-2356-tkt-bb22", Agent: "playground", Title: "visual pass", Body: "make it pretty", Status: "open", Lane: "improve", CreatedBy: "playground", Created: bish(t, "23:56"), Updated: bish(t, "23:56")},
	} {
		if err := saveTicket(tk); err != nil {
			t.Fatal(err)
		}
	}

	// 00:50: exec wave — one xhigh Opus job per open ticket (2), 7h stop each,
	// each prompt carrying its lane; every remaining slot fills with a
	// self-directed worker.
	schedTick(bishD(t, "2026-06-14", "00:50"))
	execTicket, execSelf := 0, 0
	for _, j := range loadJobs("playground") {
		if !strings.HasPrefix(j.Label, "exec · ") {
			continue
		}
		if j.Label == "exec · self-directed" {
			execSelf++
			continue
		}
		execTicket++
		if j.Model != opusModel || j.Effort != "xhigh" || j.Minutes != 420 ||
			!strings.HasPrefix(j.Prompt, "preamble playground-exec.md") ||
			!strings.Contains(j.Prompt, "## Your ticket") || !strings.Contains(j.Prompt, "**Lane:** improve") {
			t.Fatalf("exec job malformed: %+v", j)
		}
	}
	if execTicket != 2 || execSelf != maxExecWorkers-2 {
		t.Fatalf("opus exec jobs = %d ticket + %d self-directed, want 2 + %d", execTicket, execSelf, maxExecWorkers-2)
	}

	// morning: 06:50 review, 07:45 harvest, 08:05 synth, 08:10 retro — all
	// short-stopped so nothing burns Opus past the operator's 09:00 window.
	// review runs on the codex executor (ADR-0018): GPT-5.6 Sol at xhigh, with
	// the .executor sidecar persisted for the launcher's dispatch.
	schedTick(bishD(t, "2026-06-14", "06:50"))
	if j := find("review"); j == nil || j.Model != codexModel || j.Effort != "xhigh" || j.Minutes != 105 || j.Executor != "codex" {
		t.Fatalf("review job = %+v", j)
	} else if b, err := os.ReadFile(filepath.Join(jobsDir("playground"), j.ID+".executor")); err != nil || string(b) != "codex" {
		t.Fatalf("review .executor sidecar = %q err=%v", b, err)
	}
	// harvest refreshes experiment numbers before synth; noTickets, so the open
	// tickets on the board must NOT be appended to its prompt.
	schedTick(bishD(t, "2026-06-14", "07:45"))
	if j := find("harvest"); j == nil || j.Model != opusModel || j.Effort != "medium" || j.Minutes != 25 ||
		j.Prompt != "preamble playground-harvest.md" {
		t.Fatalf("harvest job = %+v", j)
	}
	if cfg := loadConfig(); cfg.LastSweep["playground/harvest"] != "2026-06-14" {
		t.Fatalf("lastSweep after harvest = %+v", cfg.LastSweep)
	}
	schedTick(bishD(t, "2026-06-14", "08:05"))
	if j := find("synth"); j == nil || j.Model != opusModel || j.Effort != "medium" || j.Minutes != 45 {
		t.Fatalf("synth job = %+v", j)
	}
	schedTick(bishD(t, "2026-06-14", "08:10"))
	if j := find("retro"); j == nil || j.Model != opusModel || j.Effort != "xhigh" || j.Minutes != 42 {
		t.Fatalf("retro job = %+v", j)
	}

	// every slot mints once per day: a later tick adds nothing
	before := len(loadJobs("playground"))
	schedTick(bishD(t, "2026-06-14", "08:30"))
	if n := len(loadJobs("playground")); n != before {
		t.Fatalf("re-minted: %d jobs, want %d", n, before)
	}
}

// launchGap keeps two runs from starting within launchGap of each other, box-
// wide — the shared-OAuth-creds guard (2026-07-05). Two jobs due at the same
// instant fire one at a time: one now, the second only once the gap has passed.
func TestSchedTickLaunchGap(t *testing.T) {
	calls := nightrunEnv(t, "agent-a")
	now := bish(t, "22:00") // before any sweep slot (23:20+): only our jobs fire
	for _, id := range []string{"20260613-2200-run-aa11", "20260613-2200-run-bb22"} {
		if err := saveJob(job{ID: id, Agent: "agent-a", Prompt: "p", At: now, Kind: "deferred", Created: now}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		n := 0
		for _, c := range *calls {
			if strings.HasPrefix(c, "run agent-a ") {
				n++
			}
		}
		return n
	}
	// first tick: only ONE of the two due jobs launches (the gap blocks the second)
	schedTick(now)
	if count() != 1 {
		t.Fatalf("first tick: %d runs, want 1 (launchGap)", count())
	}
	// a tick still within the gap launches nothing more
	schedTick(now.Add(launchGap - time.Second))
	if count() != 1 {
		t.Fatalf("within gap: %d runs total, want 1", count())
	}
	// past the gap, the second job launches
	schedTick(now.Add(launchGap + time.Second))
	if count() != 2 {
		t.Fatalf("past gap: %d runs total, want 2", count())
	}
}

// execSlot returns playground's perTicket exec slot with a "PRE" preamble on
// disk, in a fresh temp HOME — the common setup for the exec-dispatch tests.
func execSlot(t *testing.T) sweepSlot {
	t.Helper()
	nightrunEnv(t, "playground")
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nsDir(), "sweep", "playground-exec.md"), []byte("PRE"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range sweepSlots("playground", 0) {
		if s.perTicket {
			return s
		}
	}
	t.Fatal("no perTicket exec slot")
	return sweepSlot{}
}

// mkExecTicket files an open playground ticket in the given lane, Created==Updated.
func mkExecTicket(t *testing.T, id, lane string, created time.Time) {
	t.Helper()
	if err := saveTicket(ticket{ID: id, Agent: "playground", Title: lane + " " + id, Body: "b",
		Status: "open", Lane: lane, CreatedBy: "playground", Created: created, Updated: created}); err != nil {
		t.Fatal(err)
	}
}

// execLaneCounts tallies the dispatched jobs by lane (from the prompt) plus any
// self-directed top-up.
func execLaneCounts(jobs []job) (hunt, improve, ops, self int) {
	for _, j := range jobs {
		switch {
		case j.Label == "exec · self-directed":
			self++
		case strings.Contains(j.Prompt, "**Lane:** hunt"):
			hunt++
		case strings.Contains(j.Prompt, "**Lane:** ops"):
			ops++
		case strings.Contains(j.Prompt, "**Lane:** improve"):
			improve++
		}
	}
	return
}

// execWorkerJobs buckets open tickets by lane, dispatches the per-lane budget
// (3 hunt + 3 improve), and staggers workers; a full 6-ticket queue fills every
// slot with no self-directed top-up.
func TestExecWorkerJobsLanesCapAndFallback(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")

	// empty queue -> EVERY slot fills with a self-directed session
	fb, _ := execWorkerJobs("playground", now, slot, "", "")
	if len(fb) != maxExecWorkers {
		t.Fatalf("empty queue = %d jobs, want %d", len(fb), maxExecWorkers)
	}
	for _, j := range fb {
		if j.Label != "exec · self-directed" || j.Model != opusModel || strings.Contains(j.Prompt, "## Your ticket") {
			t.Fatalf("fallback job = %+v", j)
		}
	}

	// 5 hunt + 5 improve open tickets -> capped to 3 + 3 = maxExecWorkers, no top-up
	for i := range 5 {
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-h%02d", i), "hunt", now)
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-i%02d", i), "improve", now)
	}

	got, _ := execWorkerJobs("playground", now, slot, "", "")
	if len(got) != maxExecWorkers {
		t.Fatalf("cap: got %d jobs, want %d", len(got), maxExecWorkers)
	}
	seen := map[string]bool{}
	for i, j := range got {
		if j.Model != opusModel || !strings.HasPrefix(j.Prompt, "PRE") || !strings.Contains(j.Prompt, "## Your ticket") {
			t.Fatalf("worker job malformed: %+v", j)
		}
		// workers are staggered so their OAuth refreshes don't collide
		if want := now.Add(time.Duration(i) * execWorkerStagger); !j.At.Equal(want) {
			t.Errorf("worker %d At = %v, want %v (staggered)", i, j.At, want)
		}
		if seen[j.ID] {
			t.Errorf("duplicate run id %s", j.ID)
		}
		seen[j.ID] = true
	}
	hunt, improve, ops, self := execLaneCounts(got)
	if hunt != execHuntWorkers || improve != execImproveWorkers || ops != 0 || self != 0 {
		t.Fatalf("lane split = %d hunt / %d improve / %d ops / %d self, want %d / %d / 0 / 0",
			hunt, improve, ops, self, execHuntWorkers, execImproveWorkers)
	}
}

// ops tickets (medic-filed box hygiene) never consume a feature budget: they
// only fill slots left over after both feature lanes and cross-lane backfill.
func TestExecWorkerJobsOpsFillLeftoverOnly(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")

	// full feature lanes (2+2 here, budgets 3/3 so both fit) + 4 ops -> ops only
	// gets the 2 leftover slots, not all 4.
	for i := range 2 {
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-h%02d", i), "hunt", now)
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-i%02d", i), "improve", now)
	}
	for i := range 4 {
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-o%02d", i), "ops", now)
	}

	got, _ := execWorkerJobs("playground", now, slot, "", "")
	if len(got) != maxExecWorkers {
		t.Fatalf("got %d jobs, want %d", len(got), maxExecWorkers)
	}
	hunt, improve, ops, self := execLaneCounts(got)
	if hunt != 2 || improve != 2 || ops != 2 || self != 0 {
		t.Fatalf("split = %d hunt / %d improve / %d ops / %d self, want 2 / 2 / 2 / 0", hunt, improve, ops, self)
	}
	// and the ops label reads "exec · ops · <title>"
	for _, j := range got {
		if strings.Contains(j.Prompt, "**Lane:** ops") && !strings.HasPrefix(j.Label, "exec · ops · ") {
			t.Fatalf("ops label = %q", j.Label)
		}
	}
}

// within a lane, tonight's freshly-planned tickets dispatch before older backlog
// so a stale ticket can't preempt the plan.
func TestExecWorkerJobsTonightBeatsBacklog(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")

	// one improve lane: a fresh (tonight) ticket + a much older backlog ticket.
	mkExecTicket(t, "20260613-0050-tkt-old0", "improve", now.Add(-10*time.Hour)) // backlog
	mkExecTicket(t, "20260613-0050-tkt-new0", "improve", now.Add(-1*time.Hour))  // tonight

	got, _ := execWorkerJobs("playground", now, slot, "", "")
	// both fit under the improve budget, but tonight dispatches first
	newIdx, oldIdx := -1, -1
	for i, j := range got {
		if strings.Contains(j.Prompt, "20260613-0050-tkt-new0") {
			newIdx = i
		}
		if strings.Contains(j.Prompt, "20260613-0050-tkt-old0") {
			oldIdx = i
		}
	}
	if newIdx < 0 || oldIdx < 0 || newIdx > oldIdx {
		t.Fatalf("tonight-first violated: newIdx=%d oldIdx=%d", newIdx, oldIdx)
	}
}

// a thin lane's unused slots backfill from the other lane's overflow.
func TestExecWorkerJobsCrossLaneBackfill(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")

	// 1 hunt + 5 improve: hunt takes 1, improve takes its 3 budget, then the 2
	// leftover hunt slots backfill from improve overflow -> 5 improve dispatched.
	mkExecTicket(t, "20260613-0050-tkt-h000", "hunt", now)
	for i := range 5 {
		mkExecTicket(t, fmt.Sprintf("20260613-0050-tkt-i%02d", i), "improve", now)
	}

	got, _ := execWorkerJobs("playground", now, slot, "", "")
	if len(got) != maxExecWorkers {
		t.Fatalf("got %d jobs, want %d", len(got), maxExecWorkers)
	}
	hunt, improve, ops, self := execLaneCounts(got)
	if hunt != 1 || improve != 5 || ops != 0 || self != 0 {
		t.Fatalf("backfill split = %d hunt / %d improve / %d ops / %d self, want 1 / 5 / 0 / 0", hunt, improve, ops, self)
	}
}

// a ticket with a fresh claim sidecar is skipped; a stale claim is ignored.
func TestExecWorkerJobsSkipsClaimed(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")
	mkExecTicket(t, "20260613-0050-tkt-clm0", "improve", now) // fresh-claimed below
	mkExecTicket(t, "20260613-0050-tkt-opn0", "improve", now) // free
	mkExecTicket(t, "20260613-0050-tkt-stl0", "improve", now) // stale claim -> still dispatched

	fresh := filepath.Join(ticketsDir("playground"), "20260613-0050-tkt-clm0.claim")
	if err := os.WriteFile(fresh, []byte("worker"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(ticketsDir("playground"), "20260613-0050-tkt-stl0.claim")
	if err := os.WriteFile(stale, []byte("worker"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-ticketClaimTTL - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	got, _ := execWorkerJobs("playground", now, slot, "", "")
	for _, j := range got {
		if strings.Contains(j.Prompt, "20260613-0050-tkt-clm0") {
			t.Fatalf("fresh-claimed ticket dispatched: %+v", j)
		}
	}
	// the free and stale-claimed tickets both dispatch (+ self-directed fill)
	if _, improve, _, self := execLaneCounts(got); improve != 2 || self != maxExecWorkers-2 {
		t.Fatalf("claimed-skip split: %d improve / %d self, want 2 / %d", improve, self, maxExecWorkers-2)
	}
}

// every dispatched ticket gets a pre-claim sidecar so a pull-looping worker from
// an earlier slot can't grab it out from under a staggered-later worker. The
// claim is written by mintExecJobs only after the job's save succeeded.
func TestExecWorkerJobsPreClaims(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")
	ids := []string{"20260613-0050-tkt-pc00", "20260613-0050-tkt-pc01"}
	for _, id := range ids {
		mkExecTicket(t, id, "improve", now)
	}

	mintExecJobs("playground", now, slot, "", "")
	for _, id := range ids {
		b, err := os.ReadFile(filepath.Join(ticketsDir("playground"), id+".claim"))
		if err != nil {
			t.Fatalf("no pre-claim for %s: %v", id, err)
		}
		if !strings.HasPrefix(string(b), "sched:") {
			t.Fatalf("claim for %s = %q, want sched: prefix", id, b)
		}
		if !ticketClaimed("playground", id, now) {
			t.Fatalf("pre-claim for %s not seen fresh", id)
		}
	}
	// and the claimed jobs actually persisted
	if n := len(loadJobs("playground")); n != maxExecWorkers {
		t.Fatalf("minted %d jobs, want %d", n, maxExecWorkers)
	}
}

// a failed saveJob must not leave the ticket claim-suppressed for 8h with no
// worker coming: the claim is written only after the job persists.
func TestMintExecJobsNoOrphanClaimOnSaveFailure(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")
	mkExecTicket(t, "20260613-0050-tkt-orph", "improve", now)

	// block saveJob: jobs/ as a regular FILE makes MkdirAll(jobs/playground) fail
	if err := os.WriteFile(filepath.Join(nsDir(), "jobs"), []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	mintExecJobs("playground", now, slot, "", "")
	if _, err := os.Stat(filepath.Join(ticketsDir("playground"), "20260613-0050-tkt-orph.claim")); !os.IsNotExist(err) {
		t.Fatalf("orphaned claim written despite save failure: %v", err)
	}
	if ticketClaimed("playground", "20260613-0050-tkt-orph", now) {
		t.Fatal("ticket reads claimed although no worker job exists")
	}

	// unblock: the next mint saves AND claims
	if err := os.Remove(filepath.Join(nsDir(), "jobs")); err != nil {
		t.Fatal(err)
	}
	mintExecJobs("playground", now, slot, "", "")
	if !ticketClaimed("playground", "20260613-0050-tkt-orph", now) {
		t.Fatal("ticket not claimed after a successful save")
	}
	if n := len(loadJobs("playground")); n != maxExecWorkers {
		t.Fatalf("minted %d jobs after unblock, want %d", n, maxExecWorkers)
	}
}

// every idle slot fills with a self-directed worker, staggered like ticketed
// ones — a thin queue must not leave Opus slots dark.
func TestExecWorkerJobsSelfDirectedFillsAllSlots(t *testing.T) {
	slot := execSlot(t)
	now := bish(t, "00:50")

	// zero tickets -> ALL slots self-directed, staggered
	got, _ := execWorkerJobs("playground", now, slot, "", "")
	if len(got) != maxExecWorkers {
		t.Fatalf("zero-ticket = %d jobs, want %d", len(got), maxExecWorkers)
	}
	if _, _, _, self := execLaneCounts(got); self != maxExecWorkers {
		t.Fatalf("zero-ticket self = %d, want %d", self, maxExecWorkers)
	}
	seen := map[string]bool{}
	for i, j := range got {
		if want := now.Add(time.Duration(i) * execWorkerStagger); !j.At.Equal(want) {
			t.Errorf("self-directed %d At = %v, want %v (staggered)", i, j.At, want)
		}
		if seen[j.ID] {
			t.Errorf("duplicate run id %s", j.ID)
		}
		seen[j.ID] = true
	}

	// a thin queue (1 ticket) -> 1 worker + self-directed in every other slot
	mkExecTicket(t, "20260613-0050-tkt-thin", "improve", now)
	got, _ = execWorkerJobs("playground", now, slot, "", "")
	if _, improve, _, self := execLaneCounts(got); improve != 1 || self != maxExecWorkers-1 || len(got) != maxExecWorkers {
		t.Fatalf("thin queue: %d jobs (%d improve / %d self), want %d (1 / %d)",
			len(got), improve, self, maxExecWorkers, maxExecWorkers-1)
	}
}

// a valid pipeline.json replaces playground's built-in schedule: names map to
// labels + "playground/<name>" dedup keys, times parse as Bishkek wall clock,
// minutes clamp to the rc-valid [10,480] range. The file is re-read on every
// sweepSlots call, so operator edits apply without a restart.
func TestPipelineFileOverridesDefaults(t *testing.T) {
	nightrunEnv(t, "playground")
	writeFile(t, pipelinePath(), `{"slots":[
		{"name":"medic","time":"23:00","prompt":"playground-medic.md","model":"claude-opus-5","effort":"medium","minutes":50,"noTickets":true},
		{"name":"exec","time":"00:45","prompt":"playground-exec.md","effort":"xhigh","minutes":9999,"perTicket":true}
	]}`)

	got := sweepSlots("playground", 0)
	if len(got) != 2 {
		t.Fatalf("pipeline slots = %d, want 2: %+v", len(got), got)
	}
	m := got[0]
	if m.key != "playground/medic" || m.hour != 23 || m.minute != 0 || m.label != "medic" ||
		m.prompt != "playground-medic.md" || m.model != opusModel || m.effort != "medium" ||
		m.minutes != 50 || !m.noTickets || m.perTicket {
		t.Fatalf("medic slot = %+v", m)
	}
	e := got[1]
	if e.key != "playground/exec" || e.hour != 0 || e.minute != 45 || !e.perTicket ||
		e.model != "" || e.minutes != 480 { // 9999 clamped to the rc ceiling
		t.Fatalf("exec slot = %+v", e)
	}
	// company agents are untouched by the override
	if s := sweepSlots("agent-a", 0); len(s) != 1 || s[0].key != "agent-a.2" {
		t.Fatalf("company slots affected by pipeline.json: %+v", s)
	}

	// re-read each call: an edit applies immediately
	writeFile(t, pipelinePath(), `{"slots":[{"name":"solo","time":"01:30","prompt":"p.md","minutes":5}]}`)
	got = sweepSlots("playground", 0)
	if len(got) != 1 || got[0].key != "playground/solo" || got[0].minutes != 10 { // 5 clamped to the floor
		t.Fatalf("re-read slots = %+v", got)
	}

	// removing the file restores the built-ins
	if err := os.Remove(pipelinePath()); err != nil {
		t.Fatal(err)
	}
	if got := sweepSlots("playground", 0); len(got) != len(playgroundDefaultSlots()) {
		t.Fatalf("defaults not restored after remove: %d slots", len(got))
	}
}

// an absent or invalid pipeline.json falls back to the built-ins — a typo must
// never produce a dead night. Every invalid shape rejects the WHOLE file.
func TestPipelineFileInvalidFallsBack(t *testing.T) {
	nightrunEnv(t, "playground")
	warned := 0
	logf = func(string, ...any) { warned++ }

	defaults := playgroundDefaultSlots()
	check := func(name, body string, wantWarn bool) {
		t.Helper()
		pipelineWarned = ""
		before := warned
		if body != "" {
			writeFile(t, pipelinePath(), body)
		} else {
			os.Remove(pipelinePath())
		}
		got := sweepSlots("playground", 0)
		if len(got) != len(defaults) || got[0].key != defaults[0].key {
			t.Fatalf("%s: fallback not the built-ins: %+v", name, got)
		}
		if gotWarn := warned > before; gotWarn != wantWarn {
			t.Fatalf("%s: warned=%v, want %v", name, gotWarn, wantWarn)
		}
	}

	check("absent", "", false) // the normal case — silent
	check("bad json", `{"slots":[`, true)
	check("no slots", `{"slots":[]}`, true)
	check("bad effort", `{"slots":[{"name":"a","time":"23:00","prompt":"a.md","effort":"turbo","minutes":30}]}`, true)
	check("bad time", `{"slots":[{"name":"a","time":"23h00","prompt":"a.md","minutes":30}]}`, true)
	check("dup names", `{"slots":[{"name":"a","time":"23:00","prompt":"a.md","minutes":30},{"name":"a","time":"23:30","prompt":"b.md","minutes":30}]}`, true)
	check("no prompt", `{"slots":[{"name":"a","time":"23:00","minutes":30}]}`, true)
	check("no minutes", `{"slots":[{"name":"a","time":"23:00","prompt":"a.md"}]}`, true)

	// the warning is deduped: the same standing typo doesn't log every tick
	pipelineWarned = ""
	writeFile(t, pipelinePath(), `{"slots":[`)
	before := warned
	sweepSlots("playground", 0)
	sweepSlots("playground", 0)
	if warned != before+1 {
		t.Fatalf("bad-config warning logged %d times, want 1 (deduped)", warned-before)
	}
}

// the built-in defaults carry the harvest wave: 07:40, medium, 25m, noTickets,
// before synth so the morning brief reads fresh experiment numbers.
func TestPlaygroundDefaultsIncludeHarvest(t *testing.T) {
	slots := playgroundDefaultSlots()
	hIdx, sIdx := -1, -1
	for i, s := range slots {
		switch s.key {
		case "playground/harvest":
			hIdx = i
			if s.hour != 7 || s.minute != 40 || s.effort != "medium" || s.minutes != 25 ||
				s.prompt != "playground-harvest.md" || s.label != "harvest" || !s.noTickets || s.perTicket {
				t.Fatalf("harvest slot = %+v", s)
			}
		case "playground/synth":
			sIdx = i
		}
	}
	if hIdx < 0 || sIdx < 0 || hIdx > sIdx {
		t.Fatalf("harvest missing or after synth: harvest=%d synth=%d", hIdx, sIdx)
	}
}

func TestSchedTickSweepOffAndGrace(t *testing.T) {
	calls := nightrunEnv(t, "agent-a", "playground")
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nsDir(), "sweep", "agent-a.md"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(nightConfig{SweepOff: map[string]bool{"agent-a": true, "playground": true}, LastSweep: map[string]string{}}); err != nil {
		t.Fatal(err)
	}

	// 05:00: playground's post-midnight slots (plan-products 00:00, exec 00:45)
	// are due but sweep-off eats them; agent-a's 23:20 slot isn't due yet.
	schedTick(bish(t, "05:00"))
	if n := len(loadJobs("agent-a")) + len(loadJobs("playground")); n != 0 {
		t.Fatalf("jobs minted despite off: %d", n)
	}
	cfg := loadConfig()
	// agent-a.2 carries YESTERDAY's mark now: the downtime-resilient scheduler
	// also evaluates (and here, sweep-off-marks) the previous night's due.
	if cfg.LastSweep["playground/plan-products"] != "2026-06-13" || cfg.LastSweep["agent-a.2"] != "2026-06-12" {
		t.Fatalf("lastSweep = %+v", cfg.LastSweep)
	}

	// playground back on; 06:00 is past research+exec's 4h grace (skip today,
	// marked), and review (06:45) isn't due yet. Wiping LastSweep only happens
	// with a restart in prod, so clear the in-memory belt to match.
	if err := saveConfig(nightConfig{SweepOff: map[string]bool{"agent-a": true}, LastSweep: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	mintedMem = map[string]string{}
	schedTick(bish(t, "06:00"))
	if n := len(loadJobs("playground")); n != 0 {
		t.Fatalf("jobs minted despite grace: %d", n)
	}
	cfg = loadConfig()
	// exec: today marked (grace-skipped). review: only yesterday's night is
	// marked — today's 06:45 hasn't come, so tonight's review can still fire.
	if cfg.LastSweep["playground/exec"] != "2026-06-13" || cfg.LastSweep["playground/review"] != "2026-06-12" {
		t.Fatalf("lastSweep after grace = %+v", cfg.LastSweep)
	}
	if len(*calls) != 0 {
		t.Fatalf("rc calls = %v", *calls)
	}
}

func TestSchedTickFiresDueDeferred(t *testing.T) {
	calls := nightrunEnv(t, "agent-a")
	future := job{ID: "20260613-2300-run-aaaa", Agent: "agent-a", Prompt: "later",
		At: bish(t, "23:00"), Kind: "deferred", Created: bish(t, "01:00")}
	due := job{ID: "20260613-0130-run-bbbb", Agent: "agent-a", Prompt: "now",
		At: bish(t, "01:30"), Kind: "deferred", Created: bish(t, "01:00")}
	for _, j := range []job{future, due} {
		if err := saveJob(j); err != nil {
			t.Fatal(err)
		}
	}
	schedTick(bish(t, "02:00"))
	if want := "run agent-a " + due.ID; len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("rc calls = %v, want [%s]", *calls, want)
	}
	for _, j := range loadJobs("agent-a") {
		if j.ID == due.ID && !j.Started {
			t.Fatal("due job not marked started")
		}
		if j.ID == future.ID && j.Started {
			t.Fatal("future job fired early")
		}
	}
}

func TestSchedTickRcFailureRetries(t *testing.T) {
	nightrunEnv(t, "agent-a")
	execCommand = func(string, ...string) (string, error) { return "boom", fmt.Errorf("rc failed") }
	j := job{ID: "20260613-0130-run-cccc", Agent: "agent-a", Prompt: "x",
		At: bish(t, "01:30"), Kind: "deferred", Created: bish(t, "01:00")}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	schedTick(bish(t, "02:00"))
	if loadJobs("agent-a")[0].Started {
		t.Fatal("job marked started although rc failed")
	}
}

func TestReportHandlerAllowlist(t *testing.T) {
	nightrunEnv(t, "agent-a")
	if err := os.MkdirAll(reportsDir("agent-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportsDir("agent-a"), "20260613-0500-sweep-ab12.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a secret OUTSIDE the reports tree that traversal must never reach
	if err := os.WriteFile(filepath.Join(home, "secret.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleReport(w, httptest.NewRequest("GET", "/api/report?"+q, nil))
		return w
	}
	if w := get("c=agent-a&id=20260613-0500-sweep-ab12"); w.Code != 200 || w.Body.String() != "# hi" {
		t.Fatalf("valid report: %d %q", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		"c=agent-a&id=../../secret",
		"c=agent-a&id=..%2F..%2Fsecret",
		"c=evil&id=20260613-0500-sweep-ab12",
		"c=agent-a&id=UPPER",
		"c=agent-a&id=",
	} {
		if w := get(bad); w.Code == 200 {
			t.Errorf("%q must not resolve, got 200: %q", bad, w.Body.String())
		}
	}

	// listing only surfaces valid run-id .md files
	w := httptest.NewRecorder()
	handleReports(w, httptest.NewRequest("GET", "/api/reports?c=agent-a", nil))
	var metas []reportMeta
	if err := json.Unmarshal(w.Body.Bytes(), &metas); err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != "20260613-0500-sweep-ab12" {
		t.Fatalf("reports = %+v", metas)
	}
}

func TestJobCreateAndCancelHandlers(t *testing.T) {
	nightrunEnv(t, "agent-a")

	w := httptest.NewRecorder()
	body := strings.NewReader(`{"agent":"agent-a","prompt":"fix the tests","at":"2026-06-14T02:30"}`)
	handleJobCreate(w, httptest.NewRequest("POST", "/api/job", body))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var j job
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if want := bish(t, "02:30").Add(24 * time.Hour); !j.At.Equal(want) {
		t.Fatalf("at = %v, want %v (parsed as Bishkek wall clock)", j.At, want)
	}

	// bad inputs rejected
	for _, bad := range []string{
		`{"agent":"evil","prompt":"x","at":"2026-06-14T02:30"}`,
		`{"agent":"agent-a","prompt":"","at":"2026-06-14T02:30"}`,
		`{"agent":"agent-a","prompt":"x","at":"tonight"}`,
	} {
		w := httptest.NewRecorder()
		handleJobCreate(w, httptest.NewRequest("POST", "/api/job", strings.NewReader(bad)))
		if w.Code == 200 {
			t.Errorf("bad job %s accepted", bad)
		}
	}

	// cancel works while scheduled, refuses once started
	w = httptest.NewRecorder()
	handleJobCancel(w, httptest.NewRequest("POST", "/api/job/cancel?c=agent-a&id="+j.ID, nil))
	if w.Code != 200 || len(loadJobs("agent-a")) != 0 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	started := job{ID: "20260613-0100-run-dddd", Agent: "agent-a", Prompt: "x",
		At: bish(t, "01:00"), Kind: "deferred", Created: bish(t, "00:30"), Started: true}
	if err := saveJob(started); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handleJobCancel(w, httptest.NewRequest("POST", "/api/job/cancel?c=agent-a&id="+started.ID, nil))
	if w.Code != 409 {
		t.Fatalf("cancel of started job: %d, want 409", w.Code)
	}
}

func TestReportBanner(t *testing.T) {
	nightrunEnv(t, "agent-a")
	if err := os.MkdirAll(reportsDir("agent-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	withBanner, plain := "20260703-0500-sweep-ab12", "20260703-0510-sweep-cd34"
	for _, id := range []string{withBanner, plain} {
		if err := os.WriteFile(filepath.Join(reportsDir("agent-a"), id+".md"), []byte("# hi"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	png := []byte("\x89PNG-fake-bytes")
	if err := os.WriteFile(filepath.Join(reportsDir("agent-a"), withBanner+".png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	// a png OUTSIDE the reports tree that traversal must never reach
	if err := os.WriteFile(filepath.Join(home, "secret.png"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	// listing carries the banner flag, and the .png itself is not listed as a report
	w := httptest.NewRecorder()
	handleReports(w, httptest.NewRequest("GET", "/api/reports?c=agent-a", nil))
	var metas []reportMeta
	if err := json.Unmarshal(w.Body.Bytes(), &metas); err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, m := range metas {
		flags[m.ID] = m.Banner
	}
	if len(metas) != 2 || !flags[withBanner] || flags[plain] {
		t.Fatalf("metas = %+v", metas)
	}

	get := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleReportBanner(w, httptest.NewRequest("GET", "/api/report/banner?"+q, nil))
		return w
	}
	if w := get("c=agent-a&id=" + withBanner); w.Code != 200 ||
		w.Header().Get("Content-Type") != "image/png" || w.Body.String() != string(png) {
		t.Fatalf("banner fetch: %d %s %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w := get("c=agent-a&id=" + plain); w.Code != 404 {
		t.Fatalf("missing banner: %d, want 404", w.Code)
	}
	for _, bad := range []string{
		"c=agent-a&id=../secret",
		"c=agent-a&id=..%2Fsecret",
		"c=evil&id=" + withBanner,
		"c=agent-a&id=",
	} {
		if w := get(bad); w.Code == 200 {
			t.Errorf("%q must not resolve, got 200", bad)
		}
	}
}

// A pre-midnight slot missed during downtime still mints after midnight (inside
// its grace), with minutes clamped to the original end time — and never twice.
func TestSchedTickMintsMissedPreMidnightSlot(t *testing.T) {
	nightrunEnv(t, "agent-a")
	writeFile(t, filepath.Join(nsDir(), "sweep", "agent-a.md"), "sweep agent-a")

	// box was down 22:50 -> 00:10; agent-a's 23:20 slot belongs to 2026-06-12
	schedTick(bishD(t, "2026-06-13", "00:10"))
	jobs := loadJobs("agent-a")
	if len(jobs) != 1 {
		t.Fatalf("missed pre-midnight slot minted %d jobs, want 1", len(jobs))
	}
	// due 23:20 + 480m ends 07:20 -> 430m left at 00:10
	if jobs[0].Minutes != 430 {
		t.Fatalf("late mint minutes = %d, want 430 (clamped to original end)", jobs[0].Minutes)
	}
	if cfg := loadConfig(); cfg.LastSweep["agent-a.2"] != "2026-06-12" {
		t.Fatalf("dedup keyed wrong: %+v", cfg.LastSweep)
	}
	// next tick: no duplicate; tonight's own 23:20 later still fires separately
	schedTick(bishD(t, "2026-06-13", "00:11"))
	if n := len(loadJobs("agent-a")); n != 1 {
		t.Fatalf("re-mint after dedup: %d jobs", n)
	}
	schedTick(bishD(t, "2026-06-13", "23:21"))
	if n := len(loadJobs("agent-a")); n != 2 {
		t.Fatalf("tonight's own slot blocked: %d jobs", n)
	}
}

// a wave whose whole window passed during downtime is skipped, not minted short.
func TestSchedTickSkipsWaveWhoseWindowPassed(t *testing.T) {
	nightrunEnv(t, "playground")
	writeFile(t, filepath.Join(nsDir(), "sweep", "playground-medic.md"), "medic")

	// medic due 2026-06-12 23:00, 50m window -> over by 23:50; tick at 00:10
	schedTick(bishD(t, "2026-06-13", "00:10"))
	for _, j := range loadJobs("playground") {
		if j.Label == "medic" {
			t.Fatalf("medic minted after its window passed: %+v", j)
		}
	}
	if cfg := loadConfig(); cfg.LastSweep["playground/medic"] != "2026-06-12" {
		t.Fatalf("skipped wave not deduped: %+v", loadConfig().LastSweep)
	}
}

// the in-memory belt: losing config.json (failed/unwritable save) must not
// re-mint the same wave while the process lives.
func TestMintBeltSurvivesConfigLoss(t *testing.T) {
	nightrunEnv(t, "agent-a")
	writeFile(t, filepath.Join(nsDir(), "sweep", "agent-a.md"), "sweep agent-a")

	schedTick(bish(t, "23:25"))
	if n := len(loadJobs("agent-a")); n != 1 {
		t.Fatalf("want 1 job, got %d", n)
	}
	if err := os.Remove(configPath()); err != nil { // simulate the lost save
		t.Fatal(err)
	}
	schedTick(bish(t, "23:26"))
	if n := len(loadJobs("agent-a")); n != 1 {
		t.Fatalf("belt failed: %d jobs after config loss", n)
	}
}
