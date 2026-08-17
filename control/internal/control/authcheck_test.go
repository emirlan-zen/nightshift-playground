package control

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// authEnv points the package globals at a temp home, stubs the probe, and
// silences logs. Returns a setter to control the stubbed probe result.
func authEnv(t *testing.T, agents ...string) func(ok bool, detail string) {
	t.Helper()
	home = t.TempDir()
	companies = agents
	prevProbe, prevForge, prevLogf := authProbe, forgeProbe, logf
	authMu.Lock()
	authCur = authHealth{}
	forgeCur = map[string]forgeHealth{}
	authMu.Unlock()
	logf = func(string, ...any) {}
	set := func(ok bool, detail string) {
		authProbe = func() (bool, string) { return ok, detail }
	}
	set(true, "ok")
	forgeProbe = func(company string) (int, string) { return forgeOK, "token ok" }
	t.Cleanup(func() { authProbe, forgeProbe, logf = prevProbe, prevForge, prevLogf })
	return set
}

func writeMarker(t *testing.T, agent, id string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(home, ".nightshift", "reports", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".authfail")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAuthUpdatesState(t *testing.T) {
	set := authEnv(t, "playground")
	set(false, "not authenticated (Not logged in)")
	refreshAuth()
	authMu.RLock()
	cur := authCur
	authMu.RUnlock()
	if cur.OK {
		t.Fatal("expected OK=false")
	}
	if cur.Detail != "not authenticated (Not logged in)" {
		t.Fatalf("detail=%q", cur.Detail)
	}
	if cur.CheckedAt == 0 {
		t.Fatal("CheckedAt should be set")
	}
}

func TestRecentAuthFailures(t *testing.T) {
	authEnv(t, "playground", "agent-a")
	writeMarker(t, "playground", "20260705-2300-sweep-aaaa", 1*time.Hour)  // recent
	writeMarker(t, "playground", "20260704-2300-sweep-bbbb", 48*time.Hour) // too old
	writeMarker(t, "agent-a", "20260705-2320-sweep-cccc", 30*time.Minute)  // recent, newest
	writeMarker(t, "playground", "not a valid id!!", 1*time.Hour)          // bad id, ignored

	got := recentAuthFailures()
	if len(got) != 2 {
		t.Fatalf("want 2 failures, got %d: %+v", len(got), got)
	}
	if got[0].ID != "20260705-2320-sweep-cccc" { // newest first
		t.Fatalf("want newest first, got %s", got[0].ID)
	}
	if got[0].Agent != "agent-a" {
		t.Fatalf("agent=%s", got[0].Agent)
	}
}

func TestHandleHealthShape(t *testing.T) {
	set := authEnv(t, "playground")
	set(false, "not authenticated")
	refreshAuth()
	refreshForge()
	writeMarker(t, "playground", "20260705-2300-sweep-aaaa", 10*time.Minute)

	rr := httptest.NewRecorder()
	handleHealth(rr, httptest.NewRequest("GET", "/api/health", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var resp healthResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Auth.OK {
		t.Fatal("auth should be not-ok")
	}
	if len(resp.RecentFailures) != 1 {
		t.Fatalf("want 1 recent failure, got %d", len(resp.RecentFailures))
	}
	if len(resp.Forge) != 1 || !resp.Forge[0].OK || resp.Forge[0].Company != "playground" {
		t.Fatalf("forge=%+v", resp.Forge)
	}
}

func TestRefreshForgeStatuses(t *testing.T) {
	authEnv(t, "agent-a", "agent-b", "playground", "agent-c")
	codes := map[string]int{
		"agent-a":    forgeOK,
		"agent-b":     forgeDead,
		"playground": forgeNotConf,
		"agent-c":    forgeInconclusive,
	}
	forgeProbe = func(company string) (int, string) { return codes[company], "detail " + company }
	refreshForge()

	rr := httptest.NewRecorder()
	handleHealth(rr, httptest.NewRequest("GET", "/api/health", nil))
	var resp healthResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Forge) != 4 {
		t.Fatalf("want 4 forge entries, got %d", len(resp.Forge))
	}
	want := map[string]bool{
		"agent-a":    true,  // ok
		"agent-b":     false, // dead token -> the ONLY alerting state
		"playground": true,  // not configured -> non-alerting
		"agent-c":    true,  // inconclusive -> non-alerting
	}
	for _, f := range resp.Forge {
		if f.OK != want[f.Company] {
			t.Fatalf("%s: ok=%v want %v", f.Company, f.OK, want[f.Company])
		}
		if f.CheckedAt == 0 {
			t.Fatalf("%s: CheckedAt unset", f.Company)
		}
		if f.Detail != "detail "+f.Company {
			t.Fatalf("%s: detail=%q", f.Company, f.Detail)
		}
	}
	// companies order preserved for a stable render
	if resp.Forge[0].Company != "agent-a" || resp.Forge[3].Company != "agent-c" {
		t.Fatalf("order=%v", resp.Forge)
	}
}

// TestForgeProbeParsesSeamOutput exercises realForgeProbe over the execCommand
// seam: JSON detail extraction, exit-code mapping, and the probe-missing ->
// inconclusive fallback.
func TestForgeProbeParsesSeamOutput(t *testing.T) {
	prevExec := execCommand
	t.Cleanup(func() { execCommand = prevExec })

	t.Run("ok with json detail", func(t *testing.T) {
		execCommand = func(name string, args ...string) (string, error) {
			if name != forgeProbeScript || len(args) != 1 || args[0] != "agent-a" {
				t.Fatalf("unexpected exec: %s %v", name, args)
			}
			return `{"company":"agent-a","ok":true,"detail":"gh user ok"}`, nil
		}
		code, detail := realForgeProbe("agent-a")
		if code != forgeOK || detail != "gh user ok" {
			t.Fatalf("code=%d detail=%q", code, detail)
		}
	})

	t.Run("dead token exit 2", func(t *testing.T) {
		execCommand = func(string, ...string) (string, error) {
			return `{"company":"agent-a","ok":false,"detail":"401 from api.github.com"}`,
				&devExitError{code: 2}
		}
		code, detail := realForgeProbe("agent-a")
		if code != forgeDead || detail != "401 from api.github.com" {
			t.Fatalf("code=%d detail=%q", code, detail)
		}
	})

	t.Run("probe missing is inconclusive", func(t *testing.T) {
		execCommand = func(string, ...string) (string, error) {
			return "", os.ErrNotExist
		}
		code, _ := realForgeProbe("agent-a")
		if code != forgeInconclusive {
			t.Fatalf("code=%d, want inconclusive", code)
		}
	})
}
