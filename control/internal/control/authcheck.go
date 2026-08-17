// Claude auth health for the control page (ADR-0006). The night pipeline is
// dead in the water if Claude Code can't authenticate — the 2026-07-04 silent
// night, where every wave started on time, printed "Not logged in", and
// no-op'd its whole window. The launcher runs the SAME probe as a pre-flight to
// skip doomed waves; here we surface auth health so the operator can see, from
// the phone before bed, whether tonight will run.
//
// Auth is per-user (~/.claude), not per-agent, so there is a single auth state.
// The probe is an actual authenticated `claude -p` call (see claude-auth-probe
// for why a disk check is insufficient), so it is refreshed by a background
// ticker and served from cache — the status poll never blocks on it.
package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	authProbeScript   = "/usr/local/bin/claude-auth-probe"
	forgeProbeScript  = "/usr/local/bin/forge-auth-probe"
	authCheckInterval = 10 * time.Minute
	authFailWindow    = 24 * time.Hour // how far back to surface *.authfail markers
)

// forge-auth-probe exit codes. Only a dead token (2) alerts: not-configured
// and inconclusive must never paint a false red on a healthy night.
const (
	forgeOK           = 0
	forgeDead         = 2
	forgeNotConf      = 3
	forgeInconclusive = 4
)

type authHealth struct {
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	CheckedAt int64  `json:"checkedAt"` // unix; 0 = never checked yet
}

// authFailure is a night-run wave the launcher skipped because auth was dead.
type authFailure struct {
	Agent string `json:"agent"`
	ID    string `json:"id"`
	At    int64  `json:"at"` // unix mtime of the marker
}

// forgeHealth is one company's GitHub/GitLab token status. Claude auth being
// green is not enough for a night to land work: a dead GH_TOKEN blocks every
// push/PR even when the model runs fine (the second, independent auth surface
// noted in ADR-0006).
type forgeHealth struct {
	Company   string `json:"company"`
	OK        bool   `json:"ok"` // false ONLY for a verified-dead token
	Detail    string `json:"detail"`
	CheckedAt int64  `json:"checkedAt"` // unix
}

// depSkip is a triggered wave the fire loop skipped because an output-upstream
// died (ADR-0014). Surfaced next to auth failures so a hollowed sub-tree is a
// morning-visible event (the red banner + Night/Morning danger chips).
type depSkip struct {
	Agent  string `json:"agent"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	At     int64  `json:"at"` // unix mtime of the marker
}

type healthResp struct {
	Auth           authHealth    `json:"auth"`
	Codex          *authHealth   `json:"codex,omitempty"` // ADR-0018; nil = codex never probed on this box
	RecentFailures []authFailure `json:"recentFailures"`
	Forge          []forgeHealth `json:"forge"`    // additive; per checked company
	DepSkips       []depSkip     `json:"depSkips"` // ADR-0014 dependency skips in the window
}

// codexHealthStatus reads the snapshot codex-auth-probe writes to
// ~/.nightshift/health/codex.json. Deliberately NOT a ticker like the claude
// probe: an active codex call burns ChatGPT-plan quota, so freshness comes
// from launcher pre-flights (every codex wave probes before starting) and the
// UI shows the checkedAt age instead of pretending the reading is live. nil
// when the file is absent — a box that never ran a codex wave must not alert.
func codexHealthStatus() *authHealth {
	b, err := os.ReadFile(filepath.Join(nsDir(), "health", "codex.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		OK        bool   `json:"ok"`
		Detail    string `json:"detail"`
		CheckedAt string `json:"checkedAt"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var at int64
	if t, err := time.Parse(time.RFC3339, doc.CheckedAt); err == nil {
		at = t.Unix()
	}
	return &authHealth{OK: doc.OK, Detail: doc.Detail, CheckedAt: at}
}

var (
	authMu    sync.RWMutex
	authCur   authHealth
	forgeCur  = map[string]forgeHealth{} // company -> last probe result
	probeDirF = func() string {          // first company workspace, or playground
		for _, c := range companies {
			if c == "playground" {
				return filepath.Join(home, "workspace", "playground")
			}
		}
		if len(companies) > 0 {
			return filepath.Join(home, "workspace", companies[0])
		}
		return home
	}
)

// authProbe is a test seam over the real probe script. Returns (ok, detail).
var authProbe = func() (bool, string) {
	cmd := exec.Command(authProbeScript, probeDirF())
	out, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		if err != nil {
			detail = err.Error()
		} else {
			detail = "ok"
		}
	}
	return err == nil, detail
}

