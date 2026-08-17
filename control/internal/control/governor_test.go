package control

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// govEnv opens a fresh obs store in a temp HOME, wires it as the package obsDB,
// silences logs, and drops the exec preamble the governor top-up reads. Returns
// the db. Mirrors obsEnv + nightrunEnv conventions.
func govEnv(t *testing.T, agents ...string) *sql.DB {
	t.Helper()
	db := obsEnv(t, agents...)
	obsDB = db
	t.Cleanup(func() { obsDB = nil })
	prevLogf := logf
	logf = func(string, ...any) {}
	t.Cleanup(func() { logf = prevLogf })
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nsDir(), "sweep", "playground-exec.md"), []byte("EXEC-PREAMBLE"), 0o644); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedTurn inserts one usage-bearing turn (and its session) at ts. cacheW maps to
// the turns.cache_create column, cacheR to cache_read (the cost-model arg order).
func seedTurn(t *testing.T, db *sql.DB, session, agent string, ts int64, model string, in, out, cacheW, cacheR int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO sessions(session_id,agent) VALUES(?,?)`, session, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO turns(session_id,ts,model,in_tokens,out_tokens,cache_read,cache_create) VALUES(?,?,?,?,?,?,?)`,
		session, ts, model, in, out, cacheR, cacheW); err != nil {
		t.Fatal(err)
	}
}

// seedAPIErr inserts one api-error turn (zero usage, api_error=1) at ts.
func seedAPIErr(t *testing.T, db *sql.DB, session, agent string, ts int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO sessions(session_id,agent) VALUES(?,?)`, session, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO turns(session_id,ts,model,api_error) VALUES(?,?,'<synthetic>',1)`, session, ts); err != nil {
		t.Fatal(err)
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// ---- cost model --------------------------------------------------------------

func TestUsageCostUSD(t *testing.T) {
	// The 2026-07-06 window1 baseline: Opus in=300k out=1.31M cacheW=3.9M
	// cacheR=109M ≈ $339 API-equivalent. Exact: 4.5+98.25+73.125+163.5 = 339.375.
	if got := usageCostUSD(300_000, 1_310_000, 3_900_000, 109_000_000, "claude-opus-4-8"); !approx(got, 339.375) {
		t.Fatalf("opus window1 = %.3f, want 339.375", got)
	}
	// haiku rates $1/$5/$1.25/$0.10 per M
	if got := usageCostUSD(1_000_000, 1_000_000, 1_000_000, 1_000_000, "claude-haiku-4"); !approx(got, 7.35) {
		t.Fatalf("haiku = %.3f, want 7.35", got)
	}
	// unknown model bills at Opus rates (conservative): 1M input * $15/M = $15
	if got := usageCostUSD(1_000_000, 0, 0, 0, "some-future-model"); !approx(got, 15.0) {
		t.Fatalf("unknown-model input = %.3f, want 15.0 (opus rate)", got)
	}
	if got := usageCostUSD(0, 0, 0, 0, ""); got != 0 {
		t.Fatalf("zero usage = %.3f, want 0", got)
	}
}

// CostRatesPerMTok is the single source of truth for the cost model: ratesFor
// (and through it usageCostUSD, the spend queries, /api/obs) must price exactly
// off the table. A reintroduced divergent copy in either path fails here.
func TestCostTableIsSingleSource(t *testing.T) {
	for model, key := range map[string]string{
		"claude-haiku-4-5":  "haiku",
		"claude-opus-4-8":   "opus",
		"some-future-model": "opus", // unknown bills at opus, conservatively
	} {
		want := CostRatesPerMTok[key]
		got := ratesFor(model)
		if !approx(got.in*1e6, want.in) || !approx(got.out*1e6, want.out) ||
			!approx(got.cacheW*1e6, want.cacheW) || !approx(got.cacheR*1e6, want.cacheR) {
			t.Fatalf("ratesFor(%q) diverged from CostRatesPerMTok[%q]: %+v vs %+v", model, key, got, want)
		}
		// 1M tokens of each class must price to exactly the table row's sum.
		sum := want.in + want.out + want.cacheW + want.cacheR
		if got := usageCostUSD(1_000_000, 1_000_000, 1_000_000, 1_000_000, model); !approx(got, sum) {
			t.Fatalf("usageCostUSD(%q) = %.4f, want %.4f (from CostRatesPerMTok)", model, got, sum)
		}
	}
}

// nightSpendUSD sums only turns at/after the Bishkek 22:30 anchor of the night.
func TestNightSpendWindowing(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "03:00")
	ns := nightStart(now)
	if want := bishD(t, "2026-06-13", "22:30"); !ns.Equal(want) {
		t.Fatalf("nightStart(%v) = %v, want %v", now, ns, want)
	}

	// out=1M @ $75/M = $75 each. Two in-window, one before the anchor.
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "22:00").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0) // before -> excluded
	seedTurn(t, db, "s2", "playground", bishD(t, "2026-06-13", "23:00").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0) // in
	seedTurn(t, db, "s3", "playground", bishD(t, "2026-06-14", "02:00").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0) // in
	// a different agent's in-window turn must not count toward playground
	seedTurn(t, db, "n1", "agent-a", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)

	got, err := nightSpendUSD(db, "playground", ns)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(got, 150.0) {
		t.Fatalf("night spend = %.3f, want 150.0 (two in-window opus turns)", got)
	}
}

