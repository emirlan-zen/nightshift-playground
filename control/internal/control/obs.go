// Read surface for the observability store (ADR-0006, PR3). One endpoint feeds
// the control page's Health tab: open alerts, per-agent health, and the night
// ledger (which runs ran, delivered, or failed). All read-only over the shared
// obsDB the ingester writes; if ingest is disabled (db nil) it returns empties.
package control

import (
	"database/sql"
	"net/http"
	"time"
)

const obsWindowDays = 14

type obsAlert struct {
	Agent     string `json:"agent"`
	RunID     string `json:"runId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
	Ts        int64  `json:"ts"`
}

type obsAgentStat struct {
	Agent      string `json:"agent"`
	Sessions   int64  `json:"sessions"`
	Turns      int64  `json:"turns"`
	ToolCalls  int64  `json:"toolCalls"`
	ToolErrors int64  `json:"toolErrors"`
	LastTs     int64  `json:"lastTs"`
}

// obsRun is one row of the night ledger. Status is derived server-side so the
// UI (and the CLI) agree: delivered | auth-skip | no-deliverable | running.
type obsRun struct {
	Agent   string `json:"agent"`
	RunID   string `json:"runId"`
	Kind    string `json:"kind,omitempty"`
	Label   string `json:"label,omitempty"`
	Started int64  `json:"started"`
	Report  bool   `json:"report"`
	Status  string `json:"status"`
}

type obsResp struct {
	Generated int64          `json:"generated"`
	Alerts    []obsAlert     `json:"alerts"`
	Agents    []obsAgentStat `json:"agents"`
	Runs      []obsRun       `json:"runs"`
	Budget    obsBudget      `json:"budget"`
}

func handleObs(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	resp := obsResp{Generated: now.Unix(), Alerts: []obsAlert{}, Agents: []obsAgentStat{}, Runs: []obsRun{}}
	// Budget is always present: with no DB it still reports the pinned/default
	// budget and zero spend (budgetView tolerates a nil db).
	resp.Budget = budgetView(obsDB, now)
	if obsDB == nil {
		writeJSON(w, resp)
		return
	}
	cutoff := nowUnix() - int64(obsWindowDays)*86400
	resp.Alerts = queryAlerts(obsDB)
	resp.Agents = queryAgentStats(obsDB, cutoff)
	resp.Runs = queryLedger(obsDB, cutoff)
	writeJSON(w, resp)
}

func queryAlerts(db *sql.DB) []obsAlert {
	out := []obsAlert{}
	rows, err := db.Query(
		`SELECT agent, COALESCE(run_id,''), COALESCE(session_id,''), kind, COALESCE(detail,''), ts
		   FROM alerts WHERE cleared=0 ORDER BY ts DESC LIMIT 200`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a obsAlert
		if rows.Scan(&a.Agent, &a.RunID, &a.SessionID, &a.Kind, &a.Detail, &a.Ts) == nil {
			out = append(out, a)
		}
	}
	return out
}

func queryAgentStats(db *sql.DB, cutoff int64) []obsAgentStat {
	out := []obsAgentStat{}
	// Exclude the control plane's own 10-min haiku auth probe (same rule as
	// detectStalls / nightSpendUSD): it writes ~144 single-haiku-turn sessions/day
	// into playground's project dir, which otherwise dominated the Health tab's
	// per-agent session + turn counts. Haiku on this box is only ever the probe.
	rows, err := db.Query(
		`SELECT agent, COUNT(*), COALESCE(SUM(turns),0), COALESCE(SUM(tool_calls),0),
		        COALESCE(SUM(tool_errors),0), COALESCE(MAX(last_ts),0)
		   FROM sessions WHERE last_ts > ? AND COALESCE(model,'') NOT LIKE '%haiku%'
		  GROUP BY agent ORDER BY SUM(turns) DESC`, cutoff)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s obsAgentStat
		if rows.Scan(&s.Agent, &s.Sessions, &s.Turns, &s.ToolCalls, &s.ToolErrors, &s.LastTs) == nil {
			out = append(out, s)
		}
	}
	return out
}

func queryLedger(db *sql.DB, cutoff int64) []obsRun {
	out := []obsRun{}
	rows, err := db.Query(
		`SELECT agent, run_id, COALESCE(kind,''), COALESCE(label,''), COALESCE(started,0), report
		   FROM runs WHERE started > ? ORDER BY started DESC LIMIT 80`, cutoff)
	if err != nil {
		return out
	}
	defer rows.Close()
	now := nowUnix()
	for rows.Next() {
		var r obsRun
		var rep int
		if rows.Scan(&r.Agent, &r.RunID, &r.Kind, &r.Label, &r.Started, &rep) != nil {
			continue
		}
		r.Report = rep == 1
		r.Status = runStatusLabel(r.Agent, r.RunID, r.Started, r.Report, now)
		out = append(out, r)
	}
	return out
}

// runStatusLabel derives a run's ledger status without a DB round-trip per row.
func runStatusLabel(agent, id string, started int64, report bool, now int64) string {
	switch {
	case report:
		return "delivered"
	case authFailMarker(agent, id):
		return "auth-skip"
	case started > 0 && now-started > int64(runMaxLife/time.Second):
		return "no-deliverable"
	default:
		return "running"
	}
}
