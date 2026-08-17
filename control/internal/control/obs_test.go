package control

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "SUPERSECRETCONTENT" // must never reach the store

// obsEnv opens a fresh store in a temp home and silences logs.
func obsEnv(t *testing.T, agents ...string) *sql.DB {
	t.Helper()
	home = t.TempDir()
	companies = agents
	mintedMem = map[string]string{} // the belt is process-lifetime state; isolate tests
	if err := os.MkdirAll(nsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	prevLogf := logf
	logf = func(string, ...any) {}
	t.Cleanup(func() { logf = prevLogf })
	db, err := openObsDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeTranscript(t *testing.T, agent, session string, lines ...string) string {
	t.Helper()
	dir := projectDir(agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, session+".jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// a two-turn session: one tool_use (Bash) that errors, then a plain turn.
func sampleLines() []string {
	return []string{
		`{"type":"queue-operation","sessionId":"x","operation":"start"}`, // non-message line
		`{"type":"assistant","timestamp":"2026-07-05T23:00:00Z","message":{"model":"claude-opus-4-8","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"` + secret + ` reasoning"},{"type":"tool_use","name":"Bash","id":"tu_1","input":{"command":"echo ` + secret + `"}}]}}`,
		`{"type":"user","timestamp":"2026-07-05T23:00:05Z","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","is_error":true,"content":"boom ` + secret + `"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-05T23:01:00Z","message":{"model":"claude-opus-4-8","usage":{"output_tokens":3},"content":[{"type":"text","text":"done"}]}}`,
	}
}

func TestIngestRollupsAndErrors(t *testing.T) {
	db := obsEnv(t, "playground")
	p := writeTranscript(t, "playground", "sess1", sampleLines()...)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	var turns, calls, errs int
	err := db.QueryRow(`SELECT turns,tool_calls,tool_errors FROM sessions WHERE session_id='sess1'`).
		Scan(&turns, &calls, &errs)
	if err != nil {
		t.Fatal(err)
	}
	if turns != 2 || calls != 1 || errs != 1 {
		t.Fatalf("rollup turns=%d calls=%d errs=%d, want 2/1/1", turns, calls, errs)
	}
	var isErr int
	db.QueryRow(`SELECT is_error FROM tool_calls WHERE tool_use_id='tu_1'`).Scan(&isErr)
	if isErr != 1 {
		t.Fatalf("tool_result error not recorded (is_error=%d)", isErr)
	}
}

// The load-bearing invariant: no message content anywhere in the store.
func TestIngestNeverStoresContent(t *testing.T) {
	db := obsEnv(t, "playground")
	p := writeTranscript(t, "playground", "sess1", sampleLines()...)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	tables := map[string][]string{
		"sessions":   {"session_id", "agent", "run_id", "model", "stop_reason_placeholder"},
		"turns":      {"session_id", "model", "stop_reason"},
		"tool_calls": {"session_id", "tool", "tool_use_id", "input_hash"},
	}
	for table, cols := range tables {
		for _, c := range cols {
			if c == "stop_reason_placeholder" {
				continue
			}
			rows, err := db.Query(`SELECT ` + c + ` FROM ` + table)
			if err != nil {
				continue // column may not exist on that table; skip
			}
			for rows.Next() {
				var v sql.NullString
				rows.Scan(&v)
				if v.Valid && strings.Contains(v.String, secret) {
					rows.Close()
					t.Fatalf("secret leaked into %s.%s = %q", table, c, v.String)
				}
			}
			rows.Close()
		}
	}
}

// An isApiErrorMessage line (top-level flag, "<synthetic>" model, zero usage) is
// kept as an api_error turn row for limit-hit detection, but does NOT advance the
// session rollup (turn count / last model) — and its content never lands.
func TestIngestAPIErrorTurn(t *testing.T) {
	db := obsEnv(t, "playground")
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-05T23:00:00Z","message":{"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-05T23:05:00Z","isApiErrorMessage":true,"message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"` + secret + ` rate limit"}]}}`,
	}
	p := writeTranscript(t, "playground", "sess1", lines...)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	// two turn rows total, exactly one flagged api_error.
	var total, apiErr int
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(api_error),0) FROM turns WHERE session_id='sess1'`).Scan(&total, &apiErr)
	if total != 2 || apiErr != 1 {
		t.Fatalf("turns=%d api_error=%d, want 2/1", total, apiErr)
	}
	// the rollup counts only the real turn, and the session model isn't overwritten
	// with "<synthetic>".
	var turns int
	var model string
	db.QueryRow(`SELECT turns, COALESCE(model,'') FROM sessions WHERE session_id='sess1'`).Scan(&turns, &model)
	if turns != 1 || model != "claude-opus-4-8" {
		t.Fatalf("session rollup turns=%d model=%q, want 1/claude-opus-4-8", turns, model)
	}
	// privacy: the api-error content must not have leaked.
	var leaked int
	db.QueryRow(`SELECT COUNT(*) FROM turns WHERE model LIKE '%' || ? || '%'`, secret).Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("api-error content leaked into turns")
	}
}

