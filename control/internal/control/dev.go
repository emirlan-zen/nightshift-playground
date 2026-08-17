package control

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nightshift/control/internal/machine"
)

// Dev mode makes the control plane safe to run OFF the box, so agents can build
// and PR against it without ever touching prod. Enabled with NIGHTSHIFT_DEV=1
// (see the Makefile `dev` target). It:
//
//   - rewires the execCommand seam to an in-memory stub, so `sudo
//     nightshift-rc` is never invoked — no systemd unit is ever started/stopped;
//   - disables the autonomous goroutines (scheduler / auth-checker / ingester)
//     in main(), the load-bearing safety: the scheduler otherwise fires real
//     night runs on a 30s tick;
//   - auto-trusts auth (verifyAccess returns a dev email) so no Cloudflare
//     Access header is needed behind the Vite dev proxy;
//   - seeds a scratch HOME with sample jobs/tickets/reports so the UI is
//     populated.
//
// NEVER set NIGHTSHIFT_DEV on the box.
var devMode = runtimeConfig.DevMode

const devEmail = "dev@nightshift.local"

// installDevMode swaps in the stub wrapper and seeds sample data. Called from
// main() before the (skipped) goroutines.
func installDevMode() {
	execCommand = devRC
	devSeedSessions()
	seedDevData()
	// One-shot forge probe against the stub so the Health tab + banner render
	// forge rows in dev (the auth-checker ticker that owns this in prod is OFF).
	refreshForge()
	seedCodexHealth()
}

// seedCodexHealth writes the snapshot codex-auth-probe leaves at pre-flight
// in prod (ADR-0018), so the Health tab renders the codex executor row under
// `make dev`. Seed-once like the rest of the dev HOME.
func seedCodexHealth() {
	p := filepath.Join(nsDir(), "health", "codex.json")
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	doc := fmt.Sprintf(`{"ok":true,"detail":"ok","checkedAt":"%s"}`+"\n",
		time.Now().Add(-40*time.Minute).Format(time.RFC3339))
	_ = os.WriteFile(p, []byte(doc), 0o644)
}

// devVitals stands in for readVitals off the box, where /proc is absent (darwin)
// so the real reader would report OK=false with zeroed counters. Representative
// numbers for a cpx42 (8 vCPU / 16 GiB / 160 GB) keep the Health tab's Machine
// card populated under `make dev`.
func devVitals() machine.Vitals {
	const gib = 1 << 30
	return machine.Vitals{
		Generated:      time.Now().Unix(),
		UptimeSec:      6*86400 + 4*3600 + 12*60,
		Load1:          1.8,
		Load5:          2.3,
		Load15:         1.4,
		CPUCount:       8,
		MemTotalBytes:  16 * gib,
		MemUsedBytes:   9*gib + gib/2, // ~9.5 GiB
		SwapTotalBytes: 0,
		SwapUsedBytes:  0,
		DiskTotalBytes: 160 * gib,
		DiskUsedBytes:  71 * gib,
		OK:             true,
	}
}

// devExitError fabricates a probe exit code through the execCommand seam
// (forgeProbe unwraps anything with an ExitCode() — see authcheck.go).
type devExitError struct{ code int }

func (e *devExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *devExitError) ExitCode() int { return e.code }

// ---- in-memory session registry (stands in for systemd) --------------------

var (
	devMu       sync.Mutex
	devSessions = map[string]bool{} // instance -> active
)

func devSeedSessions() {
	devMu.Lock()
	defer devMu.Unlock()
	devSessions["playground"] = true          // the default session
	devSessions["playground__nightly"] = true // a named slot up
}

// devRC answers a `sudo <wrapper> <verb> <target...>` invocation entirely in
// memory. Signature matches the execCommand seam. It also stubs the
// forge-auth-probe path (invoked directly, not via sudo).
func devRC(name string, args ...string) (string, error) {
	if name == forgeProbeScript {
		c := ""
		if len(args) > 0 {
			c = args[0]
		}
		return `{"company":"` + c + `","ok":true,"detail":"token ok [dev stub]"}`, nil
	}
	if len(args) < 2 {
		return "", nil
	}
	verb := args[1]
	target := args[2:]
	devMu.Lock()
	defer devMu.Unlock()
	switch verb {
	case "sessions":
		if len(target) == 0 {
			return "", nil
		}
		c := target[0]
		var b strings.Builder
		for inst, active := range devSessions {
			if inst != c && !strings.HasPrefix(inst, c+"__") {
				continue
			}
			st := "inactive"
			if active {
				st = "active"
			}
			b.WriteString(inst + " " + st + "\n")
		}
		return b.String(), nil
	case "ttl":
		if len(target) == 0 || !devSessions[target[0]] {
			return "0", nil
		}
		return strconv.FormatInt(time.Now().Add(8*time.Hour).Unix(), 10), nil
	case "start":
		if len(target) > 0 {
			devSessions[target[0]] = true
		}
		return "started (dev)", nil
	case "stop":
		if len(target) > 0 {
			delete(devSessions, target[0])
		}
		return "stopped (dev)", nil
	case "stop-run":
		return "stopped run (dev)", nil
	case "run-status":
		// the seeded "running" night runs report active, so the Night tab shows a
		// live run + its auto-stop countdown; everything else is inactive.
		if len(target) > 1 && (target[1] == devRunningRunID || target[1] == devPlayExecID) {
			return "active", nil
		}
		return "inactive", nil
	default:
		return "", nil
	}
}

