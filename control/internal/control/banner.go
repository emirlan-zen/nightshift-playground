package control

// Report-banner compositor. agy paints a text-free collage "plate"
// (<id>.plate.png) during the night run; the operator's real numbers must never
// be trusted to a diffusion model, so all text is overlaid HERE, deterministically,
// from the report's own frontmatter. The plate reserves a clean left column +
// bottom band (see contract.md / COLLAGE.md); we draw the type there over a soft
// paper scrim so text can never collide with the art.
//
// Output is a flat PNG (not SVG) so it renders byte-identically on every phone —
// no @font-face-in-<img> flakiness. House faces are vendored (OFL) and embedded.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"

	"nightshift/control/fonts"
)

var (
	fontsOnce               sync.Once
	tfGrotesk, tfMono, tfMB *truetype.Font
)

func loadFonts() {
	fontsOnce.Do(func() {
		tfGrotesk, _ = truetype.Parse(fonts.SpaceGrotesk())
		tfMono, _ = truetype.Parse(fonts.JetBrainsMono())
		tfMB, _ = truetype.Parse(fonts.JetBrainsMonoBold())
	})
}

// House palette (matches DESIGN.md paper/ink; the plate paints the same cream so
// the scrim blends invisibly).
var (
	colPaper = color.RGBA{0xf1, 0xeb, 0xdf, 0xff}
	colInk   = color.RGBA{0x17, 0x14, 0x10, 0xff}
	colMut   = color.RGBA{0x6a, 0x65, 0x5a, 0xff}
)

// Outcome tone → accent. House discipline is "one accent"; tone only shifts its
// hue so a shipped night and a stalled night read differently at a glance.
func toneAccent(tone string) color.RGBA {
	switch strings.ToLower(strings.TrimSpace(tone)) {
	case "shipped", "productive", "built":
		return color.RGBA{0x1f, 0x7a, 0x4d, 0xff} // green
	case "partial", "late", "mixed":
		return color.RGBA{0xb8, 0x80, 0x1a, 0xff} // amber
	case "quiet", "vigil", "watch":
		return color.RGBA{0x5b, 0x64, 0x72, 0xff} // slate
	default: // stall, problem, fail, or unspecified
		return color.RGBA{0xc0, 0x39, 0x2b, 0xff} // crimson
	}
}

// bannerMeta is the deterministic text layer, parsed from report frontmatter.
type bannerMeta struct {
	kicker   string     // e.g. "// NIGHT RUN — 2026-07-05 · EXEC"
	headline string     // real outcome, e.g. "Five PRs shipped"
	stats    []statTick // label + value pairs, drawn as red-tick data ticks
	accent   color.RGBA
}

type statTick struct{ label, value string }

// hasBannerMeta cheaply reports whether a report carries banner frontmatter, so
// a quiet night (no agy plate) still gets a typographic card.
func hasBannerMeta(report []byte) bool {
	m := fmBlockRe.Find(report)
	return m != nil && bytes.Contains(m, []byte("banner_headline"))
}

// blankPaper synthesizes a cream "plate" for runs with no agy art: solid paper
// plus a faint tone-tinted rule down the right, so the typographic card isn't a
// bare rectangle.
func blankPaper(w, h int, accent color.RGBA) image.Image {
	dc := gg.NewContext(w, h)
	dc.SetColor(colPaper)
	dc.Clear()
	dc.SetRGBA255(int(accent.R), int(accent.G), int(accent.B), 26)
	dc.DrawRectangle(float64(w)*0.9, float64(h)*0.12, float64(w)*0.012, float64(h)*0.76)
	dc.Fill()
	return dc.Image()
}

var fmBlockRe = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---`)
var idDateWaveRe = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})-\d{4}-([a-z0-9]+)-`)

