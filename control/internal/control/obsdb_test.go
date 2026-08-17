package control

import (
	"database/sql"
	"os"
	"testing"
	"time"
)

// seedAge inserts one row into each pruned table (sessions/turns/tool_calls/
// alerts), all stamped at ts, tagged by the given session id.
func seedAge(t *testing.T, db *sql.DB, sid string, ts int64) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO sessions(session_id,agent,first_ts,last_ts) VALUES(?,'playground',?,?)`,
		`INSERT INTO turns(session_id,ts,model) VALUES(?,?,'claude-opus-4-8')`,
	} {
		if _, err := db.Exec(q, sid, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO tool_calls(session_id,ts,tool) VALUES(?,?,'Bash')`, sid, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO alerts(agent,run_id,session_id,kind,detail,ts,last_ts) VALUES('playground','',?,'loop','x',?,?)`,
		sid, ts, ts); err != nil {
		t.Fatal(err)
	}
}

// count returns the rows in table matching the session id (alerts key on
// session_id too, so one predicate fits all four tables).
func countFor(t *testing.T, db *sql.DB, table, sid string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE session_id=?`, sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rows older than obsRetentionDays are pruned from every table; recent rows
// stay. The reaper runs at most once per obsPruneEvery (meta-stamped), so a
// second tick inside the window is a no-op even with fresh victims.
func TestPruneObsRetention(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	old := now - int64(obsRetentionDays+1)*86400
	recent := now - 86400
	seedAge(t, db, "s-old", old)
	seedAge(t, db, "s-new", recent)

	if err := pruneObs(db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "turns", "tool_calls", "alerts"} {
		if n := countFor(t, db, table, "s-old"); n != 0 {
			t.Fatalf("%s: old row survived the prune (%d)", table, n)
		}
		if n := countFor(t, db, table, "s-new"); n != 1 {
			t.Fatalf("%s: recent row pruned (%d left)", table, n)
		}
	}

	// gated: a second call inside obsPruneEvery must not touch new victims
	seedAge(t, db, "s-old2", old)
	if err := pruneObs(db); err != nil {
		t.Fatal(err)
	}
	if n := countFor(t, db, "turns", "s-old2"); n != 1 {
		t.Fatalf("prune re-ran inside the 24h gate (%d rows left)", n)
	}

	// past the gate it prunes again
	nowUnix = func() int64 { return now + int64(obsPruneEvery/time.Second) + 60 }
	if err := pruneObs(db); err != nil {
		t.Fatal(err)
	}
	if n := countFor(t, db, "turns", "s-old2"); n != 0 {
		t.Fatalf("prune skipped past the gate (%d rows left)", n)
	}
}

// files cursor rows whose transcript vanished from disk are dropped; a live
// file's cursor survives (deleting it would re-ingest from byte 0).
func TestPruneObsFilesCursor(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	// a live transcript with a real cursor, and a cursor for a deleted one
	live := writeTranscript(t, "playground", "sess-live", sampleLines()...)
	if err := ingestFile(db, "playground", live); err != nil {
		t.Fatal(err)
	}
	goneTranscript := writeTranscript(t, "playground", "sess-gone", sampleLines()...)
	if err := ingestFile(db, "playground", goneTranscript); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(goneTranscript); err != nil {
		t.Fatal(err)
	}

	if err := pruneObs(db); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, live).Scan(&n)
	if n != 1 {
		t.Fatalf("live file's cursor pruned (%d rows)", n)
	}
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE path=?`, goneTranscript).Scan(&n)
	if n != 0 {
		t.Fatalf("missing file's cursor survived (%d rows)", n)
	}
}

// runs rows (ingest.reconcileRuns) outlive their jobs, so the reaper must prune
// them too — otherwise the table grows unbounded (the runs table was omitted
// from the original prune list).
func TestPruneObsPrunesRunsTable(t *testing.T) {
	db := obsEnv(t, "playground")
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	old := now - int64(obsRetentionDays+1)*86400
	recent := now - 86400
	for _, r := range []struct {
		id string
		ts int64
	}{{"r-old", old}, {"r-new", recent}} {
		if _, err := db.Exec(
			`INSERT INTO runs(agent,run_id,kind,label,started,report) VALUES('playground',?,'flow','',?,0)`,
			r.id, r.ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneObs(db); err != nil {
		t.Fatal(err)
	}
	var oldN, newN int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE run_id='r-old'`).Scan(&oldN); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM runs WHERE run_id='r-new'`).Scan(&newN); err != nil {
		t.Fatal(err)
	}
	if oldN != 0 {
		t.Fatalf("old run row survived the prune (%d)", oldN)
	}
	if newN != 1 {
		t.Fatalf("recent run row was pruned (%d left)", newN)
	}
}
