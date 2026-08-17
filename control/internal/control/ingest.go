// Ingester (ADR-0006): a background goroutine that tails the JSONL transcripts
// into the observability store. Incremental — each file has a byte-offset cursor
// so a tick only reads bytes appended since the last one. Metadata only: usage
// numbers, models, timestamps, tool NAMES, tool-result error flags, and a HASH
// of each tool input (for loop detection). Message text is never read into the
// store — the same privacy invariant usage.go holds.
package control

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

const ingestInterval = 30 * time.Second

// tlLine is the minimal shape we decode per transcript line. Unknown fields —
// crucially message.content's text — are dropped by the decoder unless we ask
// for them, and we only ask for the block envelope (type/name/id/is_error), not
// text bodies. `Content` is json.RawMessage so we can tolerate both the array
// (assistant/tool turns) and string (plain user text) forms.
type tlLine struct {
	Type      string    `json:"type"`
	UUID      string    `json:"uuid"` // the line's own id — the turn-dedup key
	Timestamp time.Time `json:"timestamp"`
	// APIError is Claude Code's top-level boolean marking a turn that carried an
	// API error (a rate/usage limit, an overload, etc.). It rides an "assistant"
	// line with a "<synthetic>" model and zero usage — so without this flag the
	// zero-usage guard below would drop it. We record the FLAG only; the error
	// message content is never decoded (same privacy invariant as the rest).
	APIError bool `json:"isApiErrorMessage"`
	Message  struct {
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			In          int64 `json:"input_tokens"`
			Out         int64 `json:"output_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is a single content block. For tool_use we keep name + a hash of input;
// for tool_result we keep the error flag + the id it answers. Input is hashed,
// never stored.
type block struct {
	Type      string          `json:"type"`
	Name      string          `json:"name"`        // tool_use
	ID        string          `json:"id"`          // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use — hashed only
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`    // tool_result
}

func hashInput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8]) // 16 hex chars — enough to spot repeats
}

var obsDB *sql.DB

// startIngester opens the store and tails transcripts forever. A failure to open
// is logged and disables ingest (the rest of the control plane runs fine).
func startIngester() {
	db, err := openObsDB()
	if err != nil {
		logf("obs: open failed, ingest disabled: %v", err)
		return
	}
	obsDB = db
	go func() {
		for {
			if err := ingestOnce(db); err != nil {
				logf("obs: ingest tick: %v", err)
			}
			runDetectors(db)
			// retention: self-gated to once per obsPruneEvery (see obsdb.go)
			if err := pruneObs(db); err != nil {
				logf("obs: prune: %v", err)
			}
			time.Sleep(ingestInterval)
		}
	}()
}

func ingestOnce(db *sql.DB) error {
	for _, agent := range companies {
		dir := projectDir(agent)
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		for _, f := range files {
			if err := ingestFile(db, agent, f); err != nil {
				logf("obs: ingest %s: %v", filepath.Base(f), err)
			}
		}
	}
	reconcileRuns(db)
	return nil
}

