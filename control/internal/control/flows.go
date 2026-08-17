package control

// First-class task flows (ADR-0015).
//
// Profiles remain as a compatibility surface for the established nightly
// pipeline. Flows are the task-centric layer: one goal, one isolated worktree,
// a configurable absolute deadline, and a composition of small task-agnostic
// node roles. Every node is still persisted as an ordinary job, so launch-gap,
// auth pre-flight, Remote Control attachment, auto-stop, observability, and
// report delivery keep using the battle-tested scheduler path.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"nightshift/control/internal/run"
)

const (
	flowGoalMax       = 24 * 1024
	flowGuidanceMax   = 12 * 1024
	flowAcceptanceMax = 32
	// flowNodeMax is the run's LIFETIME node budget (ADR-0017): initial chain
	// plus every routed/remediation append. Exhausting it blocks the run loudly
	// instead of minting more work — the bound that makes verdict loops safe.
	flowNodeMax = 16
	// flowStageMax caps a parallel group's width. The box-wide launchGap
	// serializes starts anyway, so a wide group only stretches the fan-in wait.
	flowStageMax = 4
)

var flowIDRe = regexp.MustCompile(`^flow-[0-9]{8}-[0-9]{4}-[a-f0-9]{8}$`)

// routeEdge is one declared exception edge (ADR-0017): when Node reports
// Verdict, append the listed roles after it and re-gate its successors onto the
// appended tail. Routing lives here as automation data — never in Go, never in
// a node's prompt. Only needs-work routes today; ok/blocked/complete have fixed
// meanings.
type routeEdge struct {
	Node    string   `json:"node"`
	Verdict string   `json:"verdict"`
	Append  []string `json:"append"`
}

type flowTemplate struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Nodes       []string    `json:"nodes"`
	Stages      [][]string  `json:"stages,omitempty"` // happy path with parallel groups; absent = one stage per Nodes entry
	Edges       []routeEdge `json:"edges,omitempty"`  // declared exception edges (ADR-0017)
	// Schedule (ADR-0019) makes the template a recurring automation: the
	// scheduler instantiates a run daily at its wall-clock time, overlap-safe.
	// nil = on-demand only. Rides the same operator-owned JSON override path
	// as every other template field.
	Schedule *templateSchedule `json:"schedule,omitempty"`
	// Emitters declares which nodes may emit runtime children (ADR-0023).
	// Emission by any node not listed here is refused with an alert.
	Emitters []emitterSpec `json:"emitters,omitempty"`
	// MemberRuntime overrides effort/minutes/executor/model per STAGE MEMBER
	// ("attempt", "attempt#2") — the harness-comparison mechanism (ADR-0023):
	// two members of the same role, same pinned prompt, different engines.
	MemberRuntime map[string]nodeRuntimeOverride `json:"memberRuntime,omitempty"`
	Builtin       bool                           `json:"builtin,omitempty"`
	Customized    bool                           `json:"customized,omitempty"`
	Revision      string                         `json:"revision,omitempty"`
	UpdatedAt     int64                          `json:"updatedAt,omitempty"`
}

// memberSlotRe splits a stage member "role#N" into its base role and slot.
// `#` is illegal in a node id (customNodeIDRe), so the split is unambiguous —
// the same trick `__` plays for session slots.
var memberSlotRe = regexp.MustCompile(`^([a-z][a-z0-9-]{1,31})#([2-4])$`)

// baseRole resolves a stage member to the catalog role it instantiates:
// "attempt#2" → "attempt". Every def/prompt/runtime lookup goes through this;
// the slot suffix survives only in the node id (and hence the member's branch
// and worktree names, which must stay unique).
func baseRole(member string) string {
	if m := memberSlotRe.FindStringSubmatch(member); m != nil {
		return m[1]
	}
	return member
}

// isMemberSlot reports whether a member carries an explicit #N slot.
func isMemberSlot(member string) bool { return memberSlotRe.MatchString(member) }

// templateStages returns the template's effective happy path: explicit stages,
// or the legacy linear node list wrapped one-per-stage.
func templateStages(t flowTemplate) [][]string {
	if len(t.Stages) > 0 {
		return t.Stages
	}
	out := make([][]string, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		out = append(out, []string{n})
	}
	return out
}

func flattenStages(stages [][]string) []string {
	var out []string
	for _, s := range stages {
		out = append(out, s...)
	}
	return out
}

var flowTemplates = []flowTemplate{
	{ID: "refine-adr", Name: "Refine an ADR", Description: "Make an ADR decided, buildable, and independently reviewed.", Nodes: []string{"refine", "review", "amend", "validate"}, Builtin: true},
	{ID: "build-feature", Name: "Build a feature", Description: "Plan, implement, review, fix, and validate a scoped feature.", Nodes: []string{"plan", "implement", "review", "amend", "validate"}, Builtin: true},
	{ID: "full-delivery", Name: "Full delivery", Description: "Refine a proposal and carry it through a verified implementation.", Nodes: []string{"refine", "plan", "implement", "review", "amend", "validate"}, Builtin: true},
	{ID: "investigate", Name: "Investigate", Description: "Diagnose a problem, challenge the findings, and verify the conclusion.", Nodes: []string{"investigate", "review", "validate"}, Builtin: true},
	// Harness comparison (ADR-0023): the same goal and the same pinned prompt,
	// run by two engines in parallel, scored by one judge. memberRuntime is the
	// only difference between the members — that is the experiment.
	{ID: "compare-harness", Name: "Compare harnesses", Description: "Run one task on two engines in parallel and have a judge score both.",
		Stages: [][]string{{"attempt", "attempt#2"}, {"judge"}, {"validate"}},
		MemberRuntime: map[string]nodeRuntimeOverride{
			"attempt":   {Executor: "claude"},
			"attempt#2": {Executor: "codex", Model: "gpt-5.6-sol"},
		}, Builtin: true},
	// Runtime fan-out (ADR-0023): plan decides how many attempts the problem
	// deserves and emits them; the judge scores what comes back.
	{ID: "explore-attempts", Name: "Explore attempts", Description: "Plan emits N independent attempts at runtime; a judge scores them.",
		Stages:   [][]string{{"plan"}, {"validate"}},
		Emitters: []emitterSpec{{Node: "plan", Max: 3, Roles: []string{"attempt"}, FanIn: "judge"}}, Builtin: true},
}

type nodeDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Effort      string `json:"effort"`
	Minutes     int    `json:"minutes"`
	Output      string `json:"output"`
	// Executor is the ADR-0017 seam: which engine runs this node's sessions.
	// Allowlist is {claude, codex} (codex since ADR-0018); "" means claude.
	// Recorded on every minted job; the launcher dispatches on the .executor
	// sidecar.
	Executor string `json:"executor,omitempty"`
	// Model optionally pins the exact model id for this node's sessions
	// (e.g. "claude-fable-5", "gpt-5.6-terra"); "" keeps the executor's
	// scheduler default (executorModel). It must belong to the node's
	// executor's family — the launcher passes it through verbatim, so a
	// claude id on a codex node (or vice versa) kills the session at start.
	Model string `json:"model,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

var nodeDefinitions = map[string]nodeDefinition{
	"refine":      {ID: "refine", Name: "Refine", Description: "Resolve ambiguity and make the task or ADR buildable.", Effort: "high", Minutes: 90, Output: "Decision-ready task or ADR report", Icon: "refine"},
	"plan":        {ID: "plan", Name: "Plan", Description: "Produce an executable, dependency-ordered implementation plan.", Effort: "high", Minutes: 75, Output: "File-specific implementation plan report", Icon: "plan"},
	"implement":   {ID: "implement", Name: "Implement", Description: "Implement the plan completely and test the change.", Effort: "xhigh", Minutes: 300, Output: "Tested branch, draft PR/MR, and implementation report", Icon: "implement"},
	"investigate": {ID: "investigate", Name: "Investigate", Description: "Establish root cause from evidence and record the conclusion.", Effort: "xhigh", Minutes: 150, Output: "Evidence-backed diagnosis report", Icon: "investigate"},
	// review runs on codex (ADR-0018): a genuinely fresh pair of eyes — a
	// different model reviewing what claude built. validate (the acceptance
	// gate) stays claude.
	"review":    {ID: "review", Name: "Review", Description: "Adversarially inspect the real artifacts with fresh context.", Effort: "xhigh", Minutes: 90, Output: "Severity-ranked findings report", Executor: "codex", Icon: "review"},
	"amend":     {ID: "amend", Name: "Amend", Description: "Fix review or validation findings and add regressions.", Effort: "xhigh", Minutes: 180, Output: "Updated branch and verified fix report", Icon: "amend"},
	"validate":  {ID: "validate", Name: "Validate", Description: "Run acceptance checks and decide whether the goal is complete.", Effort: "high", Minutes: 90, Output: "Acceptance verdict with flow_status", Icon: "validate"},
	"preview":   {ID: "preview", Name: "Preview", Description: "Create and verify a realistic local preview.", Effort: "high", Minutes: 120, Output: "Verified preview evidence and teardown notes", Icon: "preview"},
	"integrate": {ID: "integrate", Name: "Integrate", Description: "Merge a parallel group's branches into the run branch.", Effort: "high", Minutes: 90, Output: "Merged run branch and integration report", Icon: "integrate"},
	// judge + attempt are the eval loop's pair (ADR-0023): attempt is the
	// generic contestant, judge the scorer. judge stays claude by default and
	// records its own identity on every score row, so self-preference is
	// auditable rather than assumed away.
	"judge":   {ID: "judge", Name: "Judge", Description: "Score the work of upstream nodes against explicit rubrics.", Effort: "high", Minutes: 45, Output: "Scores block plus a comparative verdict", Icon: "scribe"},
	"attempt": {ID: "attempt", Name: "Attempt", Description: "Deliver the run's goal exactly as briefed, for comparison.", Effort: "high", Minutes: 90, Output: "Deliverable per the run goal", Icon: "architect"},
}

// defaultNodePrompts MUST stay byte-identical to files/nightrun/nodes/<id>.md
// (via defaultNodeFile): the UI's built-in/custom signal and "Revert to
// built-in" compare against these. Guarded by
// TestDefaultNodePromptsMatchShippedFiles.
var defaultNodePrompts = map[string]string{
	"refine": `Resolve ambiguity and make the supplied task or ADR decided and buildable.
