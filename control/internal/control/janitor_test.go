package control

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// janitorEnv points the package globals at a temp home + secrets dir, silences
// logs, and returns the secrets dir. Mirrors nightrunEnv's style.
func janitorEnv(t *testing.T, agents ...string) string {
	t.Helper()
	home = t.TempDir()
	companies = agents
	sec := t.TempDir()
	prevSec, prevBase, prevGLBase, prevLogf := secretsDir, ghAPIBase, glAPIBase, logf
	secretsDir = sec
	logf = func(string, ...any) {}
	t.Cleanup(func() { secretsDir, ghAPIBase, glAPIBase, logf = prevSec, prevBase, prevGLBase, prevLogf })
	return sec
}

func writeToken(t *testing.T, sec, agent, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(sec, agent+".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mergedPR / openPR / closedPR are httptest JSON bodies.
const (
	mergedPR = `{"merged":true,"merged_at":"2026-07-06T01:00:00Z","merge_commit_sha":"abc1234def5678","state":"closed"}`
	openPR   = `{"merged":false,"state":"open"}`
	closedPR = `{"merged":false,"state":"closed"}`
)

// ghServer serves the PR API from a path->body map; unknown paths 404. It
// records how many times each path was hit.
func ghServer(t *testing.T, bodies map[string]string) (*httptest.Server, map[string]int) {
	t.Helper()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if body == "500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	ghAPIBase = srv.URL
	return srv, hits
}

func prPath(owner, repo, num string) string {
	return "/repos/" + owner + "/" + repo + "/pulls/" + num
}

// mergedMR / openMR / closedMR are httptest JSON bodies for the GitLab MR API.
const (
	mergedMR = `{"state":"merged","merged_at":"2026-07-06T02:00:00Z","merge_commit_sha":"def6789abc0123","sha":"aaa"}`
	openMR   = `{"state":"opened","sha":"bbb"}`
	closedMR = `{"state":"closed","sha":"ccc"}`
)

// glServer serves the GitLab MR API from a path->body map; unknown paths 404.
// GitLab's project path is URL-encoded (%2F for /), so mrPath encodes it too.
func glServer(t *testing.T, bodies map[string]string) (*httptest.Server, map[string]int) {
	t.Helper()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.EscapedPath()]++
		body, ok := bodies[r.URL.EscapedPath()]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if body == "500" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	glAPIBase = srv.URL
	return srv, hits
}

func mrPath(project, num string) string {
	return "/projects/" + url.QueryEscape(project) + "/merge_requests/" + num
}

func mustTicket(t *testing.T, tk ticket) {
	t.Helper()
	if err := saveTicket(tk); err != nil {
		t.Fatal(err)
	}
}

func reload(t *testing.T, agent, id string) ticket {
	t.Helper()
	tk, ok := loadTicket(agent, id)
	if !ok {
		t.Fatalf("ticket %s vanished", id)
	}
	return tk
}

func TestPRRefsExtraction(t *testing.T) {
	tk := ticket{
		// Body refs are deliberately IGNORED: a planner body citing a merged PR
		// ("same approach as .../pull/12") is not the ticket's deliverable, and
		// counting it auto-closed unshipped work. Evidence lives in notes.
		Body: "same approach as https://github.com/agent-c/example-repo/pull/42",
		Notes: []ticketNote{
			{Text: "opened https://github.com/agent-c/second-repo/pull/7 and github.com/agent-c/second-repo/pull/7 (dup)"},
			{Text: "see also https://github.com/agent-c/app for context"}, // non-PR url ignored
			{Text: "commented, no link here"},
			{Text: "mirror at evil-github.com/attacker/repo/pull/1"},                      // host-anchored: no match
			{By: "janitor", Text: "auto-closed: … https://github.com/agent-c/old/pull/3"}, // our own notes ignored
		},
	}
	refs := prRefs(tk)
	if len(refs) != 1 {
		t.Fatalf("want 1 deduped note-ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].owner != "agent-c" || refs[0].repo != "second-repo" || refs[0].num != "7" {
		t.Errorf("ref0 = %+v", refs[0])
	}
}

// A ticket whose only PR links live in the BODY is left for the steward: the
// linked PR being merged is not evidence the ticket's own work shipped.
func TestJanitorBodyOnlyRefsNeverClose(t *testing.T) {
	sec := janitorEnv(t, "agent-c")
	writeToken(t, sec, "agent-c", "GH_TOKEN=tok\n")
	_, hits := ghServer(t, map[string]string{prPath("agent-c", "example-repo", "42"): mergedPR})

	id := "20260706-0100-tkt-ab07"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "reuse approach", Status: "review",
		Body:    "do it like https://github.com/agent-c/example-repo/pull/42",
		Notes:   []ticketNote{{Text: "done, config-only change, no PR"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	if got := reload(t, "agent-c", id); got.Status != "review" {
		t.Fatalf("body-only refs closed the ticket: status = %q", got.Status)
	}
	if len(hits) != 0 {
		t.Fatalf("body-only refs should not even hit the API, got %v", hits)
	}
}

func TestJanitorAllMergedCloses(t *testing.T) {
	sec := janitorEnv(t, "agent-c")
	writeToken(t, sec, "agent-c", "GH_TOKEN=tok123\n")
	ghServer(t, map[string]string{prPath("agent-c", "example-repo", "42"): mergedPR})

	id := "20260706-0100-tkt-aa01"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "ship it", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "done in https://github.com/agent-c/example-repo/pull/42"}},
		Created: time.Now(), Updated: time.Now()})
	// stale claim sidecar should be swept on close
	claim := ticketClaimPath("agent-c", id)
	if err := os.WriteFile(claim, []byte("sched"), 0o644); err != nil {
		t.Fatal(err)
	}

	janitorPass(time.Now())

	got := reload(t, "agent-c", id)
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	last := got.Notes[len(got.Notes)-1]
	if last.By != "janitor" || !strings.Contains(last.Text, "auto-closed") ||
		!strings.Contains(last.Text, "pull/42") || !strings.Contains(last.Text, "abc1234") {
		t.Fatalf("evidence note wrong: %+v", last)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Fatalf("claim sidecar not removed: %v", err)
	}
}

