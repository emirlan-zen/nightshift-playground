// Observability store (ADR-0006): a SQLite database DERIVED from the JSONL
// transcripts Claude Code writes — not a replacement for them. The transcripts
// stay the source of truth (the harness owns that format); this is an index +
// event store the control plane populates by tailing them, so the page and the
// agents get durable, queryable history without re-scanning every request.
//
// PRIVACY INVARIANT (same as usage.go): metadata only. We store usage numbers,
// models, timestamps, tool *names*, tool-result error flags, stop reasons, and
// text *hashes* — never message content. Enforced in ingest.go at decode.
//
// modernc.org/sqlite is a pure-Go driver (no CGO), so the control plane stays a
// single static binary that `go build` alone produces. (The box's Go was bumped
// to 1.26 for this — the current modernc line requires Go >= 1.25.)
package control

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func obsDBPath() string { return filepath.Join(nsDir(), "observability.db") }

// schema is applied idempotently on every open. All timestamps are unix seconds.
// Deliberately denormalised and index-light: this is a write-once-read-many
// store scanned by a handful of queries, not a hot transactional DB.
const obsSchema = `
CREATE TABLE IF NOT EXISTS files (
  path        TEXT PRIMARY KEY,
  size        INTEGER NOT NULL,  -- bytes consumed so far (the ingest cursor)
  mtime       INTEGER NOT NULL   -- last mtime we ingested at
);

-- One row per Claude Code session (a transcript file). Rollups are maintained
-- incrementally as turns are ingested; run_id is best-effort correlated.
CREATE TABLE IF NOT EXISTS sessions (
  session_id  TEXT PRIMARY KEY,
  agent       TEXT NOT NULL,
  run_id      TEXT,              -- night-run id if correlated, else NULL
  model       TEXT,              -- last assistant model seen
  first_ts    INTEGER,
  last_ts     INTEGER,
  turns       INTEGER NOT NULL DEFAULT 0,  -- assistant turns with usage
  tool_calls  INTEGER NOT NULL DEFAULT 0,
  tool_errors INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_agent ON sessions(agent, last_ts);
CREATE INDEX IF NOT EXISTS idx_sessions_run   ON sessions(run_id);

-- One row per assistant turn — the cadence/timeline signal. No content.
-- uuid is the transcript line's own id: with the partial unique index below it
-- makes ingestion idempotent, so a cursor reset (truncated/rotated file) can't
-- double-count a session's spend.
CREATE TABLE IF NOT EXISTS turns (
  session_id  TEXT NOT NULL,
  ts          INTEGER NOT NULL,
  model       TEXT,
  in_tokens   INTEGER NOT NULL DEFAULT 0,
  out_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_read  INTEGER NOT NULL DEFAULT 0,
  cache_create INTEGER NOT NULL DEFAULT 0,
  stop_reason TEXT,
  api_error   INTEGER NOT NULL DEFAULT 0,  -- 1 if this line was an API-error message (limit-hit signal)
  uuid        TEXT
);
CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id, ts);

-- One row per tool invocation. input_hash is a hash of the tool input (for loop
-- detection) — the input itself is NEVER stored.
CREATE TABLE IF NOT EXISTS tool_calls (
  session_id  TEXT NOT NULL,
  ts          INTEGER NOT NULL,
  tool        TEXT NOT NULL,
  tool_use_id TEXT,               -- transient id, used to attach the tool_result
  is_error    INTEGER NOT NULL DEFAULT 0,
  input_hash  TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_session ON tool_calls(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_tool_use_id  ON tool_calls(tool_use_id);

-- One row per night run, its lifecycle drawn from systemd/journald + report
-- presence. outcome is filled by detect.go (completed|skipped-auth|no-deliverable|…).
CREATE TABLE IF NOT EXISTS runs (
  agent      TEXT NOT NULL,
  run_id     TEXT NOT NULL,
  kind       TEXT,
  label      TEXT,
  started    INTEGER,
  ended      INTEGER,
  exit_code  INTEGER,
  report     INTEGER NOT NULL DEFAULT 0,  -- 1 if a morning report exists
  outcome    TEXT,
  PRIMARY KEY (agent, run_id)
);

-- Periodic liveness snapshots for currently-active runs — the freeze signal.
CREATE TABLE IF NOT EXISTS run_health (
  agent           TEXT NOT NULL,
  run_id          TEXT NOT NULL,
  ts              INTEGER NOT NULL,
  transcript_mtime INTEGER,
  turns           INTEGER,
  last_turn_age   INTEGER          -- seconds since the last assistant turn
);
CREATE INDEX IF NOT EXISTS idx_health_run ON run_health(agent, run_id, ts);

-- Store-level bookkeeping (currently just the reaper's last_prune stamp).
CREATE TABLE IF NOT EXISTS meta (
  key    TEXT PRIMARY KEY,
  value  TEXT NOT NULL
);

-- Judge/eval scores (ADR-0023): one row per (run, judge node, subject,
-- dimension). Written by reconcile in the same exactly-once step that routes a
-- verdict; UNIQUE is the backstop that keeps a replay from bending an average.
-- score is NULL for an honest unknown. Everything else is denormalised at
-- write time (the pinned job is right there) so reads stay one flat query.
CREATE TABLE IF NOT EXISTS scores (
  id            INTEGER PRIMARY KEY,
  night         TEXT NOT NULL,     -- evening-anchored nightKey
  run_id        TEXT NOT NULL,     -- flow id or wave run id
  judge_node    TEXT NOT NULL,     -- node id, or 'janitor' for outcome rows
  subject       TEXT NOT NULL,
  dimension     TEXT NOT NULL,
  score         INTEGER,           -- NULL = unknown
  max           INTEGER NOT NULL,
  rationale     TEXT,
  automation_id TEXT, automation_rev TEXT,
  subject_prompt_rev TEXT,         -- the subject job's pinned prompt revision
  judge_executor TEXT, judge_model TEXT,
  subject_executor TEXT, subject_model TEXT,
  created_at    INTEGER NOT NULL,
  UNIQUE(run_id, judge_node, subject, dimension)
);
CREATE INDEX IF NOT EXISTS idx_scores_run ON scores(run_id);
CREATE INDEX IF NOT EXISTS idx_scores_automation ON scores(automation_id, created_at);

-- Detected problems. (kind, agent, run_id, session_id) is unique so a re-run of
-- the detector updates rather than duplicates a standing alert — agent is in the
-- key because stall/limit-hit rows carry empty run/session ids, and without it
-- two agents' concurrent incidents collapsed into one row. ts = first seen;
-- last_ts = last time a detector pass still saw the condition (age-out keys on
-- this so a standing alert can't be born pre-aged or self-clear while live).
CREATE TABLE IF NOT EXISTS alerts (
  agent      TEXT NOT NULL,
  run_id     TEXT,
  session_id TEXT,
  kind       TEXT NOT NULL,       -- stall|loop|error-storm|crash|no-deliverable|zero-turns|auth
  detail     TEXT,
  ts         INTEGER NOT NULL,
  last_ts    INTEGER NOT NULL DEFAULT 0,
  cleared    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (kind, agent, run_id, session_id)
);
CREATE INDEX IF NOT EXISTS idx_alerts_open ON alerts(cleared, ts);
`

