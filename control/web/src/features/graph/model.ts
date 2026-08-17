import type { Flow, FlowNodeDefinition, FlowTemplate } from "@/features/flows/api";
import type { GraphDraft, GraphIssue, GraphNodeView } from "./types";

export const stagesOf = (t: FlowTemplate) =>
  t.stages?.length ? t.stages : (t.nodes ?? []).map((r) => [r]);

/** The automation's happy-path sequence, flattened. A stage-only automation has
 * no `nodes`, so every "list its steps" surface reads this instead. */
export const rolesOf = (t: FlowTemplate) => stagesOf(t).flat();

/** A stage member is `role` or `role#N` with N ∈ 2..4 — two members of the same
 * role in one stage, each with its own runtime pin (ADR-0023). `#` is illegal in
 * a role id, so the split is unambiguous. */
export const MEMBER_SLOT_RE = /^([a-z][a-z0-9-]{1,31})#([2-4])$/;
export function parseMember(member: string): { role: string; slot?: number } {
  const m = MEMBER_SLOT_RE.exec(member);
  return m ? { role: m[1], slot: Number(m[2]) } : { role: member };
}
/** The next free member string for `role` in a stage: role → role#2 → role#3… */
export function nextMemberSlot(stage: string[], role: string): string | null {
  if (!stage.includes(role)) return role;
  for (let slot = 2; slot <= 4; slot++)
    if (!stage.includes(`${role}#${slot}`)) return `${role}#${slot}`;
  return null;
}

export function templateToDraft(t: FlowTemplate): GraphDraft {
  const nodes: GraphNodeView[] = [];
  const emitterOf = (role: string) => (t.emitters ?? []).find((e) => e.node === role);
  stagesOf(t).forEach((stage, si) =>
    stage.forEach((member, oi) => {
      const { role, slot } = parseMember(member);
      const emitter = emitterOf(role);
      const runtime = t.memberRuntime?.[member];
      nodes.push({
        // Node ids keep the slot suffix, so a member's id (and its worktree
        // branch) stays unique within its stage.
        id: `h:${si}:${oi}:${member}`,
        role,
        member,
        slot,
        stage: si,
        order: oi,
        lane: "happy",
        executor: runtime?.executor,
        effort: runtime?.effort,
        minutes: runtime?.minutes,
        emits: emitter
          ? { max: emitter.max, roles: emitter.roles, fanIn: emitter.fanIn }
          : undefined,
      });
    }),
  );
  (t.edges ?? []).forEach((edge, ei) =>
    edge.append.forEach((role, oi) =>
      nodes.push({
        id: `r:${ei}:${oi}:${role}`,
        role,
        stage: Math.max(
          0,
          stagesOf(t).findIndex((s) => s.some((m) => parseMember(m).role === edge.node)),
        ),
        order: oi,
        lane: "route",
      }),
    ),
  );
  return { template: { id: t.id, revision: t.revision }, nodes, routes: t.edges ?? [] };
}
export function compileTemplate(base: FlowTemplate, draft: GraphDraft): FlowTemplate {
  const happy = draft.nodes.filter((n) => n.lane === "happy");
  const max = happy.reduce((m, n) => Math.max(m, n.stage), -1);
  const stages = Array.from({ length: max + 1 }, (_, stage) =>
    happy
      .filter((n) => n.stage === stage)
      .sort((a, b) => a.order - b.order)
      // Members round-trip verbatim — dropping the `#N` suffix would silently
      // collapse two compared members into one.
      .map((n) => n.member ?? n.role),
  ).filter((s) => s.length);
  return { ...base, stages, nodes: stages.flat(), edges: draft.routes };
}
export function validateDraft(d: GraphDraft, known: FlowNodeDefinition[]): GraphIssue[] {
  const issues: GraphIssue[] = [];
  const ids = new Set(known.map((n) => n.id));
  const happy = d.nodes.filter((n) => n.lane === "happy");
  if (!happy.length)
    issues.push({ code: "empty-graph", message: "Add at least one node", path: "stages" });
  const byStage = new Map<number, GraphNodeView[]>();
  happy.forEach((n) => byStage.set(n.stage, [...(byStage.get(n.stage) ?? []), n]));
  byStage.forEach((nodes, stage) => {
    if (nodes.length > 4)
      issues.push({
        code: "stage-too-wide",
        message: "Parallel stages support at most 4 nodes",
        path: `stages.${stage}`,
        stage,
      });
    // Two plain members of one role stay legal (that is today's parallel pair,
    // disambiguated by position). Two IDENTICAL SLOTS are not: they collide on
    // one `memberRuntime` key, so one pin would silently govern both.
    const seen = new Set<string>();
    nodes.forEach((n) => {
      if (!n.slot) return;
      const member = n.member ?? n.role;
      if (seen.has(member))
        issues.push({
          code: "duplicate-member",
          message: `Stage ${stage + 1} repeats ${member} — give the second member its own slot`,
          path: `stages.${stage}`,
          stage,
          role: n.role,
        });
      seen.add(member);
    });
  });
  // A slotted member belongs to exactly one stage: it exists to compare members
  // side by side, not to re-run the same pin later in the chain.
  const slotStages = new Map<string, Set<number>>();
  happy
    .filter((n) => n.slot)
    .forEach((n) =>
      slotStages.set(n.member!, (slotStages.get(n.member!) ?? new Set()).add(n.stage)),
    );
  slotStages.forEach((stages, member) => {
    if (stages.size > 1)
      issues.push({
        code: "member-across-stages",
        message: `${member} appears in ${stages.size} stages — a member slot belongs to one stage`,
        path: "stages",
        role: parseMember(member).role,
      });
  });
  d.nodes.forEach((n) => {
    if (!ids.has(n.role))
      issues.push({
        code: "unknown-node",
        message: `Unknown node ${n.role}`,
        path: "stages",
        role: n.role,
      });
  });
  return issues;
}
/** A run executes at most this many node sessions (ADR-0017/0023); emissions are
 * counted against the same budget, so an automation that cannot possibly fit is
 * refused while it is being edited rather than at 3am. */
