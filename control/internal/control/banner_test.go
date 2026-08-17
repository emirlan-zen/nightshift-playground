package control

import (
	"bytes"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"testing"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
)

func TestParseBannerMeta(t *testing.T) {
	report := `---
banner_wave: exec
banner_tone: shipped
banner_headline: Five PRs shipped
banner_stats: PRs 5 | Tickets 4 | Alerts 0
---
# Morning brief
body...`
	m := parseBannerMeta("20260705-0800-exec-32de", report)

	if m.headline != "Five PRs shipped" {
		t.Errorf("headline = %q", m.headline)
	}
	if m.kicker != "// NIGHT RUN — 2026-07-05 · EXEC" {
		t.Errorf("kicker = %q", m.kicker)
	}
	if len(m.stats) != 3 {
		t.Fatalf("stats = %d, want 3", len(m.stats))
	}
	if m.stats[0].label != "PRS" || m.stats[0].value != "5" {
		t.Errorf("stat[0] = %+v", m.stats[0])
	}
	if m.stats[1].label != "TICKETS" || m.stats[1].value != "4" {
		t.Errorf("stat[1] = %+v", m.stats[1])
	}
	if m.accent != (color.RGBA{0x1f, 0x7a, 0x4d, 0xff}) {
		t.Errorf("shipped accent = %+v", m.accent)
	}
}

func TestParseBannerMetaMultiWordLabel(t *testing.T) {
	m := parseBannerMeta("20260705-0800-plan-ab12", "---\nbanner_stats: Draft PRs 3 | Ideas 4\n---\n")
	if len(m.stats) != 2 || m.stats[0].label != "DRAFT PRS" || m.stats[0].value != "3" {
		t.Fatalf("multi-word label parse: %+v", m.stats)
	}
}

func TestParseBannerMetaFallbacks(t *testing.T) {
	// No frontmatter: wave from id, default headline, default (crimson) accent.
	m := parseBannerMeta("20260704-0520-medic-09cf", "no frontmatter here")
	if m.kicker != "// NIGHT RUN — 2026-07-04 · MEDIC" {
		t.Errorf("fallback kicker = %q", m.kicker)
	}
	if m.headline != "Night run complete" {
		t.Errorf("fallback headline = %q", m.headline)
	}
	if m.accent != toneAccent("") {
		t.Errorf("fallback accent should be default")
	}
}

func TestToneAccentDistinct(t *testing.T) {
	tones := []string{"shipped", "partial", "quiet", "stall"}
	seen := map[color.RGBA]bool{}
	for _, tn := range tones {
		seen[toneAccent(tn)] = true
	}
	if len(seen) != 4 {
		t.Errorf("tones should map to 4 distinct accents, got %d", len(seen))
	}
}

func TestComposeBannerProducesImage(t *testing.T) {
	plate := image.NewRGBA(image.Rect(0, 0, 1408, 792))
	m := parseBannerMeta("20260705-0800-exec-32de",
		"---\nbanner_tone: shipped\nbanner_headline: Five PRs shipped\nbanner_stats: PRs 5 | Tickets 4\n---\n")
	img := composeBanner(plate, m)
	if img.Bounds().Dx() != 1408 || img.Bounds().Dy() != 792 {
		t.Fatalf("output dims = %v, want 1408x792", img.Bounds())
	}
	if _, err := encodePNG(img); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestHasBannerMeta(t *testing.T) {
	yes := []byte("---\nbanner_headline: X\n---\nbody")
	no1 := []byte("---\nbanner_wave: exec\n---\nbody") // no headline key
	no2 := []byte("# just a report\nbanner_headline in prose, no frontmatter")
	if !hasBannerMeta(yes) {
		t.Error("should detect frontmatter headline")
	}
	if hasBannerMeta(no1) {
		t.Error("frontmatter without headline should be false")
	}
	if hasBannerMeta(no2) {
		t.Error("headline only in body should be false")
	}
}

func TestComposeQuietCard(t *testing.T) {
	// No plate → synthesized paper card still produces a valid image.
	m := parseBannerMeta("20260705-0800-research-aa11",
		"---\nbanner_tone: quiet\nbanner_headline: Quiet vigil\nbanner_stats: Ideas 3\n---\n")
	img := composeBanner(blankPaper(1408, 792, m.accent), m)
	if img.Bounds().Dx() != 1408 {
		t.Fatalf("dims %v", img.Bounds())
	}
	if _, err := encodePNG(img); err != nil {
		t.Fatal(err)
	}
}

func TestWrapText(t *testing.T) {
	loadFonts()
	dc := gg.NewContext(100, 100)
	dc.SetFontFace(truetype.NewFace(tfGrotesk, &truetype.Options{Size: 40}))
	lines := wrapText(dc, "one two three four five six seven eight nine", 200)
	if len(lines) < 2 {
		t.Errorf("expected wrapping, got %d lines", len(lines))
	}
	if got := wrapText(dc, "short", 9999); len(got) != 1 || got[0] != "short" {
		t.Errorf("single-line wrap = %v", got)
	}
}

// TestComposeVisual writes a composed banner from a real agy plate when one is
// present, for eyeballing. Skips in CI where the plate isn't checked in.
func TestComposeVisual(t *testing.T) {
	platePath := os.Getenv("BANNER_PLATE")
	if platePath == "" {
		t.Skip("set BANNER_PLATE to a plate png to dump a visual")
	}
	m := parseBannerMeta("20260705-0800-exec-32de",
		"---\nbanner_tone: shipped\nbanner_headline: Five PRs shipped\nbanner_stats: PRs 5 | Tickets 4 | Alerts 0\n---\n")
	if v := os.Getenv("BANNER_META"); v != "" {
		m = parseBannerMeta(os.Getenv("BANNER_ID"), v)
	}
	var plate image.Image
	if pb, err := os.ReadFile(platePath); err == nil {
		plate, _, _ = image.Decode(bytes.NewReader(pb))
	}
	if plate == nil {
		plate = blankPaper(1408, 792, m.accent)
	}
	b, err := encodePNG(composeBanner(plate, m))
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("BANNER_OUT")
	if out == "" {
		out = "banner-visual.png"
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(b))
}
