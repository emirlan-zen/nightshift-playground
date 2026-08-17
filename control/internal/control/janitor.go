// Ticket janitor: closes `review` tickets whose linked GitHub PRs / GitLab MRs
// have all merged, so landed work retires itself instead of waiting for the
// nightly steward wave or the operator. It ONLY ever closes on complete,
// positive evidence — every referenced PR/MR confirmed merged over the API. On
// any uncertainty (an unreadable PR/MR, a 404 from a token that can't see the
// repo, a missing token for a linked forge, a transient API error) it leaves the
// ticket untouched and re-checks next pass: a false close silently buries work,
// so partial information must never close.
package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// ghAPIBase / glAPIBase are package vars so tests can point them at an httptest
// server. GitLab parity covers gitlab.com only for v1 (self-hosted needs a
// per-company base URL).
var (
	ghAPIBase = "https://api.github.com"
	glAPIBase = "https://gitlab.com/api/v4"
)

// secretsDir holds the per-agent env files (GH_TOKEN et al). Var for tests.
var secretsDir = "/etc/nightshift/secrets"

// janitorInterval is how often a pass runs after the boot pass.
const janitorInterval = 30 * time.Minute

// One reused client with a hard per-request timeout — GitHub is a remote hop on
// a background loop; a stuck request must not wedge the pass.
var janitorClient = &http.Client{Timeout: 15 * time.Second}

// prRefRe pulls owner/repo/number out of a github PR URL in free text. The
// leading group anchors the host: without it `evil-github.com/o/r/pull/1`
// matched on the embedded substring and the janitor queried api.github.com for
// a repo the link never pointed at.
var prRefRe = regexp.MustCompile(`(?:^|[^\w.-])github\.com/([\w.-]+)/([\w.-]+)/pull/(\d+)`)

// mrRefRe pulls the project path + MR iid out of a gitlab.com merge-request URL.
// The project path is everything between `gitlab.com/` and `/-/merge_requests/`
// (GitLab groups nest, e.g. `example-group/technology/example-repo`), captured
// non-greedily so it stops at the first `/-/merge_requests/`. The leading group
// anchors the host the same way prRefRe does — without it
// `evil-gitlab.com/g/p/-/merge_requests/1` matched the embedded substring.
var mrRefRe = regexp.MustCompile(`(?:^|[^\w.-])gitlab\.com/([\w./-]+?)/-/merge_requests/(\d+)`)

type prRef struct {
	owner, repo, num, url string
}

type mrRef struct {
	project, num, url string
}

// prStatus is the slice of the PR API response the janitor needs. merged is the
// authoritative signal; state/merged_at/sha are for the evidence note.
type prStatus struct {
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	State          string `json:"state"`
}

// mrStatus is the slice of the GitLab MR API response the janitor needs. GitLab
// has no boolean `merged`; the authoritative signal is state=="merged".
type mrStatus struct {
	State          string `json:"state"` // opened | closed | merged | locked
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	SHA            string `json:"sha"`
}

// startJanitor runs a pass at boot then every janitorInterval. Gated exactly
// like the other background loops (off in dev mode — see main).
func startJanitor() {
	go func() {
		for {
			janitorPass(time.Now())
			time.Sleep(janitorInterval)
		}
	}()
}

// janitorPass checks every agent's `review` tickets once. HTTP happens outside
// nsMu; only the per-ticket load+mutate+save is locked, so a slow GitHub call
// never blocks the request handlers.
func janitorPass(now time.Time) {
	for _, agent := range companies {
		ghToken, hasGH := agentGHToken(agent)
		glToken, hasGL := agentGLToken(agent)
		if !hasGH && !hasGL {
			logf("janitor %s: no GH_TOKEN or GITLAB_TOKEN, skipping", agent)
			continue
		}

		nsMu.Lock()
		var review []ticket
		for _, t := range loadTickets(agent) {
			if t.Status == "review" {
				review = append(review, t)
			}
		}
		nsMu.Unlock()

		loggedErr := false
		for _, t := range review {
			janitorCheckTicket(agent, ghToken, glToken, t, now, &loggedErr)
		}
	}
}

