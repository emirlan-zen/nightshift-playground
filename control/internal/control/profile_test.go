package control

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// profileEnv resets the profile-related process globals on top of nightrunEnv.
func profileEnv(t *testing.T, agents ...string) *[]string {
	t.Helper()
	calls := nightrunEnv(t, agents...)
	effectiveProfiles = map[string]string{}
	return calls
}

func writeProfileFile(t *testing.T, p profile) {
	t.Helper()
	if err := saveProfile(p); err != nil {
		t.Fatalf("saveProfile(%s): %v", p.Name, err)
	}
}

// ---- parseProfile validator table -------------------------------------------

func TestParseProfileTable(t *testing.T) {
	profileEnv(t, "playground")
	base := func(mut func(*profile)) []byte {
		p := profile{
			Name:  "deep",
			Waves: []profileWave{{Name: "a", Time: "23:00", Prompt: "a.md", Minutes: 50}},
		}
		if mut != nil {
			mut(&p)
		}
		return mustJSON(p)
	}
	cases := []struct {
		name    string
		body    []byte
		wantErr string // substring; "" = must succeed
	}{
		{"minimal ok", base(nil), ""},
		{"bad name", base(func(p *profile) { p.Name = "Deep Perf" }), "name"},
		{"no waves", []byte(`{"name":"x","waves":[]}`), "no waves"},
		{"dup wave", base(func(p *profile) {
			p.Waves = append(p.Waves, profileWave{Name: "a", Time: "00:00", Prompt: "b.md", Minutes: 10})
		}), "duplicate"},
		{"bad time", base(func(p *profile) { p.Waves[0].Time = "25:99" }), "bad time"},
		{"no time no after", base(func(p *profile) { p.Waves[0].Time = "" }), "time or an after"},
		{"no prompt", base(func(p *profile) { p.Waves[0].Prompt = "" }), "no prompt"},
		{"bad effort", base(func(p *profile) { p.Waves[0].Effort = "turbo" }), "bad effort"},
		{"zero minutes", base(func(p *profile) { p.Waves[0].Minutes = 0 }), "minutes must be"},
		{"workers over cap", base(func(p *profile) { p.Workers = &workerSplit{Hunt: 4, Improve: 4} }), "cap"},
		{"workers zero", base(func(p *profile) { p.Workers = &workerSplit{} }), "at least 1"},
		{"workers ok", base(func(p *profile) { p.Workers = &workerSplit{Hunt: 0, Improve: 6} }), ""},
		{"deadline bad", base(func(p *profile) { p.Deadline = "8:99" }), "deadline"},
		{"after unknown", base(func(p *profile) {
			p.Waves = append(p.Waves, profileWave{Name: "b", After: []string{"ghost"}, Prompt: "b.md", Minutes: 10})
		}), "unknown wave"},
		{"self ref", base(func(p *profile) { p.Waves[0].After = []string{"a"} }), "itself"},
		{"after + perTicket", base(func(p *profile) {
			p.Waves = append(p.Waves, profileWave{Name: "b", After: []string{"a"}, PerTicket: true, Prompt: "b.md", Minutes: 10})
		}), "perTicket"},
		{"fan-out ok", base(func(p *profile) {
			p.Waves = append(p.Waves,
				profileWave{Name: "b", After: []string{"a"}, Prompt: "b.md", Minutes: 10},
				profileWave{Name: "c", After: []string{"a"}, Prompt: "c.md", Minutes: 10})
		}), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseProfile(c.body)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestParseProfileCycle(t *testing.T) {
	p := profile{Name: "cyc", Waves: []profileWave{
		{Name: "a", Time: "23:00", After: []string{"c"}, Prompt: "a.md", Minutes: 10},
		{Name: "b", After: []string{"a"}, Prompt: "b.md", Minutes: 10},
		{Name: "c", After: []string{"b"}, Prompt: "c.md", Minutes: 10},
	}}
	if _, err := parseProfile(mustJSON(p)); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestParseProfileTooManyWaves(t *testing.T) {
	p := profile{Name: "big"}
	for i := 0; i <= maxProfileWaves; i++ {
		p.Waves = append(p.Waves, profileWave{Name: "w" + itoa(i), Time: "23:00", Prompt: "x.md", Minutes: 10})
	}
	if _, err := parseProfile(mustJSON(p)); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("want wave-cap error, got %v", err)
	}
}

func itoa(i int) string { return strings.TrimSpace(string(rune('0'+i/10)) + string(rune('0'+i%10))) }

// ---- profileToSlots + builtin round-trip ------------------------------------

func TestProfileToSlots(t *testing.T) {
	p := profile{
		Name:    "deep",
		Workers: &workerSplit{Hunt: 0, Improve: 6},
		Waves: []profileWave{
			{Name: "analyze", Time: "00:00", Prompt: "an.md", Effort: "xhigh", Minutes: 45},
			{Name: "optimize", After: []string{"analyze"}, Prompt: "op.md", Minutes: 600}, // clamped to ceil
		},
	}
	slots := profileToSlots("playground", p)
	if len(slots) != 2 {
		t.Fatalf("want 2 slots, got %d", len(slots))
	}
	if slots[0].hour != 0 || slots[0].minute != 0 || slots[0].huntW != 0 || slots[0].improveW != 6 {
		t.Fatalf("analyze slot wrong: %+v", slots[0])
	}
	if slots[1].hour != -1 || len(slots[1].after) != 1 || slots[1].after[0] != "analyze" {
		t.Fatalf("optimize slot should be triggered: %+v", slots[1])
	}
	if slots[1].minutes != runMinutesCeil {
		t.Fatalf("minutes clamp: want %d got %d", runMinutesCeil, slots[1].minutes)
	}
}

func TestBuiltinRoundTrips(t *testing.T) {
	profileEnv(t, "playground")
	p := slotsToProfile("frombuiltin", playgroundDefaultSlots())
	if _, err := parseProfile(mustJSON(p)); err != nil {
		t.Fatalf("built-in derived profile must validate: %v", err)
	}
}

// ---- terminal classification -------------------------------------------------

func TestJobTerminalNoReport(t *testing.T) {
	profileEnv(t, "playground")
	now := bish(t, "05:00")
	agent := "playground"
	mk := func(mut func(*job)) job {
		j := job{ID: "20260613-0000-sweep-aa11", Agent: agent, Label: "x", Started: true, StartedAt: now.Add(-2 * time.Hour), Minutes: 30}
		if mut != nil {
			mut(&j)
		}
		return j
	}
	if !jobTerminalNoReport(agent, mk(nil), now) {
		t.Fatal("started 2h ago, 30m window, no report -> terminal")
	}
	if jobTerminalNoReport(agent, mk(func(j *job) { j.Started = false }), now) {
		t.Fatal("not started -> still pending")
	}
	if jobTerminalNoReport(agent, mk(func(j *job) { j.StartedAt = now.Add(-5 * time.Minute) }), now) {
		t.Fatal("just started, window open -> pending")
	}
	if !jobTerminalNoReport(agent, mk(func(j *job) { j.Skipped = true; j.Started = false }), now) {
		t.Fatal("skipped -> terminal")
	}
}

func TestUpstreamWaveStatusQuorum(t *testing.T) {
	profileEnv(t, "playground")
	now := bish(t, "05:00")
	agent, batch := "playground", "night-2026-06-12"
	// exec fan-out: 3 workers, 1 delivered, 2 dead -> quorum >=1 = success.
	for i, id := range []string{"20260613-0045-exec-a1", "20260613-0048-exec-b2", "20260613-0051-exec-c3"} {
		j := job{ID: id, Agent: agent, Label: "exec · improve · x", Batch: batch, Started: true, StartedAt: now.Add(-5 * time.Hour), Minutes: 60}
		if err := saveJob(j); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			writeFile(t, reportPath(agent, id), "# done")
		}
	}
	st, paths := upstreamWaveStatus(agent, "exec", batch, now)
	if st != upSuccess || len(paths) != 1 {
		t.Fatalf("fan-out quorum: want success+1 path, got st=%d paths=%v", st, paths)
	}

	// a wave whose only job ran report-less -> failed.
	_ = saveJob(job{ID: "20260613-0000-sweep-dd44", Agent: agent, Label: "medic", Batch: batch, Started: true, StartedAt: now.Add(-5 * time.Hour), Minutes: 50})
	if st, _ := upstreamWaveStatus(agent, "medic", batch, now); st != upFailed {
		t.Fatalf("report-less wave: want failed, got %d", st)
	}

	// a wave not minted yet -> pending.
	if st, _ := upstreamWaveStatus(agent, "ghost", batch, now); st != upPending {
		t.Fatalf("unminted wave: want pending, got %d", st)
	}
}

func TestGatedReadiness(t *testing.T) {
	profileEnv(t, "playground")
	now := bish(t, "05:00")
	agent, batch := "playground", "night-2026-06-12"
	upID := "20260613-0000-sweep-up11"
	dep := job{ID: "20260613-0000-dep-de22", Agent: agent, Label: "optimize", Batch: batch, After: []string{"analyze"}, Gated: true, At: now.Add(-4 * time.Hour)}

	// upstream still running -> pending.
	_ = saveJob(job{ID: upID, Agent: agent, Label: "analyze", Batch: batch, Started: true, StartedAt: now.Add(-10 * time.Minute), Minutes: 60})
	if st, _, _ := gatedReadiness(agent, dep, now); st != upPending {
		t.Fatalf("upstream running: want pending, got %d", st)
	}

	// upstream delivered -> ready, path returned.
	writeFile(t, reportPath(agent, upID), "# analysis")
	st, paths, _ := gatedReadiness(agent, dep, now)
	if st != upSuccess || len(paths) != 1 || !strings.Contains(paths[0], upID) {
		t.Fatalf("delivered: want ready+path, got st=%d paths=%v", st, paths)
	}

	// upstream dead (past window, no report) -> failed.
	_ = os.Remove(reportPath(agent, upID))
	_ = saveJob(job{ID: upID, Agent: agent, Label: "analyze", Batch: batch, Started: true, StartedAt: now.Add(-5 * time.Hour), Minutes: 60})
	if st, _, reason := gatedReadiness(agent, dep, now); st != upFailed || reason == "" {
		t.Fatalf("dead upstream: want failed+reason, got st=%d reason=%q", st, reason)
	}

	// timeout: upstream never minted, past the wait -> failed.
	dep2 := job{ID: "20260613-0000-dep-ee33", Agent: agent, Label: "z", Batch: "night-none", After: []string{"nope"}, Gated: true, At: now.Add(-triggeredMaxWait - time.Hour)}
	if st, _, _ := gatedReadiness(agent, dep2, now); st != upFailed {
		t.Fatalf("timeout: want failed, got %d", st)
	}
}

func TestDeadlineClamp(t *testing.T) {
	profileEnv(t, "playground")
	// now 07:00, deadline 08:45 -> 105 min left; a 300m wave clamps to 105.
	now := bish(t, "07:00")
	if m, ok := deadlineClampedMinutes("08:45", 300, now); !ok || m != 105 {
		t.Fatalf("clamp to deadline: want 105 ok, got %d %v", m, ok)
	}
	// a short wave under the deadline is untouched.
	if m, ok := deadlineClampedMinutes("08:45", 30, now); !ok || m != 30 {
		t.Fatalf("under deadline: want 30, got %d %v", m, ok)
	}
	// past the deadline -> skip.
	if _, ok := deadlineClampedMinutes("06:00", 30, now); ok {
		t.Fatal("past deadline should not be ok")
	}
	// empty deadline -> no clamp.
	if m, ok := deadlineClampedMinutes("", 300, now); !ok || m != 300 {
		t.Fatalf("no deadline: want 300, got %d %v", m, ok)
	}
}

// ---- end-to-end scheduler: fan-out fire + dep-skip cascade ------------------

func seedFanoutProfile(t *testing.T) {
	t.Helper()
	for _, f := range []string{"analyze.md", "optimize.md", "benchmark.md"} {
		writeFile(t, filepath.Join(nsDir(), "sweep", f), "do the "+f+" work")
	}
	writeProfileFile(t, profile{
		Name: "fan",
		Waves: []profileWave{
			{Name: "analyze", Time: "00:00", Prompt: "analyze.md", Minutes: 45, NoTickets: true},
			{Name: "optimize", After: []string{"analyze"}, Prompt: "optimize.md", Minutes: 60, NoTickets: true},
			{Name: "benchmark", After: []string{"analyze"}, Prompt: "benchmark.md", Minutes: 30, NoTickets: true},
		},
	})
	cfg := loadConfig()
	cfg.ActiveProfiles["playground"] = "fan"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func jobsByLabel(agent string) map[string]job {
	m := map[string]job{}
	for _, j := range loadJobs(agent) {
		m[j.Label] = j
	}
	return m
}

func TestFanoutFiresWhenUpstreamDelivers(t *testing.T) {
	profileEnv(t, "playground")
	seedFanoutProfile(t)
	now := bishD(t, "2026-06-13", "00:05")

	schedTick(now)
	m := jobsByLabel("playground")
	an, okA := m["analyze"]
	op, okO := m["optimize"]
	bm, okB := m["benchmark"]
	if !okA || !okO || !okB {
		t.Fatalf("want analyze+optimize+benchmark minted, got %v", keys(m))
	}
	if an.Gated {
		t.Fatal("analyze is scheduled, must not be gated")
	}
	if !op.Gated || !bm.Gated {
		t.Fatal("optimize+benchmark must be gated")
	}
	if op.Batch == "" || op.Batch != an.Batch {
		t.Fatalf("gated jobs must share the night batch: an=%q op=%q", an.Batch, op.Batch)
	}

	// analyze delivers its report. Next tick, both dependents ungate with the
	// upstream path appended to their prompt.
	writeFile(t, reportPath("playground", an.ID), "# analysis output")
	schedTick(now.Add(90 * time.Second))
	m = jobsByLabel("playground")
	if m["optimize"].Gated || m["benchmark"].Gated {
		t.Fatal("dependents should have ungated after upstream delivered")
	}
	if !strings.Contains(m["optimize"].Prompt, an.ID) {
		t.Fatalf("optimize prompt must reference the upstream report path; got:\n%s", m["optimize"].Prompt)
	}
}

func TestFanoutDepSkipsWhenUpstreamDies(t *testing.T) {
	profileEnv(t, "playground")
	seedFanoutProfile(t)
	now := bishD(t, "2026-06-13", "00:05")
	schedTick(now)
	m := jobsByLabel("playground")
	an := m["analyze"]

	// analyze "runs" but writes no report and its window elapses.
	an.Started = true
	an.StartedAt = now.Add(-3 * time.Hour)
	if err := saveJob(an); err != nil {
		t.Fatal(err)
	}
	later := now.Add(3 * time.Hour)
	schedTick(later)

	m = jobsByLabel("playground")
	if !m["optimize"].Skipped || !m["benchmark"].Skipped {
		t.Fatalf("dependents must dep-skip when upstream dies: op.skipped=%v bm.skipped=%v",
			m["optimize"].Skipped, m["benchmark"].Skipped)
	}
	if !depSkipMarker("playground", m["optimize"].ID) {
		t.Fatal("a .depskip marker must be written for the skipped dependent")
	}
}

func keys(m map[string]job) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

// ---- activate-next-night -----------------------------------------------------

func TestActivateTakesEffectNextNight(t *testing.T) {
	profileEnv(t, "playground")
	writeProfileFile(t, profile{Name: "broad", Waves: []profileWave{{Name: "a", Time: "23:00", Prompt: "a.md", Minutes: 50}}})
	writeProfileFile(t, profile{Name: "deep", Waves: []profileWave{{Name: "b", Time: "23:00", Prompt: "b.md", Minutes: 50}}})

	// Night 1: activate broad, tick once so it locks as effective.
	setActive(t, "broad")
	n1 := bishD(t, "2026-06-13", "23:30")
	schedTick(n1)
	if got := loadConfig().EffectiveProfiles["playground"]; got != "broad" {
		t.Fatalf("night 1 effective: want broad, got %q", got)
	}

	// Mid-night switch to deep: ActiveProfiles changes, EffectiveProfiles does NOT
	// (same night).
	setActive(t, "deep")
	schedTick(n1.Add(time.Hour))
	c := loadConfig()
	if c.ActiveProfiles["playground"] != "deep" || c.EffectiveProfiles["playground"] != "broad" {
		t.Fatalf("mid-night: want active=deep effective=broad, got active=%q effective=%q", c.ActiveProfiles["playground"], c.EffectiveProfiles["playground"])
	}

	// Next night boundary: effective rolls to deep.
	n2 := bishD(t, "2026-06-14", "23:30")
	schedTick(n2)
	if got := loadConfig().EffectiveProfiles["playground"]; got != "deep" {
		t.Fatalf("night 2 effective: want deep, got %q", got)
	}
}

func setActive(t *testing.T, name string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name})
	req := httptest.NewRequest("POST", "/api/profiles/active", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handleProfileActive(rec, req)
	if rec.Code != 200 {
		t.Fatalf("activate %s: %d %s", name, rec.Code, rec.Body)
	}
}

// ---- run now -----------------------------------------------------------------

func TestRunNowOffsetsFromNow(t *testing.T) {
	profileEnv(t, "playground")
	for _, f := range []string{"a.md", "b.md"} {
		writeFile(t, filepath.Join(nsDir(), "sweep", f), "work "+f)
	}
	writeProfileFile(t, profile{Name: "two", Waves: []profileWave{
		{Name: "first", Time: "23:00", Prompt: "a.md", Minutes: 50, NoTickets: true},
		{Name: "second", Time: "23:45", Prompt: "b.md", Minutes: 50, NoTickets: true}, // +45m gap
	}})

	req := httptest.NewRequest("POST", "/api/profiles/two/run", nil)
	req.SetPathValue("name", "two")
	rec := httptest.NewRecorder()
	before := time.Now()
	handleProfileRun(rec, req)
	if rec.Code != 200 {
		t.Fatalf("run now: %d %s", rec.Code, rec.Body)
	}
	m := jobsByLabel("playground")
	if len(m) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(m))
	}
	// first anchored ~now (offset 0); second ~45m later. Same run-batch.
	gap := m["second"].At.Sub(m["first"].At)
	if gap < 44*time.Minute || gap > 46*time.Minute {
		t.Fatalf("inter-wave gap preserved: want ~45m, got %v", gap)
	}
	if m["first"].At.Before(before.Add(-time.Minute)) {
		t.Fatalf("first wave should anchor to now, got %v", m["first"].At)
	}
	if m["first"].Batch != m["second"].Batch || !strings.HasPrefix(m["first"].Batch, "run-") {
		t.Fatalf("run-now jobs share a run-<id> batch: %q %q", m["first"].Batch, m["second"].Batch)
	}
}

