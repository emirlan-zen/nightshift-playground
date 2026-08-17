package control

// Emit-nodes — runtime fan-out (ADR-0023).
//
// A graph's shape is fixed when a run is created; what a node LEARNS while it
// works is not. An emitter node ends its report with a `## Emit` section
// declaring the children it wants (three attempts at a hard problem, one
// investigation per failing subsystem it found), and reconcile mints them as an
// ordinary parallel group with a fan-in node behind them.
//
// The capability is declared, not discovered: only nodes listed in the
// automation's `emitters` may emit, only into the roles that spec allows, no
// wider than its `max`, and never past the run's lifetime session budget. A
// refused emission raises an `emit-refused` alert and the run keeps routing —
// an over-eager node loses its children, never its chain.
//
// Depth is 1 by construction: a child role may not itself be an emitter of the
// same automation (checked at template save, so a run cannot be born able to
// recurse).

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// emitInputMax bounds one child's private brief (the handoff constant): the
	// prompt it is appended to is already large, and a runaway block is a bug,
	// not a brief.
	emitInputMax = 4 * 1024
	// emitMaxWidth caps a single emitter's declared width. Wider than a static
	// stage (flowStageMax) is pointless — the box-wide launch gap serialises
	// starts anyway — but a spec is allowed to declare up to this and let the
	// node decide how many it actually needs.
	emitMaxWidth = 8
	// emitRolesMax bounds the allowed-role menu on one emitter spec.
	emitRolesMax = 8
)

// emitterSpec declares that a node may emit runtime children. It lives on the
// automation (template) and is COPIED onto the run at create, so editing the
// template can never widen a live run's permissions (ADR-0016 pinning).
type emitterSpec struct {
	Node  string   `json:"node"`  // role (or member slot) on this automation's happy path
	Max   int      `json:"max"`   // width cap, 1..emitMaxWidth
	Roles []string `json:"roles"` // allowed child roles (catalog ids)
	FanIn string   `json:"fanIn"` // role that consumes the group (integrate, judge, …)
}

// emitReq is one requested child: a role, plus an optional private brief that
// is appended to that child's prompt as `### Emitted input`.
type emitReq struct {
	Role  string
	Input string
}

// extractSection slices a `## <title>` section out of a report body: from the
// heading to the next `## ` heading (or EOF). Shared by the handoff scanner and
// the emit parser so both tolerate exactly the same heading sloppiness.
func extractSection(report, title string) (string, bool) {
	lines := strings.Split(report, "\n")
	start := -1
	want := "## " + strings.ToLower(title)
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), want) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

// hasEmitSection reports whether a report asked to emit at all — the signal
// that a refusal is worth alerting about (a report with no section is the
// normal case and must stay silent).
func hasEmitSection(report string) bool {
	_, ok := extractSection(report, "Emit")
	return ok
}