// janitorCheckTicket resolves one ticket's linked PRs + MRs and applies the
// outcome. loggedErr caps error logging to one line per agent-pass.
func janitorCheckTicket(agent, ghToken, glToken string, t ticket, now time.Time, loggedErr *bool) {
	prs := prRefs(t)
	mrs := mrRefs(t)
	if len(prs) == 0 && len(mrs) == 0 {
		return // no PR/MR link — steward's judgment call, not ours
	}

	allMerged := true
	var closedUnmerged []string // abandoned PR/MR urls
	closeLines := make([]string, 0, len(prs)+len(mrs))

	// unknown records a ref we can't verify (no token for its forge, or an API
	// error). Partial information ⇒ do NOTHING and re-check next pass.
	unknown := func(refURL string, err error) {
		if !*loggedErr {
			logf("janitor %s: %s unreadable, skipping ticket %s: %v", agent, refURL, t.ID, err)
			*loggedErr = true
		}
	}
	record := func(merged bool, state, refURL, mergedAt, sha string) {
		if merged {
			closeLines = append(closeLines, fmt.Sprintf(
				"auto-closed: all linked PRs/MRs merged — %s (merged %s, %s)",
				refURL, mergedAt, shortSHA(sha)))
			return
		}
		allMerged = false
		if state == "closed" { // closed AND not merged = abandoned
			closedUnmerged = append(closedUnmerged, refURL)
		}
	}

	for _, ref := range prs {
		if ghToken == "" {
			unknown(ref.url, fmt.Errorf("no GitHub token to verify"))
			return
		}
		st, err := fetchPR(ref, ghToken)
		if err != nil {
			unknown(ref.url, err)
			return
		}
		record(st.Merged, st.State, ref.url, st.MergedAt, st.MergeCommitSHA)
	}
	for _, ref := range mrs {
		if glToken == "" {
			unknown(ref.url, fmt.Errorf("no GitLab token to verify"))
			return
		}
		st, err := fetchMR(ref, glToken)
		if err != nil {
			unknown(ref.url, err)
			return
		}
		record(st.State == "merged", st.State, ref.url, st.MergedAt, mrCommit(st))
	}

	nsMu.Lock()
	defer nsMu.Unlock()
	// Re-read under the lock: the operator or a CLI may have moved the ticket
	// out of review while we were on the network. Only act on a still-review one.
	fresh, ok := loadTicket(agent, t.ID)
	if !ok || fresh.Status != "review" {
		return
	}

	if allMerged {
		fresh.Notes = append(fresh.Notes, ticketNote{At: now, By: "janitor", Text: strings.Join(closeLines, "\n")})
		fresh.Status = "closed"
		fresh.Updated = now
		if err := saveTicket(fresh); err != nil {
			logf("janitor %s: close %s save failed: %v", agent, fresh.ID, err)
			return
		}
		// Drop any stale exec claim so the closed ticket can't look dispatched.
		_ = os.Remove(ticketClaimPath(agent, fresh.ID))
		logf("janitor %s: auto-closed %s (%d PR/MR(s) merged)", agent, fresh.ID, len(prs)+len(mrs))
		return
	}

	// Not closable. Flag any abandoned PR/MR once so the steward/operator sees it;
	// leave the ticket in review for a human to retire or reopen work on.
	changed := false
	for _, refURL := range closedUnmerged {
		text := "linked PR/MR closed without merging — " + refURL
		if noteExists(fresh.Notes, text) {
			continue
		}
		fresh.Notes = append(fresh.Notes, ticketNote{At: now, By: "janitor", Text: text})
		changed = true
	}
	if changed {
		fresh.Updated = now
		if err := saveTicket(fresh); err != nil {
			logf("janitor %s: note %s save failed: %v", agent, fresh.ID, err)
		}
	}
}