// The control plane's own 10-min haiku auth probe must contribute NOTHING to
// spend: a dead night with only probe turns has to read $0 so the governor's
// and ratchet's zero-spend guards (hold, don't mint / don't bank) still fire.
func TestNightSpendExcludesHaikuProbes(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "03:00")
	ns := nightStart(now)

	// a night of nothing but probe turns -> $0
	for i := range 6 {
		seedTurn(t, db, fmt.Sprintf("probe%d", i), "playground",
			bishD(t, "2026-06-13", "23:00").Add(time.Duration(i)*10*time.Minute).Unix(),
			"claude-haiku-4-5-20251001", 1_000_000, 1_000_000, 0, 0)
	}
	got, err := nightSpendUSD(db, "playground", ns)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("haiku-only night spend = %.3f, want 0 (probes excluded)", got)
	}

	// mixed night: only the opus turn counts
	seedTurn(t, db, "real", "playground", bishD(t, "2026-06-14", "01:00").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)
	if got, _ := nightSpendUSD(db, "playground", ns); !approx(got, 75.0) {
		t.Fatalf("mixed night spend = %.3f, want 75.0 (opus only)", got)
	}
}

// weekSpendUSD has the same probe blind spot — haiku is excluded there too.
func TestWeekSpendExcludesHaikuProbes(t *testing.T) {
	db := govEnv(t, "playground")
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	seedTurn(t, db, "probe", "playground", now-86400, "claude-haiku-4-5", 1_000_000, 1_000_000, 0, 0)
	seedTurn(t, db, "real", "playground", now-86400, "claude-opus-4-8", 0, 1_000_000, 0, 0)
	if got, _ := weekSpendUSD(db); !approx(got, 75.0) {
		t.Fatalf("week spend = %.3f, want 75.0 (haiku excluded)", got)
	}
}

func TestWeekSpendUSD(t *testing.T) {
	db := govEnv(t, "playground", "agent-a")
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC).Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	seedTurn(t, db, "s1", "playground", now-2*86400, "claude-opus-4-8", 0, 1_000_000, 0, 0) // $75, in window
	seedTurn(t, db, "s2", "agent-a", now-3*86400, "claude-opus-4-8", 0, 1_000_000, 0, 0)    // $75, in window, other agent counts
	seedTurn(t, db, "s3", "playground", now-9*86400, "claude-opus-4-8", 0, 1_000_000, 0, 0) // 9d ago -> excluded

	got, err := weekSpendUSD(db)
	if err != nil {
		t.Fatal(err)
	}
	if !approx(got, 150.0) {
		t.Fatalf("week spend = %.3f, want 150.0 (all agents, trailing 7d)", got)
	}
}

// ---- checkpoints -------------------------------------------------------------

// countTopUps counts governor top-up jobs on disk for playground.
func countTopUps(t *testing.T) int {
	t.Helper()
	n := 0
	for _, j := range loadJobs("playground") {
		if j.Label == govTopUpLabel {
			n++
		}
	}
	return n
}

