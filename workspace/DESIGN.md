# DESIGN.md — house visual style (all agents)

The current house style for anything visual you build: web frontends, preview
sites, landing pages, dashboards, README banners, og-images, report banners,
slides, generated assets (`agy`). It is derived from a reference site the
operator likes (sweepo.net, mid-2026) — Swiss-technical, monochrome, one red
accent. Follow it by default; the escape hatch at the bottom says when not to.

## Tokens

| Token | Value | Use |
|---|---|---|
| paper | `#fafafa` | page background (never pure white) |
| ink | `#0a0a0a` | text, borders, solid sections (never pure black `#000`) |
| gray-100 | `#f5f5f5` | subtle fills, alternating rows |
| gray-300 | `#d4d4d4` | hairline borders, dividers |
| gray-500 | `#737373` | secondary text, captions |
| gray-800 | `#262626` | text on light gray, dark-section fills |
| **red** | `#e63946` | THE accent. Links on hover, key numbers, active states, one highlighted word in a heading. Never backgrounds, never body text. |

One accent color. If you're reaching for a second hue, you're off-style.

## Type

| Role | Font | How |
|---|---|---|
| Headings | **Space Grotesk** 500/700 | tight, large, no letter-spacing tricks |
| Body | **Inter** 400/500/600 | 16px base, 1.6 line-height, max ~70ch |
| Labels / kickers / code | **JetBrains Mono** 400/500 | small, `text-transform: uppercase`, `letter-spacing: 0.05em+`, gray-500 or red |

Load from Google Fonts (or self-host on previews). The mono-uppercase kicker
above a heading ("// NIGHT REPORT", "01 — SCAN") is the signature move — use it.

## Layout & surfaces

- Narrow content column (`max-width: 48rem` for prose; wider only for tables/dashboards).
- Sharp edges: no rounding by default; `border-radius` ≤ 8px where unavoidable (cards, code blocks).
- Borders do the work shadows usually do: `1px solid` gray-300 for structure,
  `2–4px solid` ink for emphasis (top-rules on sections, framed stat boxes).
- Shadows rare and flat — one soft `shadow` on floating elements max.
- Solid-ink sections for contrast: a full-width `#0a0a0a` band with paper text
  for heroes, CTAs, footers. Red accent pops hardest there.
- Generous whitespace between sections; dense inside tables.

## Motion & components

- Motion minimal: 150–200ms ease on hover (color, small translate). No parallax,
  no scroll-jacking, no springy easings.
- Buttons: rectangular, ink background + paper text (or 2px ink border, ghost);
  hover inverts or shifts to red. Mono-uppercase label optional.
- Tables/stat blocks: mono numbers, hairline rules, red for the one number that matters.
- Icons: simple line icons or none — prefer typographic markers (`→`, `//`, `01`).

## Anti-patterns (never)

Purple/blue gradients on dark, glassmorphism, neon glows, rounded-2xl-everything,
Inter-for-headings sameness, emoji as icons, three-plus accent colors, centered
italic hero taglines, stock illustration packs.

## Escape hatch

The concept of the work wins over the house style. If a project has its own
established brand (a branded product, a game with its own art
direction), follow that instead — this file styles *unbranded* and *new*
things. If the medium fights the tokens (e.g. a game needs color; a data viz
needs a sequential palette), keep the *structure* rules (type roles, spacing,
one-accent discipline, mono labels) and swap the palette deliberately —
mention the deviation in your report. When in doubt: monochrome + one accent,
and say what you chose.
