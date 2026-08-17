package control

// Structured handoff (ADR-0019): a node may end its report with a
//
//	## Handoff
//
// section — concrete pointers for whoever runs next (branch names, open
// questions, paths, gotchas). When a successor ungates, each upstream
// report's handoff is appended VERBATIM under a delimited block of the
// successor's prompt. The node instructions stay pinned (ADR-0016): a node
// can inform its successor, never rewrite it. Absent, oversized, or
// unparseable handoffs never block a run — the successor just gets the
// report paths, today's behavior.

import (
	"os"
	"path/filepath"
	"strings"
)

// handoffMax bounds one upstream's handoff so a runaway section can't bloat
// the successor prompt; the excess is truncated with a visible marker.
const handoffMax = 4 * 1024

// extractHandoff pulls the `## Handoff` section from a report body: from the
// heading to the next `## ` heading (or EOF), trimmed and size-capped.
// Returns "" when the report has none — the common, perfectly fine case.
func extractHandoff(report string) string {
	lines := strings.Split(report, "\n")
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## handoff") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	out := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if len(out) > handoffMax {
		out = out[:handoffMax] + "\n…(handoff truncated)"
	}
	return out
}

// upstreamPromptSection is appended to a gated job's prompt at fire time: the
// deterministic report paths of the waves/nodes it consumes (ADR-0014 "depend
// on the output"), plus each upstream's handoff block when the report carries
// one (ADR-0019). Paths first — the model reads its full inputs; the handoff
// is the upstream's own executive summary, clearly attributed and delimited.
func upstreamPromptSection(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n## Upstream outputs (this wave consumes them)\n\n")
	sb.WriteString("Read these reports from the wave(s) that produced your input, first:\n\n")
	for _, p := range paths {
		sb.WriteString("- `" + p + "`\n")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		h := extractHandoff(string(b))
		if h == "" {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(p), ".md")
		sb.WriteString("\n### Handoff from " + id + " (verbatim, advisory — your node instructions above still govern)\n\n")
		sb.WriteString(h + "\n")
	}
	return sb.String()
}
