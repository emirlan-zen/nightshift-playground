package control

// Scheduled automations + the agent-side mutation spool (ADR-0019).
//
// An automation (flow template) may carry a `schedule`: the scheduler then
// instantiates a run every day at its wall-clock time, with the same dedup,
// grace, and kill-switch semantics as the night sweeps — this is what unifies
// "night runs" and "flows" at the definition layer. Recurrence is
// overlap-safe: a still-live previous run skips the new instantiation with a
// loud alert instead of stacking a second run of the same automation.
//
// Agents steer runs through a SPOOL, never by editing flow/job JSON (the
// control plane owns that state under nsMu; a direct file edit races
// saveFlow into lost updates). The cwd-scoped nightshift-flow CLI drops
// validated request files under ~/.nightshift/flow-updates/<agent>/ and
// reconcileFlowsLocked applies them on the next tick: crash-safe, race-free,
// auditable. Automation definitions stay propose-only (ADR-0017).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// templateSchedule turns a flow template into a recurring automation: every
// day at Time (Asia/Bishkek) the scheduler creates a run of this template for
// Agent against Repo. DeadlineMinutes bounds the whole run (FinishBy = due +
// minutes) so a scheduled automation can never straggle into the next firing.
type templateSchedule struct {
	Agent           string   `json:"agent"`
	Repo            string   `json:"repo"`
	Goal            string   `json:"goal"`
	Guidance        string   `json:"guidance,omitempty"`
	Acceptance      []string `json:"acceptance,omitempty"`
	Time            string   `json:"time"` // "HH:MM" Asia/Bishkek, daily
	DeadlineMinutes int      `json:"deadlineMinutes,omitempty"`
	MaxConcurrent   int      `json:"maxConcurrent,omitempty"`
	FastHandoff     bool     `json:"fastHandoff,omitempty"`
}

func validateTemplateSchedule(s *templateSchedule) error {
	if s == nil {
		return nil
	}
	if !isCompany(s.Agent) {
		return fmt.Errorf("schedule agent %q unknown", s.Agent)
	}
	if _, err := time.Parse("15:04", s.Time); err != nil {
		return fmt.Errorf("schedule time %q invalid (want HH:MM)", s.Time)
	}
	s.Goal = strings.TrimSpace(s.Goal)
	if s.Goal == "" || len(s.Goal) > flowGoalMax {
		return errors.New("schedule needs a goal")
	}
	if len(s.Guidance) > flowGuidanceMax || len(s.Acceptance) > flowAcceptanceMax {
		return errors.New("schedule guidance or acceptance too long")
	}
	if s.DeadlineMinutes < 0 || s.DeadlineMinutes > 24*60 {
		return errors.New("schedule deadlineMinutes out of range (0-1440)")
	}
	if s.MaxConcurrent < 0 || s.MaxConcurrent > maxAutomationCap {
		return fmt.Errorf("schedule maxConcurrent out of range (0-%d)", maxAutomationCap)
	}
	return nil
}

// ---- shared run creation -------------------------------------------------------

// flowSpec is everything needed to create a run, shared by the HTTP handler,
// the scheduler (recurring automations), and the agent spool — one validator,
// three entry points.
type flowSpec struct {
	Agent         string      `json:"agent"`
	Repo          string      `json:"repo"`
	Goal          string      `json:"goal"`
	Acceptance    []string    `json:"acceptance"`
	Guidance      string      `json:"guidance"`
	FastHandoff   bool        `json:"fastHandoff"`
	MaxConcurrent int         `json:"maxConcurrent"`
	Template      string      `json:"template"`
	Nodes         []string    `json:"nodes"`
	Stages        [][]string  `json:"stages"`
	Edges         []routeEdge `json:"edges"`
	// Emitters/MemberRuntime (ADR-0023) may be supplied explicitly for an
	// ad-hoc run; otherwise they come from the named automation and are pinned
	// onto the run exactly like Stages/Edges.
	Emitters      []emitterSpec                  `json:"emitters"`
	MemberRuntime map[string]nodeRuntimeOverride `json:"memberRuntime"`
	Deadline      *time.Time                     `json:"-"`
	Base          string                         `json:"base"`
}

