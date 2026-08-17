package control

import (
	"net/http/httptest"
	"nightshift/control/internal/access"
	"strings"
	"testing"
)

func TestDevRCSessionLifecycle(t *testing.T) {
	devMu.Lock()
	devSessions = map[string]bool{}
	devMu.Unlock()

	// start two sessions for agent-a, one for a foreign company
	devRC("sudo", "wrap", "start", "agent-a")
	devRC("sudo", "wrap", "start", "agent-a__cleanup")
	devRC("sudo", "wrap", "start", "agent-b")

	out, _ := devRC("sudo", "wrap", "sessions", "agent-a")
	lines := strings.Fields(strings.ReplaceAll(strings.TrimSpace(out), "\n", " "))
	// expect "agent-a active agent-a__cleanup active" in some order -> 4 fields
	if len(lines) != 4 || !strings.Contains(out, "agent-a active") || !strings.Contains(out, "agent-a__cleanup active") {
		t.Fatalf("sessions listing wrong: %q", out)
	}
	if strings.Contains(out, "agent-b") {
		t.Fatalf("foreign company leaked into agent-a listing: %q", out)
	}

	// ttl of an active session is a positive unix time
	if ttl, _ := devRC("sudo", "wrap", "ttl", "agent-a"); ttl == "0" {
		t.Fatalf("active session ttl should be > 0, got %q", ttl)
	}

	// stop removes it
	devRC("sudo", "wrap", "stop", "agent-a__cleanup")
	out, _ = devRC("sudo", "wrap", "sessions", "agent-a")
	if strings.Contains(out, "cleanup") {
		t.Fatalf("stopped session still listed: %q", out)
	}
	if ttl, _ := devRC("sudo", "wrap", "ttl", "agent-a__cleanup"); ttl != "0" {
		t.Fatalf("stopped session ttl should be 0, got %q", ttl)
	}
}

func TestVerifyAccessDevMode(t *testing.T) {
	verifier := access.New(access.Config{DevMode: true, DevEmail: devEmail})

	// no header → default dev email (works behind the Vite proxy)
	got, err := verifier.VerifyRequest(httptest.NewRequest("GET", "/api/status", nil))
	if err != nil || got != devEmail {
		t.Fatalf("dev no-header: got %q,%v want %q,nil", got, err, devEmail)
	}

	// explicit header is honored
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.Header.Set("Cf-Access-Authenticated-User-Email", "someone@x.com")
	if got, _ := verifier.VerifyRequest(r); got != "someone@x.com" {
		t.Fatalf("dev with header: got %q, want someone@x.com", got)
	}
}

func TestSeedDevDataPopulatesUI(t *testing.T) {
	prevHome, prevCompanies := home, companies
	t.Cleanup(func() { home, companies = prevHome, prevCompanies })
	home = t.TempDir()
	companies = []string{"playground"}

	seedDevData()

	tg := loadTickets("playground")
	if len(tg) == 0 {
		t.Fatal("expected seeded tickets for playground")
	}
	jobs := loadJobs("playground")
	if len(jobs) == 0 {
		t.Fatal("expected a seeded job for playground")
	}

	// second call is a no-op (nsDir already exists) — no panic, no duplication
	seedDevData()
}