Inspect the real repository and prior art, correct stale assumptions, and update
the task artifact where appropriate. Do not implement the feature.`,
	"plan": `Turn the supplied goal and upstream artifacts into a dependency-ordered,
file-path-specific plan that a fresh implementation session can execute without
guessing. Trust the repository over prose and call out operator decisions.`,
	"implement": `Implement the supplied goal completely in the assigned worktree. Follow the
repository's technical and product guidance, add regression coverage, run the
relevant suites, commit and push, and create or update a draft PR/MR. Before
deferring, try a safe in-scope alternative and check the remaining flow time.`,
	"investigate": `Establish root cause from repository and runtime evidence. Reproduce where safe,
separate facts from hypotheses, and leave a precise diagnosis and smallest
justified remediation. Implement only when the flow goal explicitly asks for it.`,
	"review": `Review the actual diff and artifacts with fresh context. Record each actionable
finding with severity, location, evidence, concrete fix, and regression
requirement. Do not dilute the review by doing substantive fixes. When remote CI
is already green on the exact head SHA under review, cite it instead of
re-running the full local gate suite — spend the window reading the diff; run
locally only what CI does not cover or what a specific finding makes suspect.`,
	"amend": `Use the upstream review or validation report as a worklist. Verify and fix every
actionable issue, add regressions, rerun the relevant suites, and push the branch.
Do not relabel unfinished acceptance criteria as complete.`,
	"validate": "Evaluate the entire flow against its explicit acceptance criteria using the real\n" +
		"branch, tests, CI, preview, and artifacts. If an upstream review produced\n" +
		"findings, verify each one is individually closed — fix present AND its required\n" +
		"regression in place — not merely that the acceptance criteria pass; an\n" +
		"unaddressed finding without a recorded reason means needs-work. The report\n" +
		"frontmatter MUST include exactly one of `flow_status: complete`,\n" +
		"`flow_status: needs-work`, or `flow_status: blocked`. Use complete only when\n" +
		"every required criterion passes.",
	"preview": `Build the most realistic safe local preview from the flow branch, seed
representative data, and verify the user-visible path with real requests. Record
the URL, click-through, honest limitations, and teardown/lease requirements.`,
	"judge": "Score the upstream work against explicit, written-down rubrics — you are the\n" +
		"run's measuring instrument, not another builder. State each dimension's rubric\n" +
		"before you score it, read the real artifacts (diff, branch, tests, report), and\n" +
		"write the rationale BEFORE the number. When two or more subjects did the same\n" +
		"task, compare them in both orders and say which won on each dimension and why.\n" +
		"Score `unknown` whenever the evidence does not support a number — an honest\n" +
		"unknown is worth more than an invented 3. Never score work you produced\n" +
		"yourself; if a subject is your own, say so and score it unknown.\n" +
		"\n" +
		"Record every score in the report frontmatter, beside `verdict:`:\n" +
		"\n" +
		"```\n" +
		"scores:\n" +
		"  - subject: implement\n" +
		"    dimension: correctness\n" +
		"    score: 4\n" +
		"    max: 5\n" +
		"    rationale: \"tests pass; error paths unexercised\"\n" +
		"```\n" +
		"\n" +
		"`subject` names an upstream node id or role (or the artifact judged), `dimension`\n" +
		"is lowercase-kebab, `score` an integer or `unknown`. Your own `verdict:` says\n" +
		"whether the judged work meets the bar: `ok` or `needs-work` (never `complete` —\n" +
		"you are not the acceptance node).",
	"attempt": `Deliver the run's goal exactly as briefed, including any emitted input addressed to
you, so your result can be compared with the other attempts on equal terms. Do not
invent scope, negotiate the goal, or coordinate with sibling attempts. Work in your
own worktree and branch, verify what you built, and report what you actually did —
a judge reads this report next, and an honest partial result scores better than an
unverified claim.`,
	"integrate": `Merge every branch listed for your parallel group into the run branch in the
assigned worktree, resolving conflicts with judgment and preserving each
branch's intent. Rerun the relevant suites after merging and push the result.
Report any dropped or conflicting work honestly instead of forcing a merge.`,
}

type flowNodeRun struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	JobID   string `json:"jobId"`
	Round   int    `json:"round,omitempty"`
	AfterID string `json:"afterId,omitempty"` // legacy single-upstream (pre-ADR-0017 flows on disk)
	// ADR-0017 fields. AfterIDs supersedes AfterID (multi-upstream fan-in);
	// Stage is the happy-path stage index (-1 for routed appends); Worktree and
	// Branch are set only for parallel-group members, which get their own
	// checkout branched off the run branch at fire time. VerdictSeen records the
	// verdict reconcile has already acted on, so an edge fires exactly once.
	AfterIDs []string `json:"afterIds,omitempty"`
	// Stage is ALWAYS serialized: `omitempty` dropped integer stage 0, so the SPA
	// could not tell a two-member FIRST stage from two sequential nodes and drew
	// a comparison pair as stages 0 and 1 (ADR-0023 harness comparison).
	Stage       int    `json:"stage"`
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	VerdictSeen string `json:"verdictSeen,omitempty"`
	// EmittedBy names the emitter node that minted this one at runtime
	// (ADR-0023); "" for every statically-planned node. It is also the quorum
	// discriminator: an emitted group's fan-in launches on ≥1 delivered child,
	// a static stage still requires all of them.
	EmittedBy string `json:"emittedBy,omitempty"`
	// EmitInput is the emitter's private brief for this child, appended to its
	// prompt as `### Emitted input`. Write-once at emission, ≤ emitInputMax,
	// never exposed to the web (it is prompt input, not run state).
	EmitInput string `json:"emitInput,omitempty"`
}

// upstreams returns the node's gate list across both schema generations.
func (n flowNodeRun) upstreams() []string {
	if len(n.AfterIDs) > 0 {
		return n.AfterIDs
	}
	if n.AfterID != "" {
		return []string{n.AfterID}
	}
	return nil
}

type flow struct {
	ID         string   `json:"id"`
	Agent      string   `json:"agent"`
	Repo       string   `json:"repo"`
	Goal       string   `json:"goal"`
	Acceptance []string `json:"acceptance"`
	Guidance   string   `json:"guidance,omitempty"`
	// FastHandoff (opt-in): a node launches on the first tick after its upstream
	// verdict is routed, bypassing the box-wide launchGap wait. Starts stay
	// serialized at one per tick and the launcher's auth flock remains the
	// concurrent-refresh guard; default off keeps the conservative spacing.
	FastHandoff bool `json:"fastHandoff,omitempty"`
	// MaxConcurrent (ADR-0019): this run's cap on concurrently WORKING sessions.
	// 0 = the strict default of 1 — parallel-group members render fanned-out in
	// the UI but execute one at a time; raise it explicitly to run a group wide.
	MaxConcurrent      int         `json:"maxConcurrent,omitempty"`
	Template           string      `json:"template"`
	AutomationRevision string      `json:"automationRevision,omitempty"`
	NodeRoles          []string    `json:"nodeRoles"`
	Stages             [][]string  `json:"stages,omitempty"` // pinned happy path (ADR-0017); absent = linear NodeRoles
	Edges              []routeEdge `json:"edges,omitempty"`  // pinned exception edges (ADR-0017)
	// Emitters + MemberRuntime are pinned from the automation at create
	// (ADR-0023), like Stages/Edges: a later template edit must not widen a
	// live run's emission permissions or re-point its members' engines.
	Emitters       []emitterSpec                  `json:"emitters,omitempty"`
	MemberRuntime  map[string]nodeRuntimeOverride `json:"memberRuntime,omitempty"`
	Nodes          []flowNodeRun                  `json:"nodes"`
	Deadline       *time.Time                     `json:"deadline,omitempty"`
	Created        time.Time                      `json:"created"`
	Updated        time.Time                      `json:"updated"`
	Status         string                         `json:"status"` // queued|running|complete|blocked|deadline|stopped
	Batch          string                         `json:"batch"`
	Branch         string                         `json:"branch"`
	Base           string                         `json:"base"`
	SourceRepo     string                         `json:"sourceRepo"`
	Worktree       string                         `json:"worktree"`
	WorktreeState  string                         `json:"worktreeState"` // active|cleaned|retained
	Round          int                            `json:"round"`
	LastResultJob  string                         `json:"lastResultJob,omitempty"`
	CleanupMessage string                         `json:"cleanupMessage,omitempty"`
}

