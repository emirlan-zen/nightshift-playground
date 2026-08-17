import { describe, expect, it } from "vitest";
import {
  compileTemplate,
  liveToDraft,
  nextMemberSlot,
  parseMember,
  templateToDraft,
  validateDraft,
  validateEmitters,
  worstCaseSessions,
} from "./model";
import type { Flow, FlowNodeDefinition, FlowTemplate } from "@/features/flows/api";

const defs = [
  { id: "plan" },
  { id: "implement" },
  { id: "attempt" },
  { id: "judge" },
] as unknown as FlowNodeDefinition[];

describe("graph model", () => {
  it("preserves repeated parallel roles and routes", () => {
    const t = {
      id: "x",
      name: "X",
      description: "",
      nodes: ["plan", "implement", "implement"],
      stages: [["plan"], ["implement", "implement"]],
      edges: [{ node: "implement", verdict: "needs-work" as const, append: ["amend"] }],
    };
    const d = templateToDraft(t);
    expect(d.nodes.map((n) => n.id)).toEqual([
      "h:0:0:plan",
      "h:1:0:implement",
      "h:1:1:implement",
      "r:0:0:amend",
    ]);
    expect(compileTemplate(t, d).stages).toEqual(t.stages);
  });
  it("flags an empty graph", () =>
    expect(validateDraft({ template: { id: "x" }, nodes: [], routes: [] }, [])[0].code).toBe(
      "empty-graph",
    ));
});

// ADR-0023: harness comparison rides member slots — `role#N` members share a role
// (and therefore a prompt revision) but carry their own runtime pin.
describe("member slots", () => {
  it("parses a slotted member down to its base role", () => {
    expect(parseMember("attempt")).toEqual({ role: "attempt" });
    expect(parseMember("attempt#2")).toEqual({ role: "attempt", slot: 2 });
    // Out of range and malformed suffixes are not slots — they stay whole ids so
    // validation reports them as unknown nodes instead of silently splitting.
    expect(parseMember("attempt#5")).toEqual({ role: "attempt#5" });
    expect(parseMember("attempt#")).toEqual({ role: "attempt#" });
  });

  it("hands out the next free slot within one stage", () => {
    expect(nextMemberSlot([], "attempt")).toBe("attempt");
    expect(nextMemberSlot(["attempt"], "attempt")).toBe("attempt#2");
    expect(nextMemberSlot(["attempt", "attempt#2"], "attempt")).toBe("attempt#3");
    expect(
      nextMemberSlot(["attempt", "attempt#2", "attempt#3", "attempt#4"], "attempt"),
    ).toBeNull();
  });

  const compare: FlowTemplate = {
    id: "compare-harness",
    name: "Compare harnesses",
    description: "",
    nodes: ["attempt", "attempt", "judge"],
    stages: [["attempt", "attempt#2"], ["judge"]],
    memberRuntime: {
      attempt: { executor: "claude" },
      "attempt#2": { executor: "codex", model: "gpt-5.6-sol", effort: "xhigh", minutes: 120 },
    },
  };

  it("resolves each member to its role, slot, and own runtime pin", () => {
    const d = templateToDraft(compare);
    expect(d.nodes.map((n) => n.id)).toEqual(["h:0:0:attempt", "h:0:1:attempt#2", "h:1:0:judge"]);
    expect(d.nodes[1]).toMatchObject({
      role: "attempt",
      member: "attempt#2",
      slot: 2,
      executor: "codex",
      effort: "xhigh",
      minutes: 120,
    });
    // The role stays the catalog id, so def/prompt lookup and validation are
    // unchanged by the suffix.
    expect(validateDraft(d, defs)).toEqual([]);
  });

  it("round-trips slotted members through compile", () => {
    const d = templateToDraft(compare);
    expect(compileTemplate(compare, d).stages).toEqual(compare.stages);
    expect(compileTemplate(compare, d).nodes).toEqual(["attempt", "attempt#2", "judge"]);
  });

  it("refuses an identical slot twice in a stage and a slot spread across stages", () => {
    const dup = templateToDraft({
      ...compare,
      stages: [["attempt", "attempt"], ["judge"]],
      nodes: [],
    });
    // Two plain members of one role stay legal — that is today's parallel pair.
    // Two identical slots are not: they collide on one memberRuntime key.
    expect(validateDraft(dup, defs)).toEqual([]);
    const twice = templateToDraft({
      ...compare,
      stages: [["attempt#2"], ["attempt#2"], ["judge"]],
      nodes: [],
    });
    expect(validateDraft(twice, defs).map((i) => i.code)).toContain("member-across-stages");
    const sameStage = templateToDraft({
      ...compare,
      stages: [["attempt#2", "attempt#2"], ["judge"]],
      nodes: [],
    });
    expect(validateDraft(sameStage, defs).map((i) => i.code)).toContain("duplicate-member");
  });
});

