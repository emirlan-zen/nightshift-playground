// Detectors (ADR-0006): rules over the ingested rows that raise `alerts`. Built
// to need no run<->session correlation (the transcripts don't carry the run id):
//   - loop / error-storm      — pure SQL over a session's tool_calls
//   - no-deliverable          — a run past its max auto-stop with no report
//   - stall                   — an agent with a live run but no recent turn
//
// The last two lean on the runs table + `run-status`; the first two are exact.
//
// Alerts upsert on (kind, run_id, session_id) so a standing problem refreshes
// its timestamp rather than duplicating. clearStaleAlerts (run at the end of a
// detector pass) retires alerts that no longer reflect reality — a no-deliverable
// whose report has since landed, and anything older than alertMaxAge — so a
// resolved incident stops showing as open on the Health tab and in `nightshift-obs
// stalls`. A recurrence re-opens via raiseAlert's upsert (cleared reset to 0).
package control

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	loopRepeatThreshold = 5                // same tool+input this many times = loop
	stormMinCalls       = 8                // need enough calls before an error rate means anything
	stallTurnGap        = 20 * time.Minute // live run, no assistant turn this long = stalled
	detectLookback      = 72 * time.Hour   // ignore sessions/runs older than this
	runMaxLife          = 8 * time.Hour    // >= the max per-run auto-stop; past it a run has ended
	alertMaxAge         = 36 * time.Hour   // an alert this old is aged out (cleared) regardless of kind
)

func runDetectors(db *sql.DB) {
	now := nowUnix()
	cutoff := now - int64(detectLookback/time.Second)
	detectLoops(db, cutoff, now)
	detectErrorStorms(db, cutoff, now)
	detectNoDeliverable(db, now)
	detectStalls(db, now)
	detectLimitHit(db, now)
	detectDepSkips(db, now)
	clearStaleAlerts(db, now)
}

// detectDepSkips raises a `dep-skip` alert for each fresh <id>.depskip marker
// (ADR-0014): a triggered wave the fire loop skipped because its output-upstream
// died. Pure artifact scan (no obs rows — a skipped wave never started, so it
// leaves no run to trip no-deliverable). The marker text is the reason.
func detectDepSkips(db *sql.DB, now int64) {
	cutoff := now - int64(detectLookback/time.Second)
	for _, agent := range companies {
		entries, _ := os.ReadDir(reportsDir(agent))
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".depskip")
			if !ok || !runIDRe.MatchString(id) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Unix() < cutoff {
				continue
			}
			reason, _ := os.ReadFile(filepath.Join(reportsDir(agent), e.Name()))
			detail := strings.TrimSpace(string(reason))
			if detail == "" {
				detail = "a triggered wave was skipped (upstream failed)"
			}
			raiseAlert(db, agent, id, "", "dep-skip", detail, info.ModTime().Unix(), now)
		}
	}
}

// clearStaleAlerts retires alerts that no longer reflect the world, so a resolved
// incident stops reading as open. Two rules, both idempotent:
//
//  1. condition-resolved: a no-deliverable alert whose morning report has since
//     landed on disk (same path ingest.go uses to set runs.report).
//  2. age-out: any alert older than alertMaxAge, marked " (aged out)" once.
//
// Re-raise is unaffected — raiseAlert's ON CONFLICT resets cleared=0 and
// overwrites detail with the fresh (unsuffixed) text, so a recurrence comes back
// open and clean.
func clearStaleAlerts(db *sql.DB, now int64) {
	// 1. no-deliverable whose report has appeared. Collect first (rows closed)
	//    before issuing UPDATEs to avoid a self-locking read/write overlap.
	rows, err := db.Query(
		`SELECT agent, COALESCE(run_id,'') FROM alerts
		  WHERE cleared=0 AND kind='no-deliverable' AND run_id != ''`)
	if err != nil {
		logf("obs: clear no-deliverable query: %v", err)
	} else {
		type ar struct{ agent, id string }
		var resolved []ar
		for rows.Next() {
			var a ar
			if rows.Scan(&a.agent, &a.id) != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(reportsDir(a.agent), a.id+".md")); err == nil {
				resolved = append(resolved, a)
			}
		}
		rows.Close()
		for _, a := range resolved {
			if _, err := db.Exec(
				`UPDATE alerts SET cleared=1
				  WHERE cleared=0 AND kind='no-deliverable' AND agent=? AND run_id=?`,
				a.agent, a.id); err != nil {
				logf("obs: clear no-deliverable %s/%s: %v", a.agent, a.id, err)
			}
		}
	}

	// 2. age-out. The LIKE guard + the cleared=0 filter both ensure " (aged out)"
	//    is appended at most once (aging flips cleared to 1; a later recurrence
	//    rewrites detail clean via raiseAlert).
	ageCutoff := now - int64(alertMaxAge/time.Second)
	if _, err := db.Exec(
		`UPDATE alerts SET cleared=1, detail=COALESCE(detail,'')||' (aged out)'
		  WHERE cleared=0 AND COALESCE(NULLIF(last_ts,0), ts) < ? AND detail NOT LIKE '%(aged out)'`,
		ageCutoff); err != nil {
		logf("obs: age-out alerts: %v", err)
	}
}

