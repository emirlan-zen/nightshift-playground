package control

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleObsShapeAndStatus(t *testing.T) {
	db := obsEnv(t, "playground")
	obsDB = db
	t.Cleanup(func() { obsDB = nil })

	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	db.Exec(`INSERT INTO sessions(session_id,agent,turns,tool_calls,tool_errors,last_ts) VALUES('s1','playground',100,40,5,?)`, now-3600)
	db.Exec(`INSERT INTO alerts(agent,session_id,kind,detail,ts,cleared) VALUES('playground','s1','loop','Bash x9',?,0)`, now-60)
	db.Exec(`INSERT INTO alerts(agent,run_id,kind,detail,ts,cleared) VALUES('playground','r-cleared','stall','old',?,1)`, now-60) // cleared -> excluded
	// delivered run + a no-deliverable run (old, no report, no authfail)
	db.Exec(`INSERT INTO runs(agent,run_id,label,started,report) VALUES('playground','r-ok','synth',?,1)`, now-7200)
	db.Exec(`INSERT INTO runs(agent,run_id,label,started,report) VALUES('playground','r-dead','exec',?,0)`, now-int64(10*time.Hour/time.Second))

	rr := httptest.NewRecorder()
	handleObs(rr, httptest.NewRequest("GET", "/api/obs", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var resp obsResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Alerts) != 1 || resp.Alerts[0].Kind != "loop" {
		t.Fatalf("want 1 open loop alert, got %+v", resp.Alerts)
	}
	if len(resp.Agents) != 1 || resp.Agents[0].Turns != 100 {
		t.Fatalf("agent stats wrong: %+v", resp.Agents)
	}
	byID := map[string]string{}
	for _, r := range resp.Runs {
		byID[r.RunID] = r.Status
	}
	if byID["r-ok"] != "delivered" {
		t.Fatalf("r-ok status=%q want delivered", byID["r-ok"])
	}
	if byID["r-dead"] != "no-deliverable" {
		t.Fatalf("r-dead status=%q want no-deliverable", byID["r-dead"])
	}
}

func TestHandleObsEmptyWhenNoDB(t *testing.T) {
	obsEnv(t, "playground")
	obsDB = nil
	rr := httptest.NewRecorder()
	handleObs(rr, httptest.NewRequest("GET", "/api/obs", nil))
	var resp obsResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Alerts == nil || resp.Runs == nil || resp.Agents == nil {
		t.Fatal("nil slices should serialize as empty arrays")
	}
	// budget is always present; with no DB it still reports the (default) budget.
	if resp.Budget.NightBudgetUSD != defaultNightBudgetUSD {
		t.Fatalf("budget default = %.0f, want %.0f", resp.Budget.NightBudgetUSD, defaultNightBudgetUSD)
	}
}

// /api/obs carries a budget block: pinned budget, tonight's spend, week spend,
// and top-ups minted tonight.
func TestHandleObsBudgetBlock(t *testing.T) {
	db := obsEnv(t, "playground")
	obsDB = db
	t.Cleanup(func() { obsDB = nil })

	nsMu.Lock()
	_ = saveConfig(nightConfig{SweepOff: map[string]bool{}, LastSweep: map[string]string{}, NightBudgetUSD: 2000})
	nsMu.Unlock()

	// a turn earlier tonight: out=1M @ $75/M -> $75 spend both night + week.
	ns := nightStart(time.Now())
	if _, err := db.Exec(`INSERT OR IGNORE INTO sessions(session_id,agent) VALUES('s1','playground')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO turns(session_id,ts,model,out_tokens) VALUES('s1',?,'claude-opus-4-8',1000000)`, ns.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleObs(rr, httptest.NewRequest("GET", "/api/obs", nil))
	var resp obsResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Budget.NightBudgetUSD != 2000 {
		t.Fatalf("budget = %.0f, want 2000", resp.Budget.NightBudgetUSD)
	}
	if resp.Budget.NightSpendUSD < 74 || resp.Budget.NightSpendUSD > 76 {
		t.Fatalf("night spend = %.2f, want ≈75", resp.Budget.NightSpendUSD)
	}
	if resp.Budget.WeekSpendUSD < 74 || resp.Budget.WeekSpendUSD > 76 {
		t.Fatalf("week spend = %.2f, want ≈75", resp.Budget.WeekSpendUSD)
	}
}