// A fresh store carries the api_error column; ensureColumn is idempotent so a
// second open (existing DB) doesn't error.
func TestObsDBMigrationIdempotent(t *testing.T) {
	obsEnv(t, "playground") // opens once via openObsDB
	db2, err := openObsDB() // re-open the same file: migration must be a no-op
	if err != nil {
		t.Fatalf("re-open failed: %v", err)
	}
	defer db2.Close()
	if _, err := db2.Exec(`INSERT INTO turns(session_id,ts,api_error) VALUES('x',1,1)`); err != nil {
		t.Fatalf("api_error column missing after migration: %v", err)
	}
}

func TestIngestIncrementalCursor(t *testing.T) {
	db := obsEnv(t, "playground")
	first := sampleLines()
	p := writeTranscript(t, "playground", "sess1", first...)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	// append one more assistant turn, ingest again
	extra := `{"type":"assistant","timestamp":"2026-07-05T23:02:00Z","message":{"model":"claude-opus-4-8","usage":{"output_tokens":2},"content":[{"type":"text","text":"more"}]}}`
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(extra + "\n")
	f.Close()
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	var turns int
	db.QueryRow(`SELECT turns FROM sessions WHERE session_id='sess1'`).Scan(&turns)
	if turns != 3 {
		t.Fatalf("after append want 3 turns (no dupes), got %d", turns)
	}
	var rowCount int
	db.QueryRow(`SELECT COUNT(*) FROM turns WHERE session_id='sess1'`).Scan(&rowCount)
	if rowCount != 3 {
		t.Fatalf("want 3 turn rows, got %d", rowCount)
	}
}

func TestIngestPartialLineHeldBack(t *testing.T) {
	db := obsEnv(t, "playground")
	dir := projectDir("playground")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "sess1.jsonl")
	// a complete line + a partial (no trailing newline)
	complete := `{"type":"assistant","timestamp":"2026-07-05T23:00:00Z","message":{"model":"m","usage":{"output_tokens":1},"content":[]}}`
	os.WriteFile(p, []byte(complete+"\n"+`{"type":"assistant","times`), 0o644)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	var turns int
	db.QueryRow(`SELECT turns FROM sessions WHERE session_id='sess1'`).Scan(&turns)
	if turns != 1 {
		t.Fatalf("partial line should be held back: want 1 turn, got %d", turns)
	}
}

