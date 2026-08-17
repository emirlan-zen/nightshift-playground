# COLLAGE.md — night-run banner art style (codex art plates)

Scope: **only the art plates for night-run report banners.** This is a deliberate
departure from `DESIGN.md`'s Swiss minimalism — a report banner is a keepsake the
operator opens each morning, so it earns a richer, hand-made look. Everything else
(web, previews, dashboards, og-images) stays on `DESIGN.md`.

## The two-layer rule (why this file exists)

You paint a **text-free art plate**. The control plane overlays the kicker,
headline and numbers **for you**, deterministically, from your report frontmatter
(diffusion models garble numbers — so you never draw them). That means:

- **Render NO text, letters, numbers, or labels.** Imagery only. A single ghost
  monogram *letter* (the agent initial, e.g. a faint gold `P`) is the one allowed
  exception, as texture — never a word.
- **Reserve the text zone.** Compose ALL imagery in the **right ~58%** of the 16:9
  frame; leave the **left ~42%** and the **bottom ~18%** as clean, empty cream
  paper. The overlay lives there; art bleeding in gets covered by a paper scrim,
  but keep it clear so nothing important is lost.

## Look

Vintage **digital collage / mixed-media** — realistic photo + engraving cutouts,
layered by hand with **torn paper edges, soft drop shadows, and grain**. 1950s
editorial meets botanical/scientific engraving. Think a premium gin or apothecary
label. 16:9.

| Element | Value |
|---|---|
| Background | uniform cream textured paper (`#f1ebdf` — matches the overlay scrim) |
| Photo tones | muted sepia + desaturated teal, aged |
| Gold accent | ochre `#c8a23a` — one ghost monogram letter, small metallic objects |
| Red accent | crimson, used *sparingly* (a seal, a thread, a mark) |
| Depth | overlapping cutouts, laurel/botanical framing, a stray insect (moth/bee) |

One warm metal (gold) + one red. No purple, no neon, no flat vector.

## Motif per wave — match the imagery to the kind of work

| Wave | Motif cutouts |
|---|---|
| **medic** | apothecary bottles, anatomical heart, red-cross flag, a vintage ambulance, mortar & pestle |
| **steward** | ledgers, filing cards, brass keys, a feather pen reconciling columns |
| **plan** | old maps, a compass & dividers, drafting tools, a blueprint fragment |
| **research** | brass telescope, star charts, specimen jars, a magnifier over notes |
| **exec** | a cargo ship & dock crane, brass gears, a pocket watch, laurel (shipped) |
| **review** | scales of justice, a loupe, red proof-marks, stacked galleys |
| **synth** | sunrise over rooftops, a printing press, a coffee cup, a folded newspaper |
| **sweep** (company) | a night sky, a lantern, tidy tools, a moon over a workbench |

## Tone shifts the mood (the overlay also recolours the accent)

- **shipped** — triumphant: laurels, full crates, warm light.
- **partial / late** — in-progress: half-built, tools mid-use, dusk.
- **quiet / vigil** — sparse, moonlit, a single watching object. (Usually you skip
  the plate entirely on a quiet night — the plain typographic card is enough.)
- **stall / problem** — faded, torn, an empty frame, crimson dominant.

## Budget

Plates are painted with `codex` image generation (ADR-0018 — this replaced `agy`
for banners; the invocation recipe lives in the contract's banner section):
**≤2 banner images per night, synth wave only** (one attempt + at most one
retry). If it fails or drags past ~5 minutes, skip the plate — the typographic
card renders from frontmatter regardless. Never let the banner delay the report.
(The separate ≤12 `agy` images/night budget covers non-banner image work —
mockups, preview assets, diagrams.)
