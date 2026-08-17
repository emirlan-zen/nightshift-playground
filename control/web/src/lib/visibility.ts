// Presentation mode ("banking-app" visibility): a one-tap eye toggle that hides
// every agent-scoped surface for agents the operator marked private, so the
// workbench can be shown to outsiders with only public agents (playground by
// default) on screen. Client-side only — data still flows from the API; this
// gates what renders. Enforced centrally in lib/queries.ts.
import { useSyncExternalStore } from "react";

const MODE_KEY = "ns.presentation";
const HIDDEN_KEY = "ns.hiddenAgents";
const DEFAULT_HIDDEN: string[] = [];

export interface VisibilityState {
  presenting: boolean;
  hidden: string[];
}

/** Agent identifiers come in composite forms — "workspace.2", "playground/exec",
 * "gov/playground/ratchet" — that all belong to one base agent. */
export function baseAgent(name: string): string {
  let n = name.trim().toLowerCase();
  if (n.startsWith("gov/")) n = n.slice(4);
  return n.split(/[/.]/, 1)[0] ?? n;
}

export function isAgentVisible(name: string, state: VisibilityState): boolean {
  if (!state.presenting) return true;
  return !state.hidden.includes(baseAgent(name));
}

function read(): VisibilityState {
  let hidden = DEFAULT_HIDDEN;
  try {
    const raw = localStorage.getItem(HIDDEN_KEY);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (Array.isArray(parsed)) hidden = parsed.filter((x): x is string => typeof x === "string");
    }
  } catch {
    // unreadable storage — fall back to defaults
  }
  return { presenting: localStorage.getItem(MODE_KEY) === "1", hidden };
}

let snapshot: VisibilityState = { presenting: false, hidden: DEFAULT_HIDDEN };
let hydrated = false;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function getSnapshot(): VisibilityState {
  if (!hydrated) {
    snapshot = read();
    hydrated = true;
  }
  return snapshot;
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function setPresenting(on: boolean) {
  try {
    localStorage.setItem(MODE_KEY, on ? "1" : "0");
  } catch {
    // storage full/blocked — state still updates for this tab
  }
  snapshot = { ...getSnapshot(), presenting: on };
  emit();
}

export function setAgentHidden(agent: string, hide: boolean) {
  const base = baseAgent(agent);
  const current = getSnapshot().hidden;
  const hidden = hide ? [...new Set([...current, base])] : current.filter((a) => a !== base);
  try {
    localStorage.setItem(HIDDEN_KEY, JSON.stringify(hidden));
  } catch {
    // storage full/blocked — state still updates for this tab
  }
  snapshot = { ...getSnapshot(), hidden };
  emit();
}

// Another tab (or device hand-off via synced browser) flipping the mode should
// reflect here without a reload.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key !== MODE_KEY && e.key !== HIDDEN_KEY && e.key !== null) return;
    snapshot = read();
    emit();
  });
}

export function useVisibility(): VisibilityState {
  return useSyncExternalStore(subscribe, getSnapshot);
}

/** Current state for non-React callers (and tests). */
export function getVisibility(): VisibilityState {
  return getSnapshot();
}

/** Detail-route guard: true when this agent's pages must not render. */
export function useAgentHidden(agent: string | undefined): boolean {
  const vis = useVisibility();
  return !!agent && !isAgentVisible(agent, vis);
}

/** Test hook: reset module state between cases. */
export function resetVisibilityForTests() {
  hydrated = false;
  snapshot = { presenting: false, hidden: DEFAULT_HIDDEN };
}