func TestDetectLoopAndStorm(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	prevLive := agentHasLiveRun
	agentHasLiveRun = func(string) bool { return false }
	t.Cleanup(func() { nowUnix, agentHasLiveRun = prevNow, prevLive })

	db.Exec(`INSERT INTO sessions(session_id,agent,tool_calls,tool_errors,last_ts) VALUES('s1','playground',10,9,?)`, now-60)
	// loop: 5 identical Bash calls
	for i := range 5 {
		db.Exec(`INSERT INTO tool_calls(session_id,ts,tool,tool_use_id,is_error,input_hash) VALUES('s1',?,'Bash',?,0,'abc123')`, now-60, "tu"+string(rune('a'+i)))
	}
	runDetectors(db)

	var loop, storm int
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='loop' AND session_id='s1'`).Scan(&loop)
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='error-storm' AND session_id='s1'`).Scan(&storm)
	if loop != 1 {
		t.Fatalf("want 1 loop alert, got %d", loop)
	}
	if storm != 1 {
		t.Fatalf("want 1 error-storm alert, got %d", storm)
	}
	// idempotent: re-running doesn't duplicate
	runDetectors(db)
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='loop' AND session_id='s1'`).Scan(&loop)
	if loop != 1 {
		t.Fatalf("loop alert duplicated on re-run: %d", loop)
	}
}

func TestDetectNoDeliverable(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	prevLive := agentHasLiveRun
	agentHasLiveRun = func(string) bool { return false }
	t.Cleanup(func() { nowUnix, agentHasLiveRun = prevNow, prevLive })

	old := now - int64(10*time.Hour/time.Second) // past runMaxLife
	db.Exec(`INSERT INTO runs(agent,run_id,started,report) VALUES('playground','20260705-2300-sweep-dead',?,0)`, old)
	db.Exec(`INSERT INTO runs(agent,run_id,started,report) VALUES('playground','20260705-2300-sweep-skip',?,0)`, old)
	// one has an authfail marker -> should NOT be flagged no-deliverable
	os.MkdirAll(reportsDir("playground"), 0o755)
	os.WriteFile(filepath.Join(reportsDir("playground"), "20260705-2300-sweep-skip.authfail"), nil, 0o644)

	runDetectors(db)
	var nd int
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='no-deliverable'`).Scan(&nd)
	if nd != 1 {
		t.Fatalf("want 1 no-deliverable (skip excluded), got %d", nd)
	}
	var which string
	db.QueryRow(`SELECT run_id FROM alerts WHERE kind='no-deliverable'`).Scan(&which)
	if which != "20260705-2300-sweep-dead" {
		t.Fatalf("wrong run flagged: %s", which)
	}
}