func TestJanitorOneUnmergedLeavesUntouched(t *testing.T) {
	sec := janitorEnv(t, "agent-c")
	writeToken(t, sec, "agent-c", `GH_TOKEN="tok"`)
	ghServer(t, map[string]string{
		prPath("agent-c", "app", "1"): mergedPR,
		prPath("agent-c", "app", "2"): openPR,
	})

	id := "20260706-0100-tkt-bb02"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "two prs", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "https://github.com/agent-c/app/pull/1 https://github.com/agent-c/app/pull/2"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	got := reload(t, "agent-c", id)
	if got.Status != "review" {
		t.Fatalf("status = %q, want review (one PR still open)", got.Status)
	}
	if len(got.Notes) != 1 { // just the worker's own evidence note
		t.Fatalf("open PR must not add a note, got %+v", got.Notes)
	}
}

func TestJanitorClosedUnmergedNoteOnce(t *testing.T) {
	sec := janitorEnv(t, "agent-c")
	writeToken(t, sec, "agent-c", "GH_TOKEN=tok\n")
	ghServer(t, map[string]string{prPath("agent-c", "app", "9"): closedPR})

	id := "20260706-0100-tkt-cc03"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "abandoned", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "attempted https://github.com/agent-c/app/pull/9"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())
	janitorPass(time.Now()) // second pass must not duplicate the note

	got := reload(t, "agent-c", id)
	if got.Status != "review" {
		t.Fatalf("status = %q, want review", got.Status)
	}
	n := 0
	for _, note := range got.Notes {
		if strings.Contains(note.Text, "closed without merging") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("closed-unmerged note count = %d, want exactly 1", n)
	}
}