// ---- seed data --------------------------------------------------------------

// seedDevData writes a small, believable dataset under the scratch HOME so the
// UI has something to render. Idempotent: the file/DB state persists across dev
// runs, so a fresh HOME seeds everything once and a warm HOME only re-opens the
// obs store (whose handle the ingester goroutine — OFF in dev — would normally
// own).
func seedDevData() {
	// Decide freshness BEFORE any MkdirAll so opening the obs store (which needs
	// nsDir to exist) can't fool the guard into thinking data is already present.
	fresh := false
	if _, err := os.Stat(nsDir()); err != nil {
		fresh = true
	}

	if fresh {
		seedNightData()
		seedPromptFiles()
		seedTranscripts(time.Now())
	}

	// Focus files seed even on a warm HOME (feature added after dev homes
	// existed) — but only if missing, never clobbering an operator's dev edits.
	seedFocusFiles()
	// Scout's ideas backlog so the Focus tab's promote surface renders populated.
	seedIdeaFiles()
	// Pipeline profiles + a retro proposal (ADR-0014), likewise if-missing so the
	// Pipeline tab renders populated under `make dev`.
	seedProfiles()
	seedNodeFiles()
	// First-class task flows (ADR-0015), seeded independently of the older night
	// fixtures so a warm dev home gains the new Home/Flows experience too.
	seedFlows()

	// The Health tab reads the derived obs store (observability.db) through the
	// obsDB handle. In prod the ingester goroutine holds it; in dev that
	// goroutine is OFF, so open it here — for a warm HOME too, so Health keeps
	// working without re-seeding. The DB persists; ingest is idempotent.
	if obsDB == nil {
		if err := os.MkdirAll(nsDir(), 0o755); err == nil {
			if db, err := openObsDB(); err == nil {
				obsDB = db
			}
		}
	}
	// Fold the freshly-seeded transcripts + jobs into the store once (reusing the
	// real ingester + detectors rather than hand-writing SQL). On a warm HOME the
	// rows already persist and the byte-offset cursors make this a no-op anyway.
	if fresh && obsDB != nil {
		_ = ingestOnce(obsDB)
		runDetectors(obsDB)
	}
	// Scores (ADR-0023) live in the obs store, so they seed AFTER it opens.
	// Keyed off the emitted run seedFlows just created; insertScore upserts, so
	// a warm HOME re-seeds the same rows rather than doubling an average.
	if obsDB != nil {
		for _, f := range loadFlows() {
			if f.Template == "explore-attempts" {
				seedDevScores(f, time.Now())
				break
			}
		}
	}
}

func seedNodeFiles() {
	_ = os.MkdirAll(nodesDir(), 0o755)
	for id, body := range defaultNodePrompts {
		p := filepath.Join(nodesDir(), id+".md")
		if _, err := os.Stat(p); os.IsNotExist(err) {
			_ = os.WriteFile(p, []byte("# Node · "+nodeDefinitions[id].Name+"\n\n"+body+"\n"), 0o644)
		}
	}
}