// ADR-0023: emitters are declared in the automation; a run's nodes record which
// emitter produced them.
describe("emitters", () => {
  it("annotates the declared emitter with its budget and fan-in", () => {
    const d = templateToDraft({
      id: "explore",
      name: "Explore",
      description: "",
      nodes: ["plan", "judge"],
      stages: [["plan"], ["judge"]],
      emitters: [{ node: "plan", max: 3, roles: ["attempt"], fanIn: "judge" }],
    });
    expect(d.nodes[0].emits).toEqual({ max: 3, roles: ["attempt"], fanIn: "judge" });
    expect(d.nodes[1].emits).toBeUndefined();
  });

  const explore: FlowTemplate = {
    id: "explore",
    name: "Explore",
    description: "",
    nodes: [],
    stages: [["plan"], ["judge"]],
    emitters: [{ node: "plan", max: 3, roles: ["attempt"], fanIn: "judge" }],
  };

  it("accepts a well-formed emitter and prices its worst case", () => {
    expect(validateEmitters(explore, defs)).toEqual([]);
    // 2 stage members + 3 emitted + 1 fan-in.
    expect(worstCaseSessions(explore)).toBe(6);
  });

  it("refuses the ways an emitter can be unrunnable", () => {
    const codes = (t: FlowTemplate) => validateEmitters(t, defs).map((i) => i.code);
    // Emission declared for a node the automation never runs.
    expect(codes({ ...explore, emitters: [{ ...explore.emitters![0], node: "ghost" }] })).toContain(
      "emitter-unknown-node",
    );
    expect(codes({ ...explore, emitters: [{ ...explore.emitters![0], max: 0 }] })).toContain(
      "emitter-max",
    );
    expect(codes({ ...explore, emitters: [{ ...explore.emitters![0], max: 9 }] })).toContain(
      "emitter-max",
    );
    expect(codes({ ...explore, emitters: [{ ...explore.emitters![0], roles: [] }] })).toContain(
      "emitter-no-roles",
    );
    expect(
      codes({ ...explore, emitters: [{ ...explore.emitters![0], roles: ["ghost"] }] }),
    ).toContain("emitter-unknown-role");
    // Depth stays 1: an emitted node may not itself be an emitter.
    expect(
      codes({
        ...explore,
        stages: [["plan"], ["attempt"], ["judge"]],
        emitters: [
          explore.emitters![0],
          { node: "attempt", max: 2, roles: ["implement"], fanIn: "judge" },
        ],
      }),
    ).toContain("emitter-depth");
    expect(codes({ ...explore, emitters: [{ ...explore.emitters![0], fanIn: "plan" }] })).toContain(
      "emitter-fanin-self",
    );
    expect(
      codes({ ...explore, emitters: [{ ...explore.emitters![0], fanIn: "ghost" }] }),
    ).toContain("emitter-unknown-fanin");
    expect(codes({ ...explore, emitters: [explore.emitters![0], explore.emitters![0]] })).toContain(
      "emitter-duplicate",
    );
    // The whole run — stages plus every emitter's widest group — must fit the cap.
    expect(
      codes({
        ...explore,
        stages: [["plan"], ["judge"], ["implement"], ["implement"], ["implement"]],
        emitters: [
          { node: "plan", max: 8, roles: ["attempt"], fanIn: "judge" },
          { node: "implement", max: 4, roles: ["attempt"], fanIn: "judge" },
        ],
      }),
    ).toContain("emitter-budget");
  });

  it("prices EVERY emitter occurrence against one shared budget", () => {
    const codes = (t: FlowTemplate) => validateEmitters(t, defs).map((i) => i.code);
    // Two emitters that each fit alone (3 + 8 + 1 = 12) but not together.
    const pair: FlowTemplate = {
      ...explore,
      stages: [["plan"], ["implement"], ["judge"]],
      emitters: [
        { node: "plan", max: 8, roles: ["attempt"], fanIn: "judge" },
        { node: "implement", max: 8, roles: ["attempt"], fanIn: "judge" },
      ],
    };
    expect(worstCaseSessions({ ...pair, emitters: [pair.emitters![0]] })).toBe(12);
    expect(worstCaseSessions(pair)).toBe(21);
    expect(codes(pair)).toContain("emitter-budget");
    // A ROLE-level spec governs every member of that role, so a compared pair
    // emits twice — the server counts occurrences the same way.
    const pairedMembers: FlowTemplate = {
      ...explore,
      stages: [["attempt", "attempt#2"], ["judge"], ["validate"]],
      emitters: [{ node: "attempt", max: 6, roles: ["implement"], fanIn: "integrate" }],
    };
    expect(worstCaseSessions(pairedMembers)).toBe(4 + 2 * 7);
    expect(codes(pairedMembers)).toContain("emitter-budget");
    // A member-slot spec governs only that member.
    const pinned: FlowTemplate = {
      ...pairedMembers,
      emitters: [{ node: "attempt#2", max: 6, roles: ["implement"], fanIn: "integrate" }],
    };
    expect(worstCaseSessions(pinned)).toBe(4 + 7);
    expect(codes(pinned)).not.toContain("emitter-budget");
  });

  // Go omits nothing now, but a payload that predates that (or any client that
  // falls back to the node index) drew a two-member FIRST stage as stages 0 and 1
  // — sequential, which is the opposite of a comparison.
  it("keeps a stage-zero comparison pair parallel", () => {
    const flow = {
      id: "flow-2",
      template: "compare-harness",
      automationRevision: "r1",
      stages: [["attempt", "attempt#2"], ["judge"]],
      nodeViews: [
        { id: "attempt", role: "attempt", name: "Attempt", state: "delivered", stage: 0 },
        { id: "attempt#2", role: "attempt", name: "Attempt #2", state: "running", stage: 0 },
        { id: "judge", role: "judge", name: "Judge", state: "queued", stage: 1 },
      ],
    } as unknown as Flow;
    expect(liveToDraft(flow).nodes.map((n) => n.stage)).toEqual([0, 0, 1]);
    // Same run, stage dropped from the payload: the declared stages still decide.
    const legacy = {
      ...flow,
      nodeViews: flow.nodeViews.map(({ ...n }) => {
        delete (n as { stage?: number }).stage;
        return n;
      }),
    } as unknown as Flow;
    expect(liveToDraft(legacy).nodes.map((n) => n.stage)).toEqual([0, 0, 1]);
  });

  it("carries emittedBy from a live run onto the graph", () => {
    const flow = {
      id: "flow-1",
      template: "explore",
      automationRevision: "r1",
      emitters: [{ node: "plan", max: 3, roles: ["attempt"], fanIn: "judge" }],
      nodeViews: [
        { id: "plan", role: "plan", name: "Plan", state: "delivered" },
        {
          id: "attempt#2",
          role: "attempt",
          name: "Attempt",
          state: "running",
          emittedBy: "plan",
          executor: "codex",
        },
      ],
    } as unknown as Flow;
    const d = liveToDraft(flow);
    expect(d.nodes[0]).toMatchObject({ emits: { max: 3, fanIn: "judge" } });
    expect(d.nodes[1]).toMatchObject({ emittedBy: "plan", slot: 2, executor: "codex" });
    expect(d.nodes[0].emittedBy).toBeUndefined();
  });
});