func TestJanitorZeroRefsUntouched(t *testing.T) {
	sec := janitorEnv(t, "agent-c")
	writeToken(t, sec, "agent-c", "GH_TOKEN=tok\n")
	ghServer(t, map[string]string{})

	id := "20260706-0100-tkt-dd04"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "no link", Status: "review",
		Body: "did stuff, no PR yet", Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	got := reload(t, "agent-c", id)
	if got.Status != "review" || len(got.Notes) != 0 {
		t.Fatalf("zero-ref ticket changed: %+v", got)
	}
}

func TestJanitorAPIErrorsLeaveUntouched(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"404", ""}, // path absent -> 404
		{"500", "500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := janitorEnv(t, "agent-c")
			writeToken(t, sec, "agent-c", "GH_TOKEN=tok\n")
			bodies := map[string]string{}
			if tc.body != "" {
				bodies[prPath("agent-c", "app", "5")] = tc.body
			}
			ghServer(t, bodies)

			id := "20260706-0100-tkt-ee05"
			mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "unknown", Status: "review",
				Notes:   []ticketNote{{By: "worker", Text: "https://github.com/agent-c/app/pull/5"}},
				Created: time.Now(), Updated: time.Now()})

			janitorPass(time.Now())

			got := reload(t, "agent-c", id)
			if got.Status != "review" || len(got.Notes) != 1 { // just the worker's note
				t.Fatalf("%s: ticket changed on partial info: %+v", tc.name, got)
			}
		})
	}
}

