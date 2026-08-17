import { describe, expect, it } from "vitest";
import { decodeGraphState, encodeGraphState } from "./urlState";

describe("graph URL", () => {
  it("round trips and rejects stale revisions", () => {
    const d = {
      template: { id: "x", revision: "r1" },
      nodes: [{ id: "h", role: "plan", stage: 0, order: 0, lane: "happy" as const }],
      routes: [],
    };
    const raw = encodeGraphState(d);
    expect(decodeGraphState(raw, "x", "r1")?.nodes[0].role).toBe("plan");
    expect(decodeGraphState(raw, "x", "r2")).toBeNull();
  });

  it("restores a draft that references a custom node (unknown to the catalog here)", () => {
    const d = {
      template: { id: "x", revision: "r1" },
      nodes: [{ id: "h", role: "security-audit", stage: 0, order: 0, lane: "happy" as const }],
      routes: [],
    };
    const raw = encodeGraphState(d);
    expect(decodeGraphState(raw, "x", "r1")?.nodes[0].role).toBe("security-audit");
  });
});
