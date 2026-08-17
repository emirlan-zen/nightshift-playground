// Pipeline profiles (ADR-0014): named, phone-switchable shapes of playground's
// night pipeline. A profile is one complete pipeline definition
// (~/.nightshift/profiles/<name>.json); exactly one is *active*
// (config.ActiveProfile). It supersedes the single hand-edited pipeline.json of
// ADR-0013 — a legacy pipeline.json is auto-adopted as a profile named "custom"
// so no box breaks.
//
// The schema is the ADR-0013 slot schema plus three additions: a profile-level
// `deadline` (every wave's auto-stop clamped so nothing runs past it), a
// `workers` split (exec fan-out, replacing the Go const), and per-wave `after`
// edges (output-consuming fan-out — a dependent held until its upstreams
// deliver, then skipped loudly if any dies; see the gated-job machinery in
// nightrun.go).
//
// parseProfile is the ONE validator, shared by the file-load path and every
// handler, so they cannot drift.
package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxProfileWaves = 32 // a profile may define at most this many waves
	maxWaveDeps     = 8  // and a wave at most this many `after` upstreams
)

// profileNameRe / waveNameRe: lowercase alnum + single dashes. A profile name is
// a file name (profiles/<name>.json); a wave name is a LastSweep dedup key
// ("playground/<name>"), a job label, and a job-match prefix — so both must be
// path-safe and stable.
var (
	profileNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)
	// Wave names are labels + dedup keys, not file names, so they may carry the
	// built-in mixed case ("plan-A", "plan-B") — but no slashes/dots (they build
	// "playground/<name>" keys and match job labels by prefix).
	waveNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,30}[A-Za-z0-9])?$`)
)

// effectiveProfiles maps each agent to the profile name locked for the night
// currently in progress. schedTick sets it under nsMu at each tick (rolling to
// cfg.ActiveProfiles at a night boundary); sweepSlots + governor read it. Absent
// = the agent's built-in default. Guarded by nsMu like the rest of ~/.nightshift.
var effectiveProfiles = map[string]string{}

// workerSplit is a profile's exec fan-out budget (ADR-0014 replaces the Go
// const). hunt+improve is clamped to maxExecWorkers.
type workerSplit struct {
	Hunt    int `json:"hunt"`
	Improve int `json:"improve"`
}

// profileWave is one wave. Either Time (scheduled) or After (triggered), or both
// (After + an earliest-time floor). Every ADR-0013 wave field carries over.
type profileWave struct {
	Name      string   `json:"name"`
	Time      string   `json:"time,omitempty"`  // "HH:MM" Asia/Bishkek; "" for a pure After wave
	After     []string `json:"after,omitempty"` // upstream wave names whose output this consumes
	Prompt    string   `json:"prompt"`
	Model     string   `json:"model,omitempty"`
	Effort    string   `json:"effort,omitempty"`
	Executor  string   `json:"executor,omitempty"` // claude (default) | codex, ADR-0018
	Minutes   int      `json:"minutes"`
	PerTicket bool     `json:"perTicket,omitempty"`
	NoTickets bool     `json:"noTickets,omitempty"`
}

// profile is one complete pipeline definition. Why is required on a *proposal*
// (retro explains its suggestion) and ignored otherwise.
type profile struct {
	Name     string        `json:"name"`
	Deadline string        `json:"deadline,omitempty"` // "HH:MM" Bishkek; "" = no auto-stop clamp
	Workers  *workerSplit  `json:"workers,omitempty"`  // nil = the built-in 3/3 default
	Waves    []profileWave `json:"waves"`
	Why      string        `json:"why,omitempty"` // required only for a retro proposal

	// MaxConcurrent (ADR-0019) caps the night's concurrently WORKING sessions
	// (launched, no report yet) across all of this profile's waves. 0 = uncapped
	// — the built-in pipeline's existing behavior, where exec workers, review,
	// and harvest legitimately overlap. Set 1 for a strictly serialized night.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

func profilesDir() string          { return filepath.Join(nsDir(), "profiles") }
func profilePath(n string) string  { return filepath.Join(profilesDir(), n+".json") }
func proposedDir() string          { return filepath.Join(nsDir(), "profiles.proposed") }
func proposedPath(n string) string { return filepath.Join(proposedDir(), n+".json") }

// parseProfile unmarshals + validates a profile. The error is user-facing (a
// handler returns it verbatim as the 400 body, the file loader logs it). All-or-
// nothing, like pipeline.json: one bad wave rejects the whole profile.
func parseProfile(b []byte) (profile, error) {
	var p profile
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("bad json: %v", err)
	}
	if !profileNameRe.MatchString(p.Name) {
		return p, fmt.Errorf("name %q invalid (want lowercase alnum + dashes, ≤32)", p.Name)
	}
	if p.Deadline != "" {
		if _, err := time.Parse("15:04", p.Deadline); err != nil {
			return p, fmt.Errorf("deadline %q invalid (want HH:MM)", p.Deadline)
		}
	}
	if p.Workers != nil {
		if p.Workers.Hunt < 0 || p.Workers.Improve < 0 {
			return p, fmt.Errorf("workers cannot be negative")
		}
		if p.Workers.Hunt+p.Workers.Improve < 1 {
			return p, fmt.Errorf("workers.hunt + workers.improve must be at least 1")
		}
		if p.Workers.Hunt+p.Workers.Improve > maxExecWorkers {
			return p, fmt.Errorf("workers.hunt + workers.improve = %d exceeds the %d-worker cap",
				p.Workers.Hunt+p.Workers.Improve, maxExecWorkers)
		}
	}
	if p.MaxConcurrent < 0 || p.MaxConcurrent > maxAutomationCap {
		return p, fmt.Errorf("maxConcurrent %d out of range (0-%d)", p.MaxConcurrent, maxAutomationCap)
	}
	if len(p.Waves) == 0 {
		return p, fmt.Errorf("profile has no waves")
	}
	if len(p.Waves) > maxProfileWaves {
		return p, fmt.Errorf("%d waves exceeds the %d-wave cap", len(p.Waves), maxProfileWaves)
	}
	seen := map[string]bool{}
	for i := range p.Waves {
		w := &p.Waves[i]
		if !waveNameRe.MatchString(w.Name) {
			return p, fmt.Errorf("wave %d: name %q invalid (want lowercase alnum + dashes, ≤32)", i, w.Name)
		}
		if seen[w.Name] {
			return p, fmt.Errorf("wave %q: duplicate name", w.Name)
		}
		seen[w.Name] = true
		if w.Time == "" && len(w.After) == 0 {
			return p, fmt.Errorf("wave %q: needs a time or an after edge", w.Name)
		}
		if w.Time != "" {
			if _, err := time.Parse("15:04", w.Time); err != nil {
				return p, fmt.Errorf("wave %q: bad time %q (want HH:MM)", w.Name, w.Time)
			}
		}
		if w.Prompt == "" {
			return p, fmt.Errorf("wave %q: no prompt file", w.Name)
		}
		if w.Effort != "" && !validEfforts[w.Effort] {
			return p, fmt.Errorf("wave %q: bad effort %q (want low|medium|high|xhigh|max)", w.Name, w.Effort)
		}
		if w.Executor != "" && !validExecutors[w.Executor] {
			return p, fmt.Errorf("wave %q: bad executor %q (want claude|codex)", w.Name, w.Executor)
		}
		if w.Executor == "codex" && w.PerTicket {
			return p, fmt.Errorf("wave %q: perTicket fan-out is claude-only", w.Name)
		}
		if w.Minutes <= 0 {
			return p, fmt.Errorf("wave %q: minutes must be > 0", w.Name)
		}
		if len(w.After) > maxWaveDeps {
			return p, fmt.Errorf("wave %q: %d after-edges exceeds the %d cap", w.Name, len(w.After), maxWaveDeps)
		}
		if len(w.After) > 0 && w.PerTicket {
			return p, fmt.Errorf("wave %q: after + perTicket unsupported (a triggered wave is a single session)", w.Name)
		}
	}
	// Edge validation: refs exist, no self-ref, no cycles (DFS).
	for i := range p.Waves {
		w := &p.Waves[i]
		for _, up := range w.After {
			if up == w.Name {
				return p, fmt.Errorf("wave %q: cannot depend on itself", w.Name)
			}
			if !seen[up] {
				return p, fmt.Errorf("wave %q: after names unknown wave %q", w.Name, up)
			}
		}
	}
	if cyc := findCycle(p.Waves); cyc != "" {
		return p, fmt.Errorf("dependency cycle: %s", cyc)
	}
	return p, nil
}

// findCycle returns a human-readable cycle path ("a → b → a") or "" if the
// after-edge graph is acyclic. Standard white/grey/black DFS.
func findCycle(waves []profileWave) string {
	edges := map[string][]string{}
	for _, w := range waves {
		edges[w.Name] = w.After
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var dfs func(n string) string
	dfs = func(n string) string {
		color[n] = grey
		stack = append(stack, n)
		for _, up := range edges[n] {
			switch color[up] {
			case grey:
				return strings.Join(append(stack, up), " → ")
			case white:
				if c := dfs(up); c != "" {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return ""
	}
	for _, w := range waves {
		if color[w.Name] == white {
			if c := dfs(w.Name); c != "" {
				return c
			}
		}
	}
	return ""
}

// profileToSlots converts a validated profile to scheduler slots for `agent`,
// baking the worker split + deadline onto every slot (only exec reads the
// split). A triggered wave with no time floor gets hour=-1. The LastSweep dedup
// key is namespaced per agent ("<agent>/<wave>") so two agents' profiles can't
// collide.
func profileToSlots(agent string, p profile) []sweepSlot {
	hw, iw := execHuntWorkers, execImproveWorkers
	if p.Workers != nil {
		hw, iw = p.Workers.Hunt, p.Workers.Improve
	}
	out := make([]sweepSlot, 0, len(p.Waves))
	for _, w := range p.Waves {
		hour, minute := -1, -1
		if w.Time != "" {
			tt, _ := time.Parse("15:04", w.Time) // validated in parseProfile
			hour, minute = tt.Hour(), tt.Minute()
		}
		out = append(out, sweepSlot{
			key: agent + "/" + w.Name, hour: hour, minute: minute,
			prompt: w.Prompt, model: w.Model, effort: w.Effort, executor: w.Executor, label: w.Name,
			minutes:   min(max(w.Minutes, runMinutesFloor), runMinutesCeil),
			perTicket: w.PerTicket, noTickets: w.NoTickets,
			after: w.After, deadline: p.Deadline, huntW: hw, improveW: iw,
			cap: p.MaxConcurrent,
		})
	}
	return out
}

// ---- persistence -------------------------------------------------------------

// loadProfile reads + validates one profile file. Callers hold nsMu.
func loadProfile(name string) (profile, error) {
	if !profileNameRe.MatchString(name) {
		return profile{}, fmt.Errorf("invalid profile name")
	}
	b, err := os.ReadFile(profilePath(name))
	if err != nil {
		return profile{}, err
	}
	return parseProfile(b)
}

// saveProfile validates then rename-atomically writes a profile. The on-disk
// name always matches p.Name (the file path is derived from it).
func saveProfile(p profile) error {
	if _, err := parseProfile(mustJSON(p)); err != nil {
		return err
	}
	if err := os.MkdirAll(profilesDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	tmp := profilePath(p.Name) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, profilePath(p.Name))
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// listProfileNames returns the sorted names of all valid profile files.
func listProfileNames() []string {
	matches, _ := filepath.Glob(filepath.Join(profilesDir(), "*.json"))
	var out []string
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		if profileNameRe.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ---- resolution --------------------------------------------------------------

// profileSlots resolves an agent's wave slots for a named profile. ok=false
// (empty name, or a profile that fails to load — warned loud) tells the caller
// to fall back to that agent's default so a typo never yields a dead night.
func profileSlots(agent, name string) ([]sweepSlot, bool) {
	if name == "" {
		return nil, false
	}
	p, err := loadProfile(name)
	if err != nil {
		warnPipeline("profile %q (%s): %v", name, agent, err)
		return nil, false
	}
	pipelineWarned = ""
	return profileToSlots(agent, p), true
}

// migrateLegacyPipeline adopts a lone legacy pipeline.json (ADR-0013) as a
// profile named "custom" and points ActiveProfile at it, so an existing box
// keeps its exact schedule after the upgrade. Runs once at startup: a no-op if
// any profiles/ already exist or there is no pipeline.json. Caller holds nsMu.
func migrateLegacyPipeline(cfg *nightConfig) bool {
	if len(listProfileNames()) > 0 {
		return false
	}
	slots, ok := loadPipelineSlots()
	if !ok {
		return false
	}
	p := slotsToProfile("custom", slots)
	if err := saveProfile(p); err != nil {
		logf("profile migrate: saving custom failed: %v", err)
		return false
	}
	if cfg.ActiveProfiles["playground"] == "" {
		cfg.ActiveProfiles["playground"] = "custom"
	}
	logf("profile migrate: adopted legacy pipeline.json as profile %q", "custom")
	return true
}

// slotsToProfile is the inverse of profileToSlots for the migration + the
// editor's "start from the built-in" seed: scheduler slots back to a profile.
func slotsToProfile(name string, slots []sweepSlot) profile {
	p := profile{Name: name}
	for _, s := range slots {
		w := profileWave{
			Name: s.label, Prompt: s.prompt, Model: s.model, Effort: s.effort, Executor: s.executor,
			Minutes: s.minutes, PerTicket: s.perTicket, NoTickets: s.noTickets, After: s.after,
		}
		if s.hour >= 0 {
			w.Time = fmt.Sprintf("%02d:%02d", s.hour, s.minute)
		}
		p.Waves = append(p.Waves, w)
	}
	return p
}

// ---- output-consuming fan-out: gating + terminal classification --------------
//
// A triggered wave (`after`) is minted as a *gated* job and held by the fire
// loop until every upstream is terminal-with-report, scoped to the same Batch.
// Classification is pure artifact + time math — NO run-status sudo in the fire
// path (ADR-0014): report .md = success, an .authfail/.depskip marker or a run
// past its auto-stop window with no report = fail. Quorum for a fan-out (exec)
// upstream is ≥1 report, not all.

const (
	// triggeredMaxWait bounds how long a gated wave waits on a stuck upstream
	// before it dep-skips (measured from the gated job's own mint anchor, At). A
	// full night from any anchor fits inside this; past it the window is gone.
	triggeredMaxWait = 11 * time.Hour
)

// upstream classification result.
const (
	upPending = iota
	upSuccess
	upFailed
)

func reportPath(agent, id string) string { return filepath.Join(reportsDir(agent), id+".md") }
func reportExists(agent, id string) bool { _, err := os.Stat(reportPath(agent, id)); return err == nil }

func depSkipPath(agent, id string) string { return filepath.Join(reportsDir(agent), id+".depskip") }
func depSkipMarker(agent, id string) bool {
	_, err := os.Stat(depSkipPath(agent, id))
	return err == nil
}

// writeDepSkip records why a dependent was skipped: a marker file (read by the
// detector, /api/health, and the transitive-cascade classifier) next to where
// its report would have gone. Best-effort.
func writeDepSkip(agent, id, reason string) {
	if err := os.MkdirAll(reportsDir(agent), 0o755); err != nil {
		logf("dep-skip: mkdir %s: %v", agent, err)
		return
	}
	if err := os.WriteFile(depSkipPath(agent, id), []byte(reason), 0o644); err != nil {
		logf("dep-skip: write %s/%s: %v", agent, id, err)
	}
}

// labelMatchesWave reports whether a job Label belongs to a wave name. A wave's
// job carries the wave name as its label; exec fans out to "<name> · <lane> · …",
// so a prefix match catches every worker of a fan-out wave.
func labelMatchesWave(label, wave string) bool {
	return label == wave || strings.HasPrefix(label, wave+" ")
}

// jobTerminalNoReport reports whether a job has definitively ended without ever
// producing a report — a marker, an explicit skip, or a started run past its
// auto-stop window (Minutes + 10min slack). A not-yet-started job is still
// pending (it may run later); a delivered job never reaches here (the caller
// checks reportExists first).
func jobTerminalNoReport(agent string, j job, now time.Time) bool {
	if j.Skipped || depSkipMarker(agent, j.ID) || authFailMarker(agent, j.ID) || watchdogMarker(agent, j.ID) {
		return true
	}
	if !j.Started {
		return false
	}
	mins := j.Minutes
	if mins <= 0 {
		mins = defaultRunMinutes
	}
	return now.After(j.StartedAt.Add(time.Duration(mins)*time.Minute + 10*time.Minute))
}

// upstreamWaveStatus classifies one upstream wave for a given batch: upSuccess
// (≥1 matching job delivered a report — the fan-out quorum), upFailed (matching
// jobs exist and ALL ended report-less), or upPending (no matching job yet, or
// one is still in flight). Returns the delivered report paths on success.
func upstreamWaveStatus(agent, up, batch string, now time.Time) (int, []string) {
	var paths []string
	anyJob, allTerminal := false, true
	for _, j := range loadJobs(agent) {
		if j.Batch != batch || !labelMatchesWave(j.Label, up) {
			continue
		}
		anyJob = true
		if reportExists(agent, j.ID) {
			paths = append(paths, reportPath(agent, j.ID))
			continue
		}
		if !jobTerminalNoReport(agent, j, now) {
			allTerminal = false
		}
	}
	switch {
	case len(paths) > 0:
		return upSuccess, paths
	case !anyJob:
		return upPending, nil // upstream not minted yet — the wait timeout is the backstop
	case allTerminal:
		return upFailed, nil
	default:
		return upPending, nil
	}
}

// gatedReadiness evaluates all of a gated job's upstreams. A single failure
// wins (the child has no input, so it must skip loudly); otherwise every
// upstream must have delivered before it's ready. Returns (upSuccess, paths, "")
// when ready, (upFailed, nil, reason) when a dep died or the wait timed out, or
// (upPending, …) while still waiting.
func gatedReadiness(agent string, j job, now time.Time) (int, []string, string) {
	var paths []string
	pending := false
	// Emitted children (ADR-0023) gate on QUORUM ≥1: a runtime fan-out exists
	// to try several things, and one dead attempt must not deny the judge the
	// ones that worked. Static stages keep the strict all-must-deliver gate —
	// a planned parallel group's members are not interchangeable.
	quorum := emittedGroupUpstreams(j)
	deadQuorum := 0
	for _, up := range j.After {
		st, ps := upstreamWaveStatus(agent, up, j.Batch, now)
		switch st {
		case upFailed:
			if quorum[up] {
				deadQuorum++
				continue
			}
			return upFailed, nil, "upstream " + up + " failed (no report)"
		case upPending:
			pending = true
		case upSuccess:
			paths = append(paths, ps...)
		}
	}
	if deadQuorum > 0 && len(paths) == 0 && !pending {
		return upFailed, nil, "every emitted child ended without a report"
	}
	if pending {
		// The night-window timeout is a profile concept. A flow node (ADR-0015/
		// 0017) may legitimately wait days for a long chain: its bounds are the
		// flow's absolute deadline and the lifetime node budget, not this clock.
		if j.FlowID == "" && now.After(j.At.Add(triggeredMaxWait)) {
			return upFailed, nil, "upstream(s) never delivered before the window closed"
		}
		return upPending, nil, ""
	}
	return upSuccess, paths, ""
}

// deadlineClampedMinutes trims a wave's auto-stop so the run ends by the
// profile deadline ("HH:MM" tonight, evening-anchored). Returns (minutes, ok);
// ok=false when the deadline has already passed (< the 10-minute floor) so the
// caller must skip. An empty deadline means no clamp.
func deadlineClampedMinutes(deadline string, minutes int, now time.Time) (int, bool) {
	if deadline == "" {
		return minutes, true
	}
	tt, err := time.Parse("15:04", deadline)
	if err != nil {
		return minutes, true
	}
	b := now.In(bishkek)
	end := time.Date(b.Year(), b.Month(), b.Day(), tt.Hour(), tt.Minute(), 0, 0, bishkek)
	// Evening-anchored: a pre-noon deadline (08:45) belongs to the small hours
	// AFTER a 22:30 night start, so it's tomorrow relative to an evening `now`.
	if end.Before(now) && b.Hour() >= 12 {
		end = end.AddDate(0, 0, 1)
	}
	left := int(end.Sub(now).Minutes())
	if left < runMinutesFloor {
		return 0, false
	}
	if left < minutes {
		return left, true
	}
	return minutes, true
}
