import type { GraphDraft } from "./types";

type State = {
  v: 1;
  template: string;
  revision: string;
  nodes: GraphDraft["nodes"];
  routes: GraphDraft["routes"];
  viewport: { x: number; y: number; zoom: number };
};

export function encodeGraphState(d: GraphDraft, viewport = { x: 0, y: 0, zoom: 1 }) {
  const s: State = {
    v: 1,
    template: d.template.id,
    revision: d.template.revision ?? "new",
    nodes: d.nodes,
    routes: d.routes,
    viewport,
  };
  return btoa(unescape(encodeURIComponent(JSON.stringify(s))))
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "");
}

// Decode restores the in-progress graph on a phone refresh (URL state). It does
// NOT reject unknown roles: a draft may reference a custom node created this
// session, and the catalog isn't available here. Structural validity is checked;
// unknown roles are surfaced later by validateDraft against the loaded catalog.
export function decodeGraphState(
  raw: string | null,
  template: string,
  revision: string,
): State | null {
  try {
    if (!raw || raw.length > 16384) return null;
    const json = decodeURIComponent(escape(atob(raw.replaceAll("-", "+").replaceAll("_", "/"))));
    if (json.length > 12288) return null;
    const s = JSON.parse(json) as State;
    if (
      s.v !== 1 ||
      s.template !== template ||
      s.revision !== revision ||
      !Array.isArray(s.nodes) ||
      s.nodes.length > 32 ||
      s.nodes.some(
        (n) => typeof n.role !== "string" || !n.role || ![n.stage, n.order].every(Number.isFinite),
      )
    )
      return null;
    return s;
  } catch {
    return null;
  }
}