// ---- proposal inbox ----------------------------------------------------------

func TestProposalRoundTrip(t *testing.T) {
	profileEnv(t, "playground")
	// retro writes a proposal (valid) + a junk one.
	good := profile{Name: "proposed-x", Why: "night was under pace", Waves: []profileWave{{Name: "a", Time: "23:00", Prompt: "a.md", Minutes: 50}}}
	writeFile(t, proposedPath("proposed-x"), string(mustJSON(good)))
	writeFile(t, proposedPath("junk"), `{"name":"junk","waves":[{"name":"a"}]}`)

	// list: both visible; junk carries its error.
	rec := httptest.NewRecorder()
	handleProposals(rec, httptest.NewRequest("GET", "/api/profiles/proposals", nil))
	var list []proposalView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 proposals, got %d", len(list))
	}
	var goodV, junkV *proposalView
	for i := range list {
		switch list[i].Name {
		case "proposed-x":
			goodV = &list[i]
		case "junk":
			junkV = &list[i]
		}
	}
	if goodV == nil || !goodV.Valid || goodV.Why == "" {
		t.Fatalf("good proposal should be valid with a why: %+v", goodV)
	}
	if junkV == nil || junkV.Valid || junkV.Error == "" {
		t.Fatalf("junk proposal should be invalid with an error: %+v", junkV)
	}

	// apply the good one -> promoted to profiles/, proposal consumed.
	req := httptest.NewRequest("POST", "/api/profiles/proposals/proposed-x/apply", nil)
	req.SetPathValue("name", "proposed-x")
	rec = httptest.NewRecorder()
	handleProposalApply(rec, req)
	if rec.Code != 200 {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if _, err := loadProfile("proposed-x"); err != nil {
		t.Fatalf("applied profile should load: %v", err)
	}
	if _, err := os.Stat(proposedPath("proposed-x")); !os.IsNotExist(err) {
		t.Fatal("proposal file should be consumed on apply")
	}

	// dismiss the junk one.
	req = httptest.NewRequest("DELETE", "/api/profiles/proposals/junk", nil)
	req.SetPathValue("name", "junk")
	rec = httptest.NewRecorder()
	handleProposalDismiss(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dismiss: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(proposedPath("junk")); !os.IsNotExist(err) {
		t.Fatal("junk proposal should be gone")
	}
}

// ---- CRUD handlers -----------------------------------------------------------

func TestProfileCRUDHandlers(t *testing.T) {
	profileEnv(t, "playground")
	p := profile{Name: "broad", Waves: []profileWave{{Name: "a", Time: "23:00", Prompt: "a.md", Minutes: 50}}}

	// PUT creates.
	req := httptest.NewRequest("PUT", "/api/profiles/broad", strings.NewReader(string(mustJSON(p))))
	req.SetPathValue("name", "broad")
	rec := httptest.NewRecorder()
	handleProfilePut(rec, req)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}

	// PUT with a name/url mismatch is rejected.
	req = httptest.NewRequest("PUT", "/api/profiles/broad", strings.NewReader(`{"name":"other","waves":[{"name":"a","time":"23:00","prompt":"a.md","minutes":50}]}`))
	req.SetPathValue("name", "broad")
	rec = httptest.NewRecorder()
	handleProfilePut(rec, req)
	if rec.Code != 400 {
		t.Fatalf("name mismatch should 400, got %d", rec.Code)
	}

	// GET reads it back.
	req = httptest.NewRequest("GET", "/api/profiles/broad", nil)
	req.SetPathValue("name", "broad")
	rec = httptest.NewRecorder()
	handleProfileGet(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"broad"`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}

	// DELETE while active clears the active pointer.
	setActive(t, "broad")
	req = httptest.NewRequest("DELETE", "/api/profiles/broad", nil)
	req.SetPathValue("name", "broad")
	rec = httptest.NewRecorder()
	handleProfileDelete(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if loadConfig().ActiveProfiles["playground"] != "" {
		t.Fatal("deleting the active profile should clear the agent's active pointer")
	}
	if _, err := os.Stat(profilePath("broad")); !os.IsNotExist(err) {
		t.Fatal("profile file should be gone")
	}
}

// ---- per-agent generalization (agent-b) ---------------------------------------

func TestPerAgentProfileDrivesCompanyAgent(t *testing.T) {
	profileEnv(t, "agent-b", "playground")
	for _, f := range []string{"fc-refine.md", "fc-plan.md"} {
		writeFile(t, filepath.Join(nsDir(), "sweep", f), "agent-b work "+f)
	}
	writeProfileFile(t, profile{Name: "adr-flow", Deadline: "08:30", Waves: []profileWave{
		{Name: "refine", Time: "23:00", Prompt: "fc-refine.md", Minutes: 60, NoTickets: true},
		{Name: "plan", After: []string{"refine"}, Prompt: "fc-plan.md", Minutes: 60, NoTickets: true},
	}})
	// Activate for agent-b only.
	cfg := loadConfig()
	cfg.ActiveProfiles["agent-b"] = "adr-flow"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	now := bishD(t, "2026-06-13", "23:05")
	schedTick(now)

	// agent-b ran the profile: refine (scheduled) + plan (gated), keyed "agent-b/…".
	fm := jobsByLabel("agent-b")
	if _, ok := fm["refine"]; !ok {
		t.Fatalf("agent-b should mint the profile's refine wave, got %v", keys(fm))
	}
	if !fm["plan"].Gated {
		t.Fatal("agent-b plan wave should be gated on refine")
	}
	if got := loadConfig().EffectiveProfiles["agent-b"]; got != "adr-flow" {
		t.Fatalf("agent-b effective profile: want adr-flow, got %q", got)
	}
	// playground has NO active profile → it did not adopt agent-b's, and its slot
	// keys are namespaced by agent (no collision).
	if loadConfig().EffectiveProfiles["playground"] != "" {
		t.Fatal("playground must not inherit agent-b's profile")
	}
	// The dedup key is per-agent.
	slots, ok := profileSlots("agent-b", "adr-flow")
	if !ok || slots[0].key != "agent-b/refine" {
		t.Fatalf("agent-b slot key should be namespaced: %+v", slots)
	}
}

// ---- legacy migration --------------------------------------------------------

func TestMigrateLegacyPipeline(t *testing.T) {
	profileEnv(t, "playground")
	writeFile(t, pipelinePath(), `{"slots":[{"name":"medic","time":"23:00","prompt":"m.md","minutes":50}]}`)
	cfg := loadConfig()
	if !migrateLegacyPipeline(&cfg) {
		t.Fatal("legacy pipeline.json should migrate")
	}
	if cfg.ActiveProfiles["playground"] != "custom" {
		t.Fatalf("active should point at custom, got %q", cfg.ActiveProfiles["playground"])
	}
	if _, err := loadProfile("custom"); err != nil {
		t.Fatalf("custom profile should exist: %v", err)
	}
	// idempotent: a second run is a no-op (profiles/ now non-empty).
	if migrateLegacyPipeline(&cfg) {
		t.Fatal("second migrate should be a no-op")
	}
}