// under pace + room -> exactly one top-up, with exec-slot fields and a stop
// clamped to the 07:45 deadline.
func TestGovernorMintsOneTopUpUnderPace(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "03:05")
	// $500 spent; 03:00 pace target = 0.55 * 1800 = $990 -> under pace.
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 6_666_666, 0, 0) // ≈ $500

	cfg := loadConfig()
	dirty := false
	runGovernor(now, &cfg, &dirty)

	if !dirty {
		t.Fatal("checkpoint should mark cfg dirty")
	}
	if n := countTopUps(t); n != 1 {
		t.Fatalf("top-ups minted = %d, want 1", n)
	}
	var j *job
	for _, x := range loadJobs("playground") {
		if x.Label == govTopUpLabel {
			jj := x
			j = &jj
		}
	}
	if j.Model != opusModel || j.Effort != "xhigh" || j.Kind != "sweep" ||
		!strings.HasPrefix(j.Prompt, "EXEC-PREAMBLE") || strings.Contains(j.Prompt, "## Your ticket") {
		t.Fatalf("top-up job malformed: %+v", j)
	}
	want, ok := minutesUntilToday(now, 7, 45)
	if !ok || j.Minutes != want || want != 280 {
		t.Fatalf("top-up minutes = %d, want %d (07:45 deadline, ok=%v)", j.Minutes, want, ok)
	}
	if cfg.LastSweep["gov/playground/0300"] != "2026-06-14" {
		t.Fatalf("checkpoint not deduped: %+v", cfg.LastSweep)
	}
}

// on pace, over budget, and already-2-minted all mint nothing.
func TestGovernorHoldsWhenNotWarranted(t *testing.T) {
	cases := []struct {
		name       string
		spendOut   int64 // opus output tokens -> $75/M
		pre        int   // pre-existing top-ups tonight
		wantMinted int
	}{
		{"on-pace", 16_000_000, 0, 0},     // ≈ $1200 > 0.55*1800 target, < budget
		{"over-budget", 30_000_000, 0, 0}, // ≈ $2250 >= 1800
		{"already-two", 6_000_000, 2, 0},  // ≈ $450 under pace but 2 already minted
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := govEnv(t, "playground")
			now := bishD(t, "2026-06-14", "03:05")
			seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, tc.spendOut, 0, 0)
			for i := 0; i < tc.pre; i++ {
				if err := saveJob(job{ID: newRunID("exec", now.Add(time.Duration(i)*time.Minute)), Agent: "playground",
					Prompt: "p", Label: govTopUpLabel, Kind: "sweep", At: now, Created: now}); err != nil {
					t.Fatal(err)
				}
			}
			cfg := loadConfig()
			dirty := false
			runGovernor(now, &cfg, &dirty)
			if got := countTopUps(t) - tc.pre; got != tc.wantMinted {
				t.Fatalf("minted %d, want %d", got, tc.wantMinted)
			}
		})
	}
}

// the checkpoint fires once: a second tick within the same day mints nothing more.
func TestGovernorDedupAcrossTicks(t *testing.T) {
	db := govEnv(t, "playground")
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0) // $75, under pace
	cfg := loadConfig()
	dirty := false
	runGovernor(bishD(t, "2026-06-14", "03:05"), &cfg, &dirty)
	runGovernor(bishD(t, "2026-06-14", "03:06"), &cfg, &dirty)
	if n := countTopUps(t); n != 1 {
		t.Fatalf("top-ups after two ticks = %d, want 1 (deduped)", n)
	}
	// While the 03:00 top-up is still QUEUED (unstarted), the 05:00 checkpoint
	// holds — the ADR-0019 inversion: never lengthen a queue that already holds
	// work.
	runGovernor(bishD(t, "2026-06-14", "05:05"), &cfg, &dirty)
	if n := countTopUps(t); n != 1 {
		t.Fatalf("top-ups with a queued top-up pending = %d, want 1 (queue-backlog hold)", n)
	}
	// Once the queue drained it (the real timeline: launched within minutes),
	// replaying the 05:00 checkpoint mints its own, distinct top-up.
	markTopUpsStarted(t)
	delete(cfg.LastSweep, "gov/playground/0500")
	delete(mintedMem, "gov/playground/0500")
	runGovernor(bishD(t, "2026-06-14", "05:06"), &cfg, &dirty)
	if n := countTopUps(t); n != 2 {
		t.Fatalf("top-ups after 05:00 with a drained queue = %d, want 2", n)
	}
	// but a third under-pace checkpoint would be blocked by govTopUpsMax anyway
}

