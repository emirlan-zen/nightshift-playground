// Usage governor (night-spend pacing + auto-calibrating budget). The night
// pipeline runs on a Claude Max 20x subscription; the operator wants the nights
// to actually CONSUME the plan — burn ~≥80% of the (unknown, discovered-by-doing)
// limits — while self-correcting so a bad night can't overrun into the morning
// window or leave the plan half-idle. This file owns three loops, all Opus/cost
// modelled in API-equivalent dollars off the observability store (metadata only —
// the same numbers usage.go already reads, never message content):
//
//  1. a cost model over the ingested turns (usageCostUSD / nightSpendUSD / weekSpendUSD);
//  2. governor CHECKPOINTS (03:00 + 05:00 Bishkek, playground only): if the
//     night is behind a pace target and we haven't topped up twice already, mint
//     ONE extra self-directed exec worker to spend the slack;
//  3. an auto-RATCHET (once per morning): a night that hit an API limit ratchets
//     the budget DOWN to 0.8× the spend-at-hit; three consecutive quiet nights
//     that finished under 70% ratchet it UP 1.15× (capped/floored).
//
// The checkpoints and ratchet run INSIDE schedTick, which already holds nsMu, so
// the functions here that touch ~/.nightshift file state assume the lock is held.
// budgetView (read path) and recordLimitHit (detector goroutine) take nsMu
// themselves — they're the two callers NOT under schedTick.
package control

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// effectiveBudget default when the operator hasn't pinned one. Sized against
	// the 2026-07-06 baseline (window1 ≈ $339 + window2 ≈ $66 + evening batch
	// ≈ $491): a full night in API-equivalent dollars sits comfortably under this,
	// leaving the checkpoints headroom to push toward the real plan ceiling.
	defaultNightBudgetUSD = 1800.0
	budgetFloorUSD        = 400.0  // never ratchet below this — a night still needs to do work
	budgetCapUSD          = 6000.0 // never ratchet above this — a runaway can't self-authorise infinity

	ratchetDownFactor    = 0.8  // limit-hit night -> 0.8 × spend-at-hit
	ratchetUpFactor      = 1.15 // 3 quiet nights -> ×1.15
	ratchetQuietNights   = 3    // consecutive under-target nights before ratcheting up
	ratchetQuietFraction = 0.70 // "under budget" = finished below 70% of budget

	// A limit-hit burst: this many API-error turns inside a 10-minute span for one
	// agent, seen in the trailing 2h. Five separate errors clustered tight is a
	// rate/usage limit being enforced, not one transient blip.
	limitHitBurst    = 5
	limitHitSpanSec  = 600
	limitHitLookback = 2 * time.Hour

	govTopUpsMax  = 2 // at most two governor top-ups per night (one per checkpoint)
	governorGrace = 4 * time.Hour
	govTopUpLabel = "exec · governor top-up"

	// A limit-hit recorded below this fraction of the budget is a suspect basis
	// for the down-ratchet (transient outage burst, night-boundary artifact) —
	// the ratchet half-steps instead of applying 0.8× spend-at-hit.
	suspectHitFraction = 0.25

	// exec must be dead by 07:45 Bishkek so nothing burns Opus into the operator's
	// 09:00 window — a governor top-up's auto-stop is clamped to the time remaining.
	execDeadlineHour   = 7
	execDeadlineMinute = 45
	runMinutesFloor    = 10
	runMinutesCeil     = 480
)

// budgetRatchet is the small persisted state the auto-calibration needs: how many
// consecutive quiet nights we've banked toward an up-ratchet, the last night we
// evaluated (daily dedup), the last night a limit-hit fired + the spend at that
// moment (the down-ratchet basis, persisted by the detector so nothing has to be
// parsed back out of an alert string), and when we last moved the budget.
type budgetRatchet struct {
	QuietStreak  int     `json:"quietStreak"`
	LastEval     string  `json:"lastEval,omitempty"`     // Bishkek night date last ratchet-evaluated
	LastHitNight string  `json:"lastHitNight,omitempty"` // Bishkek night date of the last limit-hit
	SpendAtHit   float64 `json:"spendAtHit,omitempty"`   // night spend when that limit-hit fired
	LastAdjust   string  `json:"lastAdjust,omitempty"`   // Bishkek date the budget last changed
}

