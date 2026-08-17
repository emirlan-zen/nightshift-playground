import { describe, it, expect, beforeEach } from "vitest";
import {
  baseAgent,
  getVisibility,
  isAgentVisible,
  setAgentHidden,
  setPresenting,
  resetVisibilityForTests,
  type VisibilityState,
} from "./visibility";

beforeEach(() => {
  localStorage.clear();
  resetVisibilityForTests();
});

describe("baseAgent", () => {
  it.each([
    ["playground", "playground"],
    ["agent-b.2", "agent-b"],
    ["agent-a/plan", "agent-a"],
    ["playground/exec", "playground"],
    ["gov/playground/ratchet", "playground"],
    ["agent-c", "agent-c"],
  ])("%s -> %s", (input, want) => {
    expect(baseAgent(input)).toBe(want);
  });
});

describe("isAgentVisible", () => {
  const hiddenCompanies: VisibilityState = {
    presenting: true,
    hidden: ["agent-a", "agent-b", "agent-c"],
  };

  it("hides nothing while presentation mode is off", () => {
    const off = { ...hiddenCompanies, presenting: false };
    expect(isAgentVisible("agent-a", off)).toBe(true);
    expect(isAgentVisible("agent-b.2", off)).toBe(true);
  });

  it("hides hidden agents and their composite session/run names when presenting", () => {
    expect(isAgentVisible("agent-a", hiddenCompanies)).toBe(false);
    expect(isAgentVisible("agent-b.2", hiddenCompanies)).toBe(false);
    expect(isAgentVisible("agent-c/review", hiddenCompanies)).toBe(false);
    expect(isAgentVisible("playground", hiddenCompanies)).toBe(true);
    expect(isAgentVisible("gov/playground/ratchet", hiddenCompanies)).toBe(true);
  });
});

describe("persistence", () => {
  it("defaults to no hidden agents", () => {
    setPresenting(true);
    expect(isAgentVisible("agent-a", getVisibility())).toBe(true);
    expect(isAgentVisible("playground", getVisibility())).toBe(true);
  });

  it("round-trips mode and per-agent choices through localStorage", () => {
    setPresenting(true);
    setAgentHidden("moonco", true);
    setAgentHidden("agent-b", false);
    expect(localStorage.getItem("ns.presentation")).toBe("1");
    const hidden = readHidden();
    expect(hidden).toContain("moonco");
    expect(hidden).not.toContain("agent-b");
    // a fresh store (new page load) reads the same state back
    resetVisibilityForTests();
    setAgentHidden("moonco", true); // no-op add, forces a read of persisted state
    expect(readHidden()).toContain("moonco");
    expect(readHidden()).not.toContain("agent-b");
  });

  it("normalizes composite names before storing", () => {
    setAgentHidden("agent-b.2", false);
    expect(readHidden()).not.toContain("agent-b");
    setAgentHidden("gov/moonco/ratchet", true);
    expect(readHidden()).toContain("moonco");
  });
});

function readHidden(): string[] {
  return JSON.parse(localStorage.getItem("ns.hiddenAgents") ?? "[]") as string[];
}