// markTopUpsStarted flips every persisted playground job to Started so the
// governor's queue-backlog guard sees a drained queue.
func markTopUpsStarted(t *testing.T) {
	t.Helper()
	for _, j := range loadJobs("playground") {
		if !j.Started {
			j.Started = true
			j.StartedAt = j.At
			if err := saveJob(j); err != nil {
				t.Fatalf("saveJob: %v", err)
			}
		}
	}
}

// governor only runs for playground; a box without it does nothing.
func TestGovernorPlaygroundOnly(t *testing.T) {
	db := govEnv(t, "agent-a")
	seedTurn(t, db, "s1", "agent-a", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 100, 0, 0)
	cfg := loadConfig()
	dirty := false
	runGovernor(bishD(t, "2026-06-14", "03:05"), &cfg, &dirty)
	if dirty {
		t.Fatal("governor acted with no playground agent")
	}
}

// ---- ratchet -----------------------------------------------------------------

// a limit-hit night ratchets the budget down to 0.8 × spend-at-hit.
func TestRatchetDownToSpendAtHit(t *testing.T) {
	govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "08:55")
	night := nightDate(now)
	cfg := loadConfig()
	cfg.Ratchet.LastHitNight = night
	cfg.Ratchet.SpendAtHit = 1000
	cfg.Ratchet.QuietStreak = 2 // must be cleared by a down-ratchet
	dirty := false
	runRatchet(now, &cfg, &dirty)

	if !approx(cfg.NightBudgetUSD, 800) {
		t.Fatalf("budget after limit-hit = %.1f, want 800 (0.8×1000)", cfg.NightBudgetUSD)
	}
	if cfg.Ratchet.QuietStreak != 0 {
		t.Fatalf("quiet streak not reset after down-ratchet: %d", cfg.Ratchet.QuietStreak)
	}
	if cfg.Ratchet.LastAdjust != now.In(bishkek).Format("2006-01-02") {
		t.Fatalf("LastAdjust = %q", cfg.Ratchet.LastAdjust)
	}
}

// the down-ratchet is floored at $400 (basis above the suspect fraction so the
// full 0.8× formula applies: 300 >= 0.25×460).
func TestRatchetDownFloor(t *testing.T) {
	govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "08:55")
	cfg := loadConfig()
	cfg.NightBudgetUSD = 460
	cfg.Ratchet.LastHitNight = nightDate(now)
	cfg.Ratchet.SpendAtHit = 300 // 0.8×300 = 240 -> floored
	dirty := false
	runRatchet(now, &cfg, &dirty)
	if !approx(cfg.NightBudgetUSD, budgetFloorUSD) {
		t.Fatalf("floored budget = %.1f, want %.1f", cfg.NightBudgetUSD, budgetFloorUSD)
	}
}

// a limit-hit recorded at implausibly low spend (an outage blip, a boundary
// artifact) must not crater the budget to the floor — it half-steps instead.
func TestRatchetSuspectBasisHalfSteps(t *testing.T) {
	govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "08:55")
	cfg := loadConfig()
	cfg.Ratchet.LastHitNight = nightDate(now)
	cfg.Ratchet.SpendAtHit = 60 // < 0.25×1800=450 -> suspect
	dirty := false
	runRatchet(now, &cfg, &dirty)
	if !approx(cfg.NightBudgetUSD, 900) { // 0.5×1800, NOT clamp(0.8×60)=400
		t.Fatalf("suspect-basis budget = %.1f, want 900 (half-step)", cfg.NightBudgetUSD)
	}
	if cfg.Ratchet.QuietStreak != 0 {
		t.Fatalf("streak should still reset on a suspect hit: %d", cfg.Ratchet.QuietStreak)
	}
}

