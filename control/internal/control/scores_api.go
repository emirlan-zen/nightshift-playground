package control

// Read surface for the judge/eval loop (ADR-0023): GET /api/scores.
//
// Two questions, one endpoint. `?run=<id>` answers "how did THIS run score" —
// the raw rows, with both identities on each, which is what a harness
// comparison is read from. `?automation=<id>` answers "is this automation
// getting better" — rows folded into (role, dimension, prompt revision)
// groups, newest revision first, so a prompt edit's effect is visible as a
// delta instead of a vibe.
//
// Metadata only, like the ledger: dimensions, numbers, revisions, and the
// judge's own short rationale — never report or transcript content.

import (
	"database/sql"
	"net/http"
	"strings"
)

type scoreGroup struct {
	Role      string `json:"role"`
	Dimension string `json:"dimension"`
	PromptRev string `json:"promptRev"`
	// Executor/Model are part of the group IDENTITY, not decoration: "same prompt
	// on claude vs codex" is the harness question, and folding both engines into
	// one average answers no question at all.
	Executor         string   `json:"executor,omitempty"`
	Model            string   `json:"model,omitempty"`
	Avg              *float64 `json:"avg"`
	Pct              *float64 `json:"pct"`
	N                int      `json:"n"`
	Max              int      `json:"max"`
	LatestRationales []string `json:"latestRationales"`
}

type scoreRowView struct {
	RunID           string `json:"runId"`
	JudgeNode       string `json:"judgeNode"`
	Subject         string `json:"subject"`
	Dimension       string `json:"dimension"`
	Score           *int   `json:"score"`
	Max             int    `json:"max"`
	Rationale       string `json:"rationale"`
	JudgeExecutor   string `json:"judgeExecutor"`
	JudgeModel      string `json:"judgeModel"`
	SubjectExecutor string `json:"subjectExecutor"`
	SubjectModel    string `json:"subjectModel"`
	CreatedAt       int64  `json:"createdAt"`
}

type scoresResponse struct {
	Groups []scoreGroup   `json:"groups"`
	Rows   []scoreRowView `json:"rows"`
}

const scoreRationaleSamples = 3

// queryScoreRows returns one run's rows, newest first.
func queryScoreRows(db *sql.DB, runID string) []scoreRowView {
	rows, err := db.Query(
		`SELECT run_id, judge_node, subject, dimension, score, max, COALESCE(rationale,''),
		        COALESCE(judge_executor,''), COALESCE(judge_model,''),
		        COALESCE(subject_executor,''), COALESCE(subject_model,''), created_at
		   FROM scores WHERE run_id=? ORDER BY created_at DESC, subject, dimension`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []scoreRowView{}
	for rows.Next() {
		var v scoreRowView
		var score sql.NullInt64
		if err := rows.Scan(&v.RunID, &v.JudgeNode, &v.Subject, &v.Dimension, &score, &v.Max,
			&v.Rationale, &v.JudgeExecutor, &v.JudgeModel, &v.SubjectExecutor, &v.SubjectModel, &v.CreatedAt); err != nil {
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

// queryScoreGroups folds an automation's (or a single run's) rows into
// (subject, dimension, subject prompt revision, subject executor, subject model)
// groups — the full identity of the thing measured, so a claude attempt and a
// codex attempt of the SAME prompt are two trends, not one blurred one.
//
// Scores are normalised to a FRACTION before averaging. A judge that switches
// scale (4/5 one night, 8/10 the next) is recording the same quality; summing
// the raw integers reported that as 6/10, i.e. a regression that never happened.
// avg is expressed on the newest row's scale and pct carries the scale-free
// number. Both are computed over non-null scores only — an `unknown` counts as
// evidence collected, never as a zero.
func queryScoreGroups(db *sql.DB, column, value string) []scoreGroup {
	rows, err := db.Query(
		`SELECT subject, dimension, COALESCE(subject_prompt_rev,''),
		        COALESCE(subject_executor,''), COALESCE(subject_model,''), score, max,
		        COALESCE(rationale,''), created_at
		   FROM scores WHERE `+column+`=? ORDER BY created_at DESC`, value)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type acc struct {
		group scoreGroup
		frac  float64
		count int
	}
	order := []string{}
	byKey := map[string]*acc{}
	for rows.Next() {
		var subject, dimension, rev, executor, model, rationale string
		var score sql.NullInt64
		var maxScore int
		var createdAt int64
		if err := rows.Scan(&subject, &dimension, &rev, &executor, &model, &score, &maxScore, &rationale, &createdAt); err != nil {
			continue
		}
		if maxScore < 1 {
			maxScore = scoreMaxDefault
		}
		key := strings.Join([]string{subject, dimension, rev, executor, model}, "\x00")
		a, ok := byKey[key]
		if !ok {
			// Rows arrive newest-first, so the first row's scale is the group's
			// display scale.
			a = &acc{group: scoreGroup{
				Role: subject, Dimension: dimension, PromptRev: rev, Executor: executor, Model: model,
				Max: maxScore, LatestRationales: []string{},
			}}
			byKey[key] = a
			order = append(order, key)
		}
		a.group.N++
		if score.Valid {
			a.frac += float64(score.Int64) / float64(maxScore)
			a.count++
		}
		if rationale != "" && len(a.group.LatestRationales) < scoreRationaleSamples {
			a.group.LatestRationales = append(a.group.LatestRationales, rationale)
		}
	}
	out := make([]scoreGroup, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		if a.count > 0 {
			frac := a.frac / float64(a.count)
			avg := frac * float64(a.group.Max)
			pct := frac * 100
			a.group.Avg, a.group.Pct = &avg, &pct
		}
		out = append(out, a.group)
	}
	return out
}

// handleScores serves exactly one of ?automation= or ?run=. Requiring exactly
// one keeps the two questions distinct: an unscoped "all scores" read has no
// consumer and would grow without bound.
func handleScores(w http.ResponseWriter, r *http.Request) {
	automation := strings.TrimSpace(r.URL.Query().Get("automation"))
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	if (automation == "") == (runID == "") {
		http.Error(w, "want exactly one of ?automation=<id> or ?run=<id>", http.StatusBadRequest)
		return
	}
	out := scoresResponse{Groups: []scoreGroup{}, Rows: []scoreRowView{}}
	if obsDB == nil {
		writeJSON(w, out)
		return
	}
	if runID != "" {
		if !flowIDRe.MatchString(runID) && !runIDRe.MatchString(runID) {
			http.Error(w, "invalid run id", http.StatusBadRequest)
			return
		}
		out.Groups = queryScoreGroups(obsDB, "run_id", runID)
		out.Rows = queryScoreRows(obsDB, runID)
	} else {
		if !flowTemplateIDRe.MatchString(automation) {
			http.Error(w, "invalid automation id", http.StatusBadRequest)
			return
		}
		out.Groups = queryScoreGroups(obsDB, "automation_id", automation)
	}
	if out.Groups == nil {
		out.Groups = []scoreGroup{}
	}
	if out.Rows == nil {
		out.Rows = []scoreRowView{}
	}
	writeJSON(w, out)
}