func seedFlows() {
	if len(loadFlows()) > 0 {
		return
	}
	now := time.Now()
	deadline := now.Add(9 * time.Hour)
	f := flow{
		ID:    "flow-" + now.In(bishkek).Format("20060102-1504") + "-a1b2c3d4",
		Agent: "playground", Repo: "example-project", Goal: "Add a verified approval workflow",
		Acceptance: []string{"Backend and web paths are test-green", "A populated preview proves approval and audit behavior", "Draft pull request is ready for review"},
		Template:   "full-delivery", NodeRoles: []string{"refine", "plan", "implement", "review", "amend", "validate"},
		Deadline: &deadline, Created: now.Add(-95 * time.Minute), Updated: now, Status: "running",
		Batch: "flow-dev-example", Base: "origin/main",
	}
	_ = prepareFlowWorktree(&f)
	_ = mintFlow(&f, now.Add(-95*time.Minute))
	if len(f.Nodes) >= 2 {
		_ = os.MkdirAll(reportsDir(f.Agent), 0o755)
		_ = os.WriteFile(reportPath(f.Agent, f.Nodes[0].JobID), []byte("---\nflow_status: complete\n---\n# ADR refined\nDecisions and build delta recorded.\n"), 0o644)
		if j, ok := findFlowJob(f, f.Nodes[1].JobID); ok {
			j.Gated, j.After, j.Started = false, nil, true
			j.StartedAt = now.Add(-22 * time.Minute)
			_ = saveJob(j)
		}
	}
	_ = saveFlow(f)

	done := flow{
		ID:    "flow-" + now.Add(-24*time.Hour).In(bishkek).Format("20060102-1504") + "-e5f6a7b8",
		Agent: "playground", Repo: "sample-cli", Goal: "Make update checks resilient to offline startup",
		Acceptance: []string{"Regression test covers offline startup", "CI passes"},
		Template:   "build-feature", NodeRoles: []string{"plan", "implement", "review", "amend", "validate"},
		Created: now.Add(-24 * time.Hour), Updated: now.Add(-22 * time.Hour), Status: "complete",
		Batch: "flow-dev-done", Base: "main", Branch: "nightshift/offline-update", WorktreeState: "cleaned",
	}
	for i, role := range done.NodeRoles {
		id := role
		jid := fmt.Sprintf("20260708-%04d-flow-%04x", 1000+i, 0x1000+i)
		done.Nodes = append(done.Nodes, flowNodeRun{ID: id, Role: role, JobID: jid, AfterID: func() string {
			if i > 0 {
				return done.NodeRoles[i-1]
			}
			return ""
		}()})
		_ = os.MkdirAll(reportsDir(done.Agent), 0o755)
		status := ""
		if role == "validate" {
			status = "flow_status: complete\n"
		}
		_ = os.WriteFile(reportPath(done.Agent, jid), []byte("---\n"+status+"---\n# "+role+" complete\n"), 0o644)
	}
	done.LastResultJob = done.Nodes[len(done.Nodes)-1].JobID
	_ = saveFlow(done)

	// ADR-0017 surfaces: a custom node definition, a parallel fan-out run with
	// per-member worktrees + verdicts, and definition proposals for the inbox.
	_ = saveCustomNodeDef(nodeDefinition{
		ID: "security-audit", Name: "Security audit",
		Description: "Adversarial security pass over the run branch.",
		Effort:      "xhigh", Minutes: 120, Output: "Severity-ranked findings report", Icon: "scout",
	})
	auditPrompt := filepath.Join(nodesDir(), "security-audit.md")
	if _, err := os.Stat(auditPrompt); os.IsNotExist(err) {
		_ = os.WriteFile(auditPrompt, []byte("# Node · Security audit\n\nAudit the run branch for injection, authz, and secret-handling flaws. Report\nseverity-ranked findings with evidence and a concrete fix each; change nothing.\n"), 0o644)
	}

	fan := flow{
		ID:    "flow-" + now.Add(-3*time.Hour).In(bishkek).Format("20060102-1504") + "-fa17b3c9",
		Agent: "playground", Repo: "sample-app", Goal: "Split the notifier: API half and worker half in parallel, then integrate",
		Acceptance: []string{"Both halves land on one green branch", "Integration suite passes"},
		Stages:     [][]string{{"plan"}, {"implement", "implement"}, {"integrate"}, {"security-audit"}, {"validate"}},
		Edges:      []routeEdge{{Node: "security-audit", Verdict: "needs-work", Append: []string{"amend"}}},
		NodeRoles:  []string{"plan", "implement", "implement", "integrate", "security-audit", "validate"},
		Created:    now.Add(-3 * time.Hour), Updated: now, Status: "running",
		Batch: "flow-dev-fan", Base: "origin/main",
	}
	_ = prepareFlowWorktree(&fan)
	_ = mintFlow(&fan, now.Add(-3*time.Hour))
	if len(fan.Nodes) >= 3 {
		_ = os.WriteFile(reportPath(fan.Agent, fan.Nodes[0].JobID), []byte("---\nverdict: ok\n---\n# Plan\nHalves split with a stable interface between them.\n"), 0o644)
		fan.Nodes[0].VerdictSeen = "ok"
		for i := 1; i <= 2; i++ {
			if j, ok := findFlowJob(fan, fan.Nodes[i].JobID); ok {
				j.Gated, j.After, j.Started = false, nil, true
				j.StartedAt = now.Add(time.Duration(-40+i*3) * time.Minute)
				_ = saveJob(j)
			}
		}
	}
	_ = saveFlow(fan)

	_ = os.MkdirAll(proposalsDir(), 0o755)
	if _, err := os.Stat(proposalPath("harden-review-node")); os.IsNotExist(err) {
		prop, _ := json.MarshalIndent(changeProposal{
			Type: "node-prompt", Target: "review",
			Why:  "Run ledger: 4 of 5 runs this week needed 2+ amend rounds after review passed the branch — the prompt under-weights regression evidence.",
			Body: "# Node · Review\n\nReview the actual diff and artifacts with fresh context. Run relevant tests and\nrecord each actionable finding with severity, location, evidence, concrete fix,\nand regression requirement. Reject any finding you cannot reproduce or evidence.\nDo not dilute the review by doing substantive fixes.\n",
		}, "", "  ")
		_ = os.WriteFile(proposalPath("harden-review-node"), prop, 0o644)
	}
	if _, err := os.Stat(proposalPath("hardened-delivery-template")); os.IsNotExist(err) {
		prop, _ := json.MarshalIndent(changeProposal{
			Type: "template",
			Why:  "Three preview nodes found blockers with no route back; this template declares the edge.",
			Template: &flowTemplate{
				ID: "verified-delivery", Name: "Verified delivery",
				Description: "Full delivery with a preview blocker route back to implementation.",
				Stages:      [][]string{{"plan"}, {"implement"}, {"preview"}, {"review"}, {"amend"}, {"validate"}},
				Edges:       []routeEdge{{Node: "preview", Verdict: "needs-work", Append: []string{"implement"}}},
			},
		}, "", "  ")
		_ = os.WriteFile(proposalPath("hardened-delivery-template"), prop, 0o644)
	}

	// ADR-0019 surfaces: a SCHEDULED automation (recurring template), and a
	// blocked run with a retained worktree (the janitor-escalation state).
	if _, err := os.Stat(flowTemplatePath("nightly-triage")); os.IsNotExist(err) {
		_ = os.MkdirAll(flowTemplatesDir(), 0o755)
		tpl, _ := json.MarshalIndent(flowTemplate{
			ID: "nightly-triage", Name: "Nightly triage", Description: "Investigate the day's incident queue and verify conclusions.",
			Nodes: []string{"investigate", "review", "validate"},
			Schedule: &templateSchedule{
				Agent: "playground", Repo: "sample-cli", Goal: "Triage yesterday's incident queue and verify each conclusion",
				Time: "02:30", DeadlineMinutes: 330, MaxConcurrent: 1,
			},
		}, "", "  ")
		_ = os.WriteFile(flowTemplatePath("nightly-triage"), tpl, 0o644)
	}
	// ADR-0023 surfaces: a run whose emitter node fanned out at RUNTIME (two
	// emitted children + their judge fan-in), and the scores that judge
	// recorded — the Automations scores table and the run-graph "emitted" chip
	// render from exactly this.
	emitted := flow{
		ID:    "flow-" + now.Add(-5*time.Hour).In(bishkek).Format("20060102-1504") + "-e311ced0",
		Agent: "playground", Repo: "sample-cli", Goal: "Find the fastest safe fix for the cold-start regression",
		Acceptance: []string{"One approach measurably wins", "The winner is merged behind tests"},
		Template:   "explore-attempts",
		Stages:     [][]string{{"plan"}, {"validate"}},
		NodeRoles:  []string{"plan", "validate"},
		Emitters:   []emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}},
		Created:    now.Add(-5 * time.Hour), Updated: now, Status: "running",
		Batch: "flow-dev-emit", Base: "origin/main",
	}
	_ = prepareFlowWorktree(&emitted)
	_ = mintFlow(&emitted, now.Add(-5*time.Hour))
	// Persist BEFORE reconciling: reconcileFlowsLocked reads flows from disk, so
	// an unsaved run is invisible to it and the emission never happens.
	_ = saveFlow(emitted)
	if len(emitted.Nodes) >= 2 {
		_ = os.WriteFile(reportPath(emitted.Agent, emitted.Nodes[0].JobID), []byte(
			"---\nverdict: ok\n---\n# Plan\nThree independent approaches are worth trying.\n\n## Emit\n- role: attempt\n  input: |\n    Cache the parsed manifest.\n- role: attempt\n  input: |\n    Lazy-load the plugin registry.\n"), 0o644)
		reconcileFlowsLocked(now.Add(-4 * time.Hour))
		if f, err := loadFlow(emitted.ID); err == nil {
			emitted = f
			for _, n := range emitted.Nodes {
				if n.EmittedBy == "" {
					continue
				}
				if j, ok := findFlowJob(emitted, n.JobID); ok {
					j.Gated, j.After, j.Started = false, nil, true
					j.StartedAt = now.Add(-3 * time.Hour)
					_ = saveJob(j)
				}
				_ = os.WriteFile(reportPath(emitted.Agent, n.JobID), []byte(
					"---\nverdict: ok\n---\n# Attempt\nImplemented and measured.\n"), 0o644)
			}
			_ = saveFlow(emitted)
		}
	}
	retained := flow{
		ID:    "flow-" + now.Add(-72*time.Hour).In(bishkek).Format("20060102-1504") + "-0e7a11ed",
		Agent: "playground", Repo: "sample-app", Goal: "Migrate the notifier config to the new schema",
		Acceptance: []string{"Migration applies cleanly", "Rollback path recorded"},
		Template:   "build-feature", NodeRoles: []string{"plan", "implement", "validate"},
		Created: now.Add(-72 * time.Hour), Updated: now.Add(-70 * time.Hour), Status: "blocked",
		Batch: "flow-dev-ret", Base: "origin/main", Branch: "nightshift/notifier-migration",
		WorktreeState:  "retained",
		CleanupMessage: "branch contains unpushed commits — operator decision needed",
	}
	if _, err := os.Stat(flowPath(retained.ID)); os.IsNotExist(err) {
		b, _ := json.MarshalIndent(retained, "", "  ") // direct write keeps the old Updated
		_ = os.MkdirAll(flowsDir(), 0o755)
		_ = os.WriteFile(flowPath(retained.ID), b, 0o644)
	}
}

