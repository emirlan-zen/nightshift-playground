// Package run is the scheduling domain kernel for nightshift runs (ADR-0020).
//
// It holds the value types and pure decisions the launch queue reasons about —
// executors, verdicts, launch tiers, and the scheduling projection of a job —
// with NO I/O and NO dependency on net/http or the control composition root.
// The control package maps its richer persisted `job` onto run.Job at the
// boundary; the queue package makes admission decisions over these values.
//
// Keeping the kernel dependency-free is what lets a night be reconstructed from
// logs (Job.LogAttrs is the one stable lifecycle schema) and lets the decisions
// be table-tested in isolation. The architecture test (control/architecture_test.go) locks the
// import direction: run imports neither the transport layer nor the control
// package.
package run

import (
	"sort"
	"strings"
	"time"
)

// Executor is the engine that runs a session (ADR-0017/0018). "" and "claude"
// both mean Claude Code; "codex" runs the OpenAI Codex CLI.
type Executor string

const (
	ExecutorClaude Executor = "claude"
	ExecutorCodex  Executor = "codex"
)

// NormalizeExecutor canonicalizes a raw executor string: trimmed, lower-cased,
// and an empty value defaulted to Claude. An unknown value is preserved (so a
// caller can report it) but reports Valid() == false.
func NormalizeExecutor(s string) Executor {
	switch e := Executor(strings.ToLower(strings.TrimSpace(s))); e {
	case "", ExecutorClaude:
		return ExecutorClaude
	default:
		return e
	}
}

// Valid reports whether the executor is one the launcher can dispatch.
func (e Executor) Valid() bool { return e == ExecutorClaude || e == ExecutorCodex }

func (e Executor) String() string { return string(e) }

// Executors lists the engines the scheduler knows how to dispatch.
func Executors() []Executor { return []Executor{ExecutorClaude, ExecutorCodex} }

// Verdict is a node report's outcome (ADR-0017). The fixed four-value
// vocabulary routes the flow; pre-ADR-0017 synonyms normalize onto it so an old
// prompt revision can't strand a run.
type Verdict string

const (
	VerdictOK        Verdict = "ok"
	VerdictNeedsWork Verdict = "needs-work"
	VerdictBlocked   Verdict = "blocked"
	VerdictComplete  Verdict = "complete"
)

// NormalizeVerdict maps a report's verdict line onto the fixed vocabulary.
// Unknown → "" (missing).
func NormalizeVerdict(s string) Verdict {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case string(VerdictOK), "done":
		return VerdictOK
	case string(VerdictNeedsWork), "partial":
		return VerdictNeedsWork
	case string(VerdictBlocked), "needs-decision":
		return VerdictBlocked
	case string(VerdictComplete):
		return VerdictComplete
	}
	return ""
}

// Tier is a job's launch priority. Operator-queued work jumps ahead of the
// night's elastic workers, and deadline-carrying spine waves jump ahead of
// open-ended ones — a 7h exec worker must not starve synth out of its window.
type Tier int

const (
	TierOperator Tier = iota // Kind "deferred" — a human queued it
	TierDeadline             // carries a Deadline or FinishBy — a window to protect
	TierElastic              // everything else
)

func (t Tier) String() string {
	switch t {
	case TierOperator:
		return "operator"
	case TierDeadline:
		return "deadline"
	default:
		return "elastic"
	}
}

// Job is the scheduling projection of a control-plane job: only the fields the
// launch queue reasons about. The control package owns the richer persisted
// struct and maps it onto this value at the boundary, so the kernel never
// depends on persistence, HTTP shapes, or the composition root.
type Job struct {
	ID        string
	Agent     string
	Node      string // flow node id, "" for a night wave
	Kind      string // "deferred" | "sweep"
	Executor  Executor
	Label     string
	At        time.Time  // due time
	Deadline  string     // profile "HH:MM" clamp, "" = none
	FinishBy  *time.Time // absolute flow deadline, nil = none
	Minutes   int        // requested auto-stop window
	StartedAt time.Time
	Cap       int    // per-automation max concurrent sessions, 0 = uncapped
	CapGroup  string // the automation instance this session belongs to
	FastStart bool   // launch on the first due/ungated tick, bypassing the launch gap
}

// Tier classifies the job for launch ordering.
func (j Job) Tier() Tier {
	switch {
	case j.Kind == "deferred":
		return TierOperator
	case j.Deadline != "" || j.FinishBy != nil:
		return TierDeadline
	default:
		return TierElastic
	}
}

// LogAttrs is the ONE lifecycle log schema (ADR-0020): the stable top-level
// identity attributes — run_id, node, agent, executor — every scheduler
// transition carries, whether it started, failed to start, was skipped, held,
// resumed, or watchdog-released. Because the identity is always at the top level
// under the same keys, a whole night is reconstructable from the journal by a
// single `run_id` filter regardless of which transition fired. The application
// service (internal/scheduler) and the control adapter both emit through it, so
// the scheduler speaks one query schema instead of a mix of top-level and
// nested-`job.run` forms.
func (j Job) LogAttrs() []any {
	return []any{
		"run_id", j.ID,
		"agent", j.Agent,
		"node", j.Node,
		"executor", string(NormalizeExecutor(string(j.Executor))),
	}
}

// Before is the deterministic launch-order comparator: tier, then due time,
// then id. The id tiebreak is what makes a control-plane restart replay the
// same queue instead of reshuffling it.
func Before(a, b Job) bool {
	if ta, tb := a.Tier(), b.Tier(); ta != tb {
		return ta < tb
	}
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	return a.ID < b.ID
}

// SortByLaunchOrder orders a tick's launch queue in place, stably.
func SortByLaunchOrder(js []Job) {
	sort.SliceStable(js, func(i, k int) bool { return Before(js[i], js[k]) })
}