// ingestFile advances one transcript from its stored cursor. It reads only whole
// lines (up to the last newline); a partial trailing line — a session mid-write —
// is left for the next tick. If the file shrank (rotation) the cursor resets.
func ingestFile(db *sql.DB, agent, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	sessionID := trimExt(filepath.Base(path))

	var cur int64
	_ = db.QueryRow(`SELECT size FROM files WHERE path=?`, path).Scan(&cur)
	if st.Size() < cur {
		cur = 0 // truncated/rotated — re-read from the top
	}
	if st.Size() == cur {
		return nil // nothing appended
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(cur, io.SeekStart); err != nil {
		return err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	// consume only up to the last complete line
	end := lastNewline(buf)
	if end < 0 {
		return nil // no complete line yet
	}
	lines := buf[:end]

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureSession(tx, sessionID, agent); err != nil {
		return err
	}
	for _, raw := range splitLines(lines) {
		if len(raw) == 0 {
			continue
		}
		var l tlLine
		if json.Unmarshal(raw, &l) != nil {
			continue // skip malformed / non-object lines
		}
		if err := applyLine(tx, sessionID, &l); err != nil {
			return err
		}
	}
	newSize := cur + int64(end) + 1
	if _, err := tx.Exec(
		`INSERT INTO files(path,size,mtime) VALUES(?,?,?)
		 ON CONFLICT(path) DO UPDATE SET size=excluded.size, mtime=excluded.mtime`,
		path, newSize, st.ModTime().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureSession(tx *sql.Tx, id, agent string) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO sessions(session_id,agent) VALUES(?,?)`, id, agent)
	return err
}

// applyLine folds one transcript line into the rollups. Only assistant turns
// with real usage and tool blocks matter; everything else is skipped.
func applyLine(tx *sql.Tx, sid string, l *tlLine) error {
	switch l.Type {
	case "assistant":
		u := l.Message.Usage
		hasUsage := l.Message.Model != "" && (u.In != 0 || u.Out != 0 || u.CacheRead != 0 || u.CacheCreate != 0)
		// An API-error line has no usage but still matters (the limit-hit signal);
		// keep it as a turn row flagged api_error=1. A plain zero-usage line is noise.
		if !hasUsage && !l.APIError {
			return nil
		}
		ts := l.Timestamp.Unix()
		apiErr := 0
		if l.APIError {
			apiErr = 1
		}
		// OR IGNORE + the (session_id, uuid) unique index make re-ingestion
		// idempotent: a cursor reset (truncated/rotated transcript) replays the
		// file from byte 0, and without this every replayed turn doubled the
		// session's spend — phantom dollars straight into the governor's pacing.
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO turns(session_id,ts,model,in_tokens,out_tokens,cache_read,cache_create,stop_reason,api_error,uuid)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			sid, ts, l.Message.Model, u.In, u.Out, u.CacheRead, u.CacheCreate, l.Message.StopReason, apiErr, l.UUID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // duplicate line (re-ingest) — rollups already counted it
		}
		// Only real (usage-bearing) turns advance the session rollup: turn count,
		// last-model, and the activity window. A synthetic api-error line must not
		// overwrite the session's model with "<synthetic>" or inflate the turn count.
		if !hasUsage {
			return nil
		}
		if _, err := tx.Exec(
			`UPDATE sessions SET turns=turns+1, model=?,
			   first_ts=COALESCE(MIN(first_ts,?),?), last_ts=MAX(COALESCE(last_ts,0),?)
			 WHERE session_id=?`,
			l.Message.Model, ts, ts, ts, sid); err != nil {
			return err
		}
		for _, b := range decodeBlocks(l.Message.Content) {
			if b.Type != "tool_use" {
				continue
			}
			if _, err := tx.Exec(
				`INSERT INTO tool_calls(session_id,ts,tool,tool_use_id,is_error,input_hash) VALUES(?,?,?,?,0,?)`,
				sid, ts, coalesce(b.Name, "?"), b.ID, hashInput(b.Input)); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE sessions SET tool_calls=tool_calls+1 WHERE session_id=?`, sid); err != nil {
				return err
			}
		}
	case "user":
		for _, b := range decodeBlocks(l.Message.Content) {
			if b.Type != "tool_result" || !b.IsError || b.ToolUseID == "" {
				continue
			}
			res, err := tx.Exec(
				`UPDATE tool_calls SET is_error=1 WHERE is_error=0 AND tool_use_id=?`,
				b.ToolUseID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if _, err := tx.Exec(`UPDATE sessions SET tool_errors=tool_errors+? WHERE session_id=?`, n, sid); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// decodeBlocks parses a content array into blocks; a string (plain text) or any
// non-array yields none — so text bodies never enter the store.
func decodeBlocks(raw json.RawMessage) []block {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return nil
	}
	return bs
}

// reconcileRuns records night-run lifecycle from the on-disk jobs + reports. It
// stays correlation-free: report presence is the only completion evidence the
// ingester needs; detect.go decides outcomes.
func reconcileRuns(db *sql.DB) {
	for _, agent := range companies {
		for _, j := range loadJobs(agent) {
			if !j.Started {
				continue
			}
			report := 0
			if _, err := os.Stat(filepath.Join(reportsDir(agent), j.ID+".md")); err == nil {
				report = 1
			}
			_, err := db.Exec(
				`INSERT INTO runs(agent,run_id,kind,label,started,report)
				 VALUES(?,?,?,?,?,?)
				 ON CONFLICT(agent,run_id) DO UPDATE SET report=excluded.report, label=excluded.label`,
				agent, j.ID, j.Kind, j.Label, j.At.Unix(), report)
			if err != nil {
				logf("obs: run upsert %s/%s: %v", agent, j.ID, err)
			}
		}
	}
}

// ---- small helpers -----------------------------------------------------------

func trimExt(name string) string {
	if i := len(name) - len(".jsonl"); i > 0 && name[i:] == ".jsonl" {
		return name[:i]
	}
	return name
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