// seedDevScores writes an eval history for one automation across TWO prompt
// revisions — the shape the scores table reads: an improving average, an
// `unknown` that must not count as a zero, and two engines judged side by side.
func seedDevScores(f flow, now time.Time) {
	judgeNode := "judge-r1"
	for _, n := range f.Nodes {
		if n.Role == "judge" {
			judgeNode = n.ID
		}
	}
	type seed struct {
		run       string
		rev       string
		subject   string
		dimension string
		score     *int
		rationale string
		subjExec  string
		subjModel string
		ageHours  int
	}
	four, five, three := 4, 5, 3
	seeds := []seed{
		{f.ID, "b41c07", "attempt-r1", "correctness", &five, "both suites green; benchmark reproduced", "claude", "claude-opus-5", 3},
		{f.ID, "b41c07", "attempt-2-r1", "correctness", &four, "works, but the registry path is untested", "codex", "gpt-5.6-sol", 3},
		{f.ID, "b41c07", "attempt-r1", "cost", nil, "no comparable timing captured for either attempt", "claude", "claude-opus-5", 3},
		{"flow-20260814-2300-9f1e2d3c", "9ac31d", "attempt", "correctness", &three, "passes, but leans on one unasserted fixture", "claude", "claude-opus-5", 60},
		{"flow-20260814-2300-9f1e2d3c", "9ac31d", "attempt", "cost", &three, "twice the sessions for the same delta", "claude", "claude-opus-5", 60},
		{"flow-20260813-2300-77aa11bb", "9ac31d", "attempt", "correctness", &four, "clean, though the error path is unexercised", "codex", "gpt-5.6-sol", 84},
	}
	for _, s := range seeds {
		ts := now.Add(-time.Duration(s.ageHours) * time.Hour)
		_ = insertScore(obsDB, scoreRow{
			Night: nightKeyOf(ts), RunID: s.run, JudgeNode: judgeNode, Subject: s.subject,
			Dimension: s.dimension, Score: s.score, Max: 5, Rationale: s.rationale,
			AutomationID: "explore-attempts", AutomationRevision: f.AutomationRevision,
			SubjectPromptRev: s.rev, JudgeExecutor: "claude", JudgeModel: "claude-opus-5",
			SubjectExecutor: s.subjExec, SubjectModel: s.subjModel, CreatedAt: ts.Unix(),
		})
	}
}

