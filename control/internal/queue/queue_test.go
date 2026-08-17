package queue

import (
	"testing"
	"time"

	"nightshift/control/internal/run"
)

func TestAdmitCap(t *testing.T) {
	j := run.Job{ID: "j", Cap: 1, CapGroup: "g"}
	cases := []struct {
		name    string
		boxCap  int
		load    Load
		job     run.Job
		wantOK  bool
		wantSub string
	}{
		{"empty box admits", 0, Load{}, j, true, ""},
		{"box cap reached holds", 1, Load{Total: 1}, run.Job{ID: "j"}, false, "box cap 1"},
		{"box cap has room", 2, Load{Total: 1}, run.Job{ID: "j"}, true, ""},
		{"automation cap reached holds", 0, Load{ByGroup: map[string]int{"g": 1}}, j, false, "automation cap 1"},
		{"automation cap has room", 0, Load{ByGroup: map[string]int{"g": 0}}, j, true, ""},
		{"uncapped job ignores group", 0, Load{ByGroup: map[string]int{"g": 9}}, run.Job{ID: "j"}, true, ""},
	}
	for _, c := range cases {
		reason, ok := AdmitCap(c.boxCap, c.load, c.job)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v (reason %q)", c.name, ok, c.wantOK, reason)
		}
		if c.wantSub != "" && !contains(reason, c.wantSub) {
			t.Errorf("%s: reason = %q, want substring %q", c.name, reason, c.wantSub)
		}
	}
}

func TestExecutorHoldClaude(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fresh := now.Add(-2 * time.Minute).Unix()
	stale := now.Add(-2 * time.Hour).Unix()
	j := run.Job{ID: "j", Executor: run.ExecutorClaude}

	if reason, held := ExecutorHold(Health{Now: now, ClaudeOK: true, ClaudeCheckedAt: fresh}, j); held {
		t.Fatalf("healthy claude must not hold (%q)", reason)
	}
	reason, held := ExecutorHold(Health{Now: now, ClaudeOK: false, ClaudeDetail: "Not logged in", ClaudeCheckedAt: fresh}, j)
	if !held || !contains(reason, "claude auth down") || !contains(reason, "Not logged in") {
		t.Fatalf("fresh dead claude must hold with detail, got held=%v reason=%q", held, reason)
	}
	// Stale verdict fails OPEN — the launcher pre-flight is the real gate.
	if _, held := ExecutorHold(Health{Now: now, ClaudeOK: false, ClaudeCheckedAt: stale}, j); held {
		t.Fatal("stale dead verdict must fail open, not hold")
	}
	// Never checked (checkedAt 0) also fails open.
	if _, held := ExecutorHold(Health{Now: now, ClaudeOK: false, ClaudeCheckedAt: 0}, j); held {
		t.Fatal("un-probed auth must fail open")
	}
}

func TestExecutorHoldLimitHit(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	j := run.Job{ID: "j", Executor: run.ExecutorClaude}
	reason, held := ExecutorHold(Health{Now: now, ClaudeOK: true, ClaudeCheckedAt: now.Unix(), LimitHit: true, LimitHitLabel: "03:00"}, j)
	if !held || !contains(reason, "limit-hit at 03:00") {
		t.Fatalf("open limit-hit must pause the queue, got held=%v reason=%q", held, reason)
	}
}

func TestExecutorHoldCodexIsolated(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fresh := now.Add(-2 * time.Minute).Unix()
	codex := run.Job{ID: "j", Executor: run.ExecutorCodex}

	// A dead CLAUDE verdict must NOT hold a codex job — different engine.
	if _, held := ExecutorHold(Health{Now: now, ClaudeOK: false, ClaudeCheckedAt: fresh, CodexKnown: true, CodexOK: true, CodexCheckedAt: fresh}, codex); held {
		t.Fatal("codex job must not be held by claude auth state")
	}
	// A limit-hit is a Claude-limit event — it must not pause codex either.
	if _, held := ExecutorHold(Health{Now: now, LimitHit: true, LimitHitLabel: "03:00", CodexKnown: true, CodexOK: true, CodexCheckedAt: fresh}, codex); held {
		t.Fatal("claude limit-hit must not pause a codex job")
	}
	// A fresh dead codex verdict holds.
	reason, held := ExecutorHold(Health{Now: now, CodexKnown: true, CodexOK: false, CodexCheckedAt: fresh}, codex)
	if !held || !contains(reason, "codex auth down") {
		t.Fatalf("fresh dead codex must hold, got held=%v reason=%q", held, reason)
	}
	// Codex never probed (unknown) fails open.
	if _, held := ExecutorHold(Health{Now: now, CodexKnown: false}, codex); held {
		t.Fatal("un-probed codex must fail open")
	}
}

func TestAbsoluteDeadlineMinutes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	in := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	cases := []struct {
		name    string
		finish  *time.Time
		minutes int
		wantM   int
		wantOK  bool
	}{
		{"no deadline keeps window", nil, 120, 120, true},
		{"deadline trims window", in(60 * time.Minute), 120, 60, true},
		{"deadline looser than window keeps it", in(300 * time.Minute), 120, 120, true},
		{"closed window skips loudly", in(5 * time.Minute), 120, 0, false},
	}
	for _, c := range cases {
		m, ok := AbsoluteDeadlineMinutes(c.finish, c.minutes, now, 10)
		if m != c.wantM || ok != c.wantOK {
			t.Errorf("%s: (%d,%v), want (%d,%v)", c.name, m, ok, c.wantM, c.wantOK)
		}
	}
}

func TestStarved(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if Starved(run.Job{At: now.Add(-10 * time.Minute)}, now) {
		t.Fatal("a 10m wait is normal backpressure, not starvation")
	}
	if !Starved(run.Job{At: now.Add(-StarvationAfter - time.Minute)}, now) {
		t.Fatal("a wait past the threshold must starve")
	}
}

func TestWatchdogDue(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	young := now.Add(-5 * time.Minute)
	old := now.Add(-30 * time.Minute)
	if WatchdogDue(young, time.Time{}, now) {
		t.Fatal("a young run must not be probed")
	}
	if !WatchdogDue(old, time.Time{}, now) {
		t.Fatal("an old, never-probed run is due")
	}
	if WatchdogDue(old, now.Add(-1*time.Minute), now) {
		t.Fatal("recently probed run must respect the interval")
	}
	if !WatchdogDue(old, now.Add(-6*time.Minute), now) {
		t.Fatal("a run past the probe interval is due again")
	}
}

func TestReleaseDead(t *testing.T) {
	if ReleaseDead("active", false) {
		t.Fatal("an active unit is alive — never release")
	}
	if ReleaseDead("inactive", true) {
		t.Fatal("a probe error is inconclusive — never release")
	}
	if !ReleaseDead("inactive", false) {
		t.Fatal("a confirmed dead unit must release its slot")
	}
	if !ReleaseDead("failed", false) {
		t.Fatal("a failed unit must release its slot")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