// resolveFlowSpec validates the spec and resolves its stages/edges (explicit,
// or from the named template). Returns the ready-to-mint flow value.
func resolveFlowSpec(in flowSpec, now time.Time) (flow, error) {
	in.Goal, in.Guidance = strings.TrimSpace(in.Goal), strings.TrimSpace(in.Guidance)
	if !isCompany(in.Agent) || in.Goal == "" || len(in.Goal) > flowGoalMax || len(in.Guidance) > flowGuidanceMax || len(in.Acceptance) > flowAcceptanceMax {
		return flow{}, errors.New("invalid agent, goal, guidance, or acceptance criteria")
	}
	if in.MaxConcurrent < 0 || in.MaxConcurrent > maxAutomationCap {
		return flow{}, fmt.Errorf("maxConcurrent out of range (0-%d)", maxAutomationCap)
	}
	stages := in.Stages
	edges := in.Edges
	emitters := in.Emitters
	memberRuntime := in.MemberRuntime
	if len(stages) == 0 && len(in.Nodes) > 0 {
		for _, n := range in.Nodes {
			stages = append(stages, []string{n})
		}
	}
	if len(stages) == 0 {
		t, ok := templateByID(in.Template)
		if !ok {
			return flow{}, errors.New("unknown flow template")
		}
		stages = templateStages(t)
		if len(edges) == 0 {
			edges = append([]routeEdge(nil), t.Edges...)
		}
		// Pinned at create (ADR-0016 discipline): editing the automation later
		// must not widen a live run's emission permissions or silently
		// re-point a member's engine mid-run.
		if len(emitters) == 0 {
			emitters = append([]emitterSpec(nil), t.Emitters...)
		}
		if len(memberRuntime) == 0 && len(t.MemberRuntime) > 0 {
			memberRuntime = make(map[string]nodeRuntimeOverride, len(t.MemberRuntime))
			for k, v := range t.MemberRuntime {
				memberRuntime[k] = v
			}
		}
	}
	if err := validateStages(stages); err != nil {
		return flow{}, err
	}
	if err := validateEdges(edges, stages); err != nil {
		return flow{}, err
	}
	if err := validateMemberRuntime(memberRuntime, stages); err != nil {
		return flow{}, err
	}
	if err := validateEmitters(emitters, stages); err != nil {
		return flow{}, err
	}
	roles := flattenStages(stages)
	f := flow{
		ID:    "flow-" + now.In(bishkek).Format("20060102-1504") + "-" + shortBatchID(),
		Agent: in.Agent, Repo: in.Repo, Goal: in.Goal, Acceptance: in.Acceptance,
		Guidance: in.Guidance, FastHandoff: in.FastHandoff, MaxConcurrent: in.MaxConcurrent,
		Template: in.Template, NodeRoles: roles, Stages: stages, Edges: edges,
		Emitters: emitters, MemberRuntime: memberRuntime, Deadline: in.Deadline,
		Created: now, Updated: now, Status: "queued", Batch: "flow-" + shortBatchID(), Base: in.Base,
	}
	// Pin the revision from the RESOLVED graph, not just its role list: a rewired
	// stage or a per-member model pin is a different automation to measure.
	f.AutomationRevision = flowAutomationRevision(f)
	return f, nil
}

// createFlowLocked resolves, prepares the worktree, mints the node jobs, and
// persists the run. Caller holds nsMu.
func createFlowLocked(in flowSpec, now time.Time) (flow, error) {
	f, err := resolveFlowSpec(in, now)
	if err != nil {
		return flow{}, err
	}
	if err := prepareFlowWorktree(&f); err != nil {
		return flow{}, err
	}
	if err := mintFlow(&f, now); err != nil {
		// mintFlow saves each node job before appending its node, so a partial
		// failure leaves already-persisted jobs whose node is in f.Nodes. Delete
		// them: an orphaned ungated first-stage job would otherwise be launched by
		// schedTick for a flow whose JSON was never written — a phantom session
		// with no reconcile owner.
		rollbackFlowJobs(f)
		f.CleanupMessage = "flow mint failed: " + err.Error()
		_ = tryCleanupWorktree(&f)
		return flow{}, err
	}
	if err := saveFlow(f); err != nil {
		// Same orphan hazard: mintFlow persisted every node job, but the flow
		// record didn't land. Roll the jobs back and drop the worktree.
		rollbackFlowJobs(f)
		f.CleanupMessage = "flow save failed: " + err.Error()
		_ = tryCleanupWorktree(&f)
		return flow{}, err
	}
	return f, nil
}

// rollbackFlowJobs deletes every node job of a flow whose creation failed before
// it was persisted. mintFlow appends a node only after its job saved, so f.Nodes
// enumerates exactly the persisted jobs (deleteJob on a never-saved id is a
// no-op). Distinct from reapUnstartedFlowJobs: a half-created flow has no
// started/delivered nodes to preserve.
func rollbackFlowJobs(f flow) {
	for _, n := range f.Nodes {
		deleteJob(f.Agent, n.JobID)
	}
}

// ---- recurrence ----------------------------------------------------------------