// seedNightData: sweeps config, a couple of tickets, and night runs of MIXED
// status so the Night + Health ledgers show delivered AND no-deliverable rows
// (the latter also trips the no-deliverable detector → an open alert).
func seedNightData() {
	now := time.Now().In(bishkek)

	// sweeps: default ON everywhere (config auto-creates on save).
	_ = saveConfig(nightConfig{SweepOff: map[string]bool{}, LastSweep: map[string]string{}})

	agent := "playground"
	if len(companies) > 0 {
		agent = companies[0]
	}
	_ = saveTicket(ticket{
		ID: "20260705-0900-tkt-a1b2", Agent: agent, Title: "Wire up dev-mode seed data",
		Body: "Sample open ticket so the board isn't empty in dev.", Status: "open", Lane: "improve",
		CreatedBy: "operator", Created: now.Add(-6 * time.Hour), Updated: now.Add(-6 * time.Hour),
	})
	// a claim sidecar so the board shows the "claimed · sched:…" indicator (1.10)
	writeTicketClaim(agent, "20260705-0900-tkt-a1b2", devRunningRunID, now.Add(-time.Hour))
	_ = saveTicket(ticket{
		ID: "20260705-0930-tkt-c3d4", Agent: agent, Title: "Refactor the Servers tab",
		Body: "In-review sample.", Status: "review", Lane: "hunt", CreatedBy: agent,
		Created: now.Add(-5 * time.Hour), Updated: now.Add(-time.Hour),
		Notes: []ticketNote{{At: now.Add(-time.Hour), By: agent, Text: "PR opened: draft #123"}},
	})

	// delivered: a sweep that finished and wrote a morning report. The banner
	// frontmatter drives the deterministic banner overlay — with no agy plate in
	// dev, /api/report/banner renders the typographic card straight from it.
	id := "20260705-0500-sweep-ef56"
	report := "---\n" +
		"banner_wave: sweep\n" +
		"banner_tone: shipped\n" +
		"banner_headline: Two PRs reviewed\n" +
		"banner_stats: PRs 2 | Tickets 1 | Alerts 0\n" +
		"---\n" +
		"# Nightly sweep — " + agent + "\n\n" +
		"_Dev-mode sample report._\n\n" +
		"- Reviewed 2 PRs\n- Filed 1 ticket\n- Quiet vigil otherwise\n"
	writeSeedFile(reportsDir(agent), id+".md", report)
	_ = saveJob(job{
		ID: id, Agent: agent, Prompt: "daily sweep", At: now.Add(-4 * time.Hour),
		Kind: "sweep", Label: "sweep", Minutes: 480, Created: now.Add(-4 * time.Hour),
		Started: true, StartedAt: now.Add(-4 * time.Hour),
	})

	// running: a live night run, so the Night tab shows the "running" badge + an
	// auto-stop countdown (StartedAt + Minutes; the rc stub reports it active).
	_ = saveJob(job{
		ID: devRunningRunID, Agent: agent, Prompt: "deep improvement work", At: now.Add(-time.Hour),
		Kind: "sweep", Label: "exec · self-directed", Effort: "xhigh", Minutes: 420,
		Created: now.Add(-time.Hour), Started: true, StartedAt: now.Add(-time.Hour),
	})

	// no-deliverable: a run started > runMaxLife (8h) ago with NO report — the
	// auth-fail night's signature. detectNoDeliverable raises a standing alert.
	ndID := "20260704-0500-sweep-9a7c"
	_ = saveJob(job{
		ID: ndID, Agent: agent, Prompt: "daily sweep", At: now.Add(-10 * time.Hour),
		Kind: "sweep", Label: "sweep", Minutes: 480, Created: now.Add(-10 * time.Hour),
		Started: true, StartedAt: now.Add(-10 * time.Hour),
	})

	seedPlaygroundNight(now)
}

