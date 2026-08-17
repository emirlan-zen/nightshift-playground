package control

import (
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// handleReportBanner composes a banner (synthesized-paper card when there's no
// plate), caches it atomically, serves a valid PNG, and reuses the cache on a
// second request without leaving any temp residue. The cache write is a
// unique-temp + rename (the handler is lock-free / concurrent), so this also
// asserts no <id>.banner.*.tmp is left behind.
func TestHandleReportBannerCachesAtomically(t *testing.T) {
	nightrunEnv(t, "playground")
	dir := reportsDir("playground")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "20260711-0800-flow-aa11"
	rep := filepath.Join(dir, id+".md")
	body := "---\nbanner_wave: exec\nbanner_tone: shipped\nbanner_headline: Six PRs\nbanner_stats: PRs 6\n---\n# report\n"
	if err := os.WriteFile(rep, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age the report so the freshly written cache is unambiguously newer.
	past := time.Now().Add(-10 * time.Second)
	_ = os.Chtimes(rep, past, past)

	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/report/banner?c=playground&id="+id, nil)
		handleReportBanner(rr, req)
		return rr
	}

	rr := call()
	if rr.Code != 200 {
		t.Fatalf("banner request: code = %d, body %q", rr.Code, rr.Body.String())
	}
	if _, err := png.Decode(rr.Body); err != nil {
		t.Fatalf("served banner is not a valid PNG: %v", err)
	}

	cache := filepath.Join(dir, id+".banner.png")
	f, err := os.Open(cache)
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if _, err := png.Decode(f); err != nil {
		t.Fatalf("cache is not a valid PNG (torn write?): %v", err)
	}
	f.Close()

	// No unique-temp residue after a successful rename.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, id+".banner.*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temp residue left behind: %v", leftovers)
	}

	// Second request serves the cache without recomposing (mtime unchanged).
	info1, _ := os.Stat(cache)
	if rr2 := call(); rr2.Code != 200 {
		t.Fatalf("second banner request: code = %d", rr2.Code)
	}
	info2, _ := os.Stat(cache)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("cache recomposed on a fresh hit (%v -> %v)", info1.ModTime(), info2.ModTime())
	}
}