// openAlertCount is a small helper: how many alerts are visible to the read
// surfaces (which all filter cleared=0), optionally scoped by kind.
func openAlertCount(t *testing.T, db *sql.DB, kind string) int {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM alerts WHERE cleared=0`
	if kind != "" {
		if err := db.QueryRow(q+` AND kind=?`, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pinClock fixes nowUnix + stubs live-run detection off for a detector test.
func pinClock(t *testing.T, now int64) {
	t.Helper()
	prevNow, prevLive := nowUnix, agentHasLiveRun
	nowUnix = func() int64 { return now }
	agentHasLiveRun = func(string) bool { return false }
	t.Cleanup(func() { nowUnix, agentHasLiveRun = prevNow, prevLive })
}

// An alert older than alertMaxAge is aged out (cleared, suffixed once) and
// vanishes from the open listing; a fresh one under the window stays open.
func TestClearStaleAlertsAgeOut(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	pinClock(t, now)

	old := now - int64(40*time.Hour/time.Second) // past alertMaxAge (36h)
	fresh := now - int64(2*time.Hour/time.Second)
	db.Exec(`INSERT INTO alerts(agent,session_id,kind,detail,ts,cleared) VALUES('playground','s-old','loop','Bash x9',?,0)`, old)
	db.Exec(`INSERT INTO alerts(agent,session_id,kind,detail,ts,cleared) VALUES('playground','s-new','loop','Bash x9',?,0)`, fresh)

	clearStaleAlerts(db, now)

	if got := openAlertCount(t, db, ""); got != 1 {
		t.Fatalf("want 1 open alert after age-out, got %d", got)
	}
	var detail string
	db.QueryRow(`SELECT detail FROM alerts WHERE session_id='s-old'`).Scan(&detail)
	if detail != "Bash x9 (aged out)" {
		t.Fatalf("aged-out detail = %q, want suffix appended once", detail)
	}
	// idempotent: a second pass neither re-appends nor touches the cleared row.
	clearStaleAlerts(db, now)
	db.QueryRow(`SELECT detail FROM alerts WHERE session_id='s-old'`).Scan(&detail)
	if detail != "Bash x9 (aged out)" {
		t.Fatalf("suffix re-appended on second pass: %q", detail)
	}
	if got := openAlertCount(t, db, ""); got != 1 {
		t.Fatalf("open count changed on second pass: %d", got)
	}
}

// A no-deliverable alert clears once the run's report lands on disk.
func TestClearStaleAlertsNoDeliverableResolves(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	pinClock(t, now)
	os.MkdirAll(reportsDir("playground"), 0o755)

	id := "20260705-2300-sweep-late"
	ts := now - int64(3*time.Hour/time.Second) // recent, so age-out doesn't fire
	db.Exec(`INSERT INTO alerts(agent,run_id,kind,detail,ts,cleared) VALUES('playground',?,'no-deliverable','no report',?,0)`, id, ts)

	// no report yet -> stays open
	clearStaleAlerts(db, now)
	if got := openAlertCount(t, db, "no-deliverable"); got != 1 {
		t.Fatalf("want alert still open before report, got %d", got)
	}

	// report appears -> clears
	os.WriteFile(filepath.Join(reportsDir("playground"), id+".md"), []byte("# report"), 0o644)
	clearStaleAlerts(db, now)
	if got := openAlertCount(t, db, "no-deliverable"); got != 0 {
		t.Fatalf("want alert cleared after report landed, got %d open", got)
	}
}

// A cleared alert re-raised by its detector comes back open with clean detail —
// no stale "(aged out)" suffix on a live alert.
func TestReRaiseAfterClearReopensClean(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	pinClock(t, now)

	// an aged-out loop alert — seeded via raiseAlert so its conflict key (run_id='')
	// matches what the detector uses on re-raise, exactly as in production.
	old := now - int64(40*time.Hour/time.Second)
	raiseAlert(db, "playground", "", "s1", "loop", "Bash x9", old, old)
	clearStaleAlerts(db, now)
	if got := openAlertCount(t, db, "loop"); got != 0 {
		t.Fatalf("precondition: alert should be aged out, got %d open", got)
	}

	// the detector sees the condition again -> upsert reopens it
	raiseAlert(db, "playground", "", "s1", "loop", "Bash called 6× with identical input", now, now)
	if got := openAlertCount(t, db, "loop"); got != 1 {
		t.Fatalf("re-raised alert should be open, got %d", got)
	}
	var detail string
	db.QueryRow(`SELECT detail FROM alerts WHERE session_id='s1' AND kind='loop'`).Scan(&detail)
	if strings.Contains(detail, "aged out") {
		t.Fatalf("re-raised live alert carries stale suffix: %q", detail)
	}
	if detail != "Bash called 6× with identical input" {
		t.Fatalf("re-raised detail = %q, want the fresh detector text", detail)
	}
}

func TestDetectStall(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	prevLive := agentHasLiveRun
	agentHasLiveRun = func(string) bool { return true } // pretend a run is live
	t.Cleanup(func() { nowUnix, agentHasLiveRun = prevNow, prevLive })

	// newest turn 30 min ago -> stalled (> stallTurnGap)
	db.Exec(`INSERT INTO sessions(session_id,agent,last_ts) VALUES('s1','playground',?)`, now-int64(30*time.Minute/time.Second))
	runDetectors(db)
	var stall int
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='stall'`).Scan(&stall)
	if stall != 1 {
		t.Fatalf("want 1 stall alert, got %d", stall)
	}
}

