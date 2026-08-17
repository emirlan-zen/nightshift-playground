package control

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// rcFunc answers a single wrapper invocation: verb is the rc verb (start, stop,
// sessions, ttl, stop-run, …), target is what follows (company/instance/id…).
type rcFunc func(verb string, target []string) (string, error)

// rcEnv points the package globals at a temp home with a fake rc wrapper and
// silenced logs, all restored on cleanup so no global leaks between tests. It
// returns the recorded "verb target…" calls in order.
//
// This is the remaining scheduler rcRun test seam. Remote-control sessions now
// exercise their own Controller port in internal/session tests.
func rcEnv(t *testing.T, h rcFunc, agents ...string) *[]string {
	t.Helper()
	prevHome, prevCompanies, prevExec, prevLogf := home, companies, execCommand, logf
	home = t.TempDir()
	companies = agents
	calls := &[]string{}
	execCommand = func(_ string, args ...string) (string, error) {
		// args = [wrapper, verb, target...]; drop the wrapper path.
		if len(args) < 2 {
			return "", nil
		}
		verb, target := args[1], args[2:]
		*calls = append(*calls, strings.TrimSpace(verb+" "+strings.Join(target, " ")))
		if h == nil {
			return "", nil
		}
		return h(verb, target)
	}
	logf = func(string, ...any) {}
	t.Cleanup(func() {
		home, companies, execCommand, logf = prevHome, prevCompanies, prevExec, prevLogf
	})
	return calls
}

func TestHandleRunStop(t *testing.T) {
	id := "20260613-0500-sweep-ab12"
	t.Run("valid id", func(t *testing.T) {
		calls := rcEnv(t, func(string, []string) (string, error) { return "done", nil }, "playground")
		rr := httptest.NewRecorder()
		handleRunStop(rr, httptest.NewRequest("POST", "/api/run/stop?c=playground&id="+id, nil))
		if rr.Code != 200 || !containsCall(*calls, "stop-run playground "+id) {
			t.Fatalf("code %d calls %v", rr.Code, *calls)
		}
	})
	t.Run("bad id 400", func(t *testing.T) {
		rcEnv(t, nil, "playground")
		rr := httptest.NewRecorder()
		handleRunStop(rr, httptest.NewRequest("POST", "/api/run/stop?c=playground&id=../etc", nil))
		if rr.Code != 400 {
			t.Fatalf("code = %d, want 400", rr.Code)
		}
	})
}

// ---- sweeps -----------------------------------------------------------------

func TestHandleSweepsAndToggle(t *testing.T) {
	rcEnv(t, nil, "agent-a", "agent-b")

	// default: every agent's sweep is ON
	rr := httptest.NewRecorder()
	handleSweeps(rr, httptest.NewRequest("GET", "/api/sweeps", nil))
	var on map[string]bool
	_ = json.Unmarshal(rr.Body.Bytes(), &on)
	if !on["agent-a"] || !on["agent-b"] {
		t.Fatalf("sweeps should default ON, got %+v", on)
	}

	// toggle agent-a OFF, persisted through config.json
	rr = httptest.NewRecorder()
	handleSweepToggle(rr, httptest.NewRequest("POST", "/api/sweep?c=agent-a&on=0", nil))
	if rr.Code != 200 {
		t.Fatalf("toggle code %d body %s", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	handleSweeps(rr, httptest.NewRequest("GET", "/api/sweeps", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &on)
	if on["agent-a"] || !on["agent-b"] {
		t.Fatalf("agent-a sweep should be OFF after toggle, got %+v", on)
	}
}

// ---- prompt viewer HTTP layer ----------------------------------------------

func TestHandlePromptHTTP(t *testing.T) {
	rcEnv(t, nil, "agent-a")
	writeFile(t, home+"/.claude/CLAUDE.md", "global rules")

	// unknown id → 400, never reads a path off the wire
	rr := httptest.NewRecorder()
	handlePrompt(rr, httptest.NewRequest("GET", "/api/prompt?id=../etc/passwd", nil))
	if rr.Code != 400 {
		t.Fatalf("bad id code = %d, want 400", rr.Code)
	}

	// allowlisted id → body
	rr = httptest.NewRecorder()
	handlePrompt(rr, httptest.NewRequest("GET", "/api/prompt?id=global", nil))
	if rr.Code != 200 || rr.Body.String() != "global rules" {
		t.Fatalf("code %d body %q", rr.Code, rr.Body.String())
	}

	// prompts index lists the group
	rr = httptest.NewRecorder()
	handlePrompts(rr, httptest.NewRequest("GET", "/api/prompts", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "global") {
		t.Fatalf("prompts index code %d body %s", rr.Code, rr.Body)
	}
}

// ---- SPA handler ------------------------------------------------------------

func TestSpaHandler(t *testing.T) {
	h := spaHandler()

	// unknown client route → index.html (no-store), so a deep-link refresh works
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("GET", "/tickets/playground/tkt_x", nil))
	if rr.Code != 200 || !strings.Contains(strings.ToLower(rr.Body.String()), "<!doctype html") {
		t.Fatalf("client route should serve index html, code %d", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("index Cache-Control = %q, want no-store", cc)
	}
}