// mintScheduledAutomations instantiates due scheduled templates. Same shape as
// the sweep mint: today's AND yesterday's due are evaluated (downtime
// resilience), dedup is per due-date via the LastSweep map (key "auto/<id>"),
// long-past dues inside the grace are skipped loudly, and the agent's sweep
// kill-switch applies. A previous run of the same automation still live ⇒
// overlap-skip with an alert — never two concurrent runs of one automation.
// Caller holds nsMu; cfg mutations ride the caller's dirty flag.
func mintScheduledAutomations(cfg *nightConfig, now time.Time, dirty *bool) {
	nb := now.In(bishkek)
	for _, t := range effectiveFlowTemplates() {
		s := t.Schedule
		if s == nil {
			continue
		}
		if err := validateTemplateSchedule(s); err != nil {
			logf("automation %s: schedule invalid, skipping: %v", t.ID, err)
			continue
		}
		tt, _ := time.Parse("15:04", s.Time)
		key := "auto/" + t.ID
		dueToday := time.Date(nb.Year(), nb.Month(), nb.Day(), tt.Hour(), tt.Minute(), 0, 0, bishkek)
		for _, due := range []time.Time{dueToday.AddDate(0, 0, -1), dueToday} {
			dday := due.In(bishkek).Format("2006-01-02")
			if now.Before(due) || alreadyMinted(cfg, key, dday) {
				continue
			}
			*dirty = true
			markMinted(cfg, key, dday)
			switch {
			case cfg.SweepOff[s.Agent]:
			case now.After(due.Add(sweepGrace)):
				logf("automation %s: %s long past (downtime?), skipping", t.ID, due.Format("15:04"))
			case liveAutomationRun(t.ID) != "":
				prev := liveAutomationRun(t.ID)
				logf("automation %s: previous run %s still live — overlap-skip", t.ID, prev)
				if obsDB != nil {
					raiseAlert(obsDB, s.Agent, prev, "", "automation-overlap",
						fmt.Sprintf("scheduled automation %q skipped its %s firing: previous run still live", t.ID, s.Time),
						due.Unix(), now.Unix())
				}
			default:
				spec := flowSpec{
					Agent: s.Agent, Repo: s.Repo, Goal: s.Goal, Guidance: s.Guidance,
					Acceptance: s.Acceptance, Template: t.ID,
					FastHandoff: s.FastHandoff, MaxConcurrent: s.MaxConcurrent,
				}
				if s.DeadlineMinutes > 0 {
					d := due.Add(time.Duration(s.DeadlineMinutes) * time.Minute)
					if d.Before(now.Add(runMinutesFloor * time.Minute)) {
						logf("automation %s: window over (due %s + %dm), skipping", t.ID, due.Format("15:04"), s.DeadlineMinutes)
						continue
					}
					spec.Deadline = &d
				}
				f, err := createFlowLocked(spec, now)
				if err != nil {
					logf("automation %s: run creation failed: %v", t.ID, err)
					continue
				}
				slog.Default().Info("automation.run_created", "template", t.ID, "agent", s.Agent, "flow_id", f.ID)
			}
		}
	}
}

// liveAutomationRun returns the id of a non-terminal run of the given
// automation, or "".
func liveAutomationRun(templateID string) string {
	for _, f := range loadFlows() {
		if f.Template == templateID && (f.Status == "queued" || f.Status == "running") {
			return f.ID
		}
	}
	return ""
}

// ---- agent spool ----------------------------------------------------------------

const (
	spoolCreateMax = 64 * 1024
	spoolAppendMax = 4 * 1024
)

// spoolResult reports a request's outcome back to the agent that queued it —
// the CLI polls <name>.result after dropping a request. Best-effort.
func spoolResult(agent, name, outcome string) {
	dir := flowUpdatesDir(agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name+".result"), []byte(outcome+"\n"), 0o644)
}

