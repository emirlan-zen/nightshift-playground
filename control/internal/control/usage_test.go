package control

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShortModelName(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":         "opus",
		"claude-fable-5":          "fable",
		"claude-sonnet-5":         "sonnet",
		"claude-haiku-4-5":        "haiku",
		"some-unknown-model-2026": "some-unknown-model-2026",
	}
	for in, want := range cases {
		if got := shortModelName(in); got != want {
			t.Errorf("shortModelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeUsageAggregates(t *testing.T) {
	// isolate home + companies for this test
	origHome, origCompanies := home, companies
	t.Cleanup(func() { home, companies = origHome, origCompanies })
	home = t.TempDir()
	companies = []string{"playground"}

	dir := projectDir("playground")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(bishkek)
	recent := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	old := now.AddDate(0, 0, -40).UTC().Format(time.RFC3339)

	// two in-window assistant turns, one out-of-window turn (ignored),
	// one non-assistant line (ignored).
	lines := "" +
		`{"type":"assistant","timestamp":"` + recent + `","message":{"model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":10}}}` + "\n" +
		`{"type":"assistant","timestamp":"` + recent + `","message":{"model":"claude-fable-5","usage":{"input_tokens":20,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}` + "\n" +
		`{"type":"user","timestamp":"` + recent + `","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","timestamp":"` + old + `","message":{"model":"claude-opus-4-8","usage":{"input_tokens":999,"output_tokens":999}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	r := computeUsage()

	if r.Totals.Messages != 2 {
		t.Errorf("messages = %d, want 2 (old + user line excluded)", r.Totals.Messages)
	}
	if r.Totals.In != 120 || r.Totals.Out != 55 || r.Totals.CacheRead != 1000 || r.Totals.CacheCreate != 10 {
		t.Errorf("totals = %+v, want in=120 out=55 cacheRead=1000 cacheCreate=10", r.Totals)
	}
	if want := int64(120 + 55 + 1000 + 10); r.Totals.Total != want {
		t.Errorf("total = %d, want %d", r.Totals.Total, want)
	}
	if len(r.Models) != 2 || r.Models[0].Model != "opus" {
		t.Errorf("models = %+v, want opus first then fable", r.Models)
	}
	if len(r.Agents) != 1 || r.Agents[0].Agent != "playground" {
		t.Errorf("agents = %+v, want [playground]", r.Agents)
	}
	if len(r.Days) != usageWindowDays {
		t.Errorf("days = %d, want %d (zero-filled window)", len(r.Days), usageWindowDays)
	}
}

func TestHandleUsageHTTP(t *testing.T) {
	origHome, origCompanies := home, companies
	t.Cleanup(func() {
		home, companies = origHome, origCompanies
		usageMu.Lock()
		usageCache = nil // don't leak this test's aggregate into another test's cache
		usageMu.Unlock()
	})
	home = t.TempDir()
	companies = []string{"playground"}
	usageMu.Lock()
	usageCache = nil // force a fresh compute
	usageMu.Unlock()

	dir := projectDir("playground")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().In(bishkek).Add(-time.Hour).UTC().Format(time.RFC3339)
	line := `{"type":"assistant","timestamp":"` + recent + `","message":{"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleUsage(rr, httptest.NewRequest("GET", "/api/usage", nil))
	if rr.Code != 200 {
		t.Fatalf("code %d body %s", rr.Code, rr.Body)
	}
	var got usageResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Totals.Messages != 1 || got.Totals.In != 10 || got.Totals.Out != 5 {
		t.Fatalf("unexpected usage totals: %+v", got.Totals)
	}
}
