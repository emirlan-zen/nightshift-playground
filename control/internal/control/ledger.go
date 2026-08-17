package control

// Run outcome ledger (ADR-0017): a METADATA-ONLY view of every run's outcome,
// derived on read from the persisted flows, jobs, and report frontmatter — no
// new store, nothing to reap. It records which automation and prompt revisions
// ran, each node's verdict, remediation rounds, durations, executor, and the
// terminal status. Never report or transcript content. This is the evidence
// base for the system retro's proposals and the operator's own tuning; the
// boundary follows the Usage-dashboard precedent.

import (
	"net/http"
	"time"
)

type ledgerNode struct {
	ID             string    `json:"id"`
	Role           string    `json:"role"`
	Round          int       `json:"round,omitempty"`
	Verdict        string    `json:"verdict,omitempty"`
	State          string    `json:"state"`
	Effort         string    `json:"effort,omitempty"`
	Minutes        int       `json:"minutes,omitempty"`
	Executor       string    `json:"executor,omitempty"`
	PromptRevision string    `json:"promptRevision,omitempty"`
	StartedAt      time.Time `json:"startedAt,omitzero"`
	DurationMin    int       `json:"durationMin,omitempty"`
	Parallel       bool      `json:"parallel,omitempty"`
	// Queue stamps (ADR-0019): when the session became launchable vs when it
	// launched — the evidence base for tuning caps, tiers, and deltas.
	QueuedAt time.Time `json:"queuedAt,omitzero"`
	WaitMin  int       `json:"waitMin,omitempty"`
	// EmittedBy names the emitter that minted this node at runtime (ADR-0023);
	// empty for statically-planned nodes. Scores are what this node RECORDED as
	// a judge — the eval loop's evidence, alongside its own verdict.
	EmittedBy string      `json:"emittedBy,omitempty"`
	Scores    []scoreView `json:"scores,omitempty"`
}

type ledgerRun struct {
	ID                 string       `json:"id"`
	Agent              string       `json:"agent"`
	Repo               string       `json:"repo"`
	Template           string       `json:"template,omitempty"`
	AutomationRevision string       `json:"automationRevision,omitempty"`
	Status             string       `json:"status"`
	Rounds             int          `json:"rounds"`
	MaxConcurrent      int          `json:"maxConcurrent,omitempty"` // effective cap (ADR-0019)
	Created            time.Time    `json:"created"`
	Updated            time.Time    `json:"updated"`
	Nodes              []ledgerNode `json:"nodes"`
}

func ledgerRuns() []ledgerRun {
	flows := loadFlows()
	out := make([]ledgerRun, 0, len(flows))
	for _, f := range flows {
		v := flowToView(f)
		run := ledgerRun{
			ID: f.ID, Agent: f.Agent, Repo: f.Repo, Template: f.Template,
			AutomationRevision: v.AutomationRevision, Status: f.Status,
			Rounds: f.Round, MaxConcurrent: flowCap(f), Created: f.Created, Updated: f.Updated,
			Nodes: []ledgerNode{},
		}
		for _, nv := range v.NodeViews {
			ln := ledgerNode{
				ID: nv.ID, Role: nv.Role, Round: nv.Round, State: nv.State,
				Effort: nv.Effort, Minutes: nv.Minutes, PromptRevision: nv.PromptRevision,
				StartedAt: nv.StartedAt, Parallel: nv.Worktree != "", EmittedBy: nv.EmittedBy,
			}
			if obsDB != nil {
				ln.Scores = queryNodeScores(obsDB, f.ID, nv.ID)
			}
			if j, ok := findFlowJob(f, nv.JobID); ok {
				ln.Executor = j.Executor
				ln.QueuedAt = j.At
				if j.Started && !j.StartedAt.IsZero() && !j.At.IsZero() {
					if w := int(j.StartedAt.Sub(j.At).Minutes()); w > 0 {
						ln.WaitMin = w
					}
				}
			}
			if ln.Executor == "" {
				ln.Executor = "claude"
			}
			if nv.State == "delivered" {
				ln.Verdict = reportVerdict(f.Agent, nv.JobID)
				// Duration approximated from the report file's mtime — metadata
				// the control plane already owns; good enough for tuning.
				if !nv.StartedAt.IsZero() {
					if end := fileMTime(reportPath(f.Agent, nv.JobID)); end > 0 {
						if d := int(time.Unix(end, 0).Sub(nv.StartedAt).Minutes()); d >= 0 {
							ln.DurationMin = d
						}
					}
				}
			}
			run.Nodes = append(run.Nodes, ln)
		}
		out = append(out, run)
	}
	return out
}

func handleLedger(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	runs := ledgerRuns()
	nsMu.Unlock()
	writeJSON(w, runs)
}
