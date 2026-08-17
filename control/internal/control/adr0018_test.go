package control

// ADR-0018: codex as the second executor. These tests pin the seam ends the
// launcher depends on — the .executor sidecar contract, the profile/pipeline
// validation, the codex model stamping, and the health snapshot parser.

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveJobExecutorSidecar(t *testing.T) {
	flowEnv(t)
	j := job{ID: "20260710-0645-sweep-c0de", Agent: "playground", Prompt: "p",
		At: time.Now(), Kind: "sweep", Model: codexModel, Effort: "xhigh", Executor: "codex", Minutes: 105}
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(jobsDir("playground"), j.ID+".executor")
	if b, err := os.ReadFile(side); err != nil || string(b) != "codex" {
		t.Fatalf(".executor sidecar = %q err=%v", b, err)
	}
	// Re-saving as claude removes the sidecar — the launcher must never
	// dispatch a claude job through a stale codex sidecar.
	j.Executor = "claude"
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf(".executor sidecar not removed on claude re-save: %v", err)
	}
	j.Executor = "codex"
	if err := saveJob(j); err != nil {
		t.Fatal(err)
	}
	deleteJob("playground", j.ID)
	if _, err := os.Stat(side); !os.IsNotExist(err) {
		t.Fatalf(".executor sidecar survived deleteJob: %v", err)
	}
}

func TestProfileExecutorValidation(t *testing.T) {
	flowEnv(t)
	mk := func(executor string, perTicket bool) []byte {
		doc := `{"name":"n","waves":[{"name":"review","time":"06:45","prompt":"playground-review.md","minutes":105`
		if executor != "" {
			doc += `,"executor":"` + executor + `"`
		}
		if perTicket {
			doc += `,"perTicket":true`
		}
		return []byte(doc + `}]}`)
	}
	if _, err := parseProfile(mk("codex", false)); err != nil {
		t.Fatalf("codex wave rejected: %v", err)
	}
	if _, err := parseProfile(mk("gemini", false)); err == nil || !strings.Contains(err.Error(), "executor") {
		t.Fatalf("unknown executor accepted: %v", err)
	}
	if _, err := parseProfile(mk("codex", true)); err == nil || !strings.Contains(err.Error(), "perTicket") {
		t.Fatalf("codex+perTicket accepted: %v", err)
	}
	// The executor survives the profile -> slots -> profile round-trip the
	// editor and migration rely on.
	p, _ := parseProfile(mk("codex", false))
	slots := profileToSlots("playground", p)
	if len(slots) != 1 || slots[0].executor != "codex" {
		t.Fatalf("profileToSlots executor = %+v", slots)
	}
	back := slotsToProfile("n", slots)
	if back.Waves[0].Executor != "codex" {
		t.Fatalf("slotsToProfile executor = %+v", back.Waves[0])
	}
}

func TestFlowReviewNodeRunsOnCodex(t *testing.T) {
	flowEnv(t)
	now := time.Now()
	f := mintTestFlow(t, "flow-20260710-1200-0018c0de", [][]string{{"implement"}, {"review"}}, nil, now)
	impl, _ := findFlowJob(f, f.Nodes[0].JobID)
	if impl.Executor != "claude" || impl.Model != opusModel {
		t.Fatalf("implement job = executor %q model %q", impl.Executor, impl.Model)
	}
	rev, _ := findFlowJob(f, f.Nodes[1].JobID)
	// The built-in review node runs on codex (ADR-0018) and must carry an
	// OpenAI model id — a codex session handed a claude model id would fail.
	if rev.Executor != "codex" || rev.Model != codexModel || rev.Effort != "xhigh" {
		t.Fatalf("review job = executor %q model %q effort %q", rev.Executor, rev.Model, rev.Effort)
	}
	if b, err := os.ReadFile(filepath.Join(jobsDir(f.Agent), rev.ID+".executor")); err != nil || string(b) != "codex" {
		t.Fatalf("review .executor sidecar = %q err=%v", b, err)
	}
}

func TestCodexHealthStatus(t *testing.T) {
	flowEnv(t)
	if got := codexHealthStatus(); got != nil {
		t.Fatalf("absent file should be nil, got %+v", got)
	}
	dir := filepath.Join(nsDir(), "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	doc := `{"ok":false,"detail":"not authenticated (refresh_token_reused)","checkedAt":"` + at.Format(time.RFC3339) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "codex.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	got := codexHealthStatus()
	if got == nil || got.OK || !strings.Contains(got.Detail, "refresh_token_reused") || got.CheckedAt != at.Unix() {
		t.Fatalf("codex health = %+v", got)
	}
	// Garbage must read as "never probed", not a false verdict either way.
	if err := os.WriteFile(filepath.Join(dir, "codex.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := codexHealthStatus(); got != nil {
		t.Fatalf("garbage file should be nil, got %+v", got)
	}
}

func TestJobCreateAcceptsExecutor(t *testing.T) {
	flowEnv(t)
	post := func(body string) int {
		rr := httptest.NewRecorder()
		handleJobCreate(rr, httptest.NewRequest("POST", "/api/job", strings.NewReader(body)))
		return rr.Code
	}
	if code := post(`{"agent":"playground","prompt":"p","at":"2026-07-11T13:00","executor":"codex"}`); code != 200 {
		t.Fatalf("codex deferred run = %d", code)
	}
	if code := post(`{"agent":"playground","prompt":"p","at":"2026-07-11T13:05","executor":"gemini"}`); code != 400 {
		t.Fatalf("unknown executor = %d", code)
	}
	// The codex deferred run got the flagship model stamped (config.toml's
	// default is the cheap delegate tier — a phone-queued codex run must not
	// silently land there).
	for _, j := range loadJobs("playground") {
		if j.Executor == "codex" && j.Model != codexModel {
			t.Fatalf("codex deferred run model = %q", j.Model)
		}
	}
}