// openObsDB opens (creating if needed) the store with WAL + a busy timeout so
// the single ingester writer and the read handlers don't trip over each other.
func openObsDB() (*sql.DB, error) {
	dsn := "file:" + obsDBPath() +
		"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(obsSchema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateObsDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrateObsDB applies additive schema changes to a store created by an earlier
// version. CREATE TABLE IF NOT EXISTS never adds a column to an existing table,
// so post-original columns are added here, guarded so it's a no-op on a fresh DB
// (which already has them) and on re-open.
func migrateObsDB(db *sql.DB) error {
	// turns.api_error (limit-hit signal, ADR usage-governor).
	if err := ensureColumn(db, "turns", "api_error", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// turns.uuid + the dedup index. The index lives here, not in obsSchema: on a
	// pre-uuid store the schema string runs before this column exists, and a
	// CREATE INDEX on a missing column would fail the open.
	if err := ensureColumn(db, "turns", "uuid", "TEXT"); err != nil {
		return err
	}
	if _, err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_turns_uuid ON turns(session_id, uuid) WHERE uuid IS NOT NULL AND uuid != ''`); err != nil {
		return err
	}
	return migrateAlertsTable(db)
}

// migrateAlertsTable rebuilds a pre-last_ts alerts table to the current shape:
// SQLite can't alter a table-level UNIQUE constraint, so widening the conflict
// key to include agent means rename + recreate + copy. last_ts is seeded from
// ts. Keyed on the last_ts column's absence, so it runs exactly once.
func migrateAlertsTable(db *sql.DB) error {
	has, err := hasColumn(db, "alerts", "last_ts")
	if err != nil || has {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`ALTER TABLE alerts RENAME TO alerts_old`,
		`CREATE TABLE alerts (
		   agent      TEXT NOT NULL,
		   run_id     TEXT,
		   session_id TEXT,
		   kind       TEXT NOT NULL,
		   detail     TEXT,
		   ts         INTEGER NOT NULL,
		   last_ts    INTEGER NOT NULL DEFAULT 0,
		   cleared    INTEGER NOT NULL DEFAULT 0,
		   UNIQUE (kind, agent, run_id, session_id)
		 )`,
		`INSERT OR IGNORE INTO alerts(agent,run_id,session_id,kind,detail,ts,last_ts,cleared)
		   SELECT agent,run_id,session_id,kind,detail,ts,ts,cleared FROM alerts_old`,
		`DROP TABLE alerts_old`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_open ON alerts(cleared, ts)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// hasColumn reports whether table has col (PRAGMA table_info).
func hasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ensureColumn adds a column if the table lacks it. Idempotent: PRAGMA
// table_info is the authoritative check, so we never depend on matching a
// driver-specific "duplicate column" error string.
func ensureColumn(db *sql.DB, table, col, decl string) error {
	has, err := hasColumn(db, table, col)
	if err != nil || has {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + decl)
	return err
}

// nowUnix is a test seam (Date.now equivalents are fine in Go tests, but the
// ingester wants a single clock the tests can pin).
var nowUnix = func() int64 { return time.Now().Unix() }

// ---- retention (the reaper) ----------------------------------------------------

const (
	// obsRetentionDays is the reaper's cutoff: rows older than this leave the
	// store. Sized well above every read window (14d usage dashboard, 7d week
	// spend, 72h detectors, 36h alert age-out) so pruning can never bend a
	// number a live surface still shows.
	obsRetentionDays = 45
	// obsPruneEvery rate-limits the reaper — retention is a daily janitorial
	// concern, not a per-tick one. The last-prune stamp persists in the meta
	// table, so a control-plane restart doesn't re-prune.
	obsPruneEvery = 24 * time.Hour
)

// pruneObs enforces the store's retention (the obs store otherwise grows
// unbounded — the ADR-0006 follow-up). Called on every ingest tick, self-gated
// to at most once per obsPruneEvery. Deletes turns / tool_calls / sessions /
// alerts / runs older than obsRetentionDays, and `files` cursor rows whose transcript
// no longer exists on disk (rotated/archived — a dead cursor would otherwise
// pin the path row forever).
func pruneObs(db *sql.DB) error {
	now := nowUnix()
	var last int64
	_ = db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='last_prune'`).Scan(&last)
	if now-last < int64(obsPruneEvery/time.Second) {
		return nil
	}
	cutoff := now - int64(obsRetentionDays)*86400
	for _, q := range []string{
		`DELETE FROM turns WHERE ts < ?`,
		`DELETE FROM tool_calls WHERE ts < ?`,
		// a session's age is its last activity; a cursor-only row (no turns yet)
		// coalesces to 0 and goes too — ensureSession recreates it if its
		// transcript ever grows again.
		`DELETE FROM sessions WHERE COALESCE(last_ts, first_ts, 0) < ?`,
		// same age rule clearStaleAlerts uses: last refresh, falling back to first-seen.
		`DELETE FROM alerts WHERE COALESCE(NULLIF(last_ts,0), ts) < ?`,
		// a run row (ingest.reconcileRuns upsert) outlives its job: once the job
		// is pruned off disk (jobKeepDays) reconcileRuns stops upserting it but
		// never deletes it, so the runs table grew unbounded. Both readers stay
		// well inside retention — detectNoDeliverable's 72h lookback and
		// queryLedger's cutoff+LIMIT — so pruning old rows bends no live surface.
		// (run_health is schema-only, never written, so it can't leak.)
		`DELETE FROM runs WHERE COALESCE(started,0) < ?`,
		// Scores age out with everything else (ADR-0023): the automation-level
		// trend reads inside this window, and a score older than retention
		// describes a prompt revision nobody runs any more.
		`DELETE FROM scores WHERE created_at < ?`,
	} {
		if _, err := db.Exec(q, cutoff); err != nil {
			return err
		}
	}
	// Cursor rows for transcripts gone from disk. Collect first (rows closed)
	// before deleting; only a confirmed not-exist drops the row — a transient
	// stat error (perms, NFS blip) must not reset a live file's cursor.
	rows, err := db.Query(`SELECT path FROM files`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			continue
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			gone = append(gone, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range gone {
		if _, err := db.Exec(`DELETE FROM files WHERE path=?`, p); err != nil {
			return err
		}
	}
	_, err = db.Exec(
		`INSERT INTO meta(key,value) VALUES('last_prune',?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, now)
	return err
}
