// HTTP surface for pipeline profiles (ADR-0014). All writers hold nsMu and are
// rename-atomic (saveProfile); a 400 carries the parseProfile message verbatim
// for inline display in the editor. The scheduler NEVER reads a proposal — the
// operator's Apply is the only path from profiles.proposed/ into profiles/.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- list / read / write / delete --------------------------------------------

type profileSummary struct {
	Name      string       `json:"name"`
	Active    bool         `json:"active"`              // the operator's recurring choice
	Effective bool         `json:"effective,omitempty"` // locked for tonight (rolls to active next night)
	Waves     int          `json:"waves"`
	Deadline  string       `json:"deadline,omitempty"`
	Workers   *workerSplit `json:"workers,omitempty"`
	HasFanout bool         `json:"hasFanout,omitempty"` // any after-edge
}

type profilesResp struct {
	Agent     string           `json:"agent"`
	Active    string           `json:"active"`    // this agent's active profile ("" = built-in default)
	Effective string           `json:"effective"` // locked for tonight
	Profiles  []profileSummary `json:"profiles"`
	Builtin   profile          `json:"builtin"` // the default pipeline, so the editor can seed from it
}

func summarize(p profile, active, effective string) profileSummary {
	s := profileSummary{
		Name: p.Name, Active: p.Name == active, Effective: p.Name == effective,
		Waves: len(p.Waves), Deadline: p.Deadline, Workers: p.Workers,
	}
	for _, w := range p.Waves {
		if len(w.After) > 0 {
			s.HasFanout = true
			break
		}
	}
	return s
}

func handleProfiles(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("c")
	if agent == "" {
		agent = "playground"
	}
	if !isCompany(agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	cfg := loadConfig()
	active, effective := cfg.ActiveProfiles[agent], cfg.EffectiveProfiles[agent]
	names := listProfileNames()
	out := profilesResp{Agent: agent, Active: active, Effective: effective}
	for _, n := range names {
		p, err := loadProfile(n)
		if err != nil {
			// A corrupt file still lists (name + 0 waves) so it's visible + fixable.
			out.Profiles = append(out.Profiles, profileSummary{Name: n, Active: n == active})
			continue
		}
		out.Profiles = append(out.Profiles, summarize(p, active, effective))
	}
	out.Builtin = slotsToProfile("builtin", playgroundDefaultSlots())
	nsMu.Unlock()
	writeJSON(w, out)
}

func handleProfileGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid profile name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	p, err := loadProfile(name)
	nsMu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no such profile", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, p)
}

