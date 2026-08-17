// Ticketboard: per-agent work queues shared by the operator and the agents.
// The operator files and closes tickets from the control page; agents file
// tickets and move them to review via the on-box `nightshift-ticket` CLI,
// which writes the same JSON files directly (both sides rename-atomically, so
// no lock is shared across processes). Each agent's nightly sweep prompt gets
// its open tickets appended, so unfinished work survives to the next night.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	ticketdomain "nightshift/control/internal/ticket"
)

type ticket = ticketdomain.Ticket
type ticketNote = ticketdomain.Note

const (
	maxTitleLen       = ticketdomain.MaxTitleLength
	maxTicketBodyLen  = ticketdomain.MaxBodyLength
	maxSweepTicketLen = ticketdomain.MaxSweepSectionSize
)

func ticketsDir(agent string) string { return filepath.Join(nsDir(), "tickets", agent) }

func newTicketID(now time.Time) string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-tkt-%s", now.In(bishkek).Format("20060102-1504"), hex.EncodeToString(b))
}

// saveTicket writes rename-atomically: the nightshift-ticket CLI reads and
// writes these files from outside this process, so a plain WriteFile could be
// seen torn. (nsMu only serializes the control plane with itself.)
func saveTicket(t ticket) error {
	dir := ticketsDir(t.Agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	tmp := filepath.Join(dir, t.ID+".json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, t.ID+".json"))
}

func loadTickets(agent string) []ticket {
	var out []ticket
	matches, _ := filepath.Glob(filepath.Join(ticketsDir(agent), "*.json"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var t ticket
		if json.Unmarshal(b, &t) == nil && runIDRe.MatchString(t.ID) &&
			t.Agent == agent && ticketdomain.ValidStatus(t.Status) {
			out = append(out, t)
		}
	}
	ticketdomain.SortByUpdated(out)
	return out
}

func loadTicket(agent, id string) (ticket, bool) {
	b, err := os.ReadFile(filepath.Join(ticketsDir(agent), id+".json"))
	if err != nil {
		return ticket{}, false
	}
	var t ticket
	if json.Unmarshal(b, &t) != nil || t.ID != id || t.Agent != agent {
		return ticket{}, false
	}
	return t, true
}

// openTicketsSection renders an agent's open tickets as a markdown section the
// scheduler appends to the sweep prompt. Empty string when there's nothing
// open. Caller holds nsMu (same contract as loadJobs in schedTick).
func openTicketsSection(agent string) string {
	return ticketdomain.OpenSection(loadTickets(agent))
}

// ---- handlers ----------------------------------------------------------------

// ticketView is a ticket plus its live claim marker — the exec dispatch claim
// (<id>.claim) isn't part of the persisted ticket, so it's decorated on read
// only. Embedding flattens the ticket's own JSON fields unchanged.
type ticketView struct {
	ticket
	Claim string `json:"claim,omitempty"` // fresh claim's first token, e.g. "sched:<runid>"; "" = unclaimed
}

type ticketGroup struct {
	Agent   string       `json:"agent"`
	Tickets []ticketView `json:"tickets"`
}

// ticketClaimMarker returns a fresh claim's compact marker (its first token) for
// the UI, or "" if the ticket is unclaimed or the claim has gone stale.
func ticketClaimMarker(agent, id string, now time.Time) string {
	if !ticketClaimed(agent, id, now) {
		return ""
	}
	b, err := os.ReadFile(ticketClaimPath(agent, id))
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(b)); len(f) > 0 {
		return f[0]
	}
	return "claimed"
}

func handleTickets(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	nsMu.Lock()
	out := make([]ticketGroup, 0, len(companies))
	for _, c := range companies {
		views := []ticketView{}
		for _, t := range loadTickets(c) {
			views = append(views, ticketView{ticket: t, Claim: ticketClaimMarker(c, t.ID, now)})
		}
		out = append(out, ticketGroup{Agent: c, Tickets: views})
	}
	nsMu.Unlock()
	writeJSON(w, out)
}

func handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent string `json:"agent"`
		Title string `json:"title"`
		Body  string `json:"body"`
		Lane  string `json:"lane,omitempty"` // "" | hunt | improve | ops
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !isCompany(in.Agent) {
		http.Error(w, "unknown agent", http.StatusBadRequest)
		return
	}
	if !ticketdomain.ValidLane(in.Lane) {
		http.Error(w, "bad lane (want hunt|improve|ops)", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	t, err := ticketdomain.New(newTicketID(now), in.Agent, in.Title, in.Body, in.Lane, "operator", now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	err = saveTicket(t)
	nsMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Default().InfoContext(r.Context(), "ticket.created",
		"agent", t.Agent, "ticket_id", t.ID, "lane", t.Lane,
	)
	writeJSON(w, t)
}

func handleTicketUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent  string `json:"agent"`
		ID     string `json:"id"`
		Status string `json:"status"` // "" = note-only update
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// same traversal gate as handleReport: both parts validated, id is never a path
	if !isCompany(in.Agent) || !runIDRe.MatchString(in.ID) {
		http.Error(w, "bad agent or id", http.StatusBadRequest)
		return
	}
	nsMu.Lock()
	defer nsMu.Unlock()
	t, ok := loadTicket(in.Agent, in.ID)
	if !ok {
		http.Error(w, "no such ticket", http.StatusNotFound)
		return
	}
	if err := t.UpdateByOperator(in.Status, in.Note, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveTicket(t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Default().InfoContext(r.Context(), "ticket.updated",
		"agent", t.Agent, "ticket_id", t.ID, "status", t.Status, "note_added", in.Note != "",
	)
	writeJSON(w, t)
}