// govCheckpoint is one pacing gate: a wall-clock Bishkek time and the fraction of
// the budget the night should have spent by then.
type govCheckpoint struct {
	hour, minute int
	pace         float64
	key          string // LastSweep-style daily dedup key
}

var govCheckpoints = []govCheckpoint{
	{hour: 3, minute: 0, pace: 0.55, key: "gov/playground/0300"},
	{hour: 5, minute: 0, pace: 0.75, key: "gov/playground/0500"},
}

// ratchetCheckpoint fires once each morning after the night is provably over
// (retro's 08:45 deadline has passed), evaluating the just-finished night.
var ratchetCheckpoint = govCheckpoint{hour: 8, minute: 50, key: "gov/playground/ratchet"}

// ---- cost model --------------------------------------------------------------

// modelRates is one in/out/cache-write/cache-read rate quad. CostRatesPerMTok
// holds it per MILLION tokens; ratesFor scales to per-token.
type modelRates struct{ in, out, cacheW, cacheR float64 }

// CostRatesPerMTok is THE per-million-token API-equivalent price table (USD) —
// the single Go source of truth for the cost model. Every Go consumer (ratesFor
// -> usageCostUSD -> the night/week spend queries and the /api/obs budget block)
// derives from it; never inline these numbers elsewhere. The agent-side bash
// copy in files/obs/nightshift-obs mirrors the same values — keep them in sync.
var CostRatesPerMTok = map[string]modelRates{
	"haiku": {in: 1.0, out: 5.0, cacheW: 1.25, cacheR: 0.10},
	"opus":  {in: 15.0, out: 75.0, cacheW: 18.75, cacheR: 1.50},
}

// ratesFor returns the per-token rates for a model. Haiku is cheap; everything
// else — Opus and, deliberately, any UNKNOWN model — is billed at Opus rates so
// the governor never under-counts spend and over-mints.
func ratesFor(model string) modelRates {
	r := CostRatesPerMTok["opus"]
	if strings.Contains(strings.ToLower(model), "haiku") {
		r = CostRatesPerMTok["haiku"]
	}
	return modelRates{in: r.in / 1e6, out: r.out / 1e6, cacheW: r.cacheW / 1e6, cacheR: r.cacheR / 1e6}
}

// usageCostUSD is the API-equivalent dollar cost of one turn's token usage.
func usageCostUSD(in, out, cacheW, cacheR int64, model string) float64 {
	r := ratesFor(model)
	return float64(in)*r.in + float64(out)*r.out + float64(cacheW)*r.cacheW + float64(cacheR)*r.cacheR
}

// nightSpendUSD sums an agent's turn costs since nightStart (Bishkek 22:30 of the
// night in question). Turns join to sessions for the agent scope; api-error
// synthetic turns carry zero usage and so contribute nothing. Haiku turns are
// excluded outright (same rule as detectStalls): the control plane's own 10-min
// auth probe writes haiku turns into playground's project dir, and even pennies
// of probe spend made a dead night read as non-zero — defeating the "$0 spend =
// obs blind or night dead" guards in governorCheckpoint and ratchetEvaluate.
// Night waves are Opus.
func nightSpendUSD(db *sql.DB, agent string, nightStart time.Time) (float64, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.Query(
		`SELECT COALESCE(t.model,''), t.in_tokens, t.out_tokens, t.cache_create, t.cache_read
		   FROM turns t JOIN sessions s ON s.session_id = t.session_id
		  WHERE s.agent = ? AND t.ts >= ? AND COALESCE(t.model,'') NOT LIKE '%haiku%'`,
		agent, nightStart.Unix())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return sumRows(rows)
}