// seedPlaygroundNight lays down a few pipeline waves under the playground agent
// (when present) so the Night tab's wave timeline (2.2) and the Morning view's
// synth brief (2.1) render populated in dev. Labels match the built-in slot
// names so jobsForWave pairs them.
func seedPlaygroundNight(now time.Time) {
	pg := ""
	for _, c := range companies {
		if c == "playground" {
			pg = c
		}
	}
	if pg == "" {
		return
	}
	// medic — delivered
	_ = saveJob(job{
		ID: "20260705-2300-medic-1a2b", Agent: pg, Prompt: "pre-flight", At: now.Add(-9 * time.Hour),
		Kind: "sweep", Label: "medic", Effort: "medium", Minutes: 50, Created: now.Add(-9 * time.Hour),
		Started: true, StartedAt: now.Add(-9 * time.Hour),
	})
	// exec — running (rc stub reports it active alongside devRunningRunID)
	_ = saveJob(job{
		ID: devPlayExecID, Agent: pg, Prompt: "deep improvement", At: now.Add(-7 * time.Hour),
		Kind: "sweep", Label: "exec · improve · deepen tests", Effort: "xhigh", Minutes: 420,
		Created: now.Add(-7 * time.Hour), Started: true, StartedAt: now.Add(-2 * time.Hour),
	})
	// synth — delivered, and its report is the Morning brief.
	synthID := "20260706-0800-synth-7c8d"
	synth := "---\n" +
		"banner_wave: synth\n" +
		"banner_tone: shipped\n" +
		"banner_headline: 4 PRs merged overnight\n" +
		"banner_stats: PRs 4 | Experiments 2 | Budget 61%\n" +
		"---\n" +
		"# Morning brief — playground\n\n" +
		"_Dev-mode synth brief._\n\n" +
		"## Merge queue\n- sample-cli: 3 merged, 1 in review\n- sample-app: 1 deployed, verified live\n\n" +
		"## Experiment scoreboard\n- sample-cli-alerts waitlist: 12 signups (go)\n\n" +
		"## Budget\n$612 / $1800 (34%), 0 top-ups.\n"
	writeSeedFile(reportsDir(pg), synthID+".md", synth)
	_ = saveJob(job{
		ID: synthID, Agent: pg, Prompt: "morning brief", At: now.Add(-time.Hour),
		Kind: "sweep", Label: "synth", Effort: "medium", Minutes: 45, Created: now.Add(-time.Hour),
		Started: true, StartedAt: now.Add(-time.Hour),
	})

	// benchmark — dep-skipped (ADR-0014): its upstream produced no output. Feeds
	// the dep-skip banner, the Night "dep-skip" chip, and the Health surface.
	depID := "20260706-0130-dep-cc33"
	_ = saveJob(job{
		ID: depID, Agent: pg, Prompt: "benchmark the optimized build", At: now.Add(-6 * time.Hour),
		Kind: "sweep", Label: "benchmark", Effort: "high", Minutes: 120, Created: now.Add(-7 * time.Hour),
		Batch: "night-2026-07-05", After: []string{"analyze"}, Skipped: true,
	})
	writeSeedFile(reportsDir(pg), depID+".depskip", "upstream analyze failed (no report)")
}

// devRunningRunID is a seeded run the rc stub reports "active", so dev exercises
// the running-badge + auto-stop countdown (1.6/1.7). devPlayExecID is a second
// live run under playground so the wave timeline shows a running exec (2.2).
const (
	devRunningRunID = "20260706-0300-run-aa11"
	devPlayExecID   = "20260706-0100-exec-bb22"
)

// seedFocusFiles writes sample operator north-star files (ADR-0008) so the
// Focus tab renders populated. Per-file if-missing guard: the seed never
// overwrites an edit saved through the tab.
func seedFocusFiles() {
	dir := filepath.Join(nsDir(), "focus")
	for id, body := range map[string]string{
		"products": devFocusProducts,
		"projects": devFocusProjects,
	} {
		if _, err := os.Stat(filepath.Join(dir, id+".md")); err == nil {
			continue
		}
		writeSeedFile(dir, id+".md", body)
	}
}

// seedIdeaFiles seeds a couple of scout ideas so the Focus tab's ideas→promote
// surface renders under `make dev`. If-missing per file.
func seedIdeaFiles() {
	dir := filepath.Join(nsDir(), "research", "ideas")
	for id, body := range map[string]string{
		"2026-07-10": "# Offline-first note capture\n\nA tiny PWA that queues notes locally and syncs when online. EV: low build cost, clear demand signal from the r/productivity threads.\n",
		"2026-07-11": "# CLI usage-analytics SaaS\n\nDrop-in telemetry for OSS CLIs — opt-in, privacy-first. Validation: land a waitlist page + measure sign-ups per 100 GitHub stars.\n",
	} {
		if _, err := os.Stat(filepath.Join(dir, id+".md")); err == nil {
			continue
		}
		writeSeedFile(dir, id+".md", body)
	}
}