// three consecutive quiet (under-70%) nights ratchet the budget up ×1.15.
// Each night carries a LITTLE real spend — a $0 night is "didn't happen"
// (auth dead / obs blind) and deliberately banks nothing.
func TestRatchetUpAfterThreeQuietNights(t *testing.T) {
	db := govEnv(t, "playground")
	for i, night := range []string{"2026-06-13", "2026-06-14", "2026-06-15"} {
		// ≈$75/night, far under 70% of $1800
		seedTurn(t, db, fmt.Sprintf("s%d", i), "playground", bishD(t, night, "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)
	}
	cfg := loadConfig()
	dirty := false
	for i, day := range []string{"2026-06-14", "2026-06-15", "2026-06-16"} {
		runRatchet(bishD(t, day, "08:55"), &cfg, &dirty)
		if i < 2 {
			if cfg.NightBudgetUSD != 0 {
				t.Fatalf("night %d ratcheted early: budget=%.1f streak=%d", i, cfg.NightBudgetUSD, cfg.Ratchet.QuietStreak)
			}
			if cfg.Ratchet.QuietStreak != i+1 {
				t.Fatalf("night %d streak = %d, want %d", i, cfg.Ratchet.QuietStreak, i+1)
			}
		}
	}
	if !approx(cfg.NightBudgetUSD, 1800*1.15) {
		t.Fatalf("budget after 3 quiet nights = %.1f, want %.1f", cfg.NightBudgetUSD, 1800*1.15)
	}
	if cfg.Ratchet.QuietStreak != 0 {
		t.Fatalf("streak not reset after up-ratchet: %d", cfg.Ratchet.QuietStreak)
	}
}

// the up-ratchet is capped at $6000.
func TestRatchetUpCap(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "08:55")
	// a little real spend — a $0 night deliberately banks nothing
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)
	cfg := loadConfig()
	cfg.NightBudgetUSD = 5500 // 5500×1.15 = 6325 -> capped
	cfg.Ratchet.QuietStreak = 2
	dirty := false
	runRatchet(now, &cfg, &dirty)
	if !approx(cfg.NightBudgetUSD, budgetCapUSD) {
		t.Fatalf("capped budget = %.1f, want %.1f", cfg.NightBudgetUSD, budgetCapUSD)
	}
}

// a busy night (over 70%) breaks the quiet streak.
func TestRatchetBusyNightResetsStreak(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "08:55")
	// $1500 spent = 83% of the $1800 default -> not quiet.
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 20_000_000, 0, 0)
	cfg := loadConfig()
	cfg.Ratchet.QuietStreak = 2
	dirty := false
	runRatchet(now, &cfg, &dirty)
	if cfg.Ratchet.QuietStreak != 0 {
		t.Fatalf("busy night should reset streak, got %d", cfg.Ratchet.QuietStreak)
	}
	if cfg.NightBudgetUSD != 0 {
		t.Fatalf("budget changed on a busy night: %.1f", cfg.NightBudgetUSD)
	}
}

// ---- limit-hit detector ------------------------------------------------------

