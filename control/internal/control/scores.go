package control

// Judge/eval loop (ADR-0023): scores are how the factory measures itself.
//
// A node report may carry a `scores:` block in its frontmatter — one entry per
// (subject, dimension) the node judged. reconcileFlowsLocked persists them in
// the same exactly-once step that routes the verdict, denormalising the pinned
// prompt/automation revisions and both executor identities at write time, so a
// read never has to reconstruct which prompt produced which number.
//
// Parsing is TOLERANT, like reportMeta (banner.go): a malformed entry is
// skipped and the rest still count — a judge that fumbles one line must not
// cost the night its whole evidence. Skipped entries surface as one
// `score-parse` alert, never as a blocked run.

import (
	"database/sql"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	scoreSubjectMax   = 120
	scoreRationaleMax = 200
	scoreMaxDefault   = 5
	scoreMaxCeil      = 10
	// scoresPerReportMax bounds one report's evidence: a runaway generator
	// can't turn a single node into thousands of rows.
	scoresPerReportMax = 64
)

var scoreDimensionRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// scoreEntry is one judged (subject, dimension) pair as written by a node.
// Score is nil for the literal `unknown` — the judge is REQUIRED to be able to
// say it doesn't know, and an honest unknown must never be stored as a zero.
type scoreEntry struct {
	Subject   string
	Dimension string
	Score     *int
	Max       int
	Rationale string
}

// scoreView is the read shape carried on a ledger node.
type scoreView struct {
	Dimension string `json:"dimension"`
	Score     *int   `json:"score"`
	Max       int    `json:"max"`
	Rationale string `json:"rationale,omitempty"`
}

// frontmatter returns the report's leading `---` block, or "" when the report
// has none. Kept separate from the parser so both scores and any later
// frontmatter reader slice the same way.
func frontmatter(report []byte) string {
	s := strings.TrimLeft(string(report), "\ufeff \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n")
		}
	}
	return ""
}

// parseScores reads the frontmatter `scores:` list. It returns the valid
// entries and the number of entries it had to skip (the `score-parse` signal).
// It accepts the full report or just its frontmatter.
func parseScores(report []byte) ([]scoreEntry, int) {
	front := frontmatter(report)
	if front == "" {
		front = string(report)
	}
	lines := strings.Split(front, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "scores:" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, 0
	}
	var (
		out     []scoreEntry
		skipped int
		cur     *scoreEntry
		curBad  bool
	)
	flush := func() {
		if cur == nil {
			return
		}
		switch {
		case curBad, cur.Subject == "", !scoreDimensionRe.MatchString(cur.Dimension):
			skipped++
		case len(out) >= scoresPerReportMax:
			skipped++
		default:
			out = append(out, *cur)
		}
		cur = nil
		curBad = false
	}
	for i := start; i < len(lines); i++ {
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		indented := raw != strings.TrimLeft(raw, " \t")
		if !indented && !strings.HasPrefix(trimmed, "- ") {
			break // dedented back to another frontmatter key
		}
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			flush()
			cur = &scoreEntry{Max: scoreMaxDefault}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if trimmed == "" {
				continue
			}
		}
		if cur == nil {
			skipped++ // a field line before any `- ` entry
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			curBad = true
			continue
		}
		applyScoreField(cur, strings.TrimSpace(key), unquote(strings.TrimSpace(value)), &curBad)
	}
	flush()
	return out, skipped
}

func applyScoreField(e *scoreEntry, key, value string, bad *bool) {
	switch key {
	case "subject":
		e.Subject = truncate(value, scoreSubjectMax)
	case "dimension":
		e.Dimension = strings.ToLower(value)
	case "score":
		if strings.EqualFold(value, "unknown") || value == "" || value == "null" {
			e.Score = nil
			return
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			*bad = true
			return
		}
		e.Score = &n
	case "max":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > scoreMaxCeil {
			*bad = true
			return
		}
		e.Max = n
	case "rationale":
		// Truncate, never drop: a too-chatty judge still carries signal.
		e.Rationale = truncate(value, scoreRationaleMax)
	default:
		*bad = true
	}
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}

// normalized clamps an entry to its own scale: an out-of-range max falls back
// to the default, and a score outside [0, max] becomes unknown rather than
// being persisted as a lie.
func (e scoreEntry) normalized() scoreEntry {
	if e.Max < 1 || e.Max > scoreMaxCeil {
		e.Max = scoreMaxDefault
	}
	if e.Score != nil && (*e.Score < 0 || *e.Score > e.Max) {
		e.Score = nil
	}
	return e
}

// ---- persistence ---------------------------------------------------------------

// scoreRow is one persisted row: the entry plus the identity of everything that
// produced it. The joins happen at WRITE time (the pinned job is right there);
// reads stay a single flat query.
type scoreRow struct {
	Night              string
	RunID              string
	JudgeNode          string
	Subject            string
	Dimension          string
	Score              *int
	Max                int
	Rationale          string
	AutomationID       string
	AutomationRevision string
	SubjectPromptRev   string
	JudgeExecutor      string
	JudgeModel         string
	SubjectExecutor    string
	SubjectModel       string
	CreatedAt          int64
}

