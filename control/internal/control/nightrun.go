// Night runs: scheduled, unattended Claude Code sessions per agent (ADR-0005).
// The control plane owns *scheduling* only — a goroutine fires queued jobs at
// their chosen times and mints the daily sweep job per agent. *Execution* is
// systemd's: each run is a transient unit started via nightshift-rc, so a
// control-plane restart never kills a run. Jobs persist as files under
// ~/.nightshift/ and re-arm on restart.
package control

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // agy art plates may be jpeg despite a .png name
	_ "image/png"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // sweeps are wall-clock Asia/Bishkek even if the box has no tzdata

	"nightshift/control/internal/queue"
	"nightshift/control/internal/scheduler"
)

const (
	// The night pipeline is timed around the operator's Opus limits: 5h rolling
	// windows, asleep by 22:30, at the desk at 09:00. Window A opens ~23:00 and
	// resets ~04:00; window B opens with the still-running workers and resets by
	// ~09:00 — so every run must be dead by 08:45 (per-slot minutes below) and
	// the operator wakes to fresh limits. Company sweeps therefore start 23:20
	// +10min stagger (an 8h session ends ~07:30), not 05:00 like they used to.
	sweepHour    = 23
	sweepMinute  = 20
	sweepStagger = 10 // +10min per agent, so several Claudes don't start at once
	sweepGrace   = 4 * time.Hour
	jobKeepDays  = 14
	maxPromptLen = 64 * 1024

	// jobsListCap bounds /api/jobs. The Night tab now groups jobs by night
	// (nightKey), so a full playground night (~11 waves + up to 6 exec workers +
	// governor top-ups) plus a night or two of history must fit — 20 clipped a
	// single busy night. run-status is only queried for still-live runs
	// (runMaybeActive), so a higher cap doesn't multiply the sudo/systemd calls.
	jobsListCap = 40

	// playground runs the two-lane nightly pipeline (ADR-0008): medic, steward,
	// scout, plan-projects, plan-products, exec workers, review, synth, retro.
	// Every wave is Opus; reasoning effort (per <id>.effort sidecar) is the lever.
	// Opus 5 since 2026-07-28 (operator decision) — a drop-in at Opus 4.8's
	// price; ratesFor already bills any non-haiku model at Opus rates.
	opusModel = "claude-opus-5"

	// codexModel is the default model for executor=codex jobs (ADR-0018):
	// GPT-5.6 Sol, OpenAI's flagship, the only 5.6 tier that accepts xhigh.
	// Review runs on it — a different model adversarially reading what claude
	// built. Delegate `codex` calls inside claude sessions use the cheap
	// config.toml default (gpt-5.6-luna) instead; this const is only for jobs
	// the scheduler mints.
	codexModel = "gpt-5.6-sol"

	// exec dispatches at most this many Opus workers per night, split per lane
	// (ADR-0008): the longest-waiting open tickets, one isolated session each.
	// Overflow waits for the next night. The real ceiling is the box's 8 vCPUs.
	execHuntWorkers    = 3 // Lane A — validation tests
	execImproveWorkers = 3 // Lane B — curated-project improvement
	maxExecWorkers     = execHuntWorkers + execImproveWorkers

	// execWorkerStagger spaces the per-ticket exec workers apart at mint time so
	// the up-to-6 Opus sessions don't all start (and refresh the shared OAuth
	// creds) in the same instant — the concurrent-refresh torn write that caused
	// the 2026-07-05 silent night. The launcher's flock is the primary guard;
	// this is belt-and-suspenders that also smooths the CPU spike. 3min × 6 = 15min
	// spread, trivial against the 7h exec window.
	execWorkerStagger = 3 * time.Minute

	// execTonightWindow splits each lane's queue: tickets Created within this
	// window of the exec wave are "tonight's" freshly-planned work and dispatch
	// before any older backlog, so a stale ticket from an earlier wave (e.g. a
	// medic-filed ops ticket) can't preempt the night's plan — the 2026-07-06
	// ops-preempts-features failure.
	execTonightWindow = 4 * time.Hour

	// ticketClaimTTL is how long a claim sidecar (<id>.claim) suppresses a ticket
	// from exec dispatch. Workers pull-loop through open tickets and claim one;
	// the scheduler also pre-claims every ticket it assigns so a staggered-later
	// worker's ticket isn't grabbed by an earlier slot's loop. A stale claim (a
	// crashed worker) expires so the ticket returns to the queue.
	ticketClaimTTL = 8 * time.Hour

	// launchGap is the minimum spacing between ANY two run launches, across every
	// agent — enforced in schedTick. The Claude OAuth credentials are shared
	// box-wide, and concurrent token refreshes tore that file into a whole-night
	// logout (2026-07-05). Slot times are already staggered; this is the
	// structural backstop so a mis-set time or a post-downtime backlog can never
	// start two sessions in the same instant. The launcher's flock is the primary
	// guard; this keeps launches from even racing to it.
	launchGap = 3 * time.Minute

	// default auto-stop for a run when its slot doesn't say otherwise (minutes)
	defaultRunMinutes = 480

	// lateMintSlack (minutes): how far past its due a sweep may mint before the
	// late-mint clamp trims its auto-stop to the original end time. Covers the
	// ordinary tick delay and small stagger without touching the slot minutes.
	lateMintSlack = 5
)

// run ids cross the sudo boundary as systemd unit-name fragments and file
// names; nightshift-rc enforces the same shape on its side.
var runIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

var bishkek = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Bishkek")
	if err != nil {
		return time.FixedZone("UTC+6", 6*60*60)
	}
	return loc
}()

type job struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Prompt  string    `json:"prompt"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`              // "deferred" | "sweep"
	Model   string    `json:"model,omitempty"`   // coordinator model, "" = account default
	Effort  string    `json:"effort,omitempty"`  // reasoning effort, "" = model default (high)
	Label   string    `json:"label,omitempty"`   // short UI label (e.g. "plan", "exec · <ticket>")
	Minutes int       `json:"minutes,omitempty"` // auto-stop, 0 = defaultRunMinutes
	Created time.Time `json:"created"`
	Started bool      `json:"started"`
	// StartedAt is when the run actually launched (rc "run" succeeded). Set once
	// Started flips true; lets the UI show an auto-stop countdown (StartedAt +
	// Minutes) on live runs the way RC sessions show their TTL.
	StartedAt time.Time `json:"startedAt,omitzero"`

	// Pipeline-profile fields (ADR-0014). A wave with After is an output-consuming
	// dependent: it is minted as a *gated* job (Gated=true, never launched) and the
	// fire loop holds it until every upstream is terminal-with-report for the same
	// Batch. Batch scopes "which pipeline run" a dependency search may match (a
	// night = "night-<date>", a run-now = "run-<id>") so a daytime run-now can't be
	// satisfied by last night's reports. Skipped marks a dependent whose upstream
	// died — a loud, never-launched terminal state (never a faked Started).
	After    []string `json:"after,omitempty"`
	Batch    string   `json:"batch,omitempty"`
	Gated    bool     `json:"gated,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"`
	Deadline string   `json:"deadline,omitempty"` // profile "HH:MM" clamp, applied when a gated wave fires

	// First-class flow fields (ADR-0015). FlowID/NodeID correlate this job to a
	// task-centric flow; Workdir is a dedicated git worktree, passed to the
	// launcher through a validated sidecar; FinishBy is an absolute deadline and
	// replaces night-only HH:MM math for new flows.
	FlowID   string     `json:"flowId,omitempty"`
	NodeID   string     `json:"nodeId,omitempty"`
	Workdir  string     `json:"workdir,omitempty"`
	FinishBy *time.Time `json:"finishBy,omitempty"`
	// FastStart (flow opt-in, flow.FastHandoff): launch on the first tick the
	// job is due/ungated instead of waiting out the box-wide launchGap. Still at
	// most one launch per tick — the launcher's auth flock stays the real
	// concurrent-refresh guard.
	FastStart bool `json:"fastStart,omitempty"`

	// Executor is the ADR-0017 seam: which engine runs this session. "" and
	// "claude" both mean Claude Code; "codex" (ADR-0018) runs the OpenAI Codex
	// CLI instead. saveJob mirrors non-claude values into an <id>.executor
	// sidecar so the launcher can dispatch without parsing the job JSON.
	Executor string `json:"executor,omitempty"`

	// Concurrency scope (ADR-0019). CapGroup names the automation instance this
	// session belongs to (a flow id, or "<agent>/<batch>" for a night's waves);
	// Cap is that instance's max concurrent sessions. 0 = uncapped (legacy waves
	// keep today's behavior); flows default to 1 at mint. A session counts
	// against the cap from launch until its report lands or it goes terminal —
	// counting delivered-but-idle sessions would deadlock successors that ungate
	// on the very report that ended the work.
	Cap      int    `json:"cap,omitempty"`
	CapGroup string `json:"capGroup,omitempty"`
}