// a burst of ≥5 api-error turns in 10 minutes raises a limit-hit alert whose
// detail carries the night spend, and persists spend-at-hit into the ratchet.
func TestDetectLimitHitBurstAndSpend(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "04:00").Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	// a real turn earlier in the night: out=1M @ $75/M -> $75 night spend.
	seedTurn(t, db, "real", "playground", bishD(t, "2026-06-13", "23:00").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)
	// 5 api-error turns inside a 10-minute span, all within the trailing 2h.
	base := now - 20*60
	for i := range 5 {
		seedAPIErr(t, db, "sess", "playground", base+int64(i)*90) // 0,90,...,360s -> < 600s span
	}

	runDetectors(db)

	var detail string
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind='limit-hit' AND cleared=0`).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 limit-hit alert, got %d", n)
	}
	db.QueryRow(`SELECT detail FROM alerts WHERE kind='limit-hit'`).Scan(&detail)
	if !strings.Contains(detail, "5 errors in 10m") || !strings.Contains(detail, "$75") || !strings.Contains(detail, "$1800") {
		t.Fatalf("limit-hit detail = %q, want burst count + spend $75 + budget $1800", detail)
	}
	// spend-at-hit persisted for the morning down-ratchet (no parsing).
	cfg := loadConfig()
	if cfg.Ratchet.LastHitNight != nightDate(time.Unix(now, 0)) || !approx(cfg.Ratchet.SpendAtHit, 75) {
		t.Fatalf("ratchet not updated: night=%q spendAtHit=%.1f", cfg.Ratchet.LastHitNight, cfg.Ratchet.SpendAtHit)
	}
}

// four api-errors, or five spread beyond 10 minutes, are below threshold.
func TestDetectLimitHitBelowThreshold(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "04:00").Unix()
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	// 4 tightly-clustered errors: below the count threshold.
	for i := range 4 {
		seedAPIErr(t, db, "s-four", "playground", now-1000+int64(i)*60)
	}
	// 5 errors but spread over 20 minutes: no 10-minute window holds 5.
	for i := range 5 {
		seedAPIErr(t, db, "s-spread", "playground", now-3000+int64(i)*300)
	}
	runDetectors(db)
	if got := openAlertCount(t, db, "limit-hit"); got != 0 {
		t.Fatalf("want 0 limit-hit alerts below threshold, got %d", got)
	}
}

func TestMaxWindowCount(t *testing.T) {
	cases := []struct {
		ts   []int64
		span int64
		want int
	}{
		{nil, 600, 0},
		{[]int64{0}, 600, 1},
		{[]int64{0, 90, 180, 270, 360}, 600, 5},
		{[]int64{0, 300, 600, 900, 1200}, 600, 3}, // 0..600 holds 3
		{[]int64{0, 700, 1400}, 600, 1},
	}
	for _, tc := range cases {
		if got := maxWindowCount(tc.ts, tc.span); got != tc.want {
			t.Fatalf("maxWindowCount(%v,%d) = %d, want %d", tc.ts, tc.span, got, tc.want)
		}
	}
}

// ---- config round-trip -------------------------------------------------------

// TestBudgetViewSurfacesRatchet: the /api/obs budget block exposes the ratchet's
// last-adjust date + measured ceiling so the auto-calibration isn't invisible.
func TestBudgetViewSurfacesRatchet(t *testing.T) {
	db := govEnv(t, "playground")
	cfg := loadConfig()
	cfg.NightBudgetUSD = 1440
	cfg.Ratchet = budgetRatchet{LastAdjust: "2026-07-05", LastHitNight: "2026-07-05", SpendAtHit: 1800}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	b := budgetView(db, time.Now())
	if b.LastAdjust != "2026-07-05" || b.LimitHitNight != "2026-07-05" || b.SpendAtHitUSD != 1800 {
		t.Fatalf("ratchet fields not surfaced: %+v", b)
	}
	if b.NightBudgetUSD != 1440 {
		t.Fatalf("budget = %v, want pinned 1440", b.NightBudgetUSD)
	}
	// A never-adjusted config leaves the fields empty (the UI hides the line).
	if err := saveConfig(nightConfig{NightBudgetUSD: 1800}); err != nil {
		t.Fatal(err)
	}
	if b := budgetView(db, time.Now()); b.LastAdjust != "" || b.LimitHitNight != "" || b.SpendAtHitUSD != 0 {
		t.Fatalf("unadjusted config should leave ratchet fields empty: %+v", b)
	}
}

func TestConfigRoundTripWithGovernorFields(t *testing.T) {
	nightrunEnv(t, "playground")
	in := nightConfig{
		SweepOff:       map[string]bool{"agent-b": true},
		LastSweep:      map[string]string{"gov/playground/0300": "2026-06-14"},
		NightBudgetUSD: 2200,
		Ratchet: budgetRatchet{
			QuietStreak: 2, LastEval: "2026-06-13", LastHitNight: "2026-06-12",
			SpendAtHit: 1234.5, LastAdjust: "2026-06-13",
		},
	}
	if err := saveConfig(in); err != nil {
		t.Fatal(err)
	}
	got := loadConfig()
	if got.NightBudgetUSD != 2200 || got.Ratchet != in.Ratchet {
		t.Fatalf("round-trip lost governor fields: %+v", got)
	}
	if !got.SweepOff["agent-b"] || got.LastSweep["gov/playground/0300"] != "2026-06-14" {
		t.Fatalf("round-trip disturbed existing fields: %+v", got)
	}
	// effectiveBudget: pinned value wins; 0 -> default.
	if effectiveBudget(got) != 2200 {
		t.Fatalf("effectiveBudget pinned = %.0f, want 2200", effectiveBudget(got))
	}
	if effectiveBudget(nightConfig{}) != defaultNightBudgetUSD {
		t.Fatalf("effectiveBudget default = %.0f, want %.0f", effectiveBudget(nightConfig{}), defaultNightBudgetUSD)
	}
}

// sweeps off = the operator's night kill-switch — the governor must not mint
// top-ups into a disabled night ($0 spend reads as maximally under-pace).
func TestGovernorSkipsWhenSweepOff(t *testing.T) {
	db := govEnv(t, "playground")
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0)
	cfg := loadConfig()
	cfg.SweepOff = map[string]bool{"playground": true}
	dirty := false
	runGovernor(bishD(t, "2026-06-14", "03:05"), &cfg, &dirty)
	if n := countTopUps(t); n != 0 {
		t.Fatalf("governor minted %d top-ups on a sweep-off night", n)
	}
}

// $0 night spend is never a pacing signal (obs blind / auth dead) — hold.
func TestGovernorHoldsOnZeroSpend(t *testing.T) {
	govEnv(t, "playground") // no turns seeded -> $0
	cfg := loadConfig()
	dirty := false
	runGovernor(bishD(t, "2026-06-14", "03:05"), &cfg, &dirty)
	if n := countTopUps(t); n != 0 {
		t.Fatalf("governor minted %d top-ups on zero evidence", n)
	}
	if cfg.LastSweep["gov/playground/0300"] != "2026-06-14" {
		t.Fatal("checkpoint should still dedup after a hold")
	}
}

// a checkpoint firing late (post-downtime, inside grace) but past the 07:45
// deadline must mint NOTHING — never roll the deadline to tomorrow.
func TestGovernorNoMintPastDeadline(t *testing.T) {
	db := govEnv(t, "playground")
	seedTurn(t, db, "s1", "playground", bishD(t, "2026-06-13", "23:30").Unix(), "claude-opus-4-8", 0, 1_000_000, 0, 0) // $75, under pace
	cfg := loadConfig()
	dirty := false
	runGovernor(bishD(t, "2026-06-14", "08:30"), &cfg, &dirty) // 05:00 cp inside grace, deadline passed
	if n := countTopUps(t); n != 0 {
		t.Fatalf("governor minted %d top-ups past the 07:45 deadline", n)
	}
}

func TestMinutesUntilToday(t *testing.T) {
	cases := []struct {
		at   string
		want int
		ok   bool
	}{
		{"03:05", 280, true}, // normal overnight checkpoint
		{"07:40", 0, false},  // under the 10m floor -> skip, not floor-pad
		{"08:30", 0, false},  // past the deadline -> skip, never tomorrow
		{"00:00", 465, true}, // midnight -> same-day 07:45
	}
	for _, tc := range cases {
		got, ok := minutesUntilToday(bishD(t, "2026-06-14", tc.at), 7, 45)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("minutesUntilToday(%s) = (%d,%v), want (%d,%v)", tc.at, got, ok, tc.want, tc.ok)
		}
	}
}

// the recorded spend-at-hit keeps the LARGEST hit of the night: an early
// low-spend blip must not mask the real ceiling a later hit measures.
func TestRecordLimitHitKeepsLargest(t *testing.T) {
	govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "01:00")
	recordLimitHit("playground", 60, now)
	recordLimitHit("playground", 900, now.Add(2*time.Hour))
	recordLimitHit("playground", 100, now.Add(3*time.Hour))
	cfg := loadConfig()
	if !approx(cfg.Ratchet.SpendAtHit, 900) {
		t.Fatalf("spend-at-hit = %.0f, want 900 (largest of the night)", cfg.Ratchet.SpendAtHit)
	}
}

// an api-error burst from BEFORE the 22:30 night boundary (evening batch) must
// not be recorded against the new night's near-$0 spend.
func TestDetectLimitHitIgnoresPreNightBurst(t *testing.T) {
	db := govEnv(t, "playground")
	now := bishD(t, "2026-06-14", "22:31").Unix() // 1 min into the new night
	prevNow := nowUnix
	nowUnix = func() int64 { return now }
	t.Cleanup(func() { nowUnix = prevNow })

	base := bishD(t, "2026-06-14", "22:00").Unix() // evening batch, pre-anchor
	for i := range 5 {
		seedAPIErr(t, db, "eve", "playground", base+int64(i)*60)
	}
	runDetectors(db)
	if got := openAlertCount(t, db, "limit-hit"); got != 0 {
		t.Fatalf("pre-night burst raised %d limit-hit alerts, want 0", got)
	}
	if cfg := loadConfig(); cfg.Ratchet.LastHitNight != "" {
		t.Fatalf("pre-night burst recorded a hit: %+v", cfg.Ratchet)
	}
}
