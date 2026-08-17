package control

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func utc(t *testing.T, hhmm string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, "2026-07-03T"+hhmm+":00Z")
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func mkTicket(t *testing.T, agent, id, status string) ticket {
	t.Helper()
	tk := ticket{ID: id, Agent: agent, Title: "title " + id, Body: "body " + id,
		Status: status, CreatedBy: "operator", Created: utc(t, "10:00"), Updated: utc(t, "10:00")}
	if err := saveTicket(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestNewTicketIDShape(t *testing.T) {
	id := newTicketID(bish(t, "14:12"))
	if !runIDRe.MatchString(id) {
		t.Fatalf("id %q does not match %s", id, runIDRe)
	}
	if !strings.HasPrefix(id, "20260613-1412-tkt-") {
		t.Fatalf("id %q lacks expected prefix", id)
	}
}

func TestTicketStoreRoundtrip(t *testing.T) {
	nightrunEnv(t, "agent-a")
	tk := mkTicket(t, "agent-a", "20260703-1412-tkt-ab12", "open")

	got := loadTickets("agent-a")
	if len(got) != 1 || got[0].ID != tk.ID || got[0].Status != "open" {
		t.Fatalf("loadTickets = %+v", got)
	}

	// update: append a note, flip status — overwrite is atomic, no .tmp left
	tk.Status = "review"
	tk.Notes = append(tk.Notes, ticketNote{At: utc(t, "11:00"), By: "agent-a", Text: "done"})
	tk.Updated = utc(t, "11:00")
	if err := saveTicket(tk); err != nil {
		t.Fatal(err)
	}
	got = loadTickets("agent-a")
	if len(got) != 1 || got[0].Status != "review" || len(got[0].Notes) != 1 {
		t.Fatalf("after update: %+v", got)
	}
	if tmp, _ := filepath.Glob(filepath.Join(ticketsDir("agent-a"), "*.tmp")); len(tmp) != 0 {
		t.Fatalf("stray tmp files: %v", tmp)
	}

	// junk in the dir is skipped: torn file, agent mismatch, bad id, bad status
	dir := ticketsDir("agent-a")
	writeRaw := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRaw("torn.json", `{"id":"20260703-1400-tkt-`)
	writeRaw("20260703-1400-tkt-eeee.json", `{"id":"20260703-1400-tkt-eeee","agent":"agent-b","title":"x","status":"open"}`)
	writeRaw("UPPER.json", `{"id":"UPPER","agent":"agent-a","title":"x","status":"open"}`)
	writeRaw("20260703-1401-tkt-ffff.json", `{"id":"20260703-1401-tkt-ffff","agent":"agent-a","title":"x","status":"weird"}`)
	if got := loadTickets("agent-a"); len(got) != 1 {
		t.Fatalf("junk not skipped: %+v", got)
	}
}

// TestTicketBashFormatCompat locks the cross-language schema: this fixture is
// byte-for-byte what files/ticket/nightshift-ticket (python3 json.dump) emits.
// If the CLI's format changes, this fixture must change with it.
func TestTicketBashFormatCompat(t *testing.T) {
	nightrunEnv(t, "agent-a")
	fixture := `{
  "id": "20260703-1412-tkt-ab12",
  "agent": "agent-a",
  "title": "Fix flaky auth test",
  "body": "details here\nsecond line",
  "status": "open",
  "createdBy": "agent-a",
  "created": "2026-07-03T14:12:03Z",
  "updated": "2026-07-03T14:12:03Z",
  "notes": []
}`
	if err := os.MkdirAll(ticketsDir("agent-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketsDir("agent-a"), "20260703-1412-tkt-ab12.json"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadTickets("agent-a")
	if len(got) != 1 {
		t.Fatalf("CLI-written ticket not parsed: %+v", got)
	}
	tk := got[0]
	if tk.Title != "Fix flaky auth test" || tk.CreatedBy != "agent-a" ||
		!tk.Created.Equal(utc(t, "14:12").Add(3*time.Second)) {
		t.Fatalf("parsed = %+v", tk)
	}

	// and the reverse: after a review-note update by the CLI (same emitter)
	reviewed := `{
  "id": "20260703-1412-tkt-ab12",
  "agent": "agent-a",
  "title": "Fix flaky auth test",
  "body": "details here\nsecond line",
  "status": "review",
  "createdBy": "agent-a",
  "created": "2026-07-03T14:12:03Z",
  "updated": "2026-07-04T05:40:00Z",
  "notes": [{"at": "2026-07-04T05:40:00Z", "by": "agent-a", "text": "done in draft PR #42"}]
}`
	if err := os.WriteFile(filepath.Join(ticketsDir("agent-a"), "20260703-1412-tkt-ab12.json"), []byte(reviewed), 0o644); err != nil {
		t.Fatal(err)
	}
	got = loadTickets("agent-a")
	if len(got) != 1 || got[0].Status != "review" || len(got[0].Notes) != 1 || got[0].Notes[0].By != "agent-a" {
		t.Fatalf("reviewed parse = %+v", got)
	}
}

func TestTicketCreateHandler(t *testing.T) {
	nightrunEnv(t, "agent-a")

	w := httptest.NewRecorder()
	body := strings.NewReader(`{"agent":"agent-a","title":"Fix the thing","body":"details"}`)
	handleTicketCreate(w, httptest.NewRequest("POST", "/api/ticket", body))
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var tk ticket
	if err := json.Unmarshal(w.Body.Bytes(), &tk); err != nil {
		t.Fatal(err)
	}
	if tk.Status != "open" || tk.CreatedBy != "operator" || !runIDRe.MatchString(tk.ID) {
		t.Fatalf("created = %+v", tk)
	}
	if got := loadTickets("agent-a"); len(got) != 1 {
		t.Fatalf("not persisted: %+v", got)
	}

	for _, bad := range []string{
		`{"agent":"evil","title":"x"}`,
		`{"agent":"agent-a","title":""}`,
		`{"agent":"agent-a","title":"  "}`,
		fmt.Sprintf(`{"agent":"agent-a","title":%q}`, strings.Repeat("a", maxTitleLen+1)),
		fmt.Sprintf(`{"agent":"agent-a","title":"x","body":%q}`, strings.Repeat("a", maxTicketBodyLen+1)),
		`not json`,
	} {
		w := httptest.NewRecorder()
		handleTicketCreate(w, httptest.NewRequest("POST", "/api/ticket", strings.NewReader(bad)))
		if w.Code == 200 {
			t.Errorf("bad ticket accepted: %.60s", bad)
		}
	}
}

func TestTicketUpdateHandler(t *testing.T) {
	nightrunEnv(t, "agent-a")
	tk := mkTicket(t, "agent-a", "20260703-1412-tkt-ab12", "open")

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleTicketUpdate(w, httptest.NewRequest("POST", "/api/ticket/update", strings.NewReader(body)))
		return w
	}
	upd := func(status, note string) *httptest.ResponseRecorder {
		return post(fmt.Sprintf(`{"agent":"agent-a","id":%q,"status":%q,"note":%q}`, tk.ID, status, note))
	}

	// open -> review (operator can too), review -> closed, closed -> open
	for _, step := range []struct{ to string }{{"review"}, {"closed"}, {"open"}} {
		if w := upd(step.to, "note for "+step.to); w.Code != 200 {
			t.Fatalf("-> %s: %d %s", step.to, w.Code, w.Body.String())
		}
	}
	got, _ := loadTicket("agent-a", tk.ID)
	if got.Status != "open" || len(got.Notes) != 3 || got.Notes[0].By != "operator" {
		t.Fatalf("after transitions: %+v", got)
	}

	// note-only update allowed, no status change
	if w := upd("", "guidance only"); w.Code != 200 {
		t.Fatalf("note-only: %d %s", w.Code, w.Body.String())
	}
	if got, _ = loadTicket("agent-a", tk.ID); got.Status != "open" || len(got.Notes) != 4 {
		t.Fatalf("after note-only: %+v", got)
	}

	// rejects
	for name, w := range map[string]*httptest.ResponseRecorder{
		"same status":   upd("open", ""),
		"bogus status":  upd("blocked", ""),
		"empty update":  upd("", ""),
		"unknown id":    post(`{"agent":"agent-a","id":"20260703-1412-tkt-9999","status":"closed"}`),
		"unknown agent": post(`{"agent":"evil","id":"` + tk.ID + `","status":"closed"}`),
	} {
		if w.Code == 200 {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestTicketHandlerTraversal(t *testing.T) {
	nightrunEnv(t, "agent-a")
	// a valid-JSON "ticket" file OUTSIDE the tickets tree that traversal must never reach
	secret := `{"id":"../../evil","agent":"agent-a","title":"x","status":"open"}`
	if err := os.WriteFile(filepath.Join(home, "evil.json"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../../evil", "..%2F..%2Fevil", "UPPER", ""} {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"agent":"agent-a","id":%q,"status":"closed"}`, id)
		handleTicketUpdate(w, httptest.NewRequest("POST", "/api/ticket/update", strings.NewReader(body)))
		if w.Code == 200 {
			t.Errorf("id %q must not resolve, got 200", id)
		}
	}
}

func TestTicketsGrouped(t *testing.T) {
	nightrunEnv(t, "agent-a", "agent-b")
	mkTicket(t, "agent-a", "20260703-1412-tkt-ab12", "open")

	w := httptest.NewRecorder()
	handleTickets(w, httptest.NewRequest("GET", "/api/tickets", nil))
	var groups []ticketGroup
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Agent != "agent-a" || groups[1].Agent != "agent-b" {
		t.Fatalf("groups = %+v", groups)
	}
	if len(groups[0].Tickets) != 1 || groups[1].Tickets == nil || len(groups[1].Tickets) != 0 {
		t.Fatalf("tickets = %+v (empty agent must serialize as [], not null)", groups)
	}
}

func TestSchedTickSweepIncludesOpenTickets(t *testing.T) {
	nightrunEnv(t, "agent-a")
	if err := os.MkdirAll(filepath.Join(nsDir(), "sweep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nsDir(), "sweep", "agent-a.md"), []byte("sweep agent-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkTicket(t, "agent-a", "20260703-1412-tkt-op11", "open")
	mkTicket(t, "agent-a", "20260703-1413-tkt-rv22", "review")
	mkTicket(t, "agent-a", "20260703-1414-tkt-cl33", "closed")

	schedTick(bish(t, "23:25"))
	jobs := loadJobs("agent-a")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	p := jobs[0].Prompt
	if !strings.HasPrefix(p, "sweep agent-a") || !strings.Contains(p, "## Open tickets") {
		t.Fatalf("prompt missing section: %q", p)
	}
	if !strings.Contains(p, "20260703-1412-tkt-op11") || !strings.Contains(p, "title 20260703-1412-tkt-op11") {
		t.Fatalf("open ticket missing from prompt: %q", p)
	}
	if strings.Contains(p, "tkt-rv22") || strings.Contains(p, "tkt-cl33") {
		t.Fatalf("non-open tickets leaked into prompt: %q", p)
	}
}

func TestOpenTicketsSectionEmptyAndCapped(t *testing.T) {
	nightrunEnv(t, "agent-a")
	if s := openTicketsSection("agent-a"); s != "" {
		t.Fatalf("no tickets must yield empty section, got %q", s)
	}

	// huge bodies get truncated so the section stays under the cap
	for i := 0; i < 3; i++ {
		tk := ticket{ID: fmt.Sprintf("20260703-141%d-tkt-bi%02d", i, i), Agent: "agent-a",
			Title: "big", Body: strings.Repeat("x", maxTicketBodyLen),
			Status: "open", CreatedBy: "operator",
			Created: utc(t, "10:00").Add(time.Duration(i) * time.Minute), Updated: utc(t, "10:00")}
		if err := saveTicket(tk); err != nil {
			t.Fatal(err)
		}
	}
	s := openTicketsSection("agent-a")
	if len(s) > maxSweepTicketLen+200 { // small slack for the truncation marker
		t.Fatalf("section too big: %d bytes", len(s))
	}
	if !strings.Contains(s, "truncated") && !strings.Contains(s, "omitted") {
		t.Fatalf("cap left no marker: %q", s[:200])
	}
}