// parseEmit reads the `## Emit` section:
//
//	## Emit
//	- role: attempt
//	  input: |
//	    Try approach B: constraint-solver first, greedy fallback.
//	- role: attempt
//
// A malformed section is an ERROR, not a partial application: emission is
// all-or-nothing, so half a fan-out never launches.
func parseEmit(report []byte) ([]emitReq, error) {
	body, ok := extractSection(string(report), "Emit")
	if !ok {
		return nil, nil
	}
	var (
		out    []emitReq
		cur    *emitReq
		inputs []string
		block  bool
		indent int
	)
	closeInput := func() {
		if cur != nil && block {
			cur.Input = strings.TrimRight(dedent(inputs), "\n")
			block, inputs = false, nil
		}
	}
	flush := func() error {
		closeInput()
		if cur == nil {
			return nil
		}
		if cur.Role == "" {
			return errors.New("an emit entry has no role")
		}
		if len(cur.Input) > emitInputMax {
			return fmt.Errorf("emit input for %q exceeds %d bytes", cur.Role, emitInputMax)
		}
		out = append(out, *cur)
		cur = nil
		return nil
	}
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		lead := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if block {
			// Inside an `input: |` block: everything more-indented than the
			// key belongs to the child, verbatim (blank lines included).
			if trimmed == "" || lead > indent {
				inputs = append(inputs, raw)
				continue
			}
			closeInput()
		}
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &emitReq{}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if trimmed == "" {
				continue
			}
		}
		if cur == nil {
			return nil, errors.New("emit section has a field before its first `- role:` entry")
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("unparseable emit line %q", trimmed)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "role":
			cur.Role = unquote(value)
		case "input":
			if value == "|" || value == ">" || value == "" {
				block, indent, inputs = true, lead, nil
				continue
			}
			cur.Input = unquote(value)
		default:
			return nil, fmt.Errorf("unknown emit field %q", key)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// dedent strips the common leading whitespace of a block scalar's lines so the
// child's brief reads as it was written, not as it was indented.
func dedent(lines []string) string {
	prefix := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if prefix < 0 || n < prefix {
			prefix = n
		}
	}
	if prefix <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if len(l) >= prefix {
			l = l[prefix:]
		} else {
			l = strings.TrimLeft(l, " \t")
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// emitterFor returns the run's declared emitter spec for a node role/member.
func emitterFor(specs []emitterSpec, role, nodeID string) (emitterSpec, bool) {
	for _, s := range specs {
		if s.Node == role || s.Node == nodeID {
			return s, true
		}
	}
	return emitterSpec{}, false
}

// validateEmitters checks an automation's emitter declarations against its own
// graph. Depth-1 and the worst-case session budget are STATIC guarantees: an
// automation that could not possibly fit is refused at save, not at 3am.
func validateEmitters(specs []emitterSpec, stages [][]string) error {
	if len(specs) == 0 {
		return nil
	}
	if len(specs) > flowNodeMax {
		return fmt.Errorf("an automation declares at most %d emitters", flowNodeMax)
	}
	members := flattenStages(stages)
	onPath := map[string]bool{}
	for _, m := range members {
		onPath[m] = true
		onPath[baseRole(m)] = true
	}
	emitterRoles := map[string]bool{}
	seen := map[string]bool{}
	for _, s := range specs {
		if seen[s.Node] {
			return fmt.Errorf("node %q declares more than one emitter", s.Node)
		}
		seen[s.Node] = true
		emitterRoles[baseRole(s.Node)] = true
	}
	for _, s := range specs {
		if !onPath[s.Node] {
			return fmt.Errorf("emitter %q is not on the happy path", s.Node)
		}
		if s.Max < 1 || s.Max > emitMaxWidth {
			return fmt.Errorf("emitter %q max must be 1-%d", s.Node, emitMaxWidth)
		}
		if len(s.Roles) == 0 || len(s.Roles) > emitRolesMax {
			return fmt.Errorf("emitter %q wants 1-%d child roles", s.Node, emitRolesMax)
		}
		for _, r := range s.Roles {
			if _, ok := nodeDefinitionByID(r); !ok {
				return fmt.Errorf("emitter %q emits unknown node %q", s.Node, r)
			}
			if emitterRoles[r] {
				// Depth-1: a child that is itself an emitter could emit again,
				// and the run's budget would be the only thing between us and a
				// session factory. Refuse the shape instead.
				return fmt.Errorf("emitter %q may not emit %q, which is itself an emitter", s.Node, r)
			}
		}
		if _, ok := nodeDefinitionByID(s.FanIn); !ok {
			return fmt.Errorf("emitter %q has unknown fan-in node %q", s.Node, s.FanIn)
		}
		if baseRole(s.FanIn) == baseRole(s.Node) {
			return fmt.Errorf("emitter %q cannot be its own fan-in", s.Node)
		}
	}
	// The budget is per AUTOMATION, not per emitter: two emitters that each fit
	// alone can exhaust the run's lifetime session budget together, and one
	// role-level spec governs EVERY member of that role (emitterFor matches by
	// role), so each occurrence can emit its own group.
	if worst := worstCaseSessions(specs, stages); worst > flowNodeMax {
		return fmt.Errorf("worst case %d sessions (%d static + every emitter's widest group and fan-in) exceeds the %d-session budget",
			worst, len(members), flowNodeMax)
	}
	return nil
}

// emitterOccurrences counts the stage members one spec governs. A spec naming a
// bare role governs every member of it (including slotted ones — `attempt` and
// `attempt#2` share the role, which is exactly what makes them comparable); a
// spec naming a member slot governs only that member.
func emitterOccurrences(s emitterSpec, members []string) int {
	n := 0
	for _, m := range members {
		if m == s.Node || baseRole(m) == s.Node {
			n++
		}
	}
	return n
}

// worstCaseSessions is the run's maximum node count: every static member plus,
// for each emitter occurrence, its widest declared group and that group's
// fan-in. Mirrored in the SPA (features/graph/model.ts) so a graph the server
// would refuse cannot be saved from the canvas either.
func worstCaseSessions(specs []emitterSpec, stages [][]string) int {
	members := flattenStages(stages)
	total := len(members)
	for _, s := range specs {
		width := s.Max
		if width < 0 {
			width = 0
		}
		total += emitterOccurrences(s, members) * (width + 1)
	}
	return total
}

// applyEmissionLocked mints an emitter's requested children plus its fan-in.
// Refusal is total: any invalid entry loses the whole emission (a partially
// applied fan-out is worse than none — the fan-in would judge a truncated
// field). Caller holds nsMu and has NOT yet stamped VerdictSeen.
func applyEmissionLocked(f *flow, n flowNodeRun, verdict string, reqs []emitReq, now time.Time) error {
	spec, ok := emitterFor(f.Emitters, n.Role, n.ID)
	if !ok {
		return fmt.Errorf("node %s is not a declared emitter of this automation", n.ID)
	}
	if verdict != verdictOK {
		return fmt.Errorf("node %s emitted with verdict %q; only ok may emit", n.ID, verdict)
	}
	if len(reqs) == 0 {
		return nil
	}
	if len(reqs) > spec.Max {
		return fmt.Errorf("node %s requested %d children, its cap is %d", n.ID, len(reqs), spec.Max)
	}
	allowed := map[string]bool{}
	for _, r := range spec.Roles {
		allowed[r] = true
	}
	for _, req := range reqs {
		if !allowed[req.Role] {
			return fmt.Errorf("node %s may not emit role %q", n.ID, req.Role)
		}
		if len(req.Input) > emitInputMax {
			return fmt.Errorf("emit input for %q exceeds %d bytes", req.Role, emitInputMax)
		}
	}
	if len(f.Nodes)+len(reqs)+1 > flowNodeMax {
		return fmt.Errorf("emission needs %d sessions, only %d of the %d-session budget remain",
			len(reqs)+1, flowNodeMax-len(f.Nodes), flowNodeMax)
	}

	// Successors of the emitter are captured BEFORE the append: they re-gate
	// onto the fan-in, exactly like a routed insertion (ADR-0017).
	var successors []int
	for i, m := range f.Nodes {
		for _, up := range m.upstreams() {
			if up == n.ID {
				successors = append(successors, i)
			}
		}
	}

	// Emission is all-or-nothing ON DISK too, not just in validation. Children are
	// persisted one job at a time, so a failure partway through would otherwise
	// leave a truncated group for the fan-in to judge, an already-re-gated
	// successor, and orphaned job files that still launch (their upstream has
	// delivered) with no node to reconcile them. Every failure path below undoes
	// every job write and every in-memory mutation.
	baseNodes, baseRound := len(f.Nodes), f.Round
	type gateSnapshot struct {
		idx      int
		afterID  string
		afterIDs []string
	}
	var (
		created  []string
		restored []gateSnapshot
	)
	rollback := func() {
		for _, id := range created {
			if c, ok := flowNodeByID(*f, id); ok {
				deleteJob(f.Agent, c.JobID)
			}
		}
		f.Nodes = f.Nodes[:baseNodes]
		f.Round = baseRound
		for _, s := range restored {
			m := &f.Nodes[s.idx]
			m.AfterID, m.AfterIDs = s.afterID, s.afterIDs
			if j, ok := findFlowJob(*f, m.JobID); ok && !j.Started {
				j.After = m.upstreams()
				_ = saveJob(j)
			}
		}
	}

	f.Round++
	counts := map[string]int{}
	childIDs := make([]string, 0, len(reqs))
	for _, req := range reqs {
		counts[req.Role]++
		id := fmt.Sprintf("%s-r%d", req.Role, f.Round)
		if counts[req.Role] > 1 {
			id = fmt.Sprintf("%s-%d-r%d", req.Role, counts[req.Role], f.Round)
		}
		child := flowNodeRun{
			ID: id, Role: req.Role, Round: f.Round, Stage: -1, AfterIDs: []string{n.ID},
			EmittedBy: n.ID, EmitInput: req.Input,
		}
		child.Branch = f.Branch + "--" + id
		child.Worktree = nodeWorktree(*f, id)
		if err := appendFlowNodeRun(f, child, now); err != nil {
			rollback()
			return err
		}
		created = append(created, id)
		childIDs = append(childIDs, id)
	}
	fanInID := fmt.Sprintf("%s-r%d", spec.FanIn, f.Round)
	fanIn := flowNodeRun{ID: fanInID, Role: spec.FanIn, Round: f.Round, Stage: -1, AfterIDs: childIDs}
	if err := appendFlowNodeRun(f, fanIn, now); err != nil {
		rollback()
		return err
	}
	created = append(created, fanInID)
	for _, i := range successors {
		m := &f.Nodes[i]
		snap := gateSnapshot{idx: i, afterID: m.AfterID, afterIDs: append([]string(nil), m.AfterIDs...)}
		ups := append([]string(nil), m.upstreams()...)
		for k, up := range ups {
			if up == n.ID {
				ups[k] = fanInID
			}
		}
		m.AfterIDs, m.AfterID = ups, ""
		restored = append(restored, snap)
		if j, ok := findFlowJob(*f, m.JobID); ok && !j.Started {
			j.After = ups
			if err := saveJob(j); err != nil {
				// The successor still gates on the emitter ON DISK, whose report
				// exists — leaving it would launch it beside the children it is
				// supposed to consume.
				rollback()
				return fmt.Errorf("re-gating %s onto %s failed: %w", m.ID, fanInID, err)
			}
		}
	}
	// Persist the whole group (and the caller's freshly stamped VerdictSeen) in one
	// rename-atomic write: without it the crash window between the last job write
	// and reconcile's end-of-loop save re-emits a SECOND group on restart.
	if err := saveFlow(*f); err != nil {
		rollback()
		return err
	}
	return nil
}

// emittedGroupUpstreams returns the subset of a gated job's upstreams that are
// emitted children — the group the ADR-0023 quorum rule applies to. A static
// stage keeps the strict all-must-deliver gate.
func emittedGroupUpstreams(j job) map[string]bool {
	if j.FlowID == "" || len(j.After) == 0 {
		return nil
	}
	f, err := loadFlow(j.FlowID)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, up := range j.After {
		for _, n := range f.Nodes {
			if n.ID == up && n.EmittedBy != "" {
				out[up] = true
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
