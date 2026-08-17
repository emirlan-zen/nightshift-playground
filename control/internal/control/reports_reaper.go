package control

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Report artifacts (the <id>.md morning report plus its <id>.plate.png /
// <id>.banner.png banner images and the <id>.authfail / <id>.forgefail /
// <id>.depskip / <id>.watchdog markers) grow unbounded: playground writes ~11
// reports/night and, unlike the obs DB (ADR-0013's 45-day reaper), these files
// never had retention. This reaper closes that gap.
const (
	// reportReapAfter matches the obs store's retention (obsRetentionDays) so a
	// report never outlives — nor is outlived by — the obs rows a live surface
	// (an alert or ledger row) may link it from. Sized well above every read
	// window (14d reports list / usage, 72h detectors, 36h alert age-out).
	reportReapAfter = time.Duration(obsRetentionDays) * 24 * time.Hour
	// reportReapEvery rate-limits the loop — retention is a daily janitorial
	// concern, not a per-tick one. startReportReaper also runs one pass at boot.
	reportReapEvery = 24 * time.Hour
)

// reportReapVariants is the exhaustive, ordered set of artifact filename
// suffixes the reaper recognizes: the report (<id>.md), the executor capture
// (<id>.last.md), the three banner images (<id>.plate.png, <id>.banner.png, and
// the legacy baked <id>.png), and the run markers. Longer variants precede the
// ones they contain (.last.md before .md; the two .*.png banners before the
// bare legacy .png) so CutSuffix strips the *whole* variant and leaves a clean
// run-id stem to validate — a bare-suffix allowlist (the pre-amend version)
// would happily reap any old README.md or hand-dropped image.
var reportReapVariants = []string{
	".last.md", ".md",
	".plate.png", ".banner.png", ".png",
	".authfail", ".forgefail", ".depskip", ".watchdog",
}

// reportIDRe is the grammar every reapable artifact's stem must match:
// <YYYYMMDD>-<HHMM>-<kind>-<4 hex>, where <kind> is one or more lowercase
// alphanumeric segments (flow, exec, sweep, dep, run, plan-products, …).
// newRunID (nightrun.go) is the sole producer of report ids, and it always
// appends a 2-byte hex tail, so requiring the full shape — not just a known
// extension — is what keeps deletion (which is irreversible) off any file the
// reaper did not itself create.
var reportIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{4}-[a-z0-9]+(?:-[a-z0-9]+)*-[0-9a-f]{4}$`)

// startReportReaper deletes stale report artifacts once at boot then every
// reportReapEvery. Gated off in dev mode exactly like the other background loops
// (it is only registered as a worker when !DevMode — see app.go — and the tick
// self-guards too so a stray call can't touch seeded dev reports).
func startReportReaper() {
	go func() {
		for {
			reapReportsTick(time.Now())
			time.Sleep(reportReapEvery)
		}
	}()
}

// reapReportsTick removes recognized report artifacts older than reportReapAfter
// across every agent's reports dir. It deletes only individual files whose name
// ends in a known suffix and whose own mtime is past the cutoff — so a banner
// recomposed (and thus freshly touched) on a recent view survives, and an
// unrecognized file is never removed.
func reapReportsTick(now time.Time) {
	if devMode {
		return
	}
	cutoff := now.Add(-reportReapAfter)
	for _, agent := range companies {
		dir := reportsDir(agent)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// Regular files only. A directory, symlink, socket, or device is
			// never a report artifact — even one whose name matches the grammar.
			// os.Remove would not follow a symlink's target anyway (it unlinks
			// the name), but we refuse to consider one at all, so a symlink
			// dropped into the reports dir is preserved, not unlinked.
			if !e.Type().IsRegular() || !reapableReportName(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				logf("reports reaper: remove %s: %v", p, err)
			}
		}
	}
}

// reapableReportName reports whether name is a recognized report artifact: a
// run-id stem (reportIDRe) followed by exactly one known variant suffix. It is
// deliberately strict in both halves — an unknown extension or a stem that is
// not a full run id is never reaped.
func reapableReportName(name string) bool {
	for _, v := range reportReapVariants {
		if stem, ok := strings.CutSuffix(name, v); ok {
			return reportIDRe.MatchString(stem)
		}
	}
	return false
}