func handleProfilePut(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var p profile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The path name is authoritative — the editor addresses a profile by URL.
	if p.Name == "" {
		p.Name = name
	}
	if p.Name != name {
		http.Error(w, "name in body must match the url", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	err := saveProfile(p) // validates via parseProfile
	nsMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, p)
}

func handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid profile name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if _, err := os.Stat(profilePath(name)); err != nil {
		http.Error(w, "no such profile", http.StatusNotFound)
		return
	}
	if err := os.Remove(profilePath(name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A deleted profile is cleared from any agent that had it active, so no
	// agent's pointer dangles (the resolver tolerates it, but this keeps config
	// honest). The next-night effective roll then falls back to the default.
	cfg := loadConfig()
	changed := false
	for agent, n := range cfg.ActiveProfiles {
		if n == name {
			cfg.ActiveProfiles[agent] = ""
			changed = true
		}
	}
	if changed {
		if err := saveConfig(cfg); err != nil {
			logf("profile delete: clearing active failed: %v", err)
		}
	}
	writeJSON(w, map[string]string{"deleted": name})
}

// ---- activate ----------------------------------------------------------------

// handleProfileActive sets the recurring active profile. It takes effect the
// NEXT night — schedTick rolls EffectiveProfile at the night boundary — so a
// mid-night switch never half-changes the running night (ADR-0014 d).
func handleProfileActive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent string `json:"agent"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Agent == "" {
		in.Agent = "playground"
	}
	if !isCompany(in.Agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if in.Name != "" { // "" clears to the built-in default
		if _, err := loadProfile(in.Name); err != nil {
			http.Error(w, "profile not found or invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	cfg := loadConfig()
	cfg.ActiveProfiles[in.Agent] = in.Name
	if err := saveConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"agent": in.Agent, "active": in.Name, "effectiveNextNight": in.Name != cfg.EffectiveProfiles[in.Agent]})
}

// ---- run now -----------------------------------------------------------------

// handleProfileRun mints a profile's waves anchored to NOW — a one-off daytime
// pass (ADR-0014 d). Timed waves keep their inter-wave gaps (offset from the
// pipeline's first wave); triggered waves stay gated; the deadline clamp is
// skipped (a deliberate daytime run has no morning-limit concern). It does NOT
// touch LastSweep, so tonight's scheduled active-profile run still happens.
func handleProfileRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid profile name", http.StatusBadRequest)
		return
	}
	agent := r.URL.Query().Get("c")
	if agent == "" {
		agent = "playground"
	}
	if !isCompany(agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	p, err := loadProfile(name)
	if err != nil {
		http.Error(w, "profile not found or invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	slots := profileToSlots(agent, p)
	now := time.Now()
	batch := "run-" + shortBatchID()

	// firstOrder = the earliest timed wave, so offsets start at 0.
	firstOrder := 1 << 30
	for _, s := range slots {
		if s.hour >= 0 {
			if o := nightOrder(s.hour, s.minute); o < firstOrder {
				firstOrder = o
			}
		}
	}
	minted := 0
	for _, s := range slots {
		s.deadline = "" // run-now skips the morning-limit clamp
		if len(s.after) > 0 {
			mintGated(agent, now, now, s, batch) // stays triggered; fires when upstreams deliver
			minted++
			continue
		}
		off := nightOrder(s.hour, s.minute) - firstOrder
		mintWave(agent, now.Add(time.Duration(off)*time.Minute), s, "", batch)
		minted++
	}
	writeJSON(w, map[string]any{"agent": agent, "ran": name, "batch": batch, "waves": minted})
}

func shortBatchID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ---- proposal inbox (retro's self-modification channel) ----------------------
//
// Retro may WRITE ~/.nightshift/profiles.proposed/<name>.json (schema + a
// required `why`). The scheduler never reads it; only the operator's Apply
// promotes one into profiles/. Junk is visible (with its error) and dismissible,
// never silently run.

type proposalView struct {
	Name    string   `json:"name"`
	Why     string   `json:"why,omitempty"`
	Valid   bool     `json:"valid"`
	Error   string   `json:"error,omitempty"`
	Profile *profile `json:"profile,omitempty"` // parsed form when valid
}

func handleProposals(w http.ResponseWriter, _ *http.Request) {
	nsMu.Lock()
	defer nsMu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(proposedDir(), "*.json"))
	sort.Strings(matches)
	out := []proposalView{}
	for _, m := range matches {
		name := strings.TrimSuffix(filepath.Base(m), ".json")
		if !profileNameRe.MatchString(name) {
			continue
		}
		v := proposalView{Name: name}
		b, err := os.ReadFile(m)
		if err != nil {
			v.Error = err.Error()
			out = append(out, v)
			continue
		}
		// Pull `why` even if the profile is invalid, so the operator sees the
		// rationale next to the error.
		var raw struct {
			Why string `json:"why"`
		}
		_ = json.Unmarshal(b, &raw)
		v.Why = raw.Why
		p, perr := parseProfile(b)
		if perr != nil {
			v.Error = perr.Error()
		} else {
			v.Valid = true
			v.Profile = &p
			v.Why = p.Why
		}
		out = append(out, v)
	}
	writeJSON(w, out)
}

func handleProposalApply(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid proposal name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	b, err := os.ReadFile(proposedPath(name))
	if err != nil {
		http.Error(w, "no such proposal", http.StatusNotFound)
		return
	}
	p, err := parseProfile(b) // re-validate at apply time — never promote junk
	if err != nil {
		http.Error(w, "proposal invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	p.Why = "" // the rationale lives on the proposal, not the promoted profile
	if err := saveProfile(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = os.Remove(proposedPath(name)) // consumed
	writeJSON(w, map[string]string{"applied": p.Name})
}

func handleProposalDismiss(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !profileNameRe.MatchString(name) {
		http.Error(w, "invalid proposal name", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	if err := os.Remove(proposedPath(name)); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no such proposal", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"dismissed": name})
}