func TestJanitorNoTokenSkipsAgent(t *testing.T) {
	janitorEnv(t, "agent-c") // no env file written
	ghServer(t, map[string]string{prPath("agent-c", "app", "1"): mergedPR})

	id := "20260706-0100-tkt-ff06"
	mustTicket(t, ticket{ID: id, Agent: "agent-c", Title: "merged", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "https://github.com/agent-c/app/pull/1"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	if got := reload(t, "agent-c", id); got.Status != "review" {
		t.Fatalf("no-token agent should be skipped, status = %q", got.Status)
	}
}

func TestMRRefsExtraction(t *testing.T) {
	tk := ticket{
		Body: "same approach as https://gitlab.com/example-group/technology/example-repo/-/merge_requests/9",
		Notes: []ticketNote{
			{Text: "opened https://gitlab.com/example-group/technology/example-repo/-/merge_requests/34 and gitlab.com/example-group/technology/example-repo/-/merge_requests/34 (dup)"},
			{Text: "see also https://gitlab.com/example-group/technology/example-repo for context"}, // non-MR url ignored
			{Text: "mirror at evil-gitlab.com/attacker/repo/-/merge_requests/1"},            // host-anchored: no match
			{By: "janitor", Text: "auto-closed: … gitlab.com/g/p/-/merge_requests/3"},       // our own notes ignored
		},
	}
	refs := mrRefs(tk)
	if len(refs) != 1 {
		t.Fatalf("want 1 deduped note-ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].project != "example-group/technology/example-repo" || refs[0].num != "34" {
		t.Errorf("ref0 = %+v", refs[0])
	}
}

func TestJanitorGitLabAllMergedCloses(t *testing.T) {
	sec := janitorEnv(t, "agent-b")
	writeToken(t, sec, "agent-b", "GITLAB_TOKEN=gltok\n")
	glServer(t, map[string]string{mrPath("example-group/technology/example-repo", "34"): mergedMR})

	id := "20260706-0100-tkt-gl01"
	mustTicket(t, ticket{ID: id, Agent: "agent-b", Title: "ship example-repo", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "done in https://gitlab.com/example-group/technology/example-repo/-/merge_requests/34"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	got := reload(t, "agent-b", id)
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed", got.Status)
	}
	last := got.Notes[len(got.Notes)-1]
	if last.By != "janitor" || !strings.Contains(last.Text, "auto-closed") ||
		!strings.Contains(last.Text, "merge_requests/34") || !strings.Contains(last.Text, "def6789") {
		t.Fatalf("evidence note wrong: %+v", last)
	}
}

func TestJanitorGitLabOpenLeavesUntouched(t *testing.T) {
	sec := janitorEnv(t, "agent-b")
	writeToken(t, sec, "agent-b", "GITLAB_TOKEN=gltok\n")
	glServer(t, map[string]string{
		mrPath("g/p", "1"): mergedMR,
		mrPath("g/p", "2"): openMR,
	})

	id := "20260706-0100-tkt-gl02"
	mustTicket(t, ticket{ID: id, Agent: "agent-b", Title: "two mrs", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "https://gitlab.com/g/p/-/merge_requests/1 https://gitlab.com/g/p/-/merge_requests/2"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	got := reload(t, "agent-b", id)
	if got.Status != "review" || len(got.Notes) != 1 {
		t.Fatalf("open MR must leave ticket untouched: %+v", got)
	}
}

// A ticket with no GitLab token to verify its MR link is left untouched — a
// missing token is partial information, never a close.
func TestJanitorGitLabNoTokenLeavesUntouched(t *testing.T) {
	sec := janitorEnv(t, "agent-b")
	writeToken(t, sec, "agent-b", "GH_TOKEN=ghonly\n") // GH token, but the link is a GitLab MR
	glServer(t, map[string]string{mrPath("g/p", "1"): mergedMR})

	id := "20260706-0100-tkt-gl03"
	mustTicket(t, ticket{ID: id, Agent: "agent-b", Title: "mr no gl token", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "https://gitlab.com/g/p/-/merge_requests/1"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	if got := reload(t, "agent-b", id); got.Status != "review" {
		t.Fatalf("no-GitLab-token ticket should be untouched, status = %q", got.Status)
	}
}

// Mixed forges: a merged PR + a merged MR on one ticket closes it (all refs
// merged across both forges).
func TestJanitorMixedForgesAllMergedCloses(t *testing.T) {
	sec := janitorEnv(t, "agent-b")
	writeToken(t, sec, "agent-b", "GH_TOKEN=ghtok\nGITLAB_TOKEN=gltok\n")
	ghServer(t, map[string]string{prPath("agent-b", "example-repo", "7"): mergedPR})
	glServer(t, map[string]string{mrPath("g/p", "5"): mergedMR})

	id := "20260706-0100-tkt-gl04"
	mustTicket(t, ticket{ID: id, Agent: "agent-b", Title: "mixed", Status: "review",
		Notes:   []ticketNote{{By: "worker", Text: "https://github.com/agent-b/example-repo/pull/7 and https://gitlab.com/g/p/-/merge_requests/5"}},
		Created: time.Now(), Updated: time.Now()})

	janitorPass(time.Now())

	if got := reload(t, "agent-b", id); got.Status != "closed" {
		t.Fatalf("mixed-forge all-merged should close, status = %q", got.Status)
	}
}

func TestEnvValueParsing(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content, want string
		ok                  bool
	}{
		{"unquoted", "GH_TOKEN=test-token-a\n", "test-token-a", true},
		{"double-quoted", "GH_TOKEN=\"test-token-a\"\n", "test-token-a", true},
		{"single-quoted", "GH_TOKEN='test-token-a'\n", "test-token-a", true},
		{"export prefix + comment", "# secrets\nexport GH_TOKEN=test-token-b\n", "test-token-b", true},
		{"other keys around", "FOO=1\nGH_TOKEN=tok\nBAR=2\n", "tok", true},
		{"empty value", "GH_TOKEN=\n", "", false},
		{"absent", "OTHER=1\n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name+".env")
			if err := os.WriteFile(p, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, ok := envValue(p, "GH_TOKEN")
			if ok != c.ok || got != c.want {
				t.Fatalf("envValue = (%q,%v), want (%q,%v)", got, ok, c.want, c.ok)
			}
		})
	}
	if _, ok := envValue(filepath.Join(dir, "nope.env"), "GH_TOKEN"); ok {
		t.Fatal("missing file should return ok=false")
	}
}