// seedProfiles writes a few pipeline profiles (ADR-0014) — including an `away`
// one and a fan-out — plus a retro proposal with a diff, so the Pipeline tab
// renders every surface under `make dev`. If-missing per file, and it only sets
// ActiveProfile when unset so it never clobbers a dev edit.
func seedProfiles() {
	if !isCompany("playground") {
		return
	}
	broad := profile{Name: "broad", Deadline: "08:45", Waves: []profileWave{
		{Name: "medic", Time: "23:00", Prompt: "playground-medic.md", Model: opusModel, Effort: "medium", Minutes: 50, NoTickets: true},
		{Name: "exec", Time: "00:45", Prompt: "playground-exec.md", Model: opusModel, Effort: "xhigh", Minutes: 420, PerTicket: true},
		{Name: "synth", Time: "08:00", Prompt: "playground-synth.md", Model: opusModel, Effort: "medium", Minutes: 45, NoTickets: true},
	}}
	deep := profile{Name: "deep-perf", Deadline: "08:45", Workers: &workerSplit{Hunt: 0, Improve: 6}, Waves: []profileWave{
		{Name: "analyze", Time: "00:00", Prompt: "playground-medic.md", Model: opusModel, Effort: "xhigh", Minutes: 45, NoTickets: true},
		{Name: "optimize", After: []string{"analyze"}, Prompt: "playground-exec.md", Model: opusModel, Effort: "xhigh", Minutes: 300, NoTickets: true},
		{Name: "benchmark", After: []string{"analyze"}, Prompt: "playground-synth.md", Model: opusModel, Effort: "high", Minutes: 120, NoTickets: true},
	}}
	away := profile{Name: "away", Waves: []profileWave{
		{Name: "medic", Time: "10:00", Prompt: "playground-medic.md", Model: opusModel, Effort: "medium", Minutes: 50, NoTickets: true},
		{Name: "exec", Time: "11:00", Prompt: "playground-exec.md", Model: opusModel, Effort: "xhigh", Minutes: 420, PerTicket: true},
		{Name: "synth", Time: "19:00", Prompt: "playground-synth.md", Model: opusModel, Effort: "medium", Minutes: 45, NoTickets: true},
	}}
	for _, p := range []profile{broad, deep, away} {
		if _, err := os.Stat(profilePath(p.Name)); err == nil {
			continue
		}
		if err := saveProfile(p); err != nil {
			logf("dev seed profile %s: %v", p.Name, err)
		}
	}
	if cfg := loadConfig(); cfg.ActiveProfile == "" {
		cfg.ActiveProfile = "broad"
		_ = saveConfig(cfg)
	}

	// A retro proposal with a diff, so the proposal inbox + Apply/Dismiss render.
	prop := profile{Name: "tighter-review", Why: "review ran long twice this week; split it earlier and shorten synth.", Waves: []profileWave{
		{Name: "medic", Time: "23:00", Prompt: "playground-medic.md", Model: opusModel, Effort: "medium", Minutes: 50, NoTickets: true},
		{Name: "exec", Time: "00:45", Prompt: "playground-exec.md", Model: opusModel, Effort: "xhigh", Minutes: 420, PerTicket: true},
		{Name: "review", Time: "06:00", Prompt: "playground-review.md", Model: opusModel, Effort: "high", Minutes: 120, NoTickets: true},
		{Name: "synth", Time: "08:10", Prompt: "playground-synth.md", Model: opusModel, Effort: "medium", Minutes: 30, NoTickets: true},
	}}
	if _, err := os.Stat(proposedPath("tighter-review")); err != nil {
		writeSeedFile(proposedDir(), "tighter-review.json", string(mustJSON(prop)))
	}
}

// seedPromptFiles writes the CLAUDE.md chain + settings.json + a skill so the
// Prompts tab renders real content (Exists=true) for the shared, skills, and
// per-agent groups buildPrompts enumerates.
func seedPromptFiles() {
	writeSeedFile(filepath.Join(home, ".claude"), "CLAUDE.md", devGlobalClaudeMd)
	writeSeedFile(filepath.Join(home, "workspace"), "CLAUDE.md", devWorkspaceClaudeMd)
	writeSeedFile(filepath.Join(home, ".claude"), "settings.json", devSettingsJSON)
	writeSeedFile(filepath.Join(home, ".claude", "skills", "code-review"), "SKILL.md", devSkillMd)
	for _, c := range companies {
		body := "# " + c + " — agent workspace\n\n" +
			"Per-agent overrides for **" + c + "**. Draft PRs only at night; " +
			"Jira read-only; never touch prod. See the workspace CLAUDE.md for the map.\n"
		writeSeedFile(filepath.Join(home, "workspace", c), "CLAUDE.md", body)
	}
}

// devTurn is one assistant turn to synthesise into a transcript. A non-empty
// tool emits a tool_use block plus a following user tool_result (is_error =
// toolErr) so the obs ingester records tool calls + error flags.
type devTurn struct {
	model           string
	in, out, cr, cc int64
	ageMin          int // minutes before now
	tool            string
	toolErr         bool
}

