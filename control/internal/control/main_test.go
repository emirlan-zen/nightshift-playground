package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPromptsAllowlist(t *testing.T) {
	dir := t.TempDir()
	home = dir
	companies = []string{"agent-a", "playground"}
	must := func(p, body string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(dir, ".claude/CLAUDE.md"), "global")
	must(filepath.Join(dir, "workspace/CLAUDE.md"), "ws")
	must(filepath.Join(dir, ".claude/settings.json"), "{}")
	must(filepath.Join(dir, ".claude/skills/demo/SKILL.md"), "---\ndescription: a demo skill\n---\nbody")
	must(filepath.Join(dir, "workspace/playground/CLAUDE.md"), "playground")
	// agent-a CLAUDE.md intentionally absent -> should be listed but not in idx

	doc, idx := buildPrompts()

	for _, id := range []string{"global", "workspace", "settings", "skill-demo", "company-playground"} {
		if _, ok := idx[id]; !ok {
			t.Errorf("expected %q in allowlist", id)
		}
	}
	// missing file: present in the rendered doc, absent from the read allowlist
	if _, ok := idx["company-agent-a"]; ok {
		t.Error("missing company-agent-a CLAUDE.md must not be readable")
	}
	// arbitrary / traversal ids are never resolvable
	for _, bad := range []string{"", "../etc/passwd", "skill-../../foo", "company-../agent-a"} {
		if _, ok := idx[bad]; ok {
			t.Errorf("id %q must not resolve", bad)
		}
	}
	if d := docFind(doc, "Skills (all agents)", "skill-demo"); d != "a demo skill" {
		t.Errorf("skill desc = %q, want %q", d, "a demo skill")
	}
}

// docFind returns the Desc of a file by group title + id, or "" if absent.
func docFind(doc promptsDoc, group, id string) string {
	for _, g := range doc.Groups {
		if g.Title != group {
			continue
		}
		for _, f := range g.Files {
			if f.ID == id {
				return f.Desc
			}
		}
	}
	return ""
}

func TestIsCompany(t *testing.T) {
	companies = []string{"agent-a", "agent-b"}
	for _, c := range []string{"agent-a", "agent-b"} {
		if !isCompany(c) {
			t.Errorf("%s should be a company", c)
		}
	}
	for _, c := range []string{"", "evil", "agent-a ", "../etc"} {
		if isCompany(c) {
			t.Errorf("%q should NOT be a company", c)
		}
	}
}