export const RUN_SESSION_CAP = 16;
export const EMITTER_MAX_WIDTH = 8;
/** Client-side mirror of the server's emitter rules — same refusals, no round
 * trip. The server stays the authority; these only front-run its rejection. */
export function validateEmitters(
  t: FlowTemplate,
  known: FlowNodeDefinition[],
  sessionCap = RUN_SESSION_CAP,
): GraphIssue[] {
  const issues: GraphIssue[] = [];
  const ids = new Set(known.map((n) => n.id));
  const members = stagesOf(t).flat();
  const roles = new Set(members.map((m) => parseMember(m).role));
  const emitters = t.emitters ?? [];
  const emitterRoles = new Set(emitters.map((e) => e.node));
  const seen = new Set<string>();
  emitters.forEach((e, i) => {
    const path = `emitters.${i}`;
    if (!roles.has(e.node))
      issues.push({
        code: "emitter-unknown-node",
        message: `${e.node} emits but is not in this automation's stages`,
        path,
        role: e.node,
      });
    if (seen.has(e.node))
      issues.push({
        code: "emitter-duplicate",
        message: `${e.node} declares emission twice`,
        path,
        role: e.node,
      });
    seen.add(e.node);
    if (!Number.isInteger(e.max) || e.max < 1 || e.max > EMITTER_MAX_WIDTH)
      issues.push({
        code: "emitter-max",
        message: `${e.node} may emit between 1 and ${EMITTER_MAX_WIDTH} nodes`,
        path,
        role: e.node,
      });
    if (!e.roles.length)
      issues.push({
        code: "emitter-no-roles",
        message: `Choose which nodes ${e.node} may emit`,
        path,
        role: e.node,
      });
    e.roles.forEach((r) => {
      if (!ids.has(r))
        issues.push({
          code: "emitter-unknown-role",
          message: `Unknown node ${r} in ${e.node}'s emitted roles`,
          path,
          role: e.node,
        });
      // Depth stays 1: an emitted node may never emit again, and that is checked
      // statically, not only when a report arrives.
      else if (emitterRoles.has(r))
        issues.push({
          code: "emitter-depth",
          message: `${r} is itself an emitter — an emitted node may not emit again`,
          path,
          role: e.node,
        });
    });
    if (!ids.has(e.fanIn))
      issues.push({
        code: "emitter-unknown-fanin",
        message: `Unknown fan-in node ${e.fanIn}`,
        path,
        role: e.node,
      });
    else if (e.fanIn === e.node)
      issues.push({
        code: "emitter-fanin-self",
        message: `${e.node} cannot fan in its own group`,
        path,
        role: e.node,
      });
  });
  const worst = worstCaseSessions(t);
  if (worst > sessionCap)
    issues.push({
      code: "emitter-budget",
      message: `Worst case ${worst} sessions exceeds the ${sessionCap}-session run budget`,
      path: "emitters",
    });
  return issues;
}
/** How many stage members one emitter spec governs. A spec naming a bare role
 * governs EVERY member of it (`attempt` and `attempt#2` share the role — that is
 * what makes them comparable), so each occurrence can emit its own group; a spec
 * naming a member slot governs only that member. Mirrors emitterOccurrences in
 * the Go validator. */