// seedTranscripts writes fake Claude Code JSONL transcripts under each agent's
// project dir. These feed BOTH read-paths: usage.go scans the JSONL directly,
// and the obs ingester folds the same lines into observability.db. Metadata
// only — models, numeric usage, timestamps, tool envelopes; no real content.
func seedTranscripts(now time.Time) {
	if len(companies) == 0 {
		return
	}

	// Agent 0: an Opus sweep that went sideways — 10 tool calls across ~2 days,
	// 6 of them errored (trips the error-storm detector; non-zero error rate).
	a0 := make([]devTurn, 0, 10)
	for i := 0; i < 10; i++ {
		model := "claude-opus-4-8"
		if i%5 == 4 {
			model = "claude-haiku-4-5" // a little cross-model variety
		}
		a0 = append(a0, devTurn{
			model: model, in: 1200 + int64(i*60), out: 600 + int64(i*40),
			cr: 8000, cc: 400, ageMin: 60 + i*300, tool: "Bash", toolErr: i < 6,
		})
	}
	writeSessionTranscript(companies[0], "dev-"+companies[0]+"-sessA", a0, now)

	// Agent 1 (if any): a calmer Sonnet/Opus session — a second agent + models
	// for Usage, one tool error for a modest (non-storm) error rate.
	if len(companies) > 1 {
		a1 := []devTurn{
			{model: "claude-sonnet-4-5", in: 900, out: 500, cr: 4000, cc: 200, ageMin: 180, tool: "Read"},
			{model: "claude-sonnet-4-5", in: 1100, out: 700, cr: 4200, ageMin: 150, tool: "Grep", toolErr: true},
			{model: "claude-sonnet-4-5", in: 800, out: 400, ageMin: 120},
			{model: "claude-opus-4-8", in: 1500, out: 900, cr: 9000, cc: 600, ageMin: 90, tool: "Bash"},
		}
		writeSessionTranscript(companies[1], "dev-"+companies[1]+"-sessA", a1, now)
	}
}

// writeSessionTranscript renders devTurns into a JSONL transcript at the agent's
// Claude Code project dir. Lines are marshalled via encoding/json so escaping is
// always correct.
func writeSessionTranscript(agent, sessionID string, turns []devTurn, now time.Time) {
	dir := projectDir(agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var b strings.Builder
	tuid := 0
	for _, t := range turns {
		ts := now.Add(-time.Duration(t.ageMin) * time.Minute)
		content := []map[string]any{{"type": "text", "text": "…"}}
		var id string
		if t.tool != "" {
			tuid++
			id = fmt.Sprintf("toolu_%s_%d", sessionID, tuid)
			content = append(content, map[string]any{
				"type": "tool_use", "name": t.tool, "id": id,
				"input": map[string]any{"note": t.tool + " " + strconv.Itoa(tuid)},
			})
		}
		writeJSONL(&b, map[string]any{
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{
				"model": t.model, "stop_reason": "end_turn",
				"usage": map[string]any{
					"input_tokens": t.in, "output_tokens": t.out,
					"cache_read_input_tokens": t.cr, "cache_creation_input_tokens": t.cc,
				},
				"content": content,
			},
		})
		if t.tool != "" {
			writeJSONL(&b, map[string]any{
				"type": "user", "timestamp": ts.Add(2 * time.Second),
				"message": map[string]any{"content": []map[string]any{
					{"type": "tool_result", "tool_use_id": id, "is_error": t.toolErr},
				}},
			})
		}
	}
	_ = os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(b.String()), 0o644)
}

func writeJSONL(b *strings.Builder, line map[string]any) {
	raw, err := json.Marshal(line)
	if err != nil {
		return
	}
	b.Write(raw)
	b.WriteByte('\n')
}

func writeSeedFile(dir, name, body string) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}

// ---- seed file contents (short but believable nightshift-flavoured) ---------

const devGlobalClaudeMd = `# Global rules (dev seed)

- Voice: terse. Commits: short, no AI attribution in the subject.
- Always branch + PR; ` + "`main`" + ` is protected.
- Run the tests before opening a PR. Never touch prod at night.
`

const devWorkspaceClaudeMd = `# Workspace (dev seed)

Each company has its own remote-control server rooted at ` + "`~/workspace/<company>`" + `.
Deploy previews onto the ` + "`web`" + ` network with Traefik labels →
` + "`https://<name>.night.example.com`" + `.
`

const devSettingsJSON = `{
  "permissions": { "defaultMode": "bypassPermissions" },
  "skipDangerousModePermissionPrompt": true,
  "env": { "GOTOOLCHAIN": "local" }
}
`

const devSkillMd = `---
name: code-review
description: Review the current diff for correctness bugs and cleanups (dev seed).
---

# Code review

Read the diff, flag correctness bugs first, then reuse/simplification wins.
`

const devFocusProducts = `# Promoted bets (dev seed)

The gate on Lane A — plan-products only builds validation tickets for bets
listed here. Scout writes ideas; only the operator promotes.

## sample-cli-alerts
Cheapest test: a landing page + waitlist counter. Kill if <10 signups in a week.
`

const devFocusProjects = `# Curated projects (dev seed)

## sample-cli
autonomy: merge-to-main
Nightly: deepen test coverage, keep the README honest.

## sample-app
autonomy: full-auto
Merge AND deploy under the gate + verify-live + rollback path (ADR-0011).
`
