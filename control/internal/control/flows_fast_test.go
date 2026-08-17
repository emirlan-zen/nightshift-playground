package control

// Fast handoff (opt-in per flow): a node launches on the first tick after its
// upstream verdict is routed, bypassing the box-wide launchGap wait. Default
// flows keep the conservative spacing.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// flowJobsByNode maps a flow's node ids to their current persisted jobs.
func flowJobsByNode(t *testing.T, f flow) map[string]job {
	t.Helper()
	out := map[string]job{}
	for _, j := range loadJobs(f.Agent) {
		if j.FlowID == f.ID {
			out[j.NodeID] = j
		}
	}
	return out
}

// mintFastTestFlow builds a two-node flow (refine -> validate) directly, with
// the given handoff mode, anchored at now.
func mintFastTestFlow(t *testing.T, id string, fast bool, now time.Time) flow {
	t.Helper()
	f := flow{
		ID: id, Agent: "agent-b", Repo: "example-repo", Goal: "ship it fast",
		NodeRoles: []string{"refine", "validate"}, FastHandoff: fast,
		Created: now, Updated: now, Status: "queued", Batch: "flow-" + id[len(id)-8:], Base: "HEAD",
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
	return f
}

func TestFlowCreateStampsFastHandoffOnEveryJob(t *testing.T) {
	flowEnv(t)
	body := `{"agent":"agent-b","repo":"example-repo","goal":"g","acceptance":["a"],"template":"refine-adr","fastHandoff":true}`
	rr := httptest.NewRecorder()
	handleFlowCreate(rr, httptest.NewRequest("POST", "/api/flows", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var got flowView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.FastHandoff {
		t.Fatal("flow did not keep fastHandoff")
	}
	for _, j := range loadJobs("agent-b") {
		if !j.FastStart {
			t.Fatalf("job %s not marked FastStart", j.ID)
		}
	}
}

func TestFlowCreateDefaultsToSpacedHandoff(t *testing.T) {
	flowEnv(t)
	body := `{"agent":"agent-b","repo":"example-repo","goal":"g","acceptance":["a"],"template":"refine-adr"}`
	rr := httptest.NewRecorder()
	handleFlowCreate(rr, httptest.NewRequest("POST", "/api/flows", strings.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	for _, j := range loadJobs("agent-b") {
		if j.FastStart {
			t.Fatalf("job %s marked FastStart without opt-in", j.ID)
		}
	}
}

// A fast-handoff successor ungates AND launches on the very tick its upstream
// verdict is routed — deep inside the launchGap window after the previous
// launch — and appended remediation nodes inherit the same behavior.
func TestFastHandoffLaunchesSuccessorSameTick(t *testing.T) {
	flowEnv(t)
	now := bish(t, "22:00") // before any sweep slot: only flow jobs fire
	f := mintFastTestFlow(t, "flow-20260613-2200-fa57fa57", true, now)

	schedTick(now)
	jobs := flowJobsByNode(t, f)
	if !jobs["refine"].Started {
		t.Fatalf("first node did not launch on the first tick: %+v", jobs["refine"])
	}
	if jobs["validate"].Started || !jobs["validate"].Gated {
		t.Fatalf("gated successor fired early: %+v", jobs["validate"])
	}

	writeFile(t, reportPath(f.Agent, jobs["refine"].ID), "---\nverdict: ok\n---\n# done\n")
	schedTick(now.Add(30 * time.Second)) // well inside launchGap of the refine launch
	jobs = flowJobsByNode(t, f)
	v := jobs["validate"]
	if v.Gated || !v.Started {
		t.Fatalf("fast successor should ungate and launch in the same tick: %+v", v)
	}
	if !strings.Contains(v.Prompt, "Upstream outputs") {
		t.Fatal("fast-launched successor is missing the upstream report section")
	}

	// The acceptance node says needs-work: the appended remediation round
	// inherits fast handoff and its first node starts on the routing tick too.
	writeFile(t, reportPath(f.Agent, v.ID), "---\nverdict: needs-work\n---\n# gaps\n")
	schedTick(now.Add(time.Minute))
	got, err := loadFlow(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobs = flowJobsByNode(t, got)
	amend, ok := jobs["amend-r1"]
	if !ok || !amend.FastStart {
		t.Fatalf("remediation node missing or not FastStart: %+v", amend)
	}
	if !amend.Started {
		t.Fatalf("remediation node should launch on the routing tick: %+v", amend)
	}
}

// Without the opt-in, a successor still ungates when its verdict is routed but
// waits out the box-wide launchGap before launching.
func TestDefaultHandoffWaitsOutLaunchGap(t *testing.T) {
	flowEnv(t)
	now := bish(t, "22:00")
	f := mintFastTestFlow(t, "flow-20260613-2200-51071d0e", false, now)

	schedTick(now) // refine launches; LastLaunch = now
	jobs := flowJobsByNode(t, f)
	if !jobs["refine"].Started {
		t.Fatalf("first node did not launch: %+v", jobs["refine"])
	}
	writeFile(t, reportPath(f.Agent, jobs["refine"].ID), "---\nverdict: ok\n---\n# done\n")

	schedTick(now.Add(time.Minute)) // inside the gap: ungated, not launched
	jobs = flowJobsByNode(t, f)
	if jobs["validate"].Gated {
		t.Fatalf("successor should ungate once the verdict is routed: %+v", jobs["validate"])
	}
	if jobs["validate"].Started {
		t.Fatal("default flow bypassed launchGap")
	}

	schedTick(now.Add(launchGap + 2*time.Minute)) // past the gap: launches
	jobs = flowJobsByNode(t, f)
	if !jobs["validate"].Started {
		t.Fatalf("successor never launched after the gap: %+v", jobs["validate"])
	}
}

// Fast handoff never stacks starts inside one tick: two fast jobs due together
// still launch one per tick.
func TestFastHandoffKeepsOneLaunchPerTick(t *testing.T) {
	flowEnv(t)
	now := bish(t, "22:00")
	for _, id := range []string{"20260613-2200-run-fa01", "20260613-2200-run-fa02"} {
		j := job{ID: id, Agent: "agent-b", Prompt: "p", At: now, Kind: "flow", Created: now, FastStart: true}
		if err := saveJob(j); err != nil {
			t.Fatal(err)
		}
	}
	schedTick(now)
	started := 0
	for _, j := range loadJobs("agent-b") {
		if j.Started {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("first tick started %d fast jobs, want 1", started)
	}
	schedTick(now.Add(30 * time.Second)) // next tick, still inside the gap: fast bypasses it
	started = 0
	for _, j := range loadJobs("agent-b") {
		if j.Started {
			started++
		}
	}
	if started != 2 {
		t.Fatalf("second tick: %d started, want 2 (fast bypasses the gap, not the per-tick cap)", started)
	}
}