// executorModel picks the scheduler's default model for an executor: jobs
// carry the model explicitly (the launcher passes it through), so a codex job
// must never inherit a claude model id or vice versa.
func executorModel(executor string) string {
	if executor == "codex" {
		return codexModel
	}
	return opusModel
}

// nightConfig is the small persisted state that isn't a job: which agents
// have their sweep switched off, and the last date each agent's sweep was
// minted (so a restart can't mint it twice).
type nightConfig struct {
	SweepOff   map[string]bool   `json:"sweepOff"`
	LastSweep  map[string]string `json:"lastSweep"`            // agent -> "2006-01-02" in Bishkek time
	LastLaunch time.Time         `json:"lastLaunch,omitempty"` // last run launch, box-wide (launchGap)

	// Usage governor (governor.go): the operator-pinnable night budget in
	// API-equivalent USD (0 = unset -> defaultNightBudgetUSD) and the auto-ratchet
	// state that self-calibrates it night over night.
	NightBudgetUSD float64       `json:"nightBudgetUSD,omitempty"`
	Ratchet        budgetRatchet `json:"budgetRatchet,omitempty"`

	// MaxConcurrentSessions (ADR-0019) is the optional BOX-WIDE ceiling on
	// concurrently working automation sessions, counted across every agent and
	// shape. 0 = off (per-automation caps still apply). Operator remote-control
	// sessions are outside it — the auth flock remains the universal
	// refresh guard for those.
	MaxConcurrentSessions int `json:"maxConcurrentSessions,omitempty"`

	// Pipeline profiles are configured per agent.
	// ActiveProfiles[agent] is that agent's recurring choice; it takes effect the
	// NEXT night. EffectiveProfiles[agent] is the profile locked for the night in
	// progress (rolled to the active one at each night boundary, EffectiveNights),
	// so activating mid-night can't half-switch the running night. "" / absent =
	// the agent's built-in default (playground's pipeline, a company's single sweep).
	ActiveProfiles    map[string]string `json:"activeProfiles,omitempty"`
	EffectiveProfiles map[string]string `json:"effectiveProfiles,omitempty"`
	EffectiveNights   map[string]string `json:"effectiveNights,omitempty"`

	// Legacy single-agent fields (ADR-0014 pre-generalization) — folded into the
	// per-agent maps under "playground" on load, then left empty. Kept only so an
	// existing config.json migrates cleanly.
	ActiveProfile    string `json:"activeProfile,omitempty"`
	EffectiveProfile string `json:"effectiveProfile,omitempty"`
	EffectiveNight   string `json:"effectiveNight,omitempty"`
}

var nsMu sync.Mutex // guards all ~/.nightshift file state

// mintedMem is an in-memory belt over cfg.LastSweep (which persists to
// config.json): if the config save fails — disk full, perms — the on-disk
// LastSweep stays stale and every 30s tick would re-mint the same wave, an
// unbounded job factory drained one launch per launchGap all night. The belt
// caps that damage to one duplicate mint per PROCESS restart. Guarded by nsMu.
var mintedMem = map[string]string{}

// alreadyMinted reports whether key has fired for `day` (or any later day),
// consulting both the persisted config and the in-memory belt.
func alreadyMinted(cfg *nightConfig, key, day string) bool {
	return cfg.LastSweep[key] >= day || mintedMem[key] >= day
}

// markMinted records the firing in both layers, monotonically (ISO dates
// compare correctly as strings).
func markMinted(cfg *nightConfig, key, day string) {
	if cfg.LastSweep[key] < day {
		cfg.LastSweep[key] = day
	}
	if mintedMem[key] < day {
		mintedMem[key] = day
	}
}

func nsDir() string                  { return filepath.Join(home, ".nightshift") }
func jobsDir(agent string) string    { return filepath.Join(nsDir(), "jobs", agent) }
func reportsDir(agent string) string { return filepath.Join(nsDir(), "reports", agent) }
func configPath() string             { return filepath.Join(nsDir(), "config.json") }

// ---- persistence -------------------------------------------------------------

func loadConfig() nightConfig {
	c := nightConfig{SweepOff: map[string]bool{}, LastSweep: map[string]string{}}
	b, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.SweepOff == nil {
		c.SweepOff = map[string]bool{}
	}
	if c.LastSweep == nil {
		c.LastSweep = map[string]string{}
	}
	if c.ActiveProfiles == nil {
		c.ActiveProfiles = map[string]string{}
	}
	if c.EffectiveProfiles == nil {
		c.EffectiveProfiles = map[string]string{}
	}
	if c.EffectiveNights == nil {
		c.EffectiveNights = map[string]string{}
	}
	// Fold the legacy single-agent fields (pre-generalization) into playground's
	// per-agent slot, once, so an existing box keeps its active profile.
	if c.ActiveProfile != "" && c.ActiveProfiles["playground"] == "" {
		c.ActiveProfiles["playground"] = c.ActiveProfile
	}
	if c.EffectiveProfile != "" && c.EffectiveProfiles["playground"] == "" {
		c.EffectiveProfiles["playground"] = c.EffectiveProfile
	}
	if c.EffectiveNight != "" && c.EffectiveNights["playground"] == "" {
		c.EffectiveNights["playground"] = c.EffectiveNight
	}
	c.ActiveProfile, c.EffectiveProfile, c.EffectiveNight = "", "", ""
	return c
}