func refreshAuth() {
	ok, detail := authProbe()
	authMu.Lock()
	authCur = authHealth{OK: ok, Detail: detail, CheckedAt: time.Now().Unix()}
	authMu.Unlock()
	if !ok {
		logf("auth check: NOT ok: %s", detail)
	}
}

// claudeAuthVerdict exposes the ticker's last Claude-auth probe result to the
// launch queue (ADR-0019 pause-don't-amputate). checkedAt==0 means no probe
// has run yet this process — callers fail open.
func claudeAuthVerdict() (ok bool, detail string, checkedAt int64) {
	authMu.RLock()
	defer authMu.RUnlock()
	return authCur.OK, authCur.Detail, authCur.CheckedAt
}

// forgeProbe is a test seam over realForgeProbe.
var forgeProbe = realForgeProbe

// realForgeProbe runs forge-auth-probe <company> and returns the exit code +
// a one-line detail. Routed through execCommand so dev mode stubs it. The
// script prints one-line JSON {"company","ok","detail"}; we prefer its detail
// field, falling back to raw output / the exec error. A probe that can't run
// at all (script missing, not an ExitError) maps to inconclusive — a box
// without the probe deployed must not alert.
func realForgeProbe(company string) (int, string) {
	out, err := execCommand(forgeProbeScript, company)
	detail := strings.TrimSpace(out)
	var doc struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal([]byte(detail), &doc) == nil && doc.Detail != "" {
		detail = doc.Detail
	}
	if err == nil {
		if detail == "" {
			detail = "ok"
		}
		return forgeOK, detail
	}
	if detail == "" {
		detail = err.Error()
	}
	// *exec.ExitError in prod; the interface also lets dev/tests fabricate codes.
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		return ee.ExitCode(), detail
	}
	return forgeInconclusive, detail
}

// refreshForge probes every company's forge token. Exit 2 (dead) flips OK to
// false; 3 (not configured) and 4 (inconclusive) stay OK=true with the detail
// carried through, so the UI can show them without alerting.
func refreshForge() {
	for _, c := range companies {
		code, detail := forgeProbe(c)
		st := forgeHealth{Company: c, OK: code != forgeDead, Detail: detail, CheckedAt: time.Now().Unix()}
		authMu.Lock()
		forgeCur[c] = st
		authMu.Unlock()
		if code == forgeDead {
			logf("forge check: %s token DEAD: %s", c, detail)
		}
	}
}

// startAuthChecker probes once at boot then every authCheckInterval. Cheap
// (one haiku call + one API get per company per tick) against the cost of a
// silently wasted Opus night.
func startAuthChecker() {
	go func() {
		for {
			refreshAuth()
			refreshForge()
			time.Sleep(authCheckInterval)
		}
	}()
}

// recentAuthFailures scans each agent's report dir for the launcher's
// `<id>.authfail` markers written within authFailWindow, newest first.
func recentAuthFailures() []authFailure {
	cutoff := time.Now().Add(-authFailWindow)
	var out []authFailure
	for _, c := range companies {
		entries, _ := os.ReadDir(reportsDir(c))
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".authfail")
			if !ok || !runIDRe.MatchString(id) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			out = append(out, authFailure{Agent: c, ID: id, At: info.ModTime().Unix()})
		}
	}
	sortByAtDesc(out)
	return out
}

func sortByAtDesc(f []authFailure) {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && f[j].At > f[j-1].At; j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

// recentDepSkips scans each agent's report dir for <id>.depskip markers written
// within authFailWindow, newest first — the same shape as recentAuthFailures.
func recentDepSkips() []depSkip {
	cutoff := time.Now().Add(-authFailWindow)
	var out []depSkip
	for _, c := range companies {
		entries, _ := os.ReadDir(reportsDir(c))
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".depskip")
			if !ok || !runIDRe.MatchString(id) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			reason, _ := os.ReadFile(filepath.Join(reportsDir(c), e.Name()))
			out = append(out, depSkip{Agent: c, ID: id, Reason: strings.TrimSpace(string(reason)), At: info.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At > out[b].At })
	return out
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	authMu.RLock()
	cur := authCur
	forge := make([]forgeHealth, 0, len(companies))
	for _, c := range companies { // companies order = stable render order
		if st, ok := forgeCur[c]; ok {
			forge = append(forge, st)
		}
	}
	authMu.RUnlock()
	failures := recentAuthFailures()
	if failures == nil {
		failures = []authFailure{}
	}
	skips := recentDepSkips()
	if skips == nil {
		skips = []depSkip{}
	}
	writeJSON(w, healthResp{Auth: cur, Codex: codexHealthStatus(), RecentFailures: failures, Forge: forge, DepSkips: skips})
}