// parseBannerMeta reads the leading YAML-ish frontmatter of a report. Recognised
// keys: banner_wave, banner_tone, banner_headline, banner_stats
// ("Label Value | Label Value | …" — last space-separated token is the value).
// id supplies the date + a wave fallback when the frontmatter omits them.
func parseBannerMeta(id, report string) bannerMeta {
	fields := map[string]string{}
	if m := fmBlockRe.FindStringSubmatch(report); m != nil {
		for _, line := range strings.Split(m[1], "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			fields[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}

	date, wave := "", strings.ToUpper(fields["banner_wave"])
	if m := idDateWaveRe.FindStringSubmatch(id); m != nil {
		date = m[1] + "-" + m[2] + "-" + m[3]
		if wave == "" {
			wave = strings.ToUpper(m[4])
		}
	}
	kicker := "// NIGHT RUN"
	if date != "" {
		kicker += " — " + date
	}
	if wave != "" {
		kicker += " · " + wave
	}

	headline := fields["banner_headline"]
	if headline == "" {
		headline = "Night run complete"
	}

	var stats []statTick
	if raw := fields["banner_stats"]; raw != "" {
		for _, part := range strings.Split(raw, "|") {
			toks := strings.Fields(part)
			if len(toks) < 2 {
				continue
			}
			stats = append(stats, statTick{
				label: strings.ToUpper(strings.Join(toks[:len(toks)-1], " ")),
				value: toks[len(toks)-1],
			})
		}
	}

	return bannerMeta{kicker: kicker, headline: headline, stats: stats, accent: toneAccent(fields["banner_tone"])}
}

// composeBanner overlays the deterministic text layer onto the art plate. All
// geometry is a fraction of the plate size, so it works at any resolution agy
// returns. Text lives in the reserved left column (≤46% width) over a paper scrim.
func composeBanner(plate image.Image, m bannerMeta) image.Image {
	loadFonts()
	b := plate.Bounds()
	W, H := float64(b.Dx()), float64(b.Dy())
	dc := gg.NewContext(b.Dx(), b.Dy())
	dc.DrawImage(plate, 0, 0)

	// Paper scrim: fade cream from the left edge (opaque) to transparent by ~52%,
	// plus a bottom fade — guarantees legibility even if the art bleeds inward.
	// gg interpolates in premultiplied space, so transparent stops MUST be NRGBA
	// (a premultiplied RGBA with 0 alpha but non-zero RGB is invalid → glitch band).
	paperOpaque := color.NRGBA{colPaper.R, colPaper.G, colPaper.B, 0xff}
	paperClear := color.NRGBA{colPaper.R, colPaper.G, colPaper.B, 0}
	left := gg.NewLinearGradient(0, 0, W, 0)
	left.AddColorStop(0, paperOpaque)
	left.AddColorStop(0.40, paperOpaque)
	left.AddColorStop(0.52, paperClear)
	dc.SetFillStyle(left)
	dc.DrawRectangle(0, 0, W, H)
	dc.Fill()
	bot := gg.NewLinearGradient(0, H, 0, 0)
	bot.AddColorStop(0, paperOpaque)
	bot.AddColorStop(0.14, paperClear)
	dc.SetFillStyle(bot)
	dc.DrawRectangle(0, H*0.7, W*0.5, H*0.3)
	dc.Fill()

	marginX := W * 0.06
	maxTextW := W*0.46 - marginX
	accent := m.accent

	// Kicker (mono) + short accent underline, top-left.
	kickSize := H * 0.032
	kickTrack := kickSize * 0.14
	dc.SetFontFace(truetype.NewFace(tfMono, &truetype.Options{Size: kickSize}))
	dc.SetColor(color.RGBA{0x3a, 0x35, 0x2c, 0xff})
	drawTracked(dc, m.kicker, marginX, H*0.13, kickTrack)
	dc.SetColor(accent)
	dc.DrawRectangle(marginX, H*0.155, kickSize*3.4, H*0.006)
	dc.Fill()

	// Headline (Space Grotesk, faux-bold), wrapped, sitting above the stat row.
	headSize := H * 0.115
	dc.SetFontFace(truetype.NewFace(tfGrotesk, &truetype.Options{Size: headSize}))
	lines := wrapText(dc, m.headline, maxTextW)
	lineH := headSize * 1.02
	baseY := H*0.82 - lineH*float64(len(lines)-1)
	dc.SetColor(colInk)
	for i, ln := range lines {
		drawFauxBold(dc, ln, marginX, baseY+lineH*float64(i))
	}

	// Stat ticks (mono), bottom-left row: red top rule, uppercase label, bold value.
	if len(m.stats) > 0 {
		lblSize := H * 0.03
		valSize := H * 0.036
		lblTrack := lblSize * 0.14
		x := marginX
		y := H * 0.92
		for _, s := range m.stats {
			dc.SetFontFace(truetype.NewFace(tfMono, &truetype.Options{Size: lblSize}))
			lblW := trackedWidth(dc, s.label, lblTrack)
			dc.SetFontFace(truetype.NewFace(tfMB, &truetype.Options{Size: valSize}))
			valW, _ := dc.MeasureString(s.value + " ")
			cellW := lblW + valW
			// red tick over the cell
			dc.SetColor(accent)
			dc.DrawRectangle(x, y-valSize*1.15, cellW, H*0.007)
			dc.Fill()
			// value (accent, bold) then label (muted)
			dc.SetColor(accent)
			dc.DrawString(s.value, x, y)
			dc.SetFontFace(truetype.NewFace(tfMono, &truetype.Options{Size: lblSize}))
			dc.SetColor(colMut)
			drawTracked(dc, s.label, x+valW, y, lblTrack)
			x += cellW + W*0.03
		}
	}

	return dc.Image()
}

// drawFauxBold thickens the variable Space Grotesk (freetype renders its default
// weight) by stamping the glyphs with small offsets.
func drawFauxBold(dc *gg.Context, s string, x, y float64) {
	for _, d := range []struct{ dx, dy float64 }{{0, 0}, {0.7, 0}, {0, 0.7}, {0.7, 0.7}} {
		dc.DrawString(s, x+d.dx, y+d.dy)
	}
}

// wrapText greedily wraps s to at most maxW using the context's current face.
func wrapText(dc *gg.Context, s string, maxW float64) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if wdt, _ := dc.MeasureString(cur + " " + w); wdt <= maxW {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	return append(lines, cur)
}

// drawTracked renders s glyph-by-glyph with extra `track` px between glyphs —
// freetype faces expose no tracking, and inserting space runes renders as .notdef
// boxes in some faces, so we advance manually. Returns the end x.
func drawTracked(dc *gg.Context, s string, x, y, track float64) float64 {
	for _, r := range s {
		g := string(r)
		dc.DrawString(g, x, y)
		w, _ := dc.MeasureString(g)
		x += w + track
	}
	return x
}

// trackedWidth is drawTracked's advance without drawing.
func trackedWidth(dc *gg.Context, s string, track float64) float64 {
	var x float64
	for _, r := range s {
		w, _ := dc.MeasureString(string(r))
		x += w + track
	}
	return x
}

// encodePNG is a small helper so callers don't import image/png directly.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