// Two agents' concurrent same-kind alerts (both with empty run/session ids)
// must be two rows — agent is part of the conflict key.
func TestAlertsPerAgentDistinct(t *testing.T) {
	db := obsEnv(t, "playground", "agent-a")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	raiseAlert(db, "playground", "", "", "stall", "p: 25m gap", now, now)
	raiseAlert(db, "agent-a", "", "", "stall", "n: 40m gap", now, now)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='stall' AND cleared=0`).Scan(&n)
	if n != 2 {
		t.Fatalf("want 2 per-agent stall rows, got %d", n)
	}
	var detail string
	db.QueryRow(`SELECT detail FROM alerts WHERE kind='stall' AND agent='playground'`).Scan(&detail)
	if detail != "p: 25m gap" {
		t.Fatalf("playground detail overwritten: %q", detail)
	}
}

// Age-out keys on last_ts (last time a detector still saw the condition), not
// first-seen ts — a standing alert first noticed long after its run started
// must not be born pre-aged and instantly cleared.
func TestAlertAgeOutUsesLastRefresh(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	old := now - int64(40*time.Hour/time.Second) // past alertMaxAge

	raiseAlert(db, "playground", "run1", "", "no-deliverable", "no report", old, now)
	clearStaleAlerts(db, now)
	if got := openAlertCount(t, db, "no-deliverable"); got != 1 {
		t.Fatalf("alert with fresh last_ts aged out: %d open", got)
	}
	// detector stops refreshing (condition gone stale) -> ages out
	if _, err := db.Exec(`UPDATE alerts SET last_ts=?`, old); err != nil {
		t.Fatal(err)
	}
	clearStaleAlerts(db, now)
	if got := openAlertCount(t, db, "no-deliverable"); got != 0 {
		t.Fatalf("stale alert not aged out: %d open", got)
	}
}

// The control plane's own 10-minute haiku auth probes must not reset the stall
// clock: a frozen Opus night with a fresh probe transcript is still a stall.
func TestDetectStallIgnoresHaikuProbeSessions(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	prevLive := agentHasLiveRun
	agentHasLiveRun = func(string) bool { return true }
	t.Cleanup(func() { nowUnix, agentHasLiveRun = prevNow, prevLive })

	// the real (frozen) night session: last turn 30 min ago
	db.Exec(`INSERT INTO sessions(session_id,agent,model,last_ts) VALUES('night','playground','claude-opus-4-8',?)`,
		now-int64(30*time.Minute/time.Second))
	// a probe session refreshed 1 min ago — would mask the stall if counted
	db.Exec(`INSERT INTO sessions(session_id,agent,model,last_ts) VALUES('probe','playground','claude-haiku-4-5-20251001',?)`,
		now-60)
	runDetectors(db)
	if got := openAlertCount(t, db, "stall"); got != 1 {
		t.Fatalf("haiku probe masked the stall: %d alerts, want 1", got)
	}
}

// The Health tab's per-agent session/turn counts must exclude the 10-min haiku
// auth probe (it writes ~144 sessions/day) — otherwise playground's numbers are
// dominated by probe noise, not real night work.
func TestAgentStatsExcludeHaikuProbes(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 5, 23, 30, 0, 0, time.UTC).Unix()

	// One real Opus night session with 5 turns...
	db.Exec(`INSERT INTO sessions(session_id,agent,model,last_ts,turns) VALUES('night','playground','claude-opus-4-8',?,5)`, now-60)
	// ...and three probe sessions (1 haiku turn each) that must NOT be counted.
	for i, sid := range []string{"probe1", "probe2", "probe3"} {
		db.Exec(`INSERT INTO sessions(session_id,agent,model,last_ts,turns) VALUES(?,'playground','claude-haiku-4-5-20251001',?,1)`,
			sid, now-int64(60*(i+1)))
	}

	stats := queryAgentStats(db, now-int64(72*time.Hour/time.Second))
	if len(stats) != 1 || stats[0].Agent != "playground" {
		t.Fatalf("stats = %+v, want a single playground row", stats)
	}
	if stats[0].Sessions != 1 {
		t.Fatalf("sessions = %d, want 1 (3 haiku probes excluded)", stats[0].Sessions)
	}
	if stats[0].Turns != 5 {
		t.Fatalf("turns = %d, want 5 (only the opus night)", stats[0].Turns)
	}
}

// A cursor reset (truncated/rotated transcript) replays lines from byte 0; the
// uuid dedup must keep spend and rollups single-counted.
func TestReingestAfterTruncationNoDoubleCount(t *testing.T) {
	db := obsEnv(t, "playground")
	l1 := `{"type":"assistant","uuid":"u1","timestamp":"2026-07-05T23:00:00Z","message":{"model":"claude-opus-4-8","usage":{"output_tokens":1000},"content":[]}}`
	l2 := `{"type":"assistant","uuid":"u2","timestamp":"2026-07-05T23:01:00Z","message":{"model":"claude-opus-4-8","usage":{"output_tokens":2000},"content":[]}}`
	p := writeTranscript(t, "playground", "sess1", l1, l2)
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	// rotation: same session file, now shorter (only line 1) -> cursor resets to 0
	if err := os.WriteFile(p, []byte(l1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ingestFile(db, "playground", p); err != nil {
		t.Fatal(err)
	}
	var rows, turns int
	var out int64
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(out_tokens),0) FROM turns WHERE session_id='sess1'`).Scan(&rows, &out)
	db.QueryRow(`SELECT turns FROM sessions WHERE session_id='sess1'`).Scan(&turns)
	if rows != 2 || out != 3000 || turns != 2 {
		t.Fatalf("re-ingest double-counted: rows=%d out=%d rollup=%d, want 2/3000/2", rows, out, turns)
	}
}