// prRefs extracts the deduped GitHub PR references from a ticket's NOTES only,
// in first-seen order. The body is deliberately excluded: a planner-written
// body often *references* a PR ("same approach as .../pull/12") that is not the
// ticket's deliverable, and a merged reference would auto-close unshipped work.
// The worker's `nightshift-ticket review` evidence note is where the deliverable
// PR link lives (contract-mandated); a ticket whose links are body-only simply
// stays for the steward's judgment — the safe direction.
func prRefs(t ticket) []prRef {
	seen := map[string]bool{}
	var out []prRef
	scan := func(s string) {
		for _, m := range prRefRe.FindAllStringSubmatch(s, -1) {
			owner, repo, num := m[1], m[2], m[3]
			url := fmt.Sprintf("https://github.com/%s/%s/pull/%s", owner, repo, num)
			if seen[url] {
				continue
			}
			seen[url] = true
			out = append(out, prRef{owner: owner, repo: repo, num: num, url: url})
		}
	}
	for _, n := range t.Notes {
		if n.By == "janitor" {
			continue // our own evidence notes must not feed the next pass
		}
		scan(n.Text)
	}
	return out
}

// mrRefs extracts the deduped gitlab.com MR references from a ticket's NOTES
// only, in first-seen order — identical policy to prRefs (body links are not the
// deliverable; the worker's review evidence note is).
func mrRefs(t ticket) []mrRef {
	seen := map[string]bool{}
	var out []mrRef
	scan := func(s string) {
		for _, m := range mrRefRe.FindAllStringSubmatch(s, -1) {
			project, num := m[1], m[2]
			url := fmt.Sprintf("https://gitlab.com/%s/-/merge_requests/%s", project, num)
			if seen[url] {
				continue
			}
			seen[url] = true
			out = append(out, mrRef{project: project, num: num, url: url})
		}
	}
	for _, n := range t.Notes {
		if n.By == "janitor" {
			continue // our own evidence notes must not feed the next pass
		}
		scan(n.Text)
	}
	return out
}

// fetchMR reads one GitLab MR's merge status. The project path is URL-encoded
// (slashes → %2F) as GitLab's `:id` path segment requires. A non-200 (incl. 404
// from a token that can't see the project) is an error → treated as unknown.
func fetchMR(ref mrRef, token string) (mrStatus, error) {
	// GitLab's project `:id` segment is the path URL-encoded (slashes → %2F).
	endpoint := fmt.Sprintf("%s/projects/%s/merge_requests/%s", glAPIBase, url.QueryEscape(ref.project), ref.num)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return mrStatus{}, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	resp, err := janitorClient.Do(req)
	if err != nil {
		return mrStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mrStatus{}, fmt.Errorf("MR %s: HTTP %d", ref.url, resp.StatusCode)
	}
	var st mrStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return mrStatus{}, err
	}
	return st, nil
}

// mrCommit is the MR's merge commit if present, else its head sha — for the
// evidence note only.
func mrCommit(st mrStatus) string {
	if st.MergeCommitSHA != "" {
		return st.MergeCommitSHA
	}
	return st.SHA
}

// fetchPR reads one PR's merge status. A non-200 (incl. 404 from a token that
// can't see the repo) is an error, so the caller treats the PR as unknown.
func fetchPR(ref prRef, token string) (prStatus, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%s", ghAPIBase, ref.owner, ref.repo, ref.num)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return prStatus{}, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := janitorClient.Do(req)
	if err != nil {
		return prStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return prStatus{}, fmt.Errorf("PR %s: HTTP %d", ref.url, resp.StatusCode)
	}
	var st prStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return prStatus{}, err
	}
	return st, nil
}

// agentGHToken reads GH_TOKEN from an agent's env file. Agents can carry
// different tokens, so this is resolved per agent.
func agentGHToken(agent string) (string, bool) {
	return envValue(secretsDir+"/"+agent+".env", "GH_TOKEN")
}

// agentGLToken reads GITLAB_TOKEN from an agent's env file. Read-only use,
// resolved per agent like agentGHToken.
func agentGLToken(agent string) (string, bool) {
	return envValue(secretsDir+"/"+agent+".env", "GITLAB_TOKEN")
}

// envValue pulls one KEY's value out of a KEY=VALUE env file, defensively:
// tolerates a leading `export `, surrounding whitespace, `#` comments, and
// single- or double-quoted values. Returns false if the file or key is absent.
func envValue(path, key string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if v == "" {
			return "", false
		}
		return v, true
	}
	return "", false
}

func noteExists(notes []ticketNote, text string) bool {
	for _, n := range notes {
		if n.Text == text {
			return true
		}
	}
	return false
}

func shortSHA(s string) string {
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}