func raiseAlert(db *sql.DB, agent, runID, sessionID, kind, detail string, ts, now int64) {
	// ts keeps the FIRST-seen time ("when did this happen"); last_ts is refreshed
	// on every re-raise ("when did the detector last still see it") — age-out
	// keys on last_ts, so a standing condition can't be born pre-aged (e.g. a
	// no-deliverable whose ts is the run's start, first noticed 40h later) or
	// get re-raised and instantly re-cleared in the same pass. The conflict key
	// includes agent: stall/limit-hit alerts carry empty run/session ids, and
	// without agent in the key two agents' concurrent incidents collapsed into
	// one row wearing the first agent's name and the second one's numbers.
	_, err := db.Exec(
		`INSERT INTO alerts(agent,run_id,session_id,kind,detail,ts,last_ts,cleared)
		 VALUES(?,?,?,?,?,?,?,0)
		 ON CONFLICT(kind,agent,run_id,session_id)
		 DO UPDATE SET detail=excluded.detail, last_ts=excluded.last_ts, cleared=0`,
		agent, runID, sessionID, kind, detail, ts, now)
	if err != nil {
		logf("obs: alert %s/%s: %v", kind, sessionID, err)
	}
}

// detectLoops: the same (tool, input) issued loopRepeatThreshold+ times in one
// session — an agent spinning on an identical action.
func detectLoops(db *sql.DB, cutoff, now int64) {
	rows, err := db.Query(
		`SELECT t.session_id, s.agent, t.tool, COUNT(*) c
		   FROM tool_calls t JOIN sessions s ON s.session_id=t.session_id
		  WHERE t.input_hash != '' AND s.last_ts > ?
		  GROUP BY t.session_id, t.tool, t.input_hash
		 HAVING c >= ?`, cutoff, loopRepeatThreshold)
	if err != nil {
		logf("obs: loop query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var sid, agent, tool string
		var c int
		if rows.Scan(&sid, &agent, &tool, &c) != nil {
			continue
		}
		raiseAlert(db, agent, "", sid, "loop",
			fmt.Sprintf("%s called %d× with identical input", tool, c), now, now)
	}
}

// detectErrorStorms: a session where most tool calls are erroring — the agent is
// off the rails (bad creds, wrong dir, broken command it keeps retrying).
func detectErrorStorms(db *sql.DB, cutoff, now int64) {
	rows, err := db.Query(
		`SELECT session_id, agent, tool_calls, tool_errors FROM sessions
		  WHERE tool_calls >= ? AND tool_errors*2 >= tool_calls AND last_ts > ?`,
		stormMinCalls, cutoff)
	if err != nil {
		logf("obs: storm query: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var sid, agent string
		var calls, errs int
		if rows.Scan(&sid, &agent, &calls, &errs) != nil {
			continue
		}
		raiseAlert(db, agent, "", sid, "error-storm",
			fmt.Sprintf("%d/%d tool calls errored", errs, calls), now, now)
	}
}

// detectNoDeliverable: a run older than the max auto-stop (so definitely ended)
// that never wrote a morning report. Correlation-free — the auth-fail night's
// signature. Skipped if an authfail marker exists (already explained by /health).
func detectNoDeliverable(db *sql.DB, now int64) {
	rows, err := db.Query(
		`SELECT agent, run_id, started FROM runs
		  WHERE report=0 AND started < ? AND started > ?`,
		now-int64(runMaxLife/time.Second), now-int64(detectLookback/time.Second))
	if err != nil {
		logf("obs: no-deliverable query: %v", err)
		return
	}
	defer rows.Close()
	type nd struct {
		agent, id string
		started   int64
	}
	var found []nd
	for rows.Next() {
		var r nd
		if rows.Scan(&r.agent, &r.id, &r.started) != nil {
			continue
		}
		found = append(found, r)
	}
	for _, r := range found {
		if authFailMarker(r.agent, r.id) || depSkipMarker(r.agent, r.id) {
			continue // already surfaced as an auth skip / dep-skip, not a mystery
		}
		// ts = the run's start, so the alert's age reflects the failed run, not
		// when the detector happened to notice (last_ts=now keeps it from being
		// born pre-aged).
		raiseAlert(db, r.agent, r.id, "", "no-deliverable",
			"run ended past its auto-stop with no morning report", r.started, now)
	}
}

// detectStalls: an agent with a currently-active run whose most recent assistant
// turn is older than stallTurnGap — a frozen session (systemd still says active).
func detectStalls(db *sql.DB, now int64) {
	for _, agent := range companies {
		if !agentHasLiveRun(agent) {
			continue
		}
		// Haiku sessions are excluded: the control plane's own 10-minute auth
		// probe writes haiku transcripts into playground's project dir, and with
		// probe interval (10m) < stallTurnGap (20m) those kept the MAX fresh
		// forever — stall detection was structurally blind for playground while
		// the control plane was up (i.e. always). Night waves are Opus.
		var newest sql.NullInt64
		if err := db.QueryRow(
			`SELECT MAX(last_ts) FROM sessions
			  WHERE agent=? AND COALESCE(model,'') NOT LIKE '%haiku%'`, agent).Scan(&newest); err != nil {
			continue
		}
		if !newest.Valid {
			continue
		}
		gap := now - newest.Int64
		if gap > int64(stallTurnGap/time.Second) {
			raiseAlert(db, agent, "", "",
				"stall", fmt.Sprintf("live run but no assistant turn for %dm", gap/60), now, now)
		}
	}
}

// agentHasLiveRun reports whether any of the agent's recently-started jobs is
// still an active run unit. Bounded to jobs from the last runMaxLife.
var agentHasLiveRun = func(agent string) bool {
	now := time.Now()
	for _, j := range loadJobs(agent) {
		if !j.Started || now.Sub(j.At) > runMaxLife {
			continue
		}
		if out, _ := rcRun("run-status", agent, j.ID); out == "active" {
			return true
		}
	}
	return false
}

// detectLimitHit: a burst of API-error turns (limitHitBurst within a 10-minute
// span) for one agent in the trailing 2h — the plan's rate/usage limit being
// enforced. The alert detail carries the night spend at that moment so the
// morning ratchet, the Health tab, and a human all see how much was in flight
// when the wall was hit. For playground it also persists spend-at-hit into the
// ratchet (the down-ratchet basis) so nothing has to be parsed back out.
func detectLimitHit(db *sql.DB, now int64) {
	cutoff := now - int64(limitHitLookback/time.Second)
	// Never count a burst across the 22:30 night boundary: an evening-batch
	// burst still inside the 2h lookback at 22:31 would otherwise be recorded
	// against the NEW night with its near-$0 spend — a phantom spend-at-hit the
	// morning ratchet would slash the budget with.
	if ns := nightStart(time.Unix(now, 0)).Unix(); ns > cutoff {
		cutoff = ns
	}
	rows, err := db.Query(
		`SELECT s.agent, t.ts FROM turns t JOIN sessions s ON s.session_id = t.session_id
		  WHERE t.api_error = 1 AND t.ts >= ? ORDER BY s.agent, t.ts`, cutoff)
	if err != nil {
		logf("obs: limit-hit query: %v", err)
		return
	}
	defer rows.Close()
	byAgent := map[string][]int64{}
	for rows.Next() {
		var agent string
		var ts int64
		if rows.Scan(&agent, &ts) != nil {
			continue
		}
		byAgent[agent] = append(byAgent[agent], ts)
	}
	if err := rows.Err(); err != nil {
		logf("obs: limit-hit scan: %v", err)
		return
	}

	nowT := time.Unix(now, 0)
	nsMu.Lock()
	budget := effectiveBudget(loadConfig())
	nsMu.Unlock()

	for agent, tss := range byAgent {
		burst := maxWindowCount(tss, limitHitSpanSec)
		if burst < limitHitBurst {
			continue
		}
		spend, err := nightSpendUSD(db, agent, nightStart(nowT))
		if err != nil {
			logf("obs: limit-hit spend for %s: %v", agent, err)
			spend = 0
		}
		raiseAlert(db, agent, "", "", "limit-hit",
			fmt.Sprintf("api-error burst: %d errors in 10m; night spend $%.0f of $%.0f", burst, spend, budget), now, now)
		recordLimitHit(agent, spend, nowT)
	}
}

// maxWindowCount is the largest number of timestamps (ascending) falling inside
// any window of `span` seconds — a two-pointer sweep, O(n).
func maxWindowCount(ts []int64, span int64) int {
	best, l := 0, 0
	for r := range ts {
		for ts[r]-ts[l] > span {
			l++
		}
		if n := r - l + 1; n > best {
			best = n
		}
	}
	return best
}

// authFailMarker reports whether the launcher wrote an <id>.authfail for this run.
func authFailMarker(agent, id string) bool {
	_, err := os.Stat(filepath.Join(reportsDir(agent), id+".authfail"))
	return err == nil
}