type flowNodeView struct {
	flowNodeRun
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Output         string    `json:"output"`
	Effort         string    `json:"effort"`
	Minutes        int       `json:"minutes"`
	Executor       string    `json:"executor"`        // claude | codex (ADR-0018)
	Model          string    `json:"model,omitempty"` // pinned model id; "" = executor default
	PromptID       string    `json:"promptId"`
	PromptRevision string    `json:"promptRevision"`
	StartedAt      time.Time `json:"startedAt,omitzero"`
	State          string    `json:"state"` // waiting|queued|running|delivered|skipped
	ReportID       string    `json:"reportId,omitempty"`
	Verdict        string    `json:"verdict,omitempty"` // delivered nodes only (ADR-0017)
}

type flowView struct {
	flow
	NodeViews []flowNodeView `json:"nodeViews"`
}

type repoOption struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
	Name  string `json:"name"`
}

func flowsDir() string                   { return filepath.Join(nsDir(), "flows") }
func flowPath(id string) string          { return filepath.Join(flowsDir(), id+".json") }
func nodesDir() string                   { return filepath.Join(nsDir(), "nodes") }
func flowUpdatesDir(agent string) string { return filepath.Join(nsDir(), "flow-updates", agent) }
func agentWorkspace(a string) string     { return filepath.Join(home, "workspace", a) }

func saveFlow(f flow) error {
	if !flowIDRe.MatchString(f.ID) {
		return errors.New("invalid flow id")
	}
	if err := os.MkdirAll(flowsDir(), 0o755); err != nil {
		return err
	}
	f.Updated = time.Now()
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := flowPath(f.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, flowPath(f.ID))
}

// normalize repairs nil slice fields on a flow loaded from disk. A
// spool-created run (nightshift-flow create, ADR-0019) may omit acceptance,
// which persists as JSON null; the SPA types it string[] and crashed on
// .length until 2026-07-28 — always serve [] so no client ever sees null.
func (f *flow) normalize() {
	if f.Acceptance == nil {
		f.Acceptance = []string{}
	}
}

func loadFlow(id string) (flow, error) {
	if !flowIDRe.MatchString(id) {
		return flow{}, errors.New("invalid flow id")
	}
	b, err := os.ReadFile(flowPath(id))
	if err != nil {
		return flow{}, err
	}
	var f flow
	if err := json.Unmarshal(b, &f); err != nil {
		return flow{}, err
	}
	f.normalize()
	return f, nil
}

