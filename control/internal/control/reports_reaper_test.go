package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReapReportsRemovesOnlyOldRecognizedArtifacts(t *testing.T) {
	nightrunEnv(t, "playground")
	prevDev := devMode
	devMode = false
	t.Cleanup(func() { devMode = prevDev })

	dir := reportsDir("playground")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-reportReapAfter - 24*time.Hour) // safely past the cutoff
	fresh := now.Add(-1 * time.Hour)

	write := func(name string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Old, recognized (a full newRunID stem + every known variant) → reaped.
	const id = "20260101-0000-flow-a1c2" // <date>-<time>-<kind>-<4 hex>
	oldReport := write(id+".md", old)
	oldLast := write(id+".last.md", old)
	oldPlate := write(id+".plate.png", old)
	oldBanner := write(id+".banner.png", old)
	oldLegacyPng := write(id+".png", old)
	oldAuthfail := write(id+".authfail", old)
	oldForgefail := write(id+".forgefail", old)
	oldDepskip := write(id+".depskip", old)
	oldWatchdog := write(id+".watchdog", old)
	// A multi-segment kind (plan-products) is still a valid run id.
	oldMultiKind := write("20260101-0000-plan-products-9f0e.md", old)

	// Old but NOT a report artifact → kept. Deletion is irreversible, so a
	// hand-dropped file that merely shares an extension must survive.
	keepReadme := write("README.md", old)            // the review's headline case
	keepNotes := write("notes.md", old)              // arbitrary markdown
	keepImage := write("logo.png", old)              // arbitrary image
	keepBadStem := write("20260101-0000-a1.md", old) // right extension, stem lacks the kind-<4 hex> tail

	// Fresh, recognized → kept (young enough, regardless of name).
	freshReport := write("20260710-2300-exec-b2d3.md", fresh)
	freshBanner := write("20260710-2300-exec-b2d3.banner.png", fresh)

	// A directory whose name matches the grammar is never reaped.
	oldDir := filepath.Join(dir, "20260101-0000-flow-d4f5.md")
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldDir, old, old); err != nil {
		t.Fatal(err)
	}

	// A symlink named exactly like an old report must be ignored entirely: the
	// link is preserved and its target (here outside the reports dir) is never
	// unlinked. Proves the reaper neither follows nor removes symlinks.
	linkTarget := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(linkTarget, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(linkTarget, old, old); err != nil {
		t.Fatal(err)
	}
	oldLink := filepath.Join(dir, "20260101-0000-flow-c3e4.md")
	if err := os.Symlink(linkTarget, oldLink); err != nil {
		t.Fatal(err)
	}

	reapReportsTick(now)

	gone := []string{
		oldReport, oldLast, oldPlate, oldBanner, oldLegacyPng,
		oldAuthfail, oldForgefail, oldDepskip, oldWatchdog, oldMultiKind,
	}
	for _, p := range gone {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected reaped: %s (err=%v)", filepath.Base(p), err)
		}
	}
	kept := []string{
		keepReadme, keepNotes, keepImage, keepBadStem, freshReport, freshBanner,
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected kept: %s (err=%v)", filepath.Base(p), err)
		}
	}
	// The directory and the symlink (plus its target) all survive.
	if _, err := os.Stat(oldDir); err != nil {
		t.Errorf("expected dir kept: %v", err)
	}
	if _, err := os.Lstat(oldLink); err != nil {
		t.Errorf("expected symlink kept: %v", err)
	}
	if _, err := os.Stat(linkTarget); err != nil {
		t.Errorf("expected symlink target kept: %v", err)
	}
}

func TestReapReportsSkipsInDevMode(t *testing.T) {
	nightrunEnv(t, "playground")
	prevDev := devMode
	devMode = true
	t.Cleanup(func() { devMode = prevDev })

	dir := reportsDir("playground")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "20260101-0000-a1.md")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-reportReapAfter - 24*time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	reapReportsTick(time.Now())
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("dev mode must not reap: %v", err)
	}
}
