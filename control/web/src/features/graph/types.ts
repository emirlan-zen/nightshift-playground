import type { FlowTemplate, RouteEdge } from "@/features/flows/api";

export type GraphMode = "edit" | "template-readonly" | "run" | "profile-readonly";
export interface GraphNodeView {
  id: string;
  role: string;
  stage: number;
  order: number;
  lane: "happy" | "route";
  state?: string;
  verdict?: string;
  round?: number;
  /** Display name when the node isn't a catalog role (run views: parallel members carry their own names). */
  label?: string;
  /** Small live annotation under the state line (run views: the node's countdown). */
  sub?: string;
  /** Which engine runs this node — "claude" | "codex" (run views). Drives the executor icon. */
  executor?: string;
  /** The raw stage member this node came from: `role` or `role#N` (ADR-0023).
   * `role` is always the base role, so def/prompt lookups stay unchanged. */
  member?: string;
  /** Member slot from a `role#N` member (2..4); absent for a plain member. */
  slot?: number;
  /** Per-member runtime pins that override the definition's defaults. */
  effort?: string;
  minutes?: number;
  /** Declared emission budget when this node is an emitter (template views). */
  emits?: { max: number; roles: string[]; fanIn: string };
  /** Node id of the emitter that produced this node at runtime (run views). */
  emittedBy?: string;
}
export interface GraphDraft {
  template: Pick<FlowTemplate, "id" | "revision">;
  nodes: GraphNodeView[];
  routes: RouteEdge[];
}
export interface GraphIssue {
  code: string;
  message: string;
  path: string;
  role?: string;
  stage?: number;
  edge?: number;
}