// nightKeyOf is the evening-anchored night a timestamp belongs to — the same
// rule the SPA uses (format.ts nightKey: shift back 12h, take the date), so a
// 00:45 run files under the evening it started from.
func nightKeyOf(t time.Time) string {
	return t.In(bishkek).Add(-12 * time.Hour).Format("2006-01-02")
}

// insertScore upserts one row. (run_id, judge_node, subject, dimension) is
// unique: reconcile is already exactly-once, this is the backstop that keeps a
// re-ingest or a manual replay from double-counting an average.
func insertScore(db *sql.DB, r scoreRow) error {
	_, err := db.Exec(
		`INSERT INTO scores(night,run_id,judge_node,subject,dimension,score,max,rationale,
		   automation_id,automation_rev,subject_prompt_rev,judge_executor,judge_model,
		   subject_executor,subject_model,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(run_id,judge_node,subject,dimension) DO UPDATE SET
		   score=excluded.score, max=excluded.max, rationale=excluded.rationale,
		   subject_prompt_rev=excluded.subject_prompt_rev,
		   judge_executor=excluded.judge_executor, judge_model=excluded.judge_model,
		   subject_executor=excluded.subject_executor, subject_model=excluded.subject_model`,
		r.Night, r.RunID, r.JudgeNode, r.Subject, r.Dimension, r.Score, r.Max, r.Rationale,
		r.AutomationID, r.AutomationRevision, r.SubjectPromptRev, r.JudgeExecutor, r.JudgeModel,
		r.SubjectExecutor, r.SubjectModel, r.CreatedAt)
	return err
}

// queryNodeScores returns the scores a run's judge node recorded, for the
// ledger. Ordered by dimension so a ledger row is stable across reads.
func queryNodeScores(db *sql.DB, runID, judgeNode string) []scoreView {
	rows, err := db.Query(
		`SELECT dimension, score, max, COALESCE(rationale,'')
		   FROM scores WHERE run_id=? AND judge_node=? ORDER BY subject, dimension`, runID, judgeNode)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []scoreView
	for rows.Next() {
		var v scoreView
		var score sql.NullInt64
		if err := rows.Scan(&v.Dimension, &score, &v.Max, &v.Rationale); err != nil {
			continue
		}
		if score.Valid {
			n := int(score.Int64)
			v.Score = &n
		}
		out = append(out, v)
	}
	return out
}

// persistFlowScores writes one delivered node's scores. Called from the
// verdict pass under nsMu; every failure is logged and swallowed — evidence
// collection must never wedge a run.
func persistFlowScores(f flow, n flowNodeRun, now time.Time) {
	if obsDB == nil {
		return
	}
	b, err := os.ReadFile(reportPath(f.Agent, n.JobID))
	if err != nil {
		return
	}
	entries, skipped := parseScores(b)
	if skipped > 0 {
		raiseAlert(obsDB, f.Agent, f.ID, "", "score-parse",
			"node "+n.ID+" of run "+f.ID+": "+strconv.Itoa(skipped)+" malformed score entr(y/ies) skipped",
			now.Unix(), now.Unix())
	}
	if len(entries) == 0 {
		return
	}
	judgeJob, _ := findFlowJob(f, n.JobID)
	for _, e := range entries {
		e = e.normalized()
		subjectExec, subjectModel, subjectRev := subjectIdentity(f, e.Subject)
		row := scoreRow{
			Night: nightKeyOf(now), RunID: f.ID, JudgeNode: n.ID,
			Subject: e.Subject, Dimension: e.Dimension, Score: e.Score, Max: e.Max,
			Rationale: e.Rationale, AutomationID: f.Template, AutomationRevision: f.AutomationRevision,
			SubjectPromptRev: subjectRev, JudgeExecutor: jobExecutor(judgeJob), JudgeModel: judgeJob.Model,
			SubjectExecutor: subjectExec, SubjectModel: subjectModel, CreatedAt: now.Unix(),
		}
		if err := insertScore(obsDB, row); err != nil {
			logf("scores: insert %s/%s failed: %v", f.ID, n.ID, err)
		}
	}
}

// subjectIdentity resolves the judged subject to a node of this run when it
// names one (id or role), so the row records WHICH executor/model/prompt
// revision produced the judged work — the whole point of harness comparison.
// A free-text subject (a PR, a document) simply carries no identity.
func subjectIdentity(f flow, subject string) (executor, model, promptRev string) {
	var match *flowNodeRun
	for i, n := range f.Nodes {
		if n.ID == subject {
			match = &f.Nodes[i]
			break
		}
		if n.Role == subject && match == nil {
			match = &f.Nodes[i]
		}
	}
	if match == nil {
		return "", "", ""
	}
	j, ok := findFlowJob(f, match.JobID)
	if !ok {
		return "", "", ""
	}
	rev := ""
	if prompt, _, cut := strings.Cut(j.Prompt, "\n\n## Flow\n"); cut {
		rev = promptRevision(prompt)
	}
	return jobExecutor(j), j.Model, rev
}

func jobExecutor(j job) string {
	if j.Executor == "" {
		return "claude"
	}
	return j.Executor
}