// applyFlowSpoolLocked consumes the cwd-scoped requests written by the
// agent-side nightshift-flow CLI: deadline updates (legacy), guidance edits,
// role appends onto a run's tail (budget-enforced), and run creation. Every
// request is validated exactly like the HTTP path; a bad request is rejected
// with a .result reason, never half-applied. Caller holds nsMu.
func applyFlowSpoolLocked(now time.Time) {
	applyFlowDeadlineUpdatesLocked(now)
	for _, agent := range companies {
		dir := flowUpdatesDir(agent)

		// Run creation: create-<nonce>.json (a flowSpec; agent forced to cwd owner).
		creates, _ := filepath.Glob(filepath.Join(dir, "create-*.json"))
		for _, p := range creates {
			name := strings.TrimSuffix(filepath.Base(p), ".json")
			outcome := func() string {
				b, err := os.ReadFile(p)
				if err != nil || len(b) > spoolCreateMax {
					return "rejected: unreadable or too large"
				}
				var spec flowSpec
				if err := json.Unmarshal(b, &spec); err != nil {
					return "rejected: bad json: " + err.Error()
				}
				spec.Agent = agent // the cwd scope is the authority, not the payload
				if deadline := struct {
					Deadline string `json:"deadline"`
				}{}; json.Unmarshal(b, &deadline) == nil && deadline.Deadline != "" {
					d, err := time.Parse(time.RFC3339, deadline.Deadline)
					if err != nil || d.Before(now.Add(runMinutesFloor*time.Minute)) {
						return "rejected: deadline must be RFC3339 and ≥10m ahead"
					}
					spec.Deadline = &d
				}
				f, err := createFlowLocked(spec, now)
				if err != nil {
					return "rejected: " + err.Error()
				}
				logf("spool %s: created run %s (%s)", agent, f.ID, f.Goal)
				return "ok " + f.ID
			}()
			_ = os.Remove(p)
			spoolResult(agent, name, outcome)
		}

		// Guidance edits: <flowid>.guidance-<nonce> (plain text; unstarted nodes
		// only — setFlowGuidanceLocked never rewrites a started job's prompt).
		// Every request carries its own nonce so two concurrent sessions can't
		// overwrite each other's request or consume the other's verdict (PR #57
		// review); the nonce-less legacy names stay accepted.
		guides, _ := filepath.Glob(filepath.Join(dir, "*.guidance*"))
		for _, p := range guides {
			name := filepath.Base(p)
			if strings.HasSuffix(name, ".result") || strings.Contains(name, ".tmp.") {
				continue
			}
			id, _, _ := strings.Cut(name, ".guidance")
			outcome := func() string {
				f, err := loadFlow(id)
				if err != nil || f.Agent != agent {
					return "rejected: unknown flow or wrong agent"
				}
				// A terminal run has no unstarted nodes to steer, and saveFlow
				// would bump f.Updated, resetting the janitor cleanup clocks — an
				// agent could keep a blocked run's worktree from ever being reaped
				// by re-spooling guidance. Mirror the append path's guard.
				switch f.Status {
				case "complete", "stopped", "blocked", "deadline":
					return "rejected: run is terminal (" + f.Status + ")"
				}
				b, err := os.ReadFile(p)
				if err != nil || len(b) > flowGuidanceMax {
					return "rejected: unreadable or too long"
				}
				setFlowGuidanceLocked(&f, strings.TrimSpace(string(b)))
				if err := saveFlow(f); err != nil {
					return "rejected: " + err.Error()
				}
				return "ok " + id
			}()
			_ = os.Remove(p)
			spoolResult(agent, name, outcome)
		}

		// Appends: <flowid>.append-<nonce>.json {"roles":[...]} — sequential
		// nodes gated on the run's current tail, inside the lifetime session
		// budget. Nonce'd like guidance; legacy <flowid>.append.json accepted.
		appends, _ := filepath.Glob(filepath.Join(dir, "*.append*.json"))
		for _, p := range appends {
			name := strings.TrimSuffix(filepath.Base(p), ".json")
			if strings.Contains(name, ".tmp.") {
				continue
			}
			id, _, _ := strings.Cut(name, ".append")
			outcome := func() string {
				f, err := loadFlow(id)
				if err != nil || f.Agent != agent {
					return "rejected: unknown flow or wrong agent"
				}
				switch f.Status {
				case "complete", "stopped", "blocked", "deadline":
					return "rejected: run is terminal (" + f.Status + ")"
				}
				b, err := os.ReadFile(p)
				if err != nil || len(b) > spoolAppendMax {
					return "rejected: unreadable or too large"
				}
				var in struct {
					Roles []string `json:"roles"`
				}
				if err := json.Unmarshal(b, &in); err != nil || len(in.Roles) == 0 {
					return "rejected: want {\"roles\": [..]}"
				}
				if err := validateNodeRoles(in.Roles); err != nil {
					return "rejected: " + err.Error()
				}
				if len(f.Nodes) == 0 {
					return "rejected: run has no nodes"
				}
				// Append after the ACCEPTANCE node (the run's real tail): with a
				// runtime fan-out the last slot is a fan-in something still consumes.
				if err := insertAfterNode(&f, acceptanceNodeID(f), in.Roles, now); err != nil {
					return "rejected: " + err.Error()
				}
				if err := saveFlow(f); err != nil {
					return "rejected: " + err.Error()
				}
				logf("spool %s: appended %v to run %s", agent, in.Roles, id)
				return "ok " + id
			}()
			_ = os.Remove(p)
			spoolResult(agent, name, outcome)
		}

		// Reap stale result files so the spool dir can't grow without bound.
		results, _ := filepath.Glob(filepath.Join(dir, "*.result"))
		for _, p := range results {
			if info, err := os.Stat(p); err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
				_ = os.Remove(p)
			}
		}
	}
}
