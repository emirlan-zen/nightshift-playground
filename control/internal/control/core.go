// nightshift control plane: a tiny mobile-first web app to start/stop the
// per-company `claude-remote-control@<company>` systemd servers on demand, so
// they are NOT always-on. Auth is enforced twice: at the edge by Cloudflare
// Access, and here by verifying the signed `Cf-Access-Jwt-Assertion` header
// (so the app is safe even if reached directly on the box IP). All privileged
// work is delegated to /usr/local/bin/nightshift-rc via scoped sudoers.
package control

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---- config (env) ----------------------------------------------------------

var (
	home      = runtimeConfig.Home
	companies = runtimeConfig.Agents
	wrapper   = runtimeConfig.RCWrapper
)

func isCompany(c string) bool {
	for _, x := range companies {
		if x == c {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- prompt files (read-only viewer) ---------------------------------------
//
// Surfaces the layered instruction files each agent loads — global + workspace
// CLAUDE.md, the shared skills + settings.json, and each company's own
// CLAUDE.md. STRICTLY read-only and STRICTLY allowlisted: the browser never
// supplies a path, only an opaque id the server maps to a known absolute path.
// Without this, a `?path=` param would let anyone past Cloudflare Access read
// arbitrary files the `agent` user can reach (e.g. ~/.claude.json credentials).

type promptFile struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Label    string `json:"label"`
	Desc     string `json:"desc,omitempty"`
	Exists   bool   `json:"exists"`
	Editable bool   `json:"editable,omitempty"`
	Source   string `json:"source,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type promptGroup struct {
	Title string       `json:"title"`
	Files []promptFile `json:"files"`
}

type promptsDoc struct {
	Groups []promptGroup `json:"groups"`
}

// buildPrompts enumerates the known prompt files and returns both the rendered
// structure and the id->absolute-path allowlist. Rebuilt per request so skills
// added on the box show up without a restart. Only server-built paths ever
// enter the index; the client can pick an id but never a path.
func buildPrompts() (promptsDoc, map[string]string) {
	idx := map[string]string{}
	add := func(g *promptGroup, id, path, label, desc string) {
		st, err := os.Stat(path)
		ok := err == nil && !st.IsDir()
		g.Files = append(g.Files, promptFile{ID: id, Path: path, Label: label, Desc: desc, Exists: ok})
		if ok {
			idx[id] = path
		}
	}
	var doc promptsDoc

	shared := promptGroup{Title: "Shared (all agents)"}
	add(&shared, "global", filepath.Join(home, ".claude/CLAUDE.md"), "Global CLAUDE.md", "")
	add(&shared, "workspace", filepath.Join(home, "workspace/CLAUDE.md"), "Workspace CLAUDE.md", "")
	add(&shared, "settings", filepath.Join(home, ".claude/settings.json"), "settings.json", "")
	doc.Groups = append(doc.Groups, shared)

	// Skills: enumerate ONLY ~/.claude/skills/*/SKILL.md (the glob's `*` can't
	// span `/`, so a skill dir name can't traverse out).
	skills := promptGroup{Title: "Skills (all agents)"}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude/skills/*/SKILL.md"))
	sort.Strings(matches)
	for _, m := range matches {
		name := filepath.Base(filepath.Dir(m))
		add(&skills, "skill-"+name, m, name, skillDesc(m))
	}
	doc.Groups = append(doc.Groups, skills)

	// Task-agnostic flow node roles (ADR-0015; custom roles ADR-0017). These are
	// operator-editable session prompts composed with the per-flow goal and
	// upstream artifacts.
	nodes := promptGroup{Title: "Flow nodes"}
	for id, def := range nodeDefinitions {
		add(&nodes, "node-"+id, filepath.Join(nodesDir(), id+".md"), def.Name, def.Description)
	}
	for id, def := range loadCustomNodeDefs() {
		add(&nodes, "node-"+id, filepath.Join(nodesDir(), id+".md"), def.Name, def.Description)
	}
	sort.Slice(nodes.Files, func(i, j int) bool { return nodes.Files[i].Label < nodes.Files[j].Label })
	for i := range nodes.Files {
		f := &nodes.Files[i]
		f.Editable = true
		role := strings.TrimPrefix(f.ID, "node-")
		if b, err := os.ReadFile(f.Path); err == nil {
			f.Revision = promptRevision(string(b))
			f.Source = "built-in"
			if string(b) != defaultNodeFile(role) {
				f.Source = "custom"
			}
		}
	}
	doc.Groups = append(doc.Groups, nodes)

	// Per-agent: each company's own CLAUDE.md (the only layer that differs).
	for _, c := range companies {
		g := promptGroup{Title: c}
		add(&g, "company-"+c, filepath.Join(home, "workspace", c, "CLAUDE.md"), c+" CLAUDE.md", "")
		doc.Groups = append(doc.Groups, g)
	}
	return doc, idx
}

// skillDesc pulls the one-line `description:` from a SKILL.md YAML frontmatter.
func skillDesc(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan() && i < 60; i++ {
		line := strings.TrimSpace(sc.Text())
		if d, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(d)
		}
	}
	return ""
}

func handlePrompts(w http.ResponseWriter, _ *http.Request) {
	doc, _ := buildPrompts()
	writeJSON(w, doc)
}

func handlePrompt(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	_, idx := buildPrompts()
	path, ok := idx[id]
	if !ok {
		http.Error(w, "unknown prompt id", http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}