func loadFlows() []flow {
	matches, _ := filepath.Glob(filepath.Join(flowsDir(), "*.json"))
	out := make([]flow, 0, len(matches))
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f flow
		if json.Unmarshal(b, &f) == nil && flowIDRe.MatchString(f.ID) {
			f.normalize()
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

func nodePrompt(role string) string {
	if b, err := os.ReadFile(filepath.Join(nodesDir(), role+".md")); err == nil && len(b) > 0 {
		return string(b)
	}
	return defaultNodePrompts[role]
}

func templateByID(id string) (flowTemplate, bool) {
	for _, t := range effectiveFlowTemplates() {
		if t.ID == id {
			return t, true
		}
	}
	return flowTemplate{}, false
}

func validateNodeRoles(roles []string) error {
	if len(roles) == 0 || len(roles) > flowNodeMax {
		return fmt.Errorf("want 1-%d nodes", flowNodeMax)
	}
	for _, role := range roles {
		if _, ok := nodeDefinitionByID(baseRole(role)); !ok {
			return fmt.Errorf("unknown node %q", role)
		}
	}
	return nil
}

// applyMemberRuntime layers a per-member override (ADR-0023) onto a resolved
// definition. Empty fields inherit — a member that only re-points its executor
// keeps the role's effort and minutes, so a comparison stays like-for-like on
// everything it does not deliberately vary.
func applyMemberRuntime(d nodeDefinition, o nodeRuntimeOverride) nodeDefinition {
	if o.Effort != "" && validEfforts[o.Effort] {
		d.Effort = o.Effort
	}
	if o.Minutes >= runMinutesFloor && o.Minutes <= runMinutesCeil {
		d.Minutes = o.Minutes
	}
	if o.Executor != "" && validExecutors[o.Executor] {
		d.Executor = o.Executor
		if o.Model == "" {
			// Re-pointing the engine without naming a model must not carry the
			// old family's pinned id across (a claude id kills a codex session
			// at start) — fall back to the new executor's scheduler default.
			d.Model = ""
		}
	}
	if o.Model != "" && modelIDRe.MatchString(o.Model) {
		d.Model = o.Model
	}
	return d
}

func validateStages(stages [][]string) error {
	if len(stages) == 0 {
		return errors.New("want at least one stage")
	}
	for _, s := range stages {
		if len(s) == 0 || len(s) > flowStageMax {
			return fmt.Errorf("a stage wants 1-%d nodes", flowStageMax)
		}
	}
	if err := validateMemberSlots(stages); err != nil {
		return err
	}
	return validateNodeRoles(flattenStages(stages))
}

// validateMemberSlots enforces the `role#N` grammar (ADR-0023). A slot is a
// SECOND member of one role inside one stage — the comparison shape — so a
// slotted member must be unique across the graph (its id, branch, and worktree
// are derived from it) and must not carry an out-of-range N.
func validateMemberSlots(stages [][]string) error {
	seen := map[string]bool{}
	for _, stage := range stages {
		for _, member := range stage {
			if !strings.Contains(member, "#") {
				continue
			}
			if !isMemberSlot(member) {
				return fmt.Errorf("invalid member slot %q (want role#2..role#4)", member)
			}
			if seen[member] {
				return fmt.Errorf("member slot %q appears more than once", member)
			}
			seen[member] = true
		}
	}
	return nil
}

// validateEdges checks the declared exception edges: only needs-work routes
// (ok/blocked/complete have fixed meanings), the source must be a happy-path
// role, and every appended role must exist in the catalog.
func validateEdges(edges []routeEdge, stages [][]string) error {
	if len(edges) > flowNodeMax {
		return fmt.Errorf("want at most %d edges", flowNodeMax)
	}
	roles := map[string]bool{}
	for _, r := range flattenStages(stages) {
		roles[r] = true
		roles[baseRole(r)] = true // a route may name the role or an exact member slot
	}
	for _, e := range edges {
		if e.Verdict != verdictNeedsWork {
			return fmt.Errorf("edge verdict must be %q", verdictNeedsWork)
		}
		if !roles[e.Node] {
			return fmt.Errorf("edge source %q is not on the happy path", e.Node)
		}
		if len(e.Append) == 0 || len(e.Append) > flowNodeMax {
			return fmt.Errorf("edge append wants 1-%d nodes", flowNodeMax)
		}
		for _, r := range e.Append {
			if _, ok := nodeDefinitionByID(baseRole(r)); !ok {
				return fmt.Errorf("edge appends unknown node %q", r)
			}
		}
	}
	return nil
}

func discoverRepos(agent string) []repoOption {
	root := agentWorkspace(agent)
	var out []repoOption
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && d.Name() != ".git" && strings.HasPrefix(d.Name(), ".") && path != root {
			return filepath.SkipDir
		}
		if rel != "." && len(strings.Split(filepath.ToSlash(rel), "/")) > 3 && d.IsDir() {
			return filepath.SkipDir
		}
		if d.Name() != ".git" {
			return nil
		}
		repoDir := filepath.Dir(path)
		r, err := filepath.Rel(root, repoDir)
		if err == nil && r != "." && !strings.HasPrefix(r, "..") {
			out = append(out, repoOption{Agent: agent, Path: filepath.ToSlash(r), Name: filepath.Base(repoDir)})
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func repoSource(agent, repo string) (string, error) {
	repo = filepath.Clean(filepath.FromSlash(strings.TrimSpace(repo)))
	if repo == "." || filepath.IsAbs(repo) || strings.HasPrefix(repo, "..") {
		return "", errors.New("invalid repository path")
	}
	root := agentWorkspace(agent)
	src := filepath.Join(root, repo)
	rel, err := filepath.Rel(root, src)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("repository escapes agent workspace")
	}
	if devMode {
		return src, nil
	}
	if _, err := os.Stat(filepath.Join(src, ".git")); err != nil {
		return "", errors.New("repository not found")
	}
	return src, nil
}

var runGit = func(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func prepareFlowWorktree(f *flow) error {
	src, err := repoSource(f.Agent, f.Repo)
	if err != nil {
		return err
	}
	if !devMode {
		// origin/<main> is only as fresh as the clone's last fetch: nothing
		// else on the box fetches these clones, so without this a run silently
		// bases itself on stale code (2026-08-17: the factory run designed
		// against a month-old main). Best-effort — a fetch failure (dead
		// token, network) is loud but non-fatal; a stale base still beats no
		// run, and the forge probe already surfaces dead tokens separately.
		if out, err := runGit("-C", src, "fetch", "--prune", "origin"); err != nil {
			logf("flow %s: fetch %s failed, branching from possibly stale base: %s", f.ID, src, strings.TrimSpace(out))
		}
	}
	base := strings.TrimSpace(f.Base)
	if base == "" || base == "default" {
		base = "origin/main"
		if !devMode {
			if out, err := runGit("-C", src, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
				base = out
			} else if _, err := runGit("-C", src, "rev-parse", "--verify", "origin/main"); err != nil {
				base = "origin/master"
			}
		}
	}
	if strings.ContainsAny(base, " \t\r\n~^:?*[\\") || strings.HasPrefix(base, "-") {
		return errors.New("invalid base ref")
	}
	f.Base = base
	f.SourceRepo = src
	f.Branch = "nightshift/" + f.ID
	f.Worktree = filepath.Join(agentWorkspace(f.Agent), ".nightshift-worktrees", f.ID, filepath.Base(src))
	if devMode {
		if err := os.MkdirAll(f.Worktree, 0o755); err != nil {
			return err
		}
		_ = os.WriteFile(filepath.Join(f.Worktree, ".nightshift-flow"), []byte(f.ID), 0o644)
		f.WorktreeState = "active"
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.Worktree), 0o755); err != nil {
		return err
	}
	if out, err := runGit("-C", src, "worktree", "add", "-b", f.Branch, f.Worktree, base); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", out, err)
	}
	f.WorktreeState = "active"
	return nil
}

// ---- verdicts (ADR-0017) -------------------------------------------------------

// The verdict vocabulary is sourced from the run domain kernel (ADR-0020) so the
// flow router and the scheduling kernel speak ONE ubiquitous language.
const (
	verdictOK        = string(run.VerdictOK)
	verdictNeedsWork = string(run.VerdictNeedsWork)
	verdictBlocked   = string(run.VerdictBlocked)
	verdictComplete  = string(run.VerdictComplete)
)

// normalizeVerdict maps a report's verdict line onto the fixed four-value
// vocabulary. Pre-ADR-0017 synonyms stay accepted so an old prompt revision
// can't strand a run. Unknown → "" (missing). The mapping lives in the kernel.
func normalizeVerdict(s string) string {
	return string(run.NormalizeVerdict(s))
}

// reportVerdict reads a node report's verdict from its frontmatter. `verdict:`
// is the ADR-0017 key; `flow_status:` remains an accepted alias.
func reportVerdict(agent, id string) string {
	b, err := os.ReadFile(reportPath(agent, id))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		for _, key := range []string{"verdict:", "flow_status:"} {
			if strings.HasPrefix(line, key) {
				if v := normalizeVerdict(strings.TrimPrefix(line, key)); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// edgeFor finds the flow's declared exception edge for a role, if any.
func edgeFor(f flow, role string) (routeEdge, bool) {
	for _, e := range f.Edges {
		if e.Node == role && e.Verdict == verdictNeedsWork {
			return e, true
		}
	}
	return routeEdge{}, false
}

// flowCap resolves a run's concurrency cap: its explicit maxConcurrent, or the
// ADR-0019 strict default of 1 — sessions of an ad-hoc run execute one at a
// time unless the operator opted the run up.
func flowCap(f flow) int {
	if f.MaxConcurrent > 0 {
		return f.MaxConcurrent
	}
	return 1
}

func effectiveStages(f flow) [][]string {
	if len(f.Stages) > 0 {
		return f.Stages
	}
	out := make([][]string, 0, len(f.NodeRoles))
	for _, r := range f.NodeRoles {
		out = append(out, []string{r})
	}
	return out
}

// nodeWorktree returns a parallel member's dedicated checkout path.
// flowNodeByID finds a node of a run by its node id.
func flowNodeByID(f flow, id string) (flowNodeRun, bool) {
	for _, n := range f.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return flowNodeRun{}, false
}

// acceptanceNodeID names the node whose verdict decides the run. Terminality is
// a property of the DEPENDENCY GRAPH, not of f.Nodes order: runtime fan-out
// (ADR-0023) appends its children and their fan-in AFTER the successors it
// re-gates, so the last slot is routinely a fan-in that another node still
// consumes. Reading `f.Nodes[len-1]` there made the judge the apparent tail of
// `explore-attempts` — its honest `ok` blocked the run and validate was reaped.
//
// The acceptance node is the LAST node nothing depends on: with a parallel tail
// that keeps the pre-ADR-0023 rule (the final member decides), and with an
// emitted group it is the static acceptance node the automation declared.
func acceptanceNodeID(f flow) string {
	dependents := map[string]bool{}
	for _, n := range f.Nodes {
		for _, up := range n.upstreams() {
			dependents[up] = true
		}
	}
	id := ""
	for _, n := range f.Nodes {
		if !dependents[n.ID] {
			id = n.ID
		}
	}
	if id == "" && len(f.Nodes) > 0 {
		// Every node depended-on means a cycle (no shape we mint can produce one).
		// Fall back to the tail so a malformed run still terminates.
		id = f.Nodes[len(f.Nodes)-1].ID
	}
	return id
}

func nodeWorktree(f flow, nodeID string) string {
	return filepath.Join(agentWorkspace(f.Agent), ".nightshift-worktrees", f.ID+"--"+nodeID, filepath.Base(f.SourceRepo))
}

func flowTaskSection(f flow, n flowNodeRun) string {
	worktree, branch := f.Worktree, f.Branch
	if n.Worktree != "" {
		worktree, branch = n.Worktree, n.Branch
	}
	var b strings.Builder
	b.WriteString("\n\n## Flow\n")
	fmt.Fprintf(&b, "- Flow: %s\n- Goal: %s\n- Repository: %s\n- Branch: %s\n- Worktree: %s\n", f.ID, f.Goal, f.Repo, branch, worktree)
	if n.Worktree != "" {
		fmt.Fprintf(&b, "- You are one member of a parallel group: work ONLY in your own worktree and branch above; a later integrate node merges the group into the run branch %s.\n", f.Branch)
	}
	if branches := groupBranches(f, n); len(branches) > 0 {
		// Any fan-in node (the built-in integrate, a judge behind an emitted
		// group, an operator's own role) needs the branches it consumes.
		fmt.Fprintf(&b, "- Group branches to merge or inspect (run branch %s): %s\n", f.Branch, strings.Join(branches, ", "))
	}
	if spec, ok := emitterFor(f.Emitters, n.Role, n.ID); ok {
		left := flowNodeMax - len(f.Nodes) - 1
		fmt.Fprintf(&b, "- You MAY emit up to %d runtime children (roles: %s), each consumed by a following %s node, by ending your report with a `## Emit` section. The run has %d sessions of budget left; emit only what the work genuinely needs.\n",
			min(spec.Max, max(left, 0)), strings.Join(spec.Roles, ", "), spec.FanIn, max(left, 0))
	}
	if f.Deadline != nil {
		fmt.Fprintf(&b, "- Finish by: %s (the flow may iterate until then)\n", f.Deadline.In(bishkek).Format(time.RFC3339))
	} else {
		b.WriteString("- Finish by: no flow deadline; individual session safety caps still apply\n")
	}
	if len(f.Acceptance) > 0 {
		b.WriteString("\n## Acceptance criteria\n")
		for _, a := range f.Acceptance {
			b.WriteString("- " + a + "\n")
		}
	}
	if f.Guidance != "" {
		b.WriteString("\n## Additional operator guidance\n" + f.Guidance + "\n")
	}
	b.WriteString("\nWork only in the assigned worktree. Keep the branch and report BODY current as you work, but hold the frontmatter `verdict:` line until the report is FINAL — the verdict is the machine handoff signal: the moment it appears, downstream nodes launch and read your report as-is. End the finished report's frontmatter with `verdict: ok|needs-work|blocked` for your own role's outcome; only the final acceptance node may declare `verdict: complete` for the whole flow.\n")
	b.WriteString("Optionally end the report with a `## Handoff` section (a few lines: branch state, open questions, gotchas) — it is appended verbatim to the next node's prompt.\n")
	if n.EmitInput != "" {
		// The emitter's private brief for THIS child (ADR-0023): why it exists
		// and what it is meant to try, straight from the node that asked for it.
		b.WriteString("\n### Emitted input (from " + n.EmittedBy + ", verbatim)\n\n" + n.EmitInput + "\n")
	}
	return b.String()
}

// groupBranches lists the branches of the parallel members an integrate node
// consumes — its direct upstreams that carry their own branch.
func groupBranches(f flow, n flowNodeRun) []string {
	var out []string
	for _, up := range n.upstreams() {
		for _, m := range f.Nodes {
			if m.ID == up && m.Branch != "" {
				out = append(out, m.Branch)
			}
		}
	}
	return out
}

func flowMinutes(f flow, wanted int, now time.Time) (int, bool) {
	wanted = min(max(wanted, runMinutesFloor), runMinutesCeil)
	if f.Deadline == nil {
		return wanted, true
	}
	left := int(f.Deadline.Sub(now).Minutes())
	if left < runMinutesFloor {
		return 0, false
	}
	return min(wanted, left), true
}

// appendFlowNode mints one node session as an ordinary job. It enforces the
// run's lifetime node budget (ADR-0017) — the bound that makes routed loops
// safe — and gives parallel-group members (parallel=true) their own worktree
// and branch, created lazily when the job fires.
func appendFlowNode(f *flow, role, id string, after []string, round, stage int, parallel bool, now time.Time) error {
	n := flowNodeRun{ID: id, Role: role, Round: round, Stage: stage, AfterIDs: after}
	if parallel {
		n.Branch = f.Branch + "--" + id
		n.Worktree = nodeWorktree(*f, id)
	}
	return appendFlowNodeRun(f, n, now)
}

// appendFlowNodeRun mints a PREPARED node (the caller has already decided its
// id, gating, worktree, and — for emitted children — its private input). The
// budget check, runtime resolution, and job persistence live here so every
// path that creates a session goes through exactly one gate.
func appendFlowNodeRun(f *flow, n flowNodeRun, now time.Time) error {
	if len(f.Nodes) >= flowNodeMax {
		return fmt.Errorf("node budget exhausted (%d sessions)", flowNodeMax)
	}
	role := baseRole(n.Role)
	n.Role = role
	d, _ := effectiveNodeDefinition(role)
	// Per-member runtime (ADR-0023) outranks the role-wide node-runtime
	// override, which outranks the definition: harness comparison is exactly
	// "same prompt, same role, different engine".
	d = applyMemberRuntime(d, f.MemberRuntime[n.ID])
	mins, ok := flowMinutes(*f, d.Minutes, now)
	if !ok {
		return errors.New("flow deadline leaves less than 10 minutes")
	}
	n.JobID = newRunID("flow", now)
	workdir := f.Worktree
	if n.Worktree != "" {
		workdir = n.Worktree
	}
	exec := effectiveExecutor(d)
	model := d.Model
	if model == "" {
		model = executorModel(exec)
	}
	j := job{
		ID: n.JobID, Agent: f.Agent, Prompt: nodePrompt(role) + flowTaskSection(*f, n),
		At: now, Kind: "flow", Model: model, Effort: d.Effort, Label: n.ID, Minutes: mins,
		Executor: exec, Created: now, Batch: f.Batch, FlowID: f.ID, NodeID: n.ID,
		Workdir: workdir, FinishBy: f.Deadline, FastStart: f.FastHandoff,
		Cap: flowCap(*f), CapGroup: f.ID,
	}
	if after := n.upstreams(); len(after) > 0 {
		j.After, j.Gated = after, true
	}
	if err := saveJob(j); err != nil {
		return err
	}
	f.Nodes = append(f.Nodes, n)
	return nil
}

func mintFlow(f *flow, now time.Time) error {
	var previous []string
	counts := map[string]int{}
	for si, stage := range effectiveStages(*f) {
		parallel := len(stage) > 1
		var ids []string
		for _, role := range stage {
			counts[role]++
			id := role
			if counts[role] > 1 {
				id = fmt.Sprintf("%s-%d", role, counts[role])
			}
			if err := appendFlowNode(f, role, id, previous, 0, si, parallel, now); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		previous = ids
	}
	return nil
}

func findFlowJob(f flow, jobID string) (job, bool) {
	for _, j := range loadJobs(f.Agent) {
		if j.ID == jobID {
			return j, true
		}
	}
	return job{}, false
}

// insertAfterNode mints roles sequentially after a delivered node (the routed
// exception path, ADR-0017), then re-gates every successor that waited on that
// node onto the appended tail — an honest insertion into the chain. The
// lifetime node budget is enforced by appendFlowNode.
func insertAfterNode(f *flow, curID string, roles []string, now time.Time) error {
	f.Round++
	var successors []int
	for i, n := range f.Nodes {
		for _, up := range n.upstreams() {
			if up == curID {
				successors = append(successors, i)
			}
		}
	}
	prev := []string{curID}
	lastID := curID
	counts := map[string]int{}
	for _, role := range roles {
		counts[role]++
		id := fmt.Sprintf("%s-r%d", role, f.Round)
		if counts[role] > 1 {
			id = fmt.Sprintf("%s-%d-r%d", role, counts[role], f.Round)
		}
		if err := appendFlowNode(f, role, id, prev, f.Round, -1, false, now); err != nil {
			return err
		}
		prev, lastID = []string{id}, id
	}
	for _, i := range successors {
		n := &f.Nodes[i]
		ups := append([]string(nil), n.upstreams()...)
		for k, up := range ups {
			if up == curID {
				ups[k] = lastID
			}
		}
		n.AfterIDs, n.AfterID = ups, ""
		if j, ok := findFlowJob(*f, n.JobID); ok && !j.Started {
			j.After = ups
			_ = saveJob(j)
		}
	}
	return nil
}

// reapUnstartedFlowJobs deletes a terminal run's node jobs that never ran AND
// never delivered, so nothing launches into (and no job JSON/sidecars leak from)
// a decided-dead chain. A delivered job's record is kept — it pins the run's
// prompt revisions for the ledger. Every terminal transition must call this;
// flowGateHold holds gated jobs of a terminal flow forever on the promise that
// they are "queued for deletion", so a transition that skips the reap leaks them.
func reapUnstartedFlowJobs(f flow) {
	for _, n := range f.Nodes {
		if j, ok := findFlowJob(f, n.JobID); ok && !j.Started && !reportExists(f.Agent, j.ID) {
			deleteJob(f.Agent, j.ID)
		}
	}
}

// reapOrphanFlowJobs deletes jobs that claim this run but are not (or no longer)
// nodes of it — the crash-recovery half of atomic emission (ADR-0023).
// applyEmissionLocked persists a child's job before the flow records the node, so
// a crash in that window leaves a gated job whose upstream has already delivered:
// the fire loop would launch it as a phantom session with no reconcile owner, and
// the restarted reconcile would emit a second group beside it. Reconcile runs
// before the fire loop every tick, so the reap always wins the race. Only
// unstarted, report-less jobs are touched — a delivered node's record is
// evidence, never garbage.
func reapOrphanFlowJobs(f flow) {
	known := map[string]bool{}
	for _, n := range f.Nodes {
		known[n.JobID] = true
	}
	for _, j := range loadJobs(f.Agent) {
		if j.FlowID != f.ID || known[j.ID] || j.Started || reportExists(f.Agent, j.ID) {
			continue
		}
		logf("flow %s: reaping orphan job %s (node %s)", f.ID, j.ID, j.NodeID)
		deleteJob(f.Agent, j.ID)
	}
}

// blockFlowLocked terminates a run that needs the operator: unstarted jobs are
// deleted so nothing launches into a decided-dead chain. Running sessions are
// left to finish and report — never killed under nsMu.
func blockFlowLocked(f *flow, reason string) {
	f.Status = "blocked"
	if reason != "" {
		f.CleanupMessage = reason
	}
	reapUnstartedFlowJobs(*f)
}

// flowGateHold keeps a gated flow job waiting even though upstream reports
// exist: reconcile hasn't acted on an upstream verdict yet (the next step may
// be a routed insertion, not this successor), an upstream said blocked, or the
// flow itself is already terminal.
func flowGateHold(agent string, j job) bool {
	f, err := loadFlow(j.FlowID)
	if err != nil {
		return false
	}
	switch f.Status {
	case "blocked", "stopped", "deadline", "complete":
		return true // terminal: the job is queued for deletion, never for launch
	}
	byID := map[string]flowNodeRun{}
	for _, n := range f.Nodes {
		byID[n.ID] = n
	}
	for _, up := range j.After {
		n, ok := byID[up]
		if !ok {
			continue
		}
		// A blocked EMITTED child does not hold its fan-in (ADR-0023 quorum): the
		// fan-in exists to weigh what the group produced, including a member that
		// gave up. A blocked static upstream still holds — its successor has no
		// input at all.
		if n.VerdictSeen == verdictBlocked && n.EmittedBy == "" {
			return true
		}
		if n.VerdictSeen == "" && reportExists(agent, n.JobID) {
			return true // delivered but unprocessed — reconcile owns the next step
		}
	}
	return false
}

// ensureFlowNodeWorktree creates a parallel member's dedicated checkout at
// fire time, branched from the run branch's current state (so the group builds
// on every prior sequential stage).
func ensureFlowNodeWorktree(j job) error {
	if j.FlowID == "" {
		return nil // profile waves have no flow worktree
	}
	f, err := loadFlow(j.FlowID)
	if err != nil {
		return err
	}
	var node flowNodeRun
	for _, n := range f.Nodes {
		if n.ID == j.NodeID {
			node = n
		}
	}
	if node.Worktree == "" {
		return nil
	}
	if _, err := os.Stat(node.Worktree); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(node.Worktree), 0o755); err != nil {
		return err
	}
	if devMode {
		if err := os.MkdirAll(node.Worktree, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(node.Worktree, ".nightshift-flow"), []byte(f.ID+"/"+node.ID), 0o644)
	}
	if out, err := runGit("-C", f.Worktree, "worktree", "add", "-b", node.Branch, node.Worktree, f.Branch); err != nil {
		return fmt.Errorf("git worktree add (%s): %s: %w", node.ID, out, err)
	}
	return nil
}

func flowToView(f flow) flowView {
	if f.AutomationRevision == "" {
		f.AutomationRevision = flowAutomationRevision(f)
	}
	if f.Nodes == nil {
		f.Nodes = []flowNodeRun{} // a node-less run must serialize as [], not null
	}
	v := flowView{flow: f, NodeViews: make([]flowNodeView, 0, len(f.Nodes))}
	for _, n := range f.Nodes {
		def, _ := effectiveNodeDefinition(n.Role)
		def = applyMemberRuntime(def, f.MemberRuntime[n.ID])
		body := nodePrompt(n.Role)
		// EmitInput is prompt input, not run state: it rides the job the
		// launcher reads, never the web payload (a child's private brief has no
		// reader on the page and would leak an emitter's raw text into it).
		n.EmitInput = ""
		nv := flowNodeView{
			flowNodeRun: n, Name: def.Name, Description: def.Description, Output: def.Output,
			Effort: def.Effort, Minutes: def.Minutes, Executor: effectiveExecutor(def), Model: def.Model,
			PromptID: "node-" + n.Role, PromptRevision: promptRevision(body), State: "queued",
		}
		if j, ok := findFlowJob(f, n.JobID); ok {
			nv.Effort, nv.Minutes, nv.StartedAt = j.Effort, j.Minutes, j.StartedAt
			if j.Executor != "" {
				nv.Executor = j.Executor // the persisted job is the truth the launcher sees
			}
			if j.Model != "" {
				nv.Model = j.Model
			}
			if prompt, _, ok := strings.Cut(j.Prompt, "\n\n## Flow\n"); ok {
				nv.PromptRevision = promptRevision(prompt)
			}
			switch {
			case reportExists(f.Agent, n.JobID):
				nv.State, nv.ReportID = "delivered", n.JobID
				nv.Verdict = reportVerdict(f.Agent, n.JobID)
			case j.Skipped || depSkipMarker(f.Agent, n.JobID) || authFailMarker(f.Agent, n.JobID) || watchdogMarker(f.Agent, n.JobID):
				// watchdogMarker: the unit is provably gone — without this the
				// node reads "running" until its window expires, hours after
				// the scheduler already released its slot (PR #57 review).
				nv.State = "skipped"
			case j.Started && runMaybeActive(j, time.Now()):
				nv.State = "running"
			case j.Started:
				nv.State = "stopped"
			case j.Gated:
				nv.State = "waiting"
			}
		} else if reportExists(f.Agent, n.JobID) {
			nv.State, nv.ReportID = "delivered", n.JobID
			nv.Verdict = reportVerdict(f.Agent, n.JobID)
		}
		v.NodeViews = append(v.NodeViews, nv)
	}
	return v
}

// appendRemediation is the acceptance node's default exception edge: a fresh
// amend→validate round. A declared edge for the acceptance role overrides it.
func appendRemediation(f *flow, now time.Time) error {
	last, ok := flowNodeByID(*f, acceptanceNodeID(*f))
	if !ok {
		return errors.New("run has no acceptance node")
	}
	roles := []string{"amend", "validate"}
	if e, ok := edgeFor(*f, last.Role); ok {
		roles = e.Append
	}
	return insertAfterNode(f, last.ID, roles, now)
}

func setFlowDeadlineLocked(f *flow, deadline *time.Time) {
	f.Deadline = deadline
	for _, n := range f.Nodes {
		if j, ok := findFlowJob(*f, n.JobID); ok && !j.Started {
			j.FinishBy = deadline
			if deadline != nil {
				def, _ := effectiveNodeDefinition(n.Role)
				if mins, ok := flowMinutes(*f, def.Minutes, time.Now()); ok {
					j.Minutes = mins
				}
			}
			_ = saveJob(j)
		}
	}
}

func setFlowGuidanceLocked(f *flow, guidance string) {
	f.Guidance = guidance
	for _, n := range f.Nodes {
		if j, ok := findFlowJob(*f, n.JobID); ok && !j.Started {
			// Keep the node instructions pinned at run creation. Editing run
			// guidance must not silently opt a queued node into a newer prompt.
			instructions, _, found := strings.Cut(j.Prompt, "\n\n## Flow\n")
			if !found {
				instructions = nodePrompt(n.Role)
			}
			j.Prompt = instructions + flowTaskSection(*f, n)
			_ = saveJob(j)
		}
	}
}

// applyFlowDeadlineUpdatesLocked consumes cwd-scoped requests written by the
// agent-side nightshift-flow CLI. Agents never edit flow/job JSON directly;
// the control plane remains the one validator and atomic writer.
func applyFlowDeadlineUpdatesLocked(now time.Time) {
	for _, agent := range companies {
		matches, _ := filepath.Glob(filepath.Join(flowUpdatesDir(agent), "*.deadline"))
		for _, p := range matches {
			id := strings.TrimSuffix(filepath.Base(p), ".deadline")
			f, err := loadFlow(id)
			if err != nil || f.Agent != agent {
				logf("flow deadline proposal %s rejected: unknown flow or wrong agent", p)
				_ = os.Remove(p)
				continue
			}
			body, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			value := strings.TrimSpace(string(body))
			var deadline *time.Time
			if value != "" && value != "none" {
				d, err := time.Parse(time.RFC3339, value)
				if err != nil || d.Before(now.Add(runMinutesFloor*time.Minute)) {
					logf("flow deadline proposal %s rejected: invalid/too near", p)
					_ = os.Remove(p)
					continue
				}
				deadline = &d
			}
			setFlowDeadlineLocked(&f, deadline)
			_ = saveFlow(f)
			_ = os.Remove(p)
		}
	}
}

// reconcileFlowsLocked is called by schedTick while nsMu is held. Verdicts are
// the feedback edges (ADR-0017): each delivered node is acted on exactly once —
// blocked blocks the run, a declared needs-work edge inserts its roles into the
// chain, and the acceptance node (the chain's tail) decides completion or a
// remediation round. The lifetime node budget turns runaway loops into a loud
// blocked run rather than an unbounded session factory.
func reconcileFlowsLocked(now time.Time) {
	applyFlowSpoolLocked(now)
	for _, f0 := range loadFlows() {
		f := f0
		if f.Status == "complete" || f.Status == "stopped" || f.Status == "blocked" || f.Status == "deadline" {
			continue
		}
		if f.Deadline != nil && now.After(*f.Deadline) {
			f.Status = "deadline"
			// Reap the still-gated/unstarted node jobs — like every other terminal
			// transition (blockFlowLocked, handleFlowStop). Without this the gated
			// downstream jobs are held forever by flowGateHold ("deadline" is
			// terminal) but never pruned, so their JSON + .stop/.model/.effort
			// sidecars leak on disk indefinitely.
			reapUnstartedFlowJobs(f)
			_ = saveFlow(f)
			continue
		}
		if len(f.Nodes) == 0 {
			continue
		}
		reapOrphanFlowJobs(f)
		dirty := false

		// Verdict pass: process newly delivered nodes in chain order. Bound the
		// loop to the nodes that existed at entry — an insertion's own nodes have
		// no reports yet and are picked up on a later tick.
		total := len(f.Nodes)
		for i := 0; i < total && f.Status != "blocked"; i++ {
			n := &f.Nodes[i]
			if n.VerdictSeen != "" {
				continue
			}
			if !reportExists(f.Agent, n.JobID) {
				// Terminal-without-report (ADR-0019) is an IMPLICIT blocked
				// verdict — a dead unit, auth-skip, or watchdog release must
				// route like any other blocker, never silently wedge the chain
				// behind a report that will never come.
				if j, ok := findFlowJob(f, n.JobID); ok && jobTerminalNoReport(f.Agent, j, now) {
					n.VerdictSeen = "terminal-no-report"
					// An EMITTED child is tolerable (ADR-0023): a runtime fan-out
					// exists to try several things, and its fan-in gates on quorum
					// ≥1. Blocking the run here ran before gatedReadiness could
					// ever apply that quorum, so one dead attempt killed the group.
					// A whole dead group still stops the run — as the fan-in's own
					// loud dep-skip, which cascades to the acceptance node.
					if n.EmittedBy == "" {
						blockFlowLocked(&f, "node "+n.ID+" ended without a report")
					}
					dirty = true
				}
				continue
			}
			v := reportVerdict(f.Agent, n.JobID)
			if v == "" {
				// Sessions keep the report current WHILE working (contract), so
				// a verdict-less report from a live session means "still
				// writing", not delivered — routing it hands half-written input
				// to the successor (2026-08-17 factory run: design launched two
				// minutes in, reading draft research). Wait for an explicit
				// verdict; fall through only once the session is definitively
				// over (markers or auto-stop window past — then it routes as
				// "none", the pre-existing fallback).
				if j, ok := findFlowJob(f, n.JobID); ok && j.Started && !jobTerminalNoReport(f.Agent, j, now) {
					continue
				}
			}
			// Evidence + runtime fan-out ride the same exactly-once step as the
			// verdict (ADR-0023): VerdictSeen is stamped below, so a node's
			// scores are persisted once and its emission applied once, no
			// matter how often reconcile runs.
			persistFlowScores(f, *n, now)
			seen := v
			if seen == "" {
				seen = "none"
			}
			if b, err := os.ReadFile(reportPath(f.Agent, n.JobID)); err == nil && hasEmitSection(string(b)) {
				reqs, perr := parseEmit(b)
				if perr == nil {
					// Stamp the exactly-once marker BEFORE the group is persisted:
					// applyEmissionLocked saves the flow itself, so the marker and
					// the children land in one write. A crash between them would
					// otherwise leave a run that re-emits a second group on restart.
					n.VerdictSeen = seen
					perr = applyEmissionLocked(&f, *n, v, reqs, now)
					// Applying (or rolling back) reallocates the node slice, so the
					// old `n` may point into a dead array — re-take it on BOTH paths
					// before anything else writes through it.
					n = &f.Nodes[i]
					if perr != nil {
						n.VerdictSeen = ""
					}
				}
				if perr != nil {
					logf("flow %s: emission from %s refused: %v", f.ID, n.ID, perr)
					if obsDB != nil {
						raiseAlert(obsDB, f.Agent, f.ID, "", "emit-refused",
							"run "+f.ID+": emission from node "+n.ID+" refused: "+perr.Error(),
							now.Unix(), now.Unix())
					}
				}
			}
			// Terminality comes from the dependency edges, re-read AFTER a possible
			// emission: the fan-in this node just appended sits in the last slot but
			// is consumed by the emitter's former successors.
			acceptance := n.ID == acceptanceNodeID(f)
			switch {
			case acceptance && v == verdictComplete:
				n.VerdictSeen = seen
				f.Status = "complete"
			case acceptance && v == verdictNeedsWork:
				n.VerdictSeen = seen
				if err := appendRemediation(&f, now); err != nil {
					blockFlowLocked(&f, "remediation stopped: "+err.Error())
				} else {
					f.Status = "running"
				}
			case acceptance:
				// blocked, ok, or a missing verdict: the acceptance node must
				// decide; anything else needs the operator.
				n.VerdictSeen = seen
				reason := "node " + n.ID + " reported blocked"
				if v != verdictBlocked {
					reason = "final report missing a complete|needs-work|blocked verdict"
				}
				blockFlowLocked(&f, reason)
			case v == verdictBlocked:
				n.VerdictSeen = seen
				// Same quorum tolerance as a report-less emitted child: a blocked
				// attempt DELIVERED its verdict, so the fan-in reads it and decides
				// — that is the judge's job, not reconcile's.
				if n.EmittedBy == "" {
					blockFlowLocked(&f, "node "+n.ID+" reported blocked")
				}
			case v == verdictNeedsWork:
				n.VerdictSeen = seen
				if e, ok := edgeFor(f, n.Role); ok {
					if err := insertAfterNode(&f, n.ID, e.Append, now); err != nil {
						blockFlowLocked(&f, "routing stopped: "+err.Error())
					}
				}
				// No declared edge mid-chain: proceed — the happy path's next
				// node (amend in the built-in templates) is the handler.
			default:
				// ok, or a mid-chain complete/missing verdict: proceed.
				n.VerdictSeen = seen
			}
			dirty = true
		}

		// Status pass: liveness and dead-chain detection for whatever the
		// verdict pass left non-terminal.
		if f.Status != "blocked" && f.Status != "complete" {
			// The acceptance node, not the last slot: a skipped fan-in cascades its
			// dep-skip down to acceptance, and THAT is what decides the run.
			last, _ := flowNodeByID(f, acceptanceNodeID(f))
			progressed := false
			anyBlocked := false
			for _, n := range f.Nodes {
				if reportExists(f.Agent, n.JobID) {
					progressed = true
				}
				if j, ok := findFlowJob(f, n.JobID); ok {
					progressed = progressed || j.Started
					anyBlocked = anyBlocked || j.Skipped || depSkipMarker(f.Agent, n.JobID) || authFailMarker(f.Agent, n.JobID)
				}
			}
			if progressed && f.Status == "queued" {
				f.Status = "running"
				dirty = true
			}
			if j, ok := findFlowJob(f, last.JobID); ok && (j.Skipped || depSkipMarker(f.Agent, last.JobID) || authFailMarker(f.Agent, last.JobID)) {
				f.Status = "blocked"
				dirty = true
			} else if anyBlocked && !progressed {
				f.Status = "blocked"
				dirty = true
			}
		}
		if dirty {
			_ = saveFlow(f)
		}
	}
}

// cleanupNodeWorktrees removes parallel members' checkouts once they are clean
// (integrate merged them into the run branch, so a clean member tree carries
// nothing unique). A dirty member worktree is retained with a visible reason.
func cleanupNodeWorktrees(f *flow) error {
	var retained []string
	for _, n := range f.Nodes {
		if n.Worktree == "" {
			continue
		}
		if _, err := os.Stat(n.Worktree); err != nil {
			continue // never created or already gone
		}
		if devMode {
			_ = os.RemoveAll(filepath.Dir(n.Worktree))
			continue
		}
		if out, err := runGit("-C", n.Worktree, "status", "--porcelain"); err != nil || out != "" {
			retained = append(retained, n.ID)
			continue
		}
		if out, err := runGit("-C", f.SourceRepo, "worktree", "remove", n.Worktree); err != nil {
			retained = append(retained, n.ID+" ("+out+")")
			continue
		}
		_ = os.Remove(filepath.Dir(n.Worktree))
		// A member branch integrate merged into the run branch carries nothing
		// unique — delete it so `<flow>--<node>` branches don't accumulate in
		// the source repo. Only when provably an ancestor of the run branch;
		// anything else is retained (a branch is cheap, lost work is not).
		if n.Branch != "" {
			if _, err := runGit("-C", f.SourceRepo, "merge-base", "--is-ancestor", n.Branch, f.Branch); err == nil {
				_, _ = runGit("-C", f.SourceRepo, "branch", "-D", n.Branch)
			}
		}
	}
	if len(retained) > 0 {
		return errors.New("member worktrees retained: " + strings.Join(retained, ", "))
	}
	return nil
}

func tryCleanupWorktree(f *flow) error {
	if f.WorktreeState == "cleaned" || f.Worktree == "" {
		return nil
	}
	if err := cleanupNodeWorktrees(f); err != nil {
		f.WorktreeState, f.CleanupMessage = "retained", err.Error()
		return err
	}
	if devMode {
		if err := os.RemoveAll(filepath.Dir(f.Worktree)); err != nil {
			return err
		}
		f.WorktreeState, f.CleanupMessage = "cleaned", "dev worktree removed"
		return nil
	}
	// A worktree dir that is already gone (manually removed, or lost to a crash/
	// reboot) is cleaned, not retained. Without this, `git status` below errors on
	// the missing dir and the tree wedges in "retained" forever, escalating to a
	// bogus worktree-retained alert at 48h. Treat absence as done and best-effort
	// prune the stale metadata `git worktree remove` never saw.
	if _, err := os.Stat(f.Worktree); os.IsNotExist(err) {
		_, _ = runGit("-C", f.SourceRepo, "worktree", "prune")
		f.WorktreeState, f.CleanupMessage = "cleaned", "worktree already absent"
		return nil
	}
	if out, err := runGit("-C", f.Worktree, "status", "--porcelain"); err != nil || out != "" {
		f.WorktreeState, f.CleanupMessage = "retained", "worktree is dirty or unreadable"
		return errors.New(f.CleanupMessage)
	}
	if _, err := runGit("-C", f.Worktree, "rev-parse", "@{upstream}"); err != nil {
		f.WorktreeState, f.CleanupMessage = "retained", "branch has no pushed upstream"
		return errors.New(f.CleanupMessage)
	}
	if out, err := runGit("-C", f.Worktree, "rev-list", "@{upstream}..HEAD"); err != nil || out != "" {
		f.WorktreeState, f.CleanupMessage = "retained", "branch contains unpushed commits"
		return errors.New(f.CleanupMessage)
	}
	if out, err := runGit("-C", f.SourceRepo, "worktree", "remove", f.Worktree); err != nil {
		f.WorktreeState, f.CleanupMessage = "retained", "worktree removal failed: "+out
		return err
	}
	_ = os.Remove(filepath.Dir(f.Worktree))
	f.WorktreeState, f.CleanupMessage = "cleaned", "clean pushed worktree removed"
	return nil
}

func flowRunActive(f flow) bool {
	for _, n := range f.Nodes {
		j, ok := findFlowJob(f, n.JobID)
		if !ok || !j.Started || !runMaybeActive(j, time.Now()) {
			continue
		}
		if state, err := rcRun("run-status", f.Agent, j.ID); err == nil && state == "active" {
			return true
		}
	}
	return false
}

// startFlowJanitor removes clean, fully-pushed worktrees of TERMINAL runs
// (ADR-0019: complete, stopped, deadline, and — after a grace for operator
// inspection — blocked; cleanup on `complete` only leaked a tree on every
// blocked night) once their last session has exited. Anything dirty,
// unpushed, preview-leased, or otherwise ambiguous is retained with a visible
// reason; a tree retained long enough escalates to an alert instead of
// retrying silently forever. Each tick also prunes stale git worktree
// metadata (crash/reboot leftovers `git worktree remove` never saw).
func startFlowJanitor() {
	go func() {
		for {
			flowJanitorTick()
			time.Sleep(5 * time.Minute)
		}
	}()
}

const (
	// blockedCleanupGrace: a blocked run sits in the Inbox for the operator —
	// don't remove its worktree out from under a morning investigation.
	blockedCleanupGrace = 24 * time.Hour
	// retainedEscalateAfter: a worktree still retained this long after its
	// run went terminal is an operator decision, not a retry loop.
	retainedEscalateAfter = 48 * time.Hour
)

func flowTerminal(f flow) bool {
	switch f.Status {
	case "complete", "stopped", "deadline", "blocked":
		return true
	}
	return false
}

func janitorEligible(f flow, now time.Time) bool {
	if !flowTerminal(f) || f.WorktreeState == "cleaned" {
		return false
	}
	if f.Status == "blocked" && now.Sub(f.Updated) < blockedCleanupGrace {
		return false
	}
	return true
}

func flowJanitorTick() {
	now := time.Now()
	nsMu.Lock()
	candidates := []flow{}
	for _, f := range loadFlows() {
		if janitorEligible(f, now) {
			candidates = append(candidates, f)
		}
	}
	nsMu.Unlock()
	pruneRepos := map[string]bool{}
	for _, candidate := range candidates {
		if flowRunActive(candidate) {
			continue
		}
		nsMu.Lock()
		f, err := loadFlow(candidate.ID)
		if err == nil && janitorEligible(f, now) {
			before := f.WorktreeState + "|" + f.CleanupMessage
			_ = tryCleanupWorktree(&f)
			pruneRepos[f.SourceRepo] = true
			// Save only on a state change: an unconditional save would bump
			// f.Updated every 5-minute tick and the escalation age below could
			// never accrue.
			if f.WorktreeState+"|"+f.CleanupMessage != before {
				_ = saveFlow(f)
			} else if f.WorktreeState == "retained" && now.Sub(f.Updated) > retainedEscalateAfter && obsDB != nil {
				raiseAlert(obsDB, f.Agent, f.ID, "", "worktree-retained",
					fmt.Sprintf("run %s (%s) worktree retained %dh: %s — operator decision needed",
						f.ID, f.Status, int(now.Sub(f.Updated).Hours()), f.CleanupMessage),
					f.Updated.Unix(), now.Unix())
			}
		}
		nsMu.Unlock()
	}
	// Prune stale worktree metadata once per source repo touched this tick —
	// a crash mid-run or a manual rm leaves .git/worktrees entries that
	// `worktree remove` never saw and that block future adds.
	if !devMode {
		for repo := range pruneRepos {
			if repo != "" {
				_, _ = runGit("-C", repo, "worktree", "prune")
			}
		}
	}
}

func handleFlowCatalog(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	defer nsMu.Unlock()
	writeJSON(w, map[string]any{"templates": effectiveFlowTemplates(), "nodes": nodeCatalogViews()})
}

func handleRepos(w http.ResponseWriter, _ *http.Request) {
	out := []repoOption{}
	for _, a := range companies {
		out = append(out, discoverRepos(a)...)
	}
	if devMode && len(out) == 0 {
		out = []repoOption{{Agent: "playground", Path: "example-project", Name: "example-project"}}
	}
	writeJSON(w, out)
}

func handleFlows(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	flows := loadFlows()
	out := make([]flowView, 0, len(flows))
	for _, f := range flows {
		out = append(out, flowToView(f))
	}
	nsMu.Unlock()
	writeJSON(w, out)
}

func handleFlowGet(w http.ResponseWriter, r *http.Request) {
	nsMu.Lock()
	f, err := loadFlow(r.PathValue("id"))
	if err == nil {
		writeJSON(w, flowToView(f))
	}
	nsMu.Unlock()
	if err != nil {
		http.Error(w, "flow not found", http.StatusNotFound)
	}
}

func handleFlowCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		flowSpec
		Deadline string `json:"deadline"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Deadline != "" {
		d, err := time.Parse(time.RFC3339, in.Deadline)
		if err != nil || d.Before(time.Now().Add(runMinutesFloor*time.Minute)) {
			http.Error(w, "deadline must be RFC3339 and at least 10 minutes ahead", http.StatusBadRequest)
			return
		}
		in.flowSpec.Deadline = &d
	}
	now := time.Now()
	nsMu.Lock()
	defer nsMu.Unlock()
	f, err := createFlowLocked(in.flowSpec, now)
	if err != nil {
		slog.Default().WarnContext(r.Context(), "flow.create_failed", "agent", in.Agent, "repo", in.Repo, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Default().InfoContext(r.Context(), "flow.created",
		"agent", f.Agent, "flow_id", f.ID, "repo", f.Repo,
		"template", f.Template, "nodes", len(f.NodeRoles), "fast_handoff", f.FastHandoff,
	)
	writeJSON(w, flowToView(f))
}

func handleFlowDeadline(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Deadline string `json:"deadline"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var deadline *time.Time
	if in.Deadline != "" {
		d, err := time.Parse(time.RFC3339, in.Deadline)
		if err != nil || d.Before(time.Now().Add(runMinutesFloor*time.Minute)) {
			http.Error(w, "deadline must be at least 10 minutes ahead", http.StatusBadRequest)
			return
		}
		deadline = &d
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	f, err := loadFlow(r.PathValue("id"))
	if err != nil {
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	if flowTerminal(f) {
		// A terminal run has no unstarted jobs to re-clamp, and saveFlow would
		// bump f.Updated — resetting the janitor's blocked-cleanup / retained-
		// escalate clocks (janitorEligible keys on Updated). Reject instead.
		http.Error(w, "flow already terminal", http.StatusConflict)
		return
	}
	setFlowDeadlineLocked(&f, deadline)
	_ = saveFlow(f)
	writeJSON(w, flowToView(f))
}

func handleFlowGuidance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Guidance string `json:"guidance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in.Guidance = strings.TrimSpace(in.Guidance)
	if len(in.Guidance) > flowGuidanceMax {
		http.Error(w, "guidance is too long", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	f, err := loadFlow(r.PathValue("id"))
	if err != nil {
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	if flowTerminal(f) {
		// No unstarted jobs to steer, and saveFlow would bump f.Updated and reset
		// the janitor cleanup clocks (see handleFlowDeadline). Reject instead —
		// mirrors the spool append path's terminal guard.
		http.Error(w, "flow already terminal", http.StatusConflict)
		return
	}
	setFlowGuidanceLocked(&f, in.Guidance)
	if err := saveFlow(f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, flowToView(f))
}

func handleFlowStop(w http.ResponseWriter, r *http.Request) {
	nsMu.Lock()
	f, err := loadFlow(r.PathValue("id"))
	if err != nil {
		nsMu.Unlock()
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	if flowTerminal(f) {
		// Never overwrite a finished run's outcome: stopping a "complete" flow
		// used to clobber it to "stopped", so the ledger + UI misreported it as
		// operator-cancelled. handleFlowCleanup already guards this way.
		nsMu.Unlock()
		http.Error(w, "flow already terminal", http.StatusConflict)
		return
	}
	f.Status = "stopped"
	for _, n := range f.Nodes {
		if j, ok := findFlowJob(f, n.JobID); ok && !j.Started {
			deleteJob(f.Agent, j.ID)
		}
	}
	_ = saveFlow(f)
	nsMu.Unlock()
	// Running sessions are stopped outside nsMu: the sudo/systemd call may block.
	for _, n := range f.Nodes {
		if j, ok := findFlowJob(f, n.JobID); ok && j.Started && runMaybeActive(j, time.Now()) {
			_, _ = rcRun("stop-run", f.Agent, j.ID)
		}
	}
	writeJSON(w, flowToView(f))
}

func handleFlowCleanup(w http.ResponseWriter, r *http.Request) {
	nsMu.Lock()
	f, err := loadFlow(r.PathValue("id"))
	if err != nil {
		nsMu.Unlock()
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	if !flowTerminal(f) {
		nsMu.Unlock()
		http.Error(w, "flow must be terminal before cleanup", http.StatusConflict)
		return
	}
	nsMu.Unlock()
	if flowRunActive(f) {
		http.Error(w, "a flow session is still active", http.StatusConflict)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	f, err = loadFlow(f.ID)
	if err != nil {
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	_ = tryCleanupWorktree(&f)
	_ = saveFlow(f)
	writeJSON(w, flowToView(f))
}