export const emitterOccurrences = (node: string, members: string[]) =>
  members.filter((m) => m === node || parseMember(m).role === node).length;
/** Stage members plus, for EVERY emitter occurrence, its widest group and that
 * group's fan-in — the run's worst-case session count. */
export function worstCaseSessions(t: FlowTemplate): number {
  const members = stagesOf(t).flat();
  return (
    members.length +
    (t.emitters ?? []).reduce(
      (sum, e) => sum + emitterOccurrences(e.node, members) * (Math.max(0, e.max) + 1),
      0,
    )
  );
}
/** Run overlay: an edge is part of the path actually taken once its source has
 * delivered — the graph then reads "how far did the run get" at a glance. */
export const edgeTraveled = (sourceState?: string) => sourceState === "delivered";
export function liveToDraft(flow: Flow): GraphDraft {
  const emitterOf = (role: string) => (flow.emitters ?? []).find((e) => e.node === role);
  // The server always serializes `stage` now; a payload that predates that (or a
  // hand-written one) falls back to the run's own stage list BEFORE the node's
  // index — reading index-as-stage drew a two-member first stage as two
  // sequential stages, which is exactly the comparison shape.
  const stageOf = (n: Flow["nodeViews"][number], i: number) => {
    if (typeof n.stage === "number") return n.stage;
    const declared = (flow.stages ?? []).findIndex((s) =>
      s.some((m) => m === n.id || parseMember(m).role === n.role),
    );
    return declared >= 0 ? declared : i;
  };
  return {
    template: { id: flow.template, revision: flow.automationRevision },
    routes: flow.edges ?? [],
    nodes: flow.nodeViews.map((n, i) => {
      const emitter = emitterOf(n.role);
      return {
        id: n.id,
        role: n.role,
        // Run node ids carry the member slot; keep it so the card can mark which
        // member of a compared pair this is.
        slot: parseMember(n.id.split(":").pop() ?? "").slot,
        stage: stageOf(n, i),
        order: i,
        lane: n.round ? "route" : "happy",
        state: n.state,
        verdict: n.verdict || undefined,
        round: n.round,
        // Parallel members share a role but carry distinct names — the graph
        // must label (and announce) each with its own name, not the catalog's.
        label: n.name,
        // Executor identity (claude/codex) so the run graph can mark who ran the node.
        executor: n.executor,
        // ADR-0023: a node the run created at runtime, and the emitter that did it.
        emittedBy: n.emittedBy || undefined,
        emits: emitter
          ? { max: emitter.max, roles: emitter.roles, fanIn: emitter.fanIn }
          : undefined,
      };
    }),
  };
}
