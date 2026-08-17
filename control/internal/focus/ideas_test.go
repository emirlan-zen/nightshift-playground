package focus

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ideasStore(t *testing.T) (*Store, *Handler) {
	t.Helper()
	home := t.TempDir()
	store := NewStore(home, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := store.Save("products", "# bets\n"); err != nil {
		t.Fatal(err)
	}
	writeIdea := func(name, body string) {
		p := filepath.Join(home, ".nightshift", "research", "ideas", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeIdea("2026-07-10.md", "# Older idea\n\nsome body\n")
	writeIdea("2026-07-11.md", "# Newest idea\n\nbetter body\n")
	writeIdea("notes.txt", "not markdown, must be ignored\n")
	return store, NewHandler(store)
}

func TestIdeasListNewestFirstWithTitles(t *testing.T) {
	store, _ := ideasStore(t)
	got := store.Ideas()
	if len(got.Files) != 2 {
		t.Fatalf("want 2 ideas (txt ignored), got %d: %+v", len(got.Files), got.Files)
	}
	if got.Files[0].ID != "2026-07-11" || got.Files[0].Title != "Newest idea" {
		t.Fatalf("newest-first/title broken: %+v", got.Files[0])
	}
	if got.Files[0].ModifiedAt == 0 {
		t.Fatalf("missing mtime: %+v", got.Files[0])
	}
}

func TestIdeaBodyAndUnknownRejected(t *testing.T) {
	store, _ := ideasStore(t)
	body, err := store.Idea("2026-07-10")
	if err != nil || !strings.Contains(body.Content, "some body") || body.Title != "Older idea" {
		t.Fatalf("idea body = %+v err = %v", body, err)
	}
	// Unknown id, a path-traversal attempt, and the ignored .txt must all 404.
	for _, bad := range []string{"nope", "../focus/products", "..", "notes"} {
		if _, err := store.Idea(bad); err != ErrUnknownIdea {
			t.Fatalf("Idea(%q) err = %v, want ErrUnknownIdea", bad, err)
		}
	}
}

func TestIdeaRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	store, _ := ideasStore(t)
	ideasDir := store.ideasDir

	// A secret outside the backlog that no idea id must ever be able to read.
	secret := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(secret, []byte("# outside\n\ntop secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// (a) A direct symlink dropped into the ideas dir, pointing outside it.
	if err := os.Symlink(secret, filepath.Join(ideasDir, "leak.md")); err != nil {
		t.Fatal(err)
	}
	for _, f := range store.Ideas().Files {
		if f.ID == "leak" {
			t.Fatalf("symlink leaked into the listing: %+v", f)
		}
	}
	if body, err := store.Idea("leak"); err != ErrUnknownIdea {
		t.Fatalf("Idea(leak) = %+v err=%v, want ErrUnknownIdea (no secret)", body, err)
	}

	// (b) A directory named like an idea is not a regular file → ignored.
	if err := os.Mkdir(filepath.Join(ideasDir, "adir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if store.knownIdea("adir") {
		t.Fatal("a directory must not be a known idea")
	}
	if _, err := store.Idea("adir"); err != ErrUnknownIdea {
		t.Fatalf("Idea(adir) err=%v, want ErrUnknownIdea", err)
	}

	// (c) TOCTOU: a real file, known at list time, replaced by a symlink to the
	// secret before the read. The listing gate drops it, and — the point of the
	// O_NOFOLLOW open — the low-level reader refuses it directly too, so even a
	// swap in the list→open window can never follow the link out of the backlog.
	real := filepath.Join(ideasDir, "swap.md")
	if err := os.WriteFile(real, []byte("# real\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !store.knownIdea("swap") {
		t.Fatal("swap should be a known idea before the swap")
	}
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, real); err != nil {
		t.Fatal(err)
	}
	if !store.knownIdea("swap") {
		// listing correctly drops the now-symlink
	} else {
		t.Fatal("a symlink must not be a known idea after the swap")
	}
	if _, _, err := store.readIdeaFile("swap"); err == nil {
		t.Fatal("readIdeaFile followed a symlink — the O_NOFOLLOW open regressed")
	}

	// (d) A normal markdown file still reads fine (no false positives).
	if body, err := store.Idea("2026-07-11"); err != nil || !strings.Contains(body.Content, "better body") {
		t.Fatalf("normal idea broke: %+v err=%v", body, err)
	}
}

func TestPromoteAppendsToProductsAtomically(t *testing.T) {
	store, _ := ideasStore(t)
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	doc, err := store.Promote("2026-07-11", "worth a validation test", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.Content, "# bets\n") {
		t.Fatalf("existing content clobbered: %q", doc.Content)
	}
	for _, want := range []string{"## Newest idea", "worth a validation test", "2026-07-11", "research/ideas/2026-07-11.md"} {
		if !strings.Contains(doc.Content, want) {
			t.Fatalf("promoted products.md missing %q: %q", want, doc.Content)
		}
	}
	// The idea file is history — promotion must not touch it.
	if _, err := store.Idea("2026-07-11"); err != nil {
		t.Fatalf("idea file removed by promote: %v", err)
	}
	// Round-trips to disk through the atomic focus save.
	saved, _ := store.Get("products")
	if saved.Content != doc.Content {
		t.Fatalf("disk != returned doc")
	}
}

func TestPromoteUnknownIdIsNotFound(t *testing.T) {
	store, handler := ideasStore(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ideas/{id}/promote", handler.Promote)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ideas/does-not-exist/promote", strings.NewReader(`{"note":"x"}`)))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	// products.md untouched after a rejected promote.
	if doc, _ := store.Get("products"); doc.Content != "# bets\n" {
		t.Fatalf("products changed after rejection: %q", doc.Content)
	}
}

func TestPromoteHandlerWithEmptyBodyOK(t *testing.T) {
	_, handler := ideasStore(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ideas/{id}/promote", handler.Promote)
	rr := httptest.NewRecorder()
	// No request body at all — a note is optional.
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ideas/2026-07-10/promote", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body)
	}
	var doc Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Content, "## Older idea") {
		t.Fatalf("promote with no note failed to append: %q", doc.Content)
	}
}