// A pre-last_ts store (old schema: no last_ts, conflict key without agent)
// migrates in place: rows survive, and the new conflict key works.
func TestAlertsTableMigration(t *testing.T) {
	obsEnv(t, "playground")
	// hand-build an OLD-shape db
	old := filepath.Join(nsDir(), "observability.db")
	os.Remove(old)
	db1, err := sql.Open("sqlite", "file:"+old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Exec(`CREATE TABLE alerts (
		agent TEXT NOT NULL, run_id TEXT, session_id TEXT, kind TEXT NOT NULL,
		detail TEXT, ts INTEGER NOT NULL, cleared INTEGER NOT NULL DEFAULT 0,
		UNIQUE (kind, run_id, session_id));
		INSERT INTO alerts(agent,run_id,session_id,kind,detail,ts,cleared)
		VALUES('playground','r1','','no-deliverable','old row',100,0)`); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := openObsDB() // runs the migration
	if err != nil {
		t.Fatalf("open over old schema failed: %v", err)
	}
	defer db2.Close()
	var detail string
	var lastTs int64
	if err := db2.QueryRow(`SELECT detail, last_ts FROM alerts WHERE run_id='r1'`).Scan(&detail, &lastTs); err != nil {
		t.Fatalf("migrated row lost: %v", err)
	}
	if detail != "old row" || lastTs != 100 { // last_ts seeded from ts
		t.Fatalf("migrated row = %q/%d, want 'old row'/100", detail, lastTs)
	}
	// the widened conflict key accepts two agents on the same (kind,'','')
	raiseAlert(db2, "playground", "", "", "stall", "p", 200, 200)
	raiseAlert(db2, "agent-a", "", "", "stall", "n", 200, 200)
	var n int
	db2.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='stall'`).Scan(&n)
	if n != 2 {
		t.Fatalf("post-migration conflict key wrong: %d stall rows", n)
	}
}