func saveConfig(c nightConfig) error {
	if err := os.MkdirAll(nsDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	// Rename-atomic: a crash mid-write must not leave a torn config — losing
	// LastSweep/LastLaunch re-mints waves and drops the launch-gap anchor.
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// jobWriteVeto is a test seam over the job store, like execCommand is over
// exec.Command: it is the only deterministic way to fail the Nth of N writes,
// which is what the atomic-emission rollback regressions (ADR-0023) need. Always
// nil in production.
var jobWriteVeto func(job) error

func saveJob(j job) error {
	if jobWriteVeto != nil {
		if err := jobWriteVeto(j); err != nil {
			return err
		}
	}
	dir := jobsDir(j.Agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// the prompt file is what the launcher reads box-side; the json is ours
	if err := os.WriteFile(filepath.Join(dir, j.ID+".prompt"), []byte(j.Prompt), 0o644); err != nil {
		return err
	}
	// optional model sidecar: the launcher reads it to pick the session's
	// coordinator model. Absent = account default (the common case).
	modelPath := filepath.Join(dir, j.ID+".model")
	if j.Model != "" {
		if err := os.WriteFile(modelPath, []byte(j.Model), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(modelPath)
	}
	// optional effort sidecar: the launcher reads it (validated) to pass
	// `--effort <level>` to claude. Absent = model default (high). Agent-owned,
	// no sudo crossing — same shape as the model sidecar.
	effortPath := filepath.Join(dir, j.ID+".effort")
	if j.Effort != "" {
		if err := os.WriteFile(effortPath, []byte(j.Effort), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(effortPath)
	}
	// optional auto-stop sidecar: nightshift-rc reads it (validated + capped
	// root-side) to arm the run's stop timer. Absent = 8h, the old fixed value.
	// Late waves (review/synth) need short stops so nothing burns Opus past
	// 08:45 and eats into the operator's 09:00 window.
	stopPath := filepath.Join(dir, j.ID+".stop")
	if j.Minutes > 0 {
		if err := os.WriteFile(stopPath, []byte(strconv.Itoa(j.Minutes)), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(stopPath)
	}
	// optional executor sidecar (ADR-0018): the launcher reads it to dispatch
	// `codex exec` instead of `claude --remote-control`. Written only for
	// non-claude executors so the common case stays sidecar-free.
	executorPath := filepath.Join(dir, j.ID+".executor")
	if j.Executor != "" && j.Executor != "claude" {
		if err := os.WriteFile(executorPath, []byte(j.Executor), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(executorPath)
	}
	// Flow jobs execute in a dedicated worktree so unattended work never checks
	// out branches beneath a manual remote-control session. The launcher accepts
	// only paths inside the selected agent workspace.
	workdirPath := filepath.Join(dir, j.ID+".workdir")
	if j.Workdir != "" {
		if err := os.WriteFile(workdirPath, []byte(j.Workdir), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(workdirPath)
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	// Rename-atomic: loadJobs silently skips unparseable json, so a torn write
	// would make the job invisible AND immortal (never launched, never pruned).
	tmp := filepath.Join(dir, j.ID+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, j.ID+".json"))
}

func loadJobs(agent string) []job {
	var out []job
	matches, _ := filepath.Glob(filepath.Join(jobsDir(agent), "*.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var j job
		if json.Unmarshal(b, &j) == nil && runIDRe.MatchString(j.ID) {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At.After(out[b].At) })
	return out
}

func deleteJob(agent, id string) {
	dir := jobsDir(agent)
	_ = os.Remove(filepath.Join(dir, id+".json"))
	_ = os.Remove(filepath.Join(dir, id+".prompt"))
	_ = os.Remove(filepath.Join(dir, id+".model"))
	_ = os.Remove(filepath.Join(dir, id+".effort"))
	_ = os.Remove(filepath.Join(dir, id+".stop"))
	_ = os.Remove(filepath.Join(dir, id+".workdir"))
	_ = os.Remove(filepath.Join(dir, id+".executor"))
}

// sweepSlot is one scheduled sweep: when it fires, which prompt it loads, and
// the coordinator model the session runs as. Most agents have exactly one slot
// (23:20 + per-agent stagger, account-default model). playground runs the full
// multi-wave pipeline — see the timing rationale on the consts above.
type sweepSlot struct {
	key       string // LastSweep key — stable, unique per (agent, wave)
	hour      int
	minute    int
	prompt    string // filename under ~/.nightshift/sweep/ (preamble when perTicket)
	model     string // "" = account default
	effort    string // reasoning effort, "" = model default (high)
	executor  string // "" or "claude" = Claude Code; "codex" = Codex CLI (ADR-0018)
	label     string // short UI label for the minted job(s)
	minutes   int    // auto-stop for the run, 0 = defaultRunMinutes
	perTicket bool   // mint one job per open ticket instead of one sweep job
	noTickets bool   // don't append the open-tickets section to the prompt

	// Pipeline-profile additions (ADR-0014). after names upstream waves this one
	// consumes the output of: it's minted gated and held until they deliver. hour
	// is -1 for a triggered wave with no earliest-time floor. deadline is the
	// profile-level "HH:MM" clamp (every wave's auto-stop trimmed so nothing runs
	// past it; "" = no clamp). huntW/improveW are the exec fan-out split; 0/0 =
	// the built-in execHuntWorkers/execImproveWorkers default.
	after    []string
	deadline string
	huntW    int
	improveW int

	// cap (ADR-0019): the profile's max concurrent sessions for the whole
	// night's batch. 0 = uncapped — the built-in pipeline's existing behavior.
	cap int
}

// NB on keys: a slot's LastSweep key dedups per calendar day. Pre-midnight
// slots that moved from a post-midnight time (plan) — and the company sweeps
// that moved from 05:00 — carry a ".2" suffix so the first night after the
// schedule change isn't swallowed by the key having already fired that
// morning under the old schedule.
func sweepSlots(agent string, idx int) []sweepSlot {
	// The night's shape is a per-agent named profile (ADR-0014). effectiveProfiles
	// holds the one locked for the night in progress (schedTick rolls it at each
	// night boundary); a load failure falls back to the agent's default so a typo
	// can never produce a dead night.
	if slots, ok := profileSlots(agent, effectiveProfiles[agent]); ok {
		return slots
	}
	if agent == "playground" {
		// Playground's default: a legacy pipeline.json override, else the built-ins.
		if slots, ok := loadPipelineSlots(); ok {
			return slots
		}
		return playgroundDefaultSlots()
	}
	// A company agent's default: one nightly sweep (23:20 + per-agent stagger).
	return []sweepSlot{{key: agent + ".2", hour: sweepHour, minute: sweepMinute + idx*sweepStagger, prompt: agent + ".md", minutes: defaultRunMinutes}}
}

// playgroundDefaultSlots is the built-in playground pipeline — the fallback
// whenever pipeline.json is absent or invalid.
func playgroundDefaultSlots() []sweepSlot {
	// Two lanes on one spine (ADR-0008), all Opus; effort scales to the work.
	// xhigh only for the three genuine next-level waves (scout strategy, exec
	// multi-hour coding, retro metacognition); nothing at max. Times are
	// staggered ≥3min (launchGap) so no two sessions refresh OAuth together.
	return []sweepSlot{
		// spine: VM pre-flight so a full disk at 23:05 can't waste the night
		{key: "playground/medic", hour: 23, minute: 0, prompt: "playground-medic.md", model: opusModel, effort: "medium", label: "medic", minutes: 50, noTickets: true},
		// Lane B: reconcile the board + close verified-landed, brief plan-B
		{key: "playground/steward", hour: 23, minute: 10, prompt: "playground-steward.md", model: opusModel, effort: "high", label: "steward", minutes: 45},
		// Lane A: the one deep-strategy wave — hunt + EV/kill-keep judgment,
		// detached (files no tickets; the operator promotes via focus/products.md)
		{key: "playground/scout", hour: 23, minute: 15, prompt: "playground-scout.md", model: opusModel, effort: "xhigh", label: "scout", minutes: 90, noTickets: true},
		// Lane B planner: focus/projects.md → improvement tickets (lane=improve)
		{key: "playground/plan-projects", hour: 23, minute: 55, prompt: "playground-plan-projects.md", model: opusModel, effort: "high", label: "plan-B", minutes: 60},
		// Lane A planner: a promoted bet → validation-test tickets (lane=hunt)
		{key: "playground/plan-products", hour: 0, minute: 0, prompt: "playground-plan-products.md", model: opusModel, effort: "high", label: "plan-A", minutes: 60, noTickets: true},
		// exec workers span both limit windows; 7h stop = dead by 07:45. xhigh
		// is the Opus default for multi-hour agentic coding (Anthropic, 2026-07).
		{key: "playground/exec", hour: 0, minute: 45, prompt: "playground-exec.md", model: opusModel, effort: "xhigh", label: "exec", minutes: 420, perTicket: true},
		// spine: adversarial gate on both lanes' PRs + sample-cli-main post-merge
		// audit. Runs on codex/GPT-5.6-Sol at xhigh (ADR-0018): the reviewer is
		// a different model than the one that wrote the code, and the review
		// burn moves off the Anthropic night budget onto the ChatGPT plan.
		{key: "playground/review", hour: 6, minute: 45, prompt: "playground-review.md", model: codexModel, effort: "xhigh", executor: "codex", label: "review", minutes: 105, noTickets: true},
		// spine: harvest fresh experiment numbers right before synth so the
		// morning brief reads tonight's data, not yesterday's
		{key: "playground/harvest", hour: 7, minute: 40, prompt: "playground-harvest.md", model: opusModel, effort: "medium", label: "harvest", minutes: 25, noTickets: true},
		// spine: one consolidated morning brief (scoreboard + ledger)
		{key: "playground/synth", hour: 8, minute: 0, prompt: "playground-synth.md", model: opusModel, effort: "medium", label: "synth", minutes: 45, noTickets: true},
		// spine: self-improvement — critiques the night, files morning action
		// points + an evidence-gated draft PR. Last to finish (42min → 08:45).
		{key: "playground/retro", hour: 8, minute: 3, prompt: "playground-retro.md", model: opusModel, effort: "xhigh", label: "retro", minutes: 42, noTickets: true},
	}
}

// ---- operator-editable pipeline schedule --------------------------------------

// pipelineSlot is one wave in the optional schedule override
// ~/.nightshift/pipeline.json — a field-for-field JSON mirror of sweepSlot.
// The file is re-read on every schedTick (it's tiny), so operator edits apply
// without a restart. Absent -> built-ins, silently; present but INVALID -> a
// (deduped) warning and the built-ins — fail-safe, a typo must never produce a
// dead night. Exec worker counts stay in code. Full schema:
//
//	{
//	  "slots": [
//	    {
//	      "name": "medic",                  // required, unique; the job label AND the
//	                                        // LastSweep dedup key ("playground/<name>")
//	      "time": "23:00",                  // required, "HH:MM" (Asia/Bishkek wall clock)
//	      "prompt": "playground-medic.md",  // required, filename under ~/.nightshift/sweep/
//	      "model": "claude-opus-5",         // optional; "" = account default
//	      "effort": "medium",               // optional; low|medium|high|xhigh|max; "" = model default
//	      "executor": "claude",             // optional; claude|codex (ADR-0018); "" = claude

//	      "minutes": 50,                    // required > 0; auto-stop, clamped [10,480]
//	      "perTicket": false,               // optional; exec-style fan-out (one worker per open ticket)
//	      "noTickets": true                 // optional; don't append the open-tickets section
//	    }
//	  ]
//	}
type pipelineSlot struct {
	Name      string `json:"name"`
	Time      string `json:"time"`
	Prompt    string `json:"prompt"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Executor  string `json:"executor,omitempty"` // claude (default) | codex, ADR-0018
	Minutes   int    `json:"minutes"`
	PerTicket bool   `json:"perTicket,omitempty"`
	NoTickets bool   `json:"noTickets,omitempty"`
}

type pipelineFile struct {
	Slots []pipelineSlot `json:"slots"`
}

func pipelinePath() string { return filepath.Join(nsDir(), "pipeline.json") }

// validEfforts matches the launcher's effort-sidecar validation ("" = model default).
var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// pipelineWarned dedups the invalid-config warning so a standing typo doesn't
// spam the journal on every 30s tick. Reset on a successful or absent load.
// Guarded by nsMu like the rest of the ~/.nightshift state.
var pipelineWarned string

func warnPipeline(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg != pipelineWarned {
		logf("pipeline.json invalid, using built-in schedule: %s", msg)
		pipelineWarned = msg
	}
}

// loadPipelineSlots reads + validates the pipeline override. ok=false (file
// absent or invalid) means "use the built-ins" — one bad slot rejects the whole
// file rather than running a half-parsed night.
func loadPipelineSlots() ([]sweepSlot, bool) {
	b, err := os.ReadFile(pipelinePath())
	if err != nil {
		if !os.IsNotExist(err) {
			warnPipeline("unreadable: %v", err)
			return nil, false
		}
		pipelineWarned = "" // absent is the normal case, not a warning
		return nil, false
	}
	var pf pipelineFile
	if err := json.Unmarshal(b, &pf); err != nil {
		warnPipeline("bad json: %v", err)
		return nil, false
	}
	if len(pf.Slots) == 0 {
		warnPipeline("no slots")
		return nil, false
	}
	out := make([]sweepSlot, 0, len(pf.Slots))
	seen := map[string]bool{}
	for i, p := range pf.Slots {
		if p.Name == "" || seen[p.Name] {
			warnPipeline("slot %d: name %q empty or duplicate", i, p.Name)
			return nil, false
		}
		seen[p.Name] = true
		tt, err := time.Parse("15:04", p.Time)
		if err != nil {
			warnPipeline("slot %q: bad time %q (want HH:MM)", p.Name, p.Time)
			return nil, false
		}
		if p.Effort != "" && !validEfforts[p.Effort] {
			warnPipeline("slot %q: bad effort %q (want low|medium|high|xhigh|max)", p.Name, p.Effort)
			return nil, false
		}
		if p.Executor != "" && !validExecutors[p.Executor] {
			warnPipeline("slot %q: bad executor %q (want claude|codex)", p.Name, p.Executor)
			return nil, false
		}
		if p.Executor == "codex" && p.PerTicket {
			warnPipeline("slot %q: perTicket fan-out is claude-only", p.Name)
			return nil, false
		}
		if p.Prompt == "" {
			warnPipeline("slot %q: no prompt file", p.Name)
			return nil, false
		}
		if p.Minutes <= 0 {
			warnPipeline("slot %q: minutes must be > 0", p.Name)
			return nil, false
		}
		out = append(out, sweepSlot{
			key: "playground/" + p.Name, hour: tt.Hour(), minute: tt.Minute(),
			prompt: p.Prompt, model: p.Model, effort: p.Effort, executor: p.Executor, label: p.Name,
			minutes:   min(max(p.Minutes, runMinutesFloor), runMinutesCeil),
			perTicket: p.PerTicket, noTickets: p.NoTickets,
		})
	}
	pipelineWarned = ""
	return out, true
}

// ticketClaimPath is a ticket's claim sidecar — a marker that exec has (or is
// about to) dispatch a worker for it. Same two-part validated shape as the
// ticket json, so it can't name an arbitrary file.
func ticketClaimPath(agent, id string) string {
	return filepath.Join(ticketsDir(agent), id+".claim")
}

// ticketClaimed reports whether a ticket carries a FRESH claim (mtime younger
// than ticketClaimTTL) — a worker or an earlier mint grabbed it. Stale claims
// are ignored so a crashed worker's ticket returns to the queue.
func ticketClaimed(agent, id string, now time.Time) bool {
	info, err := os.Stat(ticketClaimPath(agent, id))
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) < ticketClaimTTL
}

// writeTicketClaim pre-claims a ticket for a dispatched job. Best-effort: a
// failed write just means the ticket isn't suppressed, so log and move on.
func writeTicketClaim(agent, ticketID, jobID string, now time.Time) {
	content := fmt.Sprintf("sched:%s %s", jobID, now.Format(time.RFC3339))
	if err := os.WriteFile(ticketClaimPath(agent, ticketID), []byte(content), 0o644); err != nil {
		logf("exec: claim %s failed: %v", ticketID, err)
	}
}

// orderLane orders a lane's queue tonight-first: tickets Created within
// execTonightWindow of now (oldest first), then the backlog (oldest first). A
// zero/unset Created counts as backlog so it can't jump the freshly-planned work.
func orderLane(ts []ticket, now time.Time) []ticket {
	cutoff := now.Add(-execTonightWindow)
	tonight, backlog := make([]ticket, 0, len(ts)), make([]ticket, 0, len(ts))
	for _, t := range ts {
		if !t.Created.IsZero() && t.Created.After(cutoff) {
			tonight = append(tonight, t)
		} else {
			backlog = append(backlog, t)
		}
	}
	oldestFirst := func(s []ticket) {
		sort.SliceStable(s, func(a, b int) bool { return s[a].Created.Before(s[b].Created) })
	}
	oldestFirst(tonight)
	oldestFirst(backlog)
	return append(tonight, backlog...)
}

// ticketLabel is a short one-line version of a ticket title for the jobs list.
func ticketLabel(title string) string {
	title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
	if len(title) > 40 {
		return title[:39] + "…"
	}
	if title == "" {
		return "untitled"
	}
	return title
}

// execWorkerJobs builds the exec wave's jobs: one Opus session per open ticket,
// dispatched into maxExecWorkers slots. Lane A (hunt) and Lane B (improve) each
// get a guaranteed budget (execHuntWorkers / execImproveWorkers). Slots a thin
// lane leaves unused backfill from the other feature lane's overflow, then from
// the ops lane (box-hygiene, medic-filed) — ops NEVER consumes a feature budget,
// so a stale ops ticket can't starve a hunt/improve slot. Within a lane, tonight's
// freshly-planned tickets dispatch before older backlog (orderLane). Already-
// claimed tickets are skipped, and every remaining slot fills with a self-directed
// session so a thin (or empty) queue never idles Opus slots the night's budget
// paid for. Returns the jobs plus a jobID -> ticketID map; the caller (holding
// nsMu) saves each job and pre-claims its ticket only AFTER the save succeeds —
// a claim written before a failed save suppressed the ticket for ticketClaimTTL
// with no worker ever coming (see mintExecJobs).
func execWorkerJobs(agent string, now time.Time, s sweepSlot, extra, batch string) ([]job, map[string]string) {
	preamble, err := os.ReadFile(filepath.Join(nsDir(), "sweep", s.prompt))
	if err != nil {
		logf("exec %s: no worker preamble, skipping: %v", s.key, err)
		return nil, nil
	}
	base := string(preamble) + extra
	// Fan-out split from the active profile (ADR-0014); 0/0 = the built-in default.
	hw, iw := s.huntW, s.improveW
	if hw == 0 && iw == 0 {
		hw, iw = execHuntWorkers, execImproveWorkers
	}
	total := hw + iw
	// Bucket open, unclaimed tickets by lane; a lane-less ticket (operator-filed)
	// defaults to improve, the build lane.
	var hunt, improve, ops []ticket
	for _, t := range loadTickets(agent) {
		if t.Status != "open" || ticketClaimed(agent, t.ID, now) {
			continue
		}
		switch t.Lane {
		case "hunt":
			hunt = append(hunt, t)
		case "ops":
			ops = append(ops, t)
		default:
			improve = append(improve, t)
		}
	}
	hunt, improve, ops = orderLane(hunt, now), orderLane(improve, now), orderLane(ops, now)

	// Per-lane budgets first, then cross-lane backfill from each feature lane's
	// overflow, then ops — never exceeding the profile's total worker count.
	huntTake, improveTake := min(len(hunt), hw), min(len(improve), iw)
	picked := make([]ticket, 0, total)
	picked = append(picked, hunt[:huntTake]...)
	picked = append(picked, improve[:improveTake]...)
	backfill := append(append([]ticket{}, hunt[huntTake:]...), improve[improveTake:]...)
	backfill = append(backfill, ops...)
	for _, t := range backfill {
		if len(picked) >= total {
			break
		}
		picked = append(picked, t)
	}

	jobs := make([]job, 0, total)
	claims := make(map[string]string, len(picked))
	for _, t := range picked {
		lane := t.Lane
		if lane == "" {
			lane = "improve"
		}
		p := base + fmt.Sprintf("\n\n## Your ticket\n\n**Lane:** %s\n\n### %s — %s\n\n%s\n", lane, t.ID, t.Title, t.Body)
		// Stagger worker starts so their OAuth refreshes don't collide (see
		// execWorkerStagger). newRunID embeds the staggered minute, keeping ids unique.
		at := now.Add(time.Duration(len(jobs)) * execWorkerStagger)
		j := job{ID: newRunID("exec", at), Agent: agent, Prompt: p,
			Model: s.model, Effort: s.effort, Label: "exec · " + lane + " · " + ticketLabel(t.Title), Minutes: s.minutes, At: at, Kind: "sweep", Created: now, Batch: batch,
			Cap: s.cap, CapGroup: capGroupFor(agent, batch)}
		claims[j.ID] = t.ID // pre-claimed by the caller once the job persists
		jobs = append(jobs, j)
	}

	// Fill EVERY remaining slot with a self-directed session, same stagger as
	// the ticketed workers — a thin (or empty) queue used to mint just ONE
	// top-up, leaving up to 5 of 6 Opus slots dark on exactly the nights with
	// the most free capacity.
	for len(jobs) < total {
		at := now.Add(time.Duration(len(jobs)) * execWorkerStagger)
		jobs = append(jobs, job{ID: newRunID("exec", at), Agent: agent, Prompt: base,
			Model: s.model, Effort: s.effort, Label: "exec · self-directed", Minutes: s.minutes, At: at, Kind: "sweep", Created: now, Batch: batch,
			Cap: s.cap, CapGroup: capGroupFor(agent, batch)})
	}
	return jobs, claims
}

// mintExecJobs persists the exec wave's jobs, pre-claiming each job's ticket
// (`sched:<runid>`) only AFTER its json landed — the pre-claim keeps a
// pull-looping worker from an earlier slot from grabbing a ticket the scheduler
// just assigned to a staggered-later worker, but writing it before the save
// meant a failed save orphaned the claim: the ticket sat suppressed for
// ticketClaimTTL (8h) with no worker coming. The caller holds nsMu.
func mintExecJobs(agent string, now time.Time, s sweepSlot, extra, batch string) {
	jobs, claims := execWorkerJobs(agent, now, s, extra, batch)
	for _, j := range jobs {
		if err := saveJob(j); err != nil {
			logf("exec %s: save failed: %v", s.key, err)
			continue
		}
		if tid, ok := claims[j.ID]; ok {
			writeTicketClaim(agent, tid, j.ID, now)
		}
	}
}

func newRunID(kind string, at time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s", at.In(bishkek).Format("20060102-1504"), kind, hex.EncodeToString(b))
}

// nightBatch is the batch id shared by every job of one night (dependency
// evaluation is scoped to a batch, so a daytime run-now can't be satisfied by
// last night's reports). Keyed by the 22:30 evening-anchor date of `t`.
func nightBatch(t time.Time) string { return "night-" + nightDate(t) }

// mintWave persists one non-gated wave: the exec fan-out (perTicket) or a single
// sweep job. `at` is the run's scheduled time (now for a nightly wave, an offset
// for a run-now wave). extra is appended to the prompt (upstream report paths for
// a wave that just became ready); batch scopes the job(s) to their pipeline run.
func mintWave(agent string, at time.Time, s sweepSlot, extra, batch string) {
	if s.perTicket {
		mintExecJobs(agent, at, s, extra, batch)
		return
	}
	prompt, err := os.ReadFile(filepath.Join(nsDir(), "sweep", s.prompt))
	if err != nil {
		logf("sweep %s: no sweep prompt, skipping: %v", s.key, err)
		return
	}
	body := string(prompt) + extra
	if !s.noTickets {
		body += openTicketsSection(agent)
	}
	j := job{ID: newRunID("sweep", at), Agent: agent, Prompt: body,
		Model: s.model, Effort: s.effort, Executor: s.executor, Label: s.label, Minutes: s.minutes, At: at, Kind: "sweep", Created: at, Batch: batch,
		Deadline: s.deadline, Cap: s.cap, CapGroup: capGroupFor(agent, batch)}
	if err := saveJob(j); err != nil {
		logf("sweep %s: save failed: %v", s.key, err)
	}
}

// mintGated persists a triggered wave (ADR-0014) as a *gated* job: never
// launched by the fire loop until its upstreams deliver, then either fired (with
// their report paths appended) or dep-skipped. It carries After/Batch/Deadline
// so the fire loop can evaluate it. A missing prompt file is a loud no-op (the
// slot is still marked minted so it isn't retried forever). Single-session only
// (parseProfile rejects after + perTicket). Caller holds nsMu.
func mintGated(agent string, now, at time.Time, s sweepSlot, batch string) {
	prompt, err := os.ReadFile(filepath.Join(nsDir(), "sweep", s.prompt))
	if err != nil {
		logf("dep %s: no prompt, skipping: %v", s.key, err)
		return
	}
	body := string(prompt)
	if !s.noTickets {
		body += openTicketsSection(agent)
	}
	j := job{ID: newRunID("dep", at), Agent: agent, Prompt: body,
		Model: s.model, Effort: s.effort, Executor: s.executor, Label: s.label, Minutes: s.minutes, At: at, Kind: "sweep",
		Created: now, Batch: batch, After: s.after, Gated: true, Deadline: s.deadline,
		Cap: s.cap, CapGroup: capGroupFor(agent, batch)}
	if err := saveJob(j); err != nil {
		logf("dep %s: save failed: %v", s.key, err)
	}
}

// earliestTimed returns the (hour, minute) of the pipeline's first scheduled
// wave in night order — the mint anchor a floorless triggered wave inherits so
// it enters the fire loop (and starts waiting) at the night's start. Defaults to
// 23:00 when nothing is timed (parseProfile guarantees a timed root, so this is
// only a belt).
func earliestTimed(slots []sweepSlot) (int, int) {
	bh, bm, best := 23, 0, 1<<30
	for _, s := range slots {
		if s.hour < 0 {
			continue
		}
		if o := nightOrder(s.hour, s.minute); o < best {
			best, bh, bm = o, s.hour, s.minute
		}
	}
	return bh, bm
}

// ---- scheduler ---------------------------------------------------------------

func startScheduler() {
	// One-time migration: adopt a lone legacy pipeline.json as a "custom" profile
	// (ADR-0014) so an existing box keeps its exact schedule after the upgrade.
	nsMu.Lock()
	cfg := loadConfig()
	if migrateLegacyPipeline(&cfg) {
		if err := saveConfig(cfg); err != nil {
			logf("profile migrate: config save failed: %v", err)
		}
	}
	nsMu.Unlock()
	go func() {
		for {
			schedTick(time.Now())
			time.Sleep(30 * time.Second)
		}
	}()
}

// schedTick mints due sweep jobs, fires due jobs, and prunes old ones. Errors
// are logged and retried next tick; a job only flips to Started when rc ran.
func schedTick(now time.Time) {
	nsMu.Lock()
	defer nsMu.Unlock()

	cfg := loadConfig()
	cfgDirty := false
	day := now.In(bishkek).Format("2006-01-02")

	// Roll each agent's effective profile at the night boundary: activating
	// mid-night updates ActiveProfiles but the night in progress keeps its locked
	// shape (ADR-0014 "activate takes effect next night — no half-switched night").
	nd := nightDate(now)
	for _, agent := range companies {
		if cfg.EffectiveNights[agent] != nd {
			cfg.EffectiveProfiles[agent] = cfg.ActiveProfiles[agent]
			cfg.EffectiveNights[agent] = nd
			cfgDirty = true
		}
	}
	effectiveProfiles = cfg.EffectiveProfiles

	nb := now.In(bishkek)
	for i, agent := range companies {
		slots := sweepSlots(agent, i)
		pipeH, pipeM := earliestTimed(slots) // anchor for floorless triggered waves
		for _, s := range slots {
			// A triggered wave (has after, no time floor) enters the schedule at the
			// pipeline's first wave so it's minted-gated early and starts waiting;
			// one with a time floor uses that. Everything else uses its own time.
			sh, sm := s.hour, s.minute
			if sh < 0 {
				sh, sm = pipeH, pipeM
			}
			// Evaluate today's due AND yesterday's: a pre-midnight slot (23:00
			// medic) missed during a 22:50→00:10 outage used to vanish silently —
			// rebuilding `due` from *today's* date put it in the future, so the
			// slot was never recognized as due and the night ran headless. Dedup
			// is keyed by the due's own date, so each night fires at most once.
			dueToday := time.Date(nb.Year(), nb.Month(), nb.Day(), sh, sm, 0, 0, bishkek)
			for _, due := range []time.Time{dueToday.AddDate(0, 0, -1), dueToday} {
				dday := due.In(bishkek).Format("2006-01-02")
				if now.Before(due) || alreadyMinted(&cfg, s.key, dday) {
					continue
				}
				cfgDirty = true
				markMinted(&cfg, s.key, dday) // even on skip/error: never mint a night's slot twice
				batch := nightBatch(due)
				switch {
				case cfg.SweepOff[agent]:
				case now.After(due.Add(sweepGrace)):
					if dday == day { // yesterday's long-past dues are routine, not news
						logf("sweep %s: %s long past (downtime?), skipping today", s.key, due.Format("15:04"))
					}
				case len(s.after) > 0:
					// Triggered wave: mint a gated placeholder the fire loop holds
					// until upstreams deliver. No late-mint clamp — its window is the
					// fire-time deadline clamp + triggeredMaxWait.
					mintGated(agent, now, due, s, batch)
				default:
					// Late mint (post-downtime, inside grace): the wave still ends
					// when it would have — clamp minutes to the original due+minutes
					// horizon so a 00:45 exec minted at 04:00 can't run to 11:00.
					// A few minutes of ordinary tick lag is tolerated so the
					// normal path keeps its slot minutes untouched.
					s := s
					if left := int(due.Add(time.Duration(s.minutes) * time.Minute).Sub(now).Minutes()); s.minutes > 0 && left < s.minutes-lateMintSlack {
						if left < 10 {
							logf("sweep %s: window over (due %s + %dm), skipping", s.key, due.Format("15:04"), s.minutes)
							continue
						}
						logf("sweep %s: late mint, clamping %dm -> %dm to keep the original end time", s.key, s.minutes, left)
						s.minutes = left
					}
					// Profile deadline: trim the auto-stop so nothing runs past it.
					if m, ok := deadlineClampedMinutes(s.deadline, s.minutes, now); !ok {
						logf("sweep %s: past profile deadline %s, skipping", s.key, s.deadline)
						continue
					} else {
						s.minutes = m
					}
					mintWave(agent, now, s, "", batch)
				}
			}
		}
	}
	// Scheduled automations (ADR-0019): recurring flow templates mint their
	// daily run with the same dedup/grace/kill-switch semantics as the sweeps.
	mintScheduledAutomations(&cfg, now, &cfgDirty)

	// Usage governor: pace checkpoints (03:00/05:00) and the morning auto-ratchet
	// (08:50). Both run under nsMu (held here), mutate cfg, and mark it dirty so
	// the single save below persists any budget change or minted top-up bookkeeping.
	runGovernor(now, &cfg, &cfgDirty)
	runRatchet(now, &cfg, &cfgDirty)

	if cfgDirty {
		if err := saveConfig(cfg); err != nil {
			logf("config save failed: %v", err)
		}
	}

	// Route verdicts BEFORE the fire loop so a delivered upstream's successor can
	// ungate (and, on a fast-handoff flow, launch) in this same tick instead of
	// burning two extra ticks on hold → ungate → launch staging.
	reconcileFlowsLocked(now)

	launched := false
	// Maintenance pass: prune old jobs, evaluate gated dependents, and collect
	// the tick's launch candidates. Launching is a QUEUE decision (ADR-0019),
	// not an iteration-order accident — candidates are gathered first, sorted
	// deterministically, then admitted one per tick below.
	var cands []launchCandidate
	for _, agent := range companies {
		for _, j := range loadJobs(agent) {
			switch {
			case j.Skipped:
				// dep-skipped: never launches; prune with the same horizon as a run.
				if now.Sub(j.At) > jobKeepDays*24*time.Hour {
					deleteJob(agent, j.ID)
				}
			case j.Gated && !j.Started:
				// Output-consuming dependent: hold until every upstream is
				// terminal-with-report (ADR-0014). Ready → a launch candidate this
				// same tick; a dead upstream → skip loudly. Flow jobs add a verdict
				// hold (ADR-0017): a delivered upstream whose verdict reconcile
				// hasn't acted on yet must not fire its successor — the next step
				// may be a routed insertion, not this node.
				if j.FlowID != "" && flowGateHold(agent, j) {
					continue
				}
				st, paths, reason := gatedReadiness(agent, j, now)
				switch st {
				case upSuccess:
					m, ok := deadlineClampedMinutes(j.Deadline, j.Minutes, now)
					if ok {
						m, ok = absoluteDeadlineMinutes(j.FinishBy, m, now)
					}
					if !ok {
						const reason = "past flow/profile deadline before upstreams delivered"
						j.Gated, j.Skipped = false, true
						writeDepSkip(agent, j.ID, reason)
						logf("dep %s/%s: past deadline, skipping", agent, j.ID)
						noteSkip(j, reason)
						_ = saveJob(j)
					} else if err := ensureFlowNodeWorktree(j); err != nil {
						// A parallel member without its checkout must not launch
						// into the shared tree: skip loudly (never a faked run).
						reason := "worktree creation failed: " + err.Error()
						j.Gated, j.Skipped = false, true
						writeDepSkip(agent, j.ID, reason)
						logf("dep %s/%s: %v", agent, j.ID, err)
						noteSkip(j, reason)
						_ = saveJob(j)
					} else {
						j.Minutes = m
						// Deadline stays on the job: if the queue holds this node
						// (cap busy, executor down) the launch-time re-clamp still
						// ends it on schedule.
						j.Prompt += upstreamPromptSection(paths)
						j.Gated, j.After = false, nil
						if err := saveJob(j); err != nil {
							logf("dep %s/%s: ungate save failed: %v", agent, j.ID, err)
						} else if !j.At.After(now) {
							cands = append(cands, launchCandidate{agent, j})
						}
					}
				case upFailed:
					j.Gated, j.Skipped = false, true
					writeDepSkip(agent, j.ID, reason)
					logf("dep %s/%s: dep-skip — %s", agent, j.ID, reason)
					noteSkip(j, reason)
					_ = saveJob(j)
				}
			case !j.Started && j.FinishBy != nil && now.Add(runMinutesFloor*time.Minute).After(*j.FinishBy):
				// Absolute flow deadline. Unlike a profile's evening-anchored HH:MM,
				// this is unambiguous for daytime and multi-day work.
				j.Gated, j.Skipped = false, true
				writeDepSkip(agent, j.ID, "flow deadline reached before launch")
				noteSkip(j, "flow deadline reached before launch")
				_ = saveJob(j)
			case !j.Started && !j.At.After(now):
				// First-stage parallel members are minted UNGATED, so the gated
				// path's fire-time worktree creation above never runs for them —
				// ensure the member checkout here too, or the launcher fails with
				// "flow worktree missing" and the watchdog blocks the whole run
				// (2026-08-15 factory run). Idempotent no-op for everything else.
				if err := ensureFlowNodeWorktree(j); err != nil {
					reason := "worktree creation failed: " + err.Error()
					j.Skipped = true
					writeDepSkip(agent, j.ID, reason)
					logf("launch %s/%s: %v", agent, j.ID, err)
					noteSkip(j, reason)
					_ = saveJob(j)
					continue
				}
				cands = append(cands, launchCandidate{agent, j})
			case j.Started && now.Sub(j.At) > jobKeepDays*24*time.Hour:
				deleteJob(agent, j.ID)
			}
		}
	}

	// Launch phase (ADR-0019): deterministic order (tier, due time, id — a
	// restart replays the same queue), one launch per tick. The box-wide
	// launchGap still spaces starts (concurrent-refresh guard; FastStart
	// bypasses the wait, not the one-per-tick rule); executor-health holds
	// pause the queue rather than skipping work; concurrency caps keep an
	// automation's sessions to its declared width; and the launch-time
	// deadline re-clamp ends a late launch on the original schedule — or
	// skips it loudly when the window is gone.
	load := loadQueue(now)
	sortCandidates(cands)
	gapBlocked := now.Sub(cfg.LastLaunch) < launchGap
	// The admit → persist → launch lifecycle is the scheduler application service
	// (internal/scheduler): it decides each candidate over the pure queue rules
	// and effects the winner through the Clock/Store/Launcher ports these adapters
	// bind. schedTick keeps the broader concerns — candidate gathering, the
	// one-launch-per-tick stop, and observability (starvation, hold dedup, dep-skip
	// markers) — while the service owns launching and emits the lifecycle schema.
	store := newLaunchStore(cands)
	svc := scheduler.NewService(launchClock{now}, store, rcLauncher{}, slog.Default())
	tick := newLaunchTick(cfg, load, now, gapBlocked)
	for _, c := range cands {
		j := c.j
		if launched {
			noteStarvation(c.agent, j, now)
			continue
		}
		switch dec, reason := svc.Admit(context.Background(), runJob(j), tick); dec {
		case scheduler.Launched:
			launched = true
			cfg.LastLaunch = now
			clearQueueHold(j)
		case scheduler.Held:
			noteQueueHold(j, reason) // paused, not skipped: retried every tick until health returns
		case scheduler.SkippedWindow:
			// The service persisted the skip + emitted run.skipped; the adapter
			// writes the dep-skip marker successors ungate on.
			writeDepSkip(c.agent, j.ID, reason)
			logf("queue %s/%s: window closed before launch, skipping", c.agent, j.ID)
		case scheduler.CapBusy, scheduler.GapBlocked:
			noteStarvation(c.agent, j, now) // waiting on a slot is normal; pathological waits alert
		case scheduler.LaunchFailed:
			// The service logged the failure; nothing launched — retry next tick.
		}
	}

	// Watchdog: release cap slots held by sessions whose unit died without a
	// report — under a cap of 1 a single dead unit would otherwise stall the
	// whole box until its auto-stop window expired.
	runWatchdog(now, load)

	if launched {
		if err := saveConfig(cfg); err != nil {
			logf("config save (lastLaunch) failed: %v", err)
		}
	}
}

// absoluteDeadlineMinutes applies a flow's finish-by timestamp to one session
// window (ADR-0020: the pure math lives in internal/queue; this adapter binds
// the control-plane launcher floor). nil means no flow-level deadline; the
// ordinary per-session cap still applies. A deadline with less than the
// 10-minute launcher floor left is terminal.
func absoluteDeadlineMinutes(finish *time.Time, minutes int, now time.Time) (int, bool) {
	return queue.AbsoluteDeadlineMinutes(finish, minutes, now, runMinutesFloor)
}

// execCommand is a test seam over exec.Command.
var execCommand = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func rcRun(action, agent, id string) (string, error) {
	out, err := execCommand("sudo", wrapper, action, agent, id)
	return strings.TrimSpace(out), err
}

// logf is a test seam: schedTick runs in tests where log output is noise.
var logf = func(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	lower := strings.ToLower(message)
	level := slog.LevelInfo
	for _, marker := range []string{"failed", "error", "invalid", "unreadable", "dead", "skipping", "holding"} {
		if strings.Contains(lower, marker) {
			level = slog.LevelWarn
			break
		}
	}
	slog.Default().Log(context.Background(), level, "control.background_event", "message", message)
}

// ---- handlers ----------------------------------------------------------------

type jobView struct {
	job
	RunState string `json:"runState,omitempty"` // active|inactive|… for started jobs
}

func handleJobs(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("c")
	if !isCompany(c) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	jobs := loadJobs(c)
	nsMu.Unlock()
	if len(jobs) > jobsListCap {
		jobs = jobs[:jobsListCap]
	}
	now := time.Now()
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		v := jobView{job: j}
		// Only query rc for runs that could still be live. A run past its
		// StartedAt+Minutes window has auto-stopped, so it's inactive without a
		// sudo/systemd round-trip — this keeps the cost ~one call per active run
		// even as jobsListCap grows to hold several nights of history.
		if j.Started {
			if runMaybeActive(j, now) {
				v.RunState, _ = rcRun("run-status", c, j.ID)
			} else {
				v.RunState = "inactive"
			}
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

// runMaybeActive reports whether a started run could still be alive, so
// handleJobs can skip the rc run-status call for runs whose auto-stop window has
// clearly elapsed. A legacy job with no StartedAt (pre-1.7) can't be reasoned
// about, so ask rc. A small grace covers stop-timer/clock slop.
func runMaybeActive(j job, now time.Time) bool {
	if j.StartedAt.IsZero() {
		return true
	}
	mins := j.Minutes
	if mins <= 0 {
		mins = defaultRunMinutes
	}
	return now.Before(j.StartedAt.Add(time.Duration(mins)*time.Minute + 10*time.Minute))
}

func handleJobCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent    string `json:"agent"`
		Prompt   string `json:"prompt"`
		At       string `json:"at"`                 // datetime-local value, operator wall clock = Bishkek
		Effort   string `json:"effort,omitempty"`   // "" | low|medium|high|xhigh|max
		Executor string `json:"executor,omitempty"` // "" | claude | codex (ADR-0018)
		Minutes  int    `json:"minutes,omitempty"`  // auto-stop; 0 = default, else clamped [10,480]
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isCompany(in.Agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" || len(in.Prompt) > maxPromptLen {
		http.Error(w, "prompt empty or too long", http.StatusBadRequest)
		return
	}
	// Effort + auto-stop are validated exactly like the launcher/rc do, so a
	// phone-queued run can be xhigh or short-windowed (1.8).
	if in.Effort != "" && !validEfforts[in.Effort] {
		http.Error(w, "bad effort (want low|medium|high|xhigh|max)", http.StatusBadRequest)
		return
	}
	if in.Executor != "" && !validExecutors[in.Executor] {
		http.Error(w, "bad executor (want claude|codex)", http.StatusBadRequest)
		return
	}
	if in.Minutes != 0 {
		in.Minutes = min(max(in.Minutes, runMinutesFloor), runMinutesCeil)
	}
	at, err := time.ParseInLocation("2006-01-02T15:04", in.At, bishkek)
	if err != nil {
		http.Error(w, "bad time (want YYYY-MM-DDTHH:MM): "+err.Error(), http.StatusBadRequest)
		return
	}
	j := job{ID: newRunID("run", at), Agent: in.Agent, Prompt: in.Prompt,
		At: at, Kind: "deferred", Effort: in.Effort, Executor: in.Executor, Minutes: in.Minutes, Created: time.Now()}
	if in.Executor == "codex" {
		// claude runs keep the account default ("" model); codex must not — the
		// config.toml default is the cheap delegate tier, not the review flagship.
		j.Model = codexModel
	}
	nsMu.Lock()
	err = saveJob(j)
	nsMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Default().InfoContext(r.Context(), "job.queued",
		"agent", j.Agent, "run_id", j.ID, "at", j.At,
		"effort", j.Effort, "minutes", j.Minutes,
	)
	writeJSON(w, j)
}

func handleJobCancel(w http.ResponseWriter, r *http.Request) {
	c, id := r.URL.Query().Get("c"), r.URL.Query().Get("id")
	if !isCompany(c) || !runIDRe.MatchString(id) {
		http.Error(w, "bad agent or id", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	for _, j := range loadJobs(c) {
		if j.ID != id {
			continue
		}
		if j.Started {
			http.Error(w, "already started — stop the run instead", http.StatusConflict)
			return
		}
		deleteJob(c, id)
		slog.Default().InfoContext(r.Context(), "job.cancelled", "agent", c, "run_id", id)
		writeJSON(w, map[string]string{"cancelled": id})
		return
	}
	http.Error(w, "no such job", http.StatusNotFound)
}

func handleRunStop(w http.ResponseWriter, r *http.Request) {
	c, id := r.URL.Query().Get("c"), r.URL.Query().Get("id")
	if !isCompany(c) || !runIDRe.MatchString(id) {
		http.Error(w, "bad agent or id", http.StatusBadRequest)
		return
	}
	out, err := rcRun("stop-run", c, id)
	if err != nil {
		http.Error(w, out+"\n"+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"stopped": id, "result": out})
}

func handleSweeps(w http.ResponseWriter, r *http.Request) {
	nsMu.Lock()
	cfg := loadConfig()
	nsMu.Unlock()
	out := map[string]bool{}
	for _, c := range companies {
		out[c] = !cfg.SweepOff[c]
	}
	writeJSON(w, out)
}

func handleSweepToggle(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("c")
	if !isCompany(c) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	on := r.URL.Query().Get("on") == "1"
	nsMu.Lock()
	cfg := loadConfig()
	if on {
		delete(cfg.SweepOff, c)
	} else {
		cfg.SweepOff[c] = true
	}
	err := saveConfig(cfg)
	nsMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"agent": c, "sweep": on})
}

// ---- pipeline (read surface over the wave schedule) --------------------------
//
// /api/pipeline exposes playground's wave schedule — the ACTIVE profile
// (ADR-0014), a legacy pipeline.json, or the built-ins — so the Night tab can
// render the night as a wave timeline and label each wave by name/time, and the
// Pipeline editor can show the active shape. Read-only; the schedule already
// crosses the sudo boundary as jobs, this just surfaces it.

type pipelineWave struct {
	Name      string   `json:"name"`            // slot label; matches a job's Label (exec: "<name> · …")
	Time      string   `json:"time"`            // "HH:MM" Asia/Bishkek; "" for a pure triggered wave
	After     []string `json:"after,omitempty"` // upstream waves this one is gated on (ADR-0014)
	Effort    string   `json:"effort,omitempty"`
	Executor  string   `json:"executor,omitempty"` // "codex" when the wave runs on Codex (ADR-0018)
	Minutes   int      `json:"minutes"`            // auto-stop window
	PerTicket bool     `json:"perTicket,omitempty"`
	NoTickets bool     `json:"noTickets,omitempty"`
}

type pipelineDoc struct {
	Agent   string         `json:"agent"`
	Source  string         `json:"source"`            // "builtin" | "profile" | "legacy"
	Profile string         `json:"profile,omitempty"` // active profile name when source=="profile"
	Waves   []pipelineWave `json:"waves"`             // ordered evening-first (crosses midnight; triggered after their upstreams)
}

// nightOrder sorts a slot into the night's chronological order: an early-morning
// wave (retro 08:03) sorts AFTER a late-evening one (medic 23:00) by treating
// pre-noon times as belonging to the small hours of the same night.
func nightOrder(hour, minute int) int {
	m := hour*60 + minute
	if hour < 12 {
		m += 24 * 60
	}
	return m
}

func handlePipeline(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("c")
	if agent == "" {
		agent = "playground"
	}
	if !isCompany(agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	cfg := loadConfig()
	name := cfg.ActiveProfiles[agent]
	var slots []sweepSlot
	source := "builtin"
	profileName := ""
	if name != "" {
		if p, err := loadProfile(name); err == nil {
			slots, source, profileName = profileToSlots(agent, p), "profile", name
		}
	}
	if slots == nil {
		if agent == "playground" {
			if s, ok := loadPipelineSlots(); ok {
				slots, source = s, "legacy"
			} else {
				slots = playgroundDefaultSlots()
			}
		} else {
			slots = sweepSlots(agent, 0) // a company agent's single default sweep
		}
	}
	nsMu.Unlock()

	order := waveDisplayOrder(slots)
	sort.SliceStable(slots, func(a, b int) bool { return order[slots[a].label] < order[slots[b].label] })
	waves := make([]pipelineWave, 0, len(slots))
	for _, s := range slots {
		t := ""
		if s.hour >= 0 {
			t = fmt.Sprintf("%02d:%02d", s.hour, s.minute)
		}
		waves = append(waves, pipelineWave{
			Name: s.label, Time: t, After: s.after,
			Effort: s.effort, Executor: s.executor, Minutes: s.minutes, PerTicket: s.perTicket, NoTickets: s.noTickets,
		})
	}
	writeJSON(w, pipelineDoc{Agent: agent, Source: source, Profile: profileName, Waves: waves})
}

// waveDisplayOrder ranks waves for the timeline: timed waves by night order,
// triggered waves just after their latest upstream (a DAG, so a few relaxation
// passes converge). A triggered wave whose upstream is unresolved sorts last.
func waveDisplayOrder(slots []sweepSlot) map[string]int {
	order := map[string]int{}
	for _, s := range slots {
		if s.hour >= 0 {
			order[s.label] = nightOrder(s.hour, s.minute)
		}
	}
	for range slots {
		for _, s := range slots {
			if s.hour >= 0 {
				continue
			}
			mx, known := 0, true
			for _, up := range s.after {
				o, ok := order[up]
				if !ok {
					known = false
					break
				}
				if o > mx {
					mx = o
				}
			}
			if known {
				order[s.label] = mx + 1
			}
		}
	}
	for _, s := range slots {
		if _, ok := order[s.label]; !ok {
			order[s.label] = 1 << 30
		}
	}
	return order
}

// ---- reports (read-only, same shape as the prompt viewer) --------------------
//
// A report id is a run id; the only readable path is
// ~/.nightshift/reports/<agent>/<id>.md with both parts validated, so the
// browser can never name an arbitrary file.

type reportMeta struct {
	ID     string `json:"id"`
	Mtime  int64  `json:"mtime"`
	Banner bool   `json:"banner,omitempty"` // <id>.png exists next to the report
	// Banner frontmatter, surfaced so the reports list is glanceable at morning
	// scan time without opening each report. All optional — a report with no
	// frontmatter carries only id + mtime.
	Headline string `json:"headline,omitempty"`
	Tone     string `json:"tone,omitempty"`  // shipped | partial | quiet | stall
	Wave     string `json:"wave,omitempty"`  // e.g. "exec", "synth"
	Stats    string `json:"stats,omitempty"` // raw "Label Value | Label Value …"
}

// reportFrontmatter pulls the banner_* fields from a report file for the reports
// list (1.1). Cheap and best-effort: it reuses the banner frontmatter block and
// returns zero values when a report has none. wave falls back to the id.
func reportFrontmatter(path, id string) (headline, tone, wave, stats string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	fields := map[string]string{}
	if m := fmBlockRe.FindSubmatch(b); m != nil {
		for _, line := range strings.Split(string(m[1]), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			fields[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	wave = strings.ToLower(fields["banner_wave"])
	if wave == "" {
		if m := idDateWaveRe.FindStringSubmatch(id); m != nil {
			wave = m[4]
		}
	}
	return fields["banner_headline"], strings.ToLower(fields["banner_tone"]), wave, fields["banner_stats"]
}

func handleReports(w http.ResponseWriter, r *http.Request) {
	c := r.URL.Query().Get("c")
	if !isCompany(c) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	// Window the list to match the obs ledger (obsWindowDays). Playground now
	// writes ~11 reports/night and the files have no retention reaper, so an
	// unbounded list grows without bound; the Night tab only groups recent
	// nights anyway. Older report files stay on disk and remain deep-linkable.
	cutoff := time.Now().Unix() - int64(obsWindowDays)*86400
	out := []reportMeta{}
	entries, _ := os.ReadDir(reportsDir(c))
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !runIDRe.MatchString(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Unix() < cutoff {
			continue
		}
		m := reportMeta{ID: id, Mtime: info.ModTime().Unix(), Banner: hasBanner(c, id)}
		m.Headline, m.Tone, m.Wave, m.Stats = reportFrontmatter(filepath.Join(reportsDir(c), e.Name()), id)
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Mtime > out[b].Mtime })
	writeJSON(w, out)
}

func handleReport(w http.ResponseWriter, r *http.Request) {
	c, id := r.URL.Query().Get("c"), r.URL.Query().Get("id")
	if !isCompany(c) || !runIDRe.MatchString(id) {
		http.Error(w, "bad agent or id", http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(filepath.Join(reportsDir(c), id+".md"))
	if err != nil {
		http.Error(w, "no such report", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

// hasBanner reports whether a run has any servable banner. Three sources:
// an agy art plate (<id>.plate.png), banner frontmatter in the report (quiet
// nights skip agy but still get a typographic card), or a legacy/cached png.
func hasBanner(c, id string) bool {
	dir := reportsDir(c)
	for _, n := range []string{id + ".plate.png", id + ".banner.png", id + ".png"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	rep, _ := os.ReadFile(filepath.Join(dir, id+".md"))
	return hasBannerMeta(rep)
}

// handleReportBanner serves a run's banner. New flow: agy drops a text-free art
// plate (<id>.plate.png) — or nothing, on a quiet night — and the agent records
// the real numbers in the report frontmatter; we overlay them deterministically
// (over the plate, or over synthesized paper) and cache to <id>.banner.png,
// recomposing whenever the plate or report is newer. Legacy runs that baked text
// into <id>.png are still served as-is. Same two-part path validation as the
// report — only these <id>.* siblings are reachable.
func handleReportBanner(w http.ResponseWriter, r *http.Request) {
	c, id := r.URL.Query().Get("c"), r.URL.Query().Get("id")
	if !isCompany(c) || !runIDRe.MatchString(id) {
		http.Error(w, "bad agent or id", http.StatusBadRequest)
		return
	}
	dir := reportsDir(c)
	plate := filepath.Join(dir, id+".plate.png")
	rep := filepath.Join(dir, id+".md")
	cache := filepath.Join(dir, id+".banner.png")

	pInfo, pErr := os.Stat(plate)
	repBytes, _ := os.ReadFile(rep)
	compose := pErr == nil || hasBannerMeta(repBytes)

	if compose {
		newest := fileMTime(rep)
		if pErr == nil && pInfo.ModTime().Unix() > newest {
			newest = pInfo.ModTime().Unix()
		}
		if cInfo, err := os.Stat(cache); err != nil || cInfo.ModTime().Unix() < newest {
			if b, cErr := composeReportBanner(dir, id); cErr == nil {
				// Atomic, concurrency-safe cache write. This handler serves on
				// concurrent HTTP requests with no lock, so two requests racing to
				// refresh a stale banner would interleave a plain os.WriteFile into
				// a torn PNG (and the serveFile below could ship the half-written
				// file). Every other persist path temp+renames, but under nsMu, so
				// a fixed ".tmp" is enough for them; here the writers are unlocked,
				// so use a UNIQUE temp (a shared name would itself race) then
				// rename atomically into place.
				if tmp, tErr := os.CreateTemp(dir, id+".banner.*.tmp"); tErr == nil {
					_, wErr := tmp.Write(b)
					clErr := tmp.Close()
					if wErr == nil && clErr == nil {
						_ = os.Rename(tmp.Name(), cache)
					} else {
						_ = os.Remove(tmp.Name())
					}
				}
			}
		}
		if serveFile(w, cache) {
			return
		}
		if pErr == nil && serveFile(w, plate) { // compose failed → at least the art
			return
		}
	}
	if serveFile(w, filepath.Join(dir, id+".png")) { // legacy baked banner
		return
	}
	http.Error(w, "no banner", http.StatusNotFound)
}

// composeReportBanner overlays the deterministic text layer (from report
// frontmatter) onto the art plate if present, else onto synthesized paper.
func composeReportBanner(dir, id string) ([]byte, error) {
	report, _ := os.ReadFile(filepath.Join(dir, id+".md"))
	meta := parseBannerMeta(id, string(report))
	var plate image.Image
	if pb, err := os.ReadFile(filepath.Join(dir, id+".plate.png")); err == nil {
		plate, _, err = image.Decode(bytes.NewReader(pb)) // agy plates can be jpeg despite .png
		if err != nil {
			plate = nil
		}
	}
	if plate == nil {
		plate = blankPaper(1408, 792, meta.accent) // quiet-night typographic card
	}
	return encodePNG(composeBanner(plate, meta))
}

func fileMTime(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().Unix()
	}
	return 0
}

// serveFile writes path as an immutable png banner; returns false if unreadable.
func serveFile(w http.ResponseWriter, path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600") // banners are immutable per run id
	_, _ = w.Write(b)
	return true
}