// weekSpendUSD sums turn costs across ALL agents over the trailing 7 days — the
// operator's rolling burn against the subscription. Haiku excluded like
// nightSpendUSD: ~144 probe turns/day is pure noise, not plan burn.
func weekSpendUSD(db *sql.DB) (float64, error) {
	if db == nil {
		return 0, nil
	}
	cutoff := nowUnix() - 7*86400
	rows, err := db.Query(
		`SELECT COALESCE(model,''), in_tokens, out_tokens, cache_create, cache_read
		   FROM turns WHERE ts >= ? AND COALESCE(model,'') NOT LIKE '%haiku%'`, cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return sumRows(rows)
}

// sumRows folds (model,in,out,cacheW,cacheR) rows into a dollar total.
func sumRows(rows *sql.Rows) (float64, error) {
	var total float64
	for rows.Next() {
		var model string
		var in, out, cacheW, cacheR int64
		if err := rows.Scan(&model, &in, &out, &cacheW, &cacheR); err != nil {
			return total, err
		}
		total += usageCostUSD(in, out, cacheW, cacheR, model)
	}
	return total, rows.Err()
}

// ---- night boundaries --------------------------------------------------------

// nightStart is the Bishkek 22:30 anchor of the night containing `now`: today's
// 22:30 if we're already past it, else yesterday's (so a 03:00/05:00/08:50 read
// looks back over the whole night, not the coming evening).
func nightStart(now time.Time) time.Time {
	b := now.In(bishkek)
	anchor := time.Date(b.Year(), b.Month(), b.Day(), 22, 30, 0, 0, bishkek)
	if b.Before(anchor) {
		anchor = anchor.AddDate(0, 0, -1)
	}
	return anchor
}

// nightDate labels a night by its 22:30 anchor date — a stable key that both the
// morning ratchet and the overnight detector agree on for "which night".
func nightDate(now time.Time) string { return nightStart(now).Format("2006-01-02") }

// effectiveBudget resolves the operator's pinned budget or the default.
func effectiveBudget(cfg nightConfig) float64 {
	if cfg.NightBudgetUSD > 0 {
		return cfg.NightBudgetUSD
	}
	return defaultNightBudgetUSD
}

// ---- checkpoints (called from schedTick, nsMu held) --------------------------

// runGovernor evaluates the pacing checkpoints for playground. Each checkpoint
// fires once per day (LastSweep dedup, marked even on skip). Mutations land on
// the caller's cfg and flip *dirty so schedTick's single save persists them.
func runGovernor(now time.Time, cfg *nightConfig, dirty *bool) {
	if !isCompany("playground") {
		return
	}
	// The operator's night kill-switch applies here too: sweeps off = no waves =
	// $0 spend = maximally "under pace" — without this guard the governor would
	// mint top-ups on exactly the nights the operator explicitly disabled.
	if cfg.SweepOff["playground"] {
		return
	}
	nb := now.In(bishkek)
	day := nb.Format("2006-01-02")
	budget := effectiveBudget(*cfg)
	for _, cp := range govCheckpoints {
		due := time.Date(nb.Year(), nb.Month(), nb.Day(), cp.hour, cp.minute, 0, 0, bishkek)
		if now.Before(due) || alreadyMinted(cfg, cp.key, day) {
			continue
		}
		*dirty = true
		markMinted(cfg, cp.key, day) // fire once per day even if we skip/act below
		if now.After(due.Add(governorGrace)) {
			logf("governor %s: %s long past (downtime?), skipping", cp.key, due.Format("15:04"))
			continue
		}
		governorCheckpoint(now, cp, budget, cfg)
	}
}

// governorCheckpoint decides whether to mint one top-up worker. It logs the full
// decision (spend / budget / pace target / verdict) on every path.
func governorCheckpoint(now time.Time, cp govCheckpoint, budget float64, cfg *nightConfig) {
	ns := nightStart(now)
	spend, err := nightSpendUSD(obsDB, "playground", ns)
	if err != nil {
		logf("governor %s: night-spend query failed: %v", cp.key, err)
		return
	}
	target := cp.pace * budget
	topUps := countGovTopUps(now, ns)
	switch {
	case obsDB == nil:
		logf("governor %s: obs store unavailable, holding (budget $%.0f)", cp.key, budget)
	case spend == 0:
		// $0 by 03:00 is never a pacing signal: medic/steward/scout ran hours ago,
		// so a zero here means the obs pipeline is blind (broken ingest, moved
		// transcripts) or the night itself is dead (auth). Minting more workers on
		// zero evidence is how a broken night doubles its damage — hold.
		logf("governor %s: night spend reads $0 — obs blind or night dead, holding (budget $%.0f)", cp.key, budget)
	case spend >= budget:
		logf("governor %s: spend $%.0f of $%.0f budget (pace $%.0f) — at/over budget, no top-up",
			cp.key, spend, budget, target)
	case spend >= target:
		logf("governor %s: spend $%.0f of $%.0f budget (pace $%.0f) — on pace, no top-up",
			cp.key, spend, budget, target)
	case topUps >= govTopUpsMax:
		logf("governor %s: spend $%.0f of $%.0f budget (pace $%.0f) — under pace but %d top-ups already minted, holding",
			cp.key, spend, budget, target, topUps)
	case queueBacklogged("playground", now):
		// ADR-0019 inversion: under a serialized queue there is no idle capacity
		// to top up while unstarted work is still queued — minting more would
		// just lengthen the queue. Top up only when the night would otherwise
		// drain early.
		logf("governor %s: spend $%.0f of $%.0f budget (pace $%.0f) — under pace but the queue still holds unstarted work, no top-up",
			cp.key, spend, budget, target)
	default:
		j, ok := governorTopUpJob(now)
		if !ok {
			logf("governor %s: spend $%.0f of $%.0f (pace $%.0f) under pace but no top-up possible (deadline passed or no exec preamble)",
				cp.key, spend, budget, target)
			return
		}
		if err := saveJob(j); err != nil {
			logf("governor %s: top-up save failed: %v", cp.key, err)
			return
		}
		logf("governor %s: spend $%.0f of $%.0f budget (pace $%.0f) — under pace, minted top-up %s (%dm)",
			cp.key, spend, budget, target, j.ID, j.Minutes)
	}
}

// countGovTopUps counts governor top-up jobs minted since `since` (this night).
// Assumes nsMu is held by the caller (schedTick / budgetView).
func countGovTopUps(now, since time.Time) int {
	n := 0
	for _, j := range loadJobs("playground") {
		if j.Label == govTopUpLabel && !j.Created.Before(since) {
			n++
		}
	}
	return n
}

// governorTopUpJob builds a self-directed exec worker copying the exec slot's
// preamble/model/effort, with the auto-stop clamped to the time left before the
// 07:45 hard deadline. Saved with At=now so schedTick's launchGap machinery
// spaces its launch like any other job. Returns false when TODAY's deadline has
// already passed — a checkpoint firing late (post-downtime, inside its grace)
// must never roll the deadline to tomorrow and mint an 8h worker into the
// operator's day, which is exactly what the old clamp did when it mattered most.
func governorTopUpJob(now time.Time) (job, bool) {
	mins, ok := minutesUntilToday(now, execDeadlineHour, execDeadlineMinute)
	if !ok {
		return job{}, false
	}
	slot, ok := playgroundExecSlot()
	if !ok {
		return job{}, false
	}
	preamble, err := os.ReadFile(filepath.Join(nsDir(), "sweep", slot.prompt))
	if err != nil {
		logf("governor: exec preamble unreadable: %v", err)
		return job{}, false
	}
	return job{
		ID:      newRunID("exec", now),
		Agent:   "playground",
		Prompt:  string(preamble),
		Model:   slot.model,
		Effort:  slot.effort,
		Label:   govTopUpLabel,
		Minutes: mins,
		At:      now,
		Kind:    "sweep",
		Created: now,
		Batch:   nightBatch(now),
	}, true
}

// playgroundExecSlot returns playground's perTicket exec slot (its fields are the
// template for a governor top-up).
func playgroundExecSlot() (sweepSlot, bool) {
	for _, s := range sweepSlots("playground", 0) {
		if s.perTicket {
			return s, true
		}
	}
	return sweepSlot{}, false
}

// minutesUntilToday returns whole minutes from now to TODAY's Bishkek hh:mm,
// clamped to the rc-valid auto-stop range [10,480]. ok=false when the deadline
// has already passed (or is under the 10-minute floor) — the caller must skip,
// never roll to tomorrow.
func minutesUntilToday(now time.Time, hour, minute int) (int, bool) {
	b := now.In(bishkek)
	deadline := time.Date(b.Year(), b.Month(), b.Day(), hour, minute, 0, 0, bishkek)
	mins := int(deadline.Sub(now).Minutes())
	if mins < runMinutesFloor {
		return 0, false
	}
	if mins > runMinutesCeil {
		return runMinutesCeil, true
	}
	return mins, true
}

// ---- auto-ratchet (called from schedTick, nsMu held) -------------------------

// runRatchet re-calibrates the budget once each morning from the just-finished
// night. Fires once per day (LastSweep dedup), only when playground is live.
func runRatchet(now time.Time, cfg *nightConfig, dirty *bool) {
	if !isCompany("playground") {
		return
	}
	nb := now.In(bishkek)
	day := nb.Format("2006-01-02")
	cp := ratchetCheckpoint
	due := time.Date(nb.Year(), nb.Month(), nb.Day(), cp.hour, cp.minute, 0, 0, bishkek)
	if now.Before(due) || alreadyMinted(cfg, cp.key, day) {
		return
	}
	*dirty = true
	markMinted(cfg, cp.key, day)
	if now.After(due.Add(governorGrace)) {
		logf("ratchet: %s long past (downtime?), skipping", due.Format("15:04"))
		return
	}
	ratchetEvaluate(now, cfg)
}

// ratchetEvaluate applies the down/up rules to cfg for the night ending at `now`.
func ratchetEvaluate(now time.Time, cfg *nightConfig) {
	budget := effectiveBudget(*cfg)
	night := nightDate(now)
	if cfg.Ratchet.LastEval == night {
		return // already evaluated this night
	}
	cfg.Ratchet.LastEval = night

	// Down-ratchet: the night hit an API limit. Basis is the spend the detector
	// recorded at the moment of the hit (persisted into the ratchet — no parsing).
	// A hit at implausibly low spend (< suspectHitFraction of budget) is more
	// likely a transient provider outage burst than the plan ceiling — an
	// api-error blip at $60 must not slash an $1800 budget to the $400 floor
	// (recovery would take ~10 quiet nights). Half-step instead: repeated real
	// hits still walk the budget down, one phantom can't crater it.
	if cfg.Ratchet.LastHitNight == night {
		nb := clampBudget(ratchetDownFactor * cfg.Ratchet.SpendAtHit)
		if cfg.Ratchet.SpendAtHit < suspectHitFraction*budget {
			nb = clampBudget(0.5 * budget)
			logf("ratchet: night %s limit-hit at only $%.0f spend (<%d%% of $%.0f budget) — suspect basis (outage blip?), half-stepping budget -> $%.0f",
				night, cfg.Ratchet.SpendAtHit, int(suspectHitFraction*100), budget, nb)
		} else {
			logf("ratchet: night %s hit an API limit at $%.0f spend -> budget $%.0f -> $%.0f (0.8× spend-at-hit)",
				night, cfg.Ratchet.SpendAtHit, budget, nb)
		}
		cfg.NightBudgetUSD = nb
		cfg.Ratchet.QuietStreak = 0
		cfg.Ratchet.LastAdjust = now.In(bishkek).Format("2006-01-02")
		return
	}

	// Up-ratchet: a quiet night that finished well under budget banks a streak;
	// three in a row raises the budget so the pipeline reaches for the real ceiling.
	if obsDB == nil {
		logf("ratchet: obs store unavailable, streak unchanged")
		return
	}
	spend, err := nightSpendUSD(obsDB, "playground", nightStart(now))
	if err != nil {
		logf("ratchet: night-spend query failed, streak unchanged: %v", err)
		return
	}
	// A $0 night is not "quiet", it's a night that didn't happen (auth dead,
	// sweeps off, obs blind). Banking those toward an up-ratchet would inflate
	// the budget on exactly the nights that produced nothing — hold the streak.
	if spend == 0 {
		logf("ratchet: night %s spend reads $0 — not banking a quiet night (obs blind or night dead)", night)
		return
	}
	if spend < ratchetQuietFraction*budget {
		cfg.Ratchet.QuietStreak++
	} else {
		cfg.Ratchet.QuietStreak = 0
	}
	logf("ratchet: night %s finished $%.0f of $%.0f budget (quiet streak %d/%d)",
		night, spend, budget, cfg.Ratchet.QuietStreak, ratchetQuietNights)
	if cfg.Ratchet.QuietStreak >= ratchetQuietNights {
		nb := clampBudget(ratchetUpFactor * budget)
		logf("ratchet: %d quiet nights -> budget $%.0f -> $%.0f (×1.15)", ratchetQuietNights, budget, nb)
		cfg.NightBudgetUSD = nb
		cfg.Ratchet.QuietStreak = 0
		cfg.Ratchet.LastAdjust = now.In(bishkek).Format("2006-01-02")
	}
}

// clampBudget keeps a proposed budget inside the [floor, cap] rails.
func clampBudget(v float64) float64 {
	if v < budgetFloorUSD {
		return budgetFloorUSD
	}
	if v > budgetCapUSD {
		return budgetCapUSD
	}
	return v
}

// recordLimitHit persists the spend-at-hit for tonight so the morning ratchet can
// cut the budget without parsing an alert string. Called from the detector
// goroutine (NOT under schedTick), so it takes nsMu. Only playground drives the
// budget, so only its hits are recorded; a company-agent limit-hit still raises a
// visible alert but doesn't cut playground's budget.
func recordLimitHit(agent string, spend float64, now time.Time) {
	if agent != "playground" {
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	cfg := loadConfig()
	night := nightDate(now)
	// Keep the LARGEST spend-at-hit of the night, not the first: an early
	// low-spend blip must not mask the real ceiling a later hit measures.
	if cfg.Ratchet.LastHitNight == night && spend <= cfg.Ratchet.SpendAtHit {
		return
	}
	cfg.Ratchet.LastHitNight = night
	cfg.Ratchet.SpendAtHit = spend
	if err := saveConfig(cfg); err != nil {
		logf("governor: recording limit-hit spend failed: %v", err)
	}
}

// ---- read surface ------------------------------------------------------------

// obsBudget is the /api/obs budget block: the current budget, tonight's spend so
// far, the trailing-7-day burn, and how many top-ups the governor minted tonight.
// The ratchet fields make the otherwise-silent auto-calibration visible (ADR-0012):
// when the budget last moved, and the measured ceiling (spend at the last
// limit-hit) that a down-ratchet calibrates to.
type obsBudget struct {
	NightBudgetUSD float64 `json:"nightBudgetUSD"`
	NightSpendUSD  float64 `json:"nightSpendUSD"`
	WeekSpendUSD   float64 `json:"weekSpendUSD"`
	TopUpsMinted   int     `json:"topUpsMinted"`
	LastAdjust     string  `json:"lastAdjust,omitempty"`    // Bishkek date the budget last changed
	LimitHitNight  string  `json:"limitHitNight,omitempty"` // last night a limit-hit measured the ceiling
	SpendAtHitUSD  float64 `json:"spendAtHitUSD,omitempty"` // API-equiv spend at that limit-hit
}

// budgetView assembles the read-path budget block. Takes nsMu for the config +
// job reads; the DB reads are concurrency-safe on their own.
func budgetView(db *sql.DB, now time.Time) obsBudget {
	nsMu.Lock()
	cfg := loadConfig()
	ns := nightStart(now)
	topUps := countGovTopUps(now, ns)
	nsMu.Unlock()

	spend, _ := nightSpendUSD(db, "playground", ns)
	week, _ := weekSpendUSD(db)
	return obsBudget{
		NightBudgetUSD: effectiveBudget(cfg),
		NightSpendUSD:  spend,
		WeekSpendUSD:   week,
		TopUpsMinted:   topUps,
		LastAdjust:     cfg.Ratchet.LastAdjust,
		LimitHitNight:  cfg.Ratchet.LastHitNight,
		SpendAtHitUSD:  cfg.Ratchet.SpendAtHit,
	}
}
