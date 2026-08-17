import { describe, it, expect } from "vitest";
import type { Job, Report, PipelineWave, Flow, FlowNode } from "./api";
import { nightsFor, jobStatus, jobsForWave, aggregateStatus, indexFlowReports } from "./night";

// Pure logic — the night grouping/status the Night view uses. Times
// are written WITHOUT a UTC offset so they parse as local wall-clock, keeping
// the evening-anchored bucketing deterministic regardless of the CI timezone
// (production assumes the viewer is in Bishkek, same as fmtAt).

function mkJob(p: Partial<Job> & { id: string; at: string }): Job {
  return {
    agent: "playground",
    prompt: "x",
    kind: "sweep",
    created: p.at,
    started: false,
    ...p,
  };
}
function mkReport(id: string, at: string): Report {
  return { id, mtime: Math.floor(new Date(at).getTime() / 1000) };
}
describe("night grouping is evening-anchored", () => {
  it("puts a 23:00 wave, a 00:45 exec, and an 08:03 retro in the same night", () => {
    const jobs = [
      mkJob({ id: "a", label: "medic", at: "2026-07-05T23:00:00" }),
      mkJob({ id: "b", label: "exec · improve · x", at: "2026-07-06T00:45:00" }),
      mkJob({ id: "c", label: "retro", at: "2026-07-06T08:03:00" }),
    ];
    const nights = nightsFor(jobs, []);
    expect(nights).toHaveLength(1);
    expect(nights[0].key).toBe("2026-07-05");
    expect(nights[0].jobs.map((j) => j.id).sort()).toEqual(["a", "b", "c"]);
  });

  it("separates a daytime deferred run into its own night, newest first", () => {
    const jobs = [
      mkJob({ id: "old", label: "medic", at: "2026-07-05T23:00:00" }),
      mkJob({ id: "new", label: "run", kind: "deferred", at: "2026-07-07T14:00:00" }),
    ];
    const nights = nightsFor(jobs, []);
    expect(nights.map((n) => n.key)).toEqual(["2026-07-07", "2026-07-05"]);
  });

  it("merges reports into the night by their mtime", () => {
    const jobs = [mkJob({ id: "a", label: "synth", at: "2026-07-06T08:00:00" })];
    const reports = [mkReport("a", "2026-07-06T08:05:00")];
    const nights = nightsFor(jobs, reports);
    expect(nights).toHaveLength(1);
    expect(nights[0].reports.map((r) => r.id)).toEqual(["a"]);
  });
});

describe("jobStatus unifies the three signals", () => {
  const base = { id: "x", at: "2026-07-06T00:00:00" };
  it("scheduled before it starts", () => {
    expect(jobStatus(mkJob({ ...base, started: false }), false)).toBe("scheduled");
  });
  it("running while rc reports active", () => {
    expect(jobStatus(mkJob({ ...base, started: true, runState: "active" }), false)).toBe("running");
  });
  it("delivered when the report exists, even if obs lags", () => {
    expect(jobStatus(mkJob({ ...base, started: true }), true, undefined)).toBe("delivered");
  });
  it("distinguishes auth-skip and no-report from obs", () => {
    expect(jobStatus(mkJob({ ...base, started: true }), false, "auth-skip")).toBe("auth-skip");
    expect(jobStatus(mkJob({ ...base, started: true }), false, "no-deliverable")).toBe("no-report");
  });
  it("done when started, ended, no report and no obs verdict", () => {
    expect(jobStatus(mkJob({ ...base, started: true }), false, undefined)).toBe("done");
  });
});

describe("indexFlowReports joins report ids to their run", () => {
  function mkNode(p: Partial<FlowNode> & { role: string; jobId: string }): FlowNode {
    return {
      id: p.role,
      name: p.role,
      description: "",
      output: "",
      effort: "high",
      minutes: 60,
      promptId: `node-${p.role}`,
      promptRevision: "",
      state: "delivered",
      ...p,
    };
  }
  function mkFlow(p: Partial<Flow> & { id: string; nodeViews: FlowNode[] }): Flow {
    return {
      agent: "playground",
      repo: "sample-cli",
      goal: "Make update checks resilient to offline startup",
      acceptance: [],
      template: "build-feature",
      nodeRoles: [],
      created: "",
      updated: "",
      status: "complete",
      batch: "b",
      branch: "x",
      base: "main",
      worktreeState: "cleaned",
      round: 0,
      ...p,
    };
  }

  it("maps each delivered node's report id to goal/template/role/verdict", () => {
    const flows = [
      mkFlow({
        id: "flow-1",
        goal: "Audit the control-plane web UI",
        template: "ui-audit",
        nodeViews: [
          mkNode({
            role: "implement",
            jobId: "20260712-0900-flow-aa11",
            reportId: "20260712-0900-flow-aa11",
            verdict: "ok",
          }),
          mkNode({
            role: "validate",
            jobId: "20260712-1000-flow-bb22",
            reportId: "20260712-1000-flow-bb22",
            verdict: "needs-work",
          }),
        ],
      }),
    ];
    const idx = indexFlowReports(flows);
    expect(idx.get("20260712-0900-flow-aa11")).toEqual({
      goal: "Audit the control-plane web UI",
      template: "ui-audit",
      node: "implement",
      verdict: "ok",
    });
    expect(idx.get("20260712-1000-flow-bb22")?.verdict).toBe("needs-work");
  });

  it("skips nodes without a report id and leaves an empty verdict undefined", () => {
    const flows = [
      mkFlow({
        id: "flow-2",
        nodeViews: [
          mkNode({ role: "plan", jobId: "j-plan" }), // no reportId → not delivered
          mkNode({ role: "review", jobId: "j-review", reportId: "j-review", verdict: "" }),
        ],
      }),
    ];
    const idx = indexFlowReports(flows);
    expect(idx.has("j-plan")).toBe(false);
    expect(idx.get("j-review")?.verdict).toBeUndefined();
  });

  it("tolerates flows with no node views", () => {
    expect(indexFlowReports([mkFlow({ id: "f", nodeViews: [] })]).size).toBe(0);
  });
});

describe("wave <-> job pairing + aggregation", () => {
  const wave: PipelineWave = { name: "exec", time: "00:45", minutes: 420 };
  it("matches exec fan-out by label prefix, not other waves", () => {
    const jobs = [
      mkJob({ id: "1", label: "exec · improve · a", at: "2026-07-06T00:45:00" }),
      mkJob({ id: "2", label: "exec · self-directed", at: "2026-07-06T00:48:00" }),
      mkJob({ id: "3", label: "review", at: "2026-07-06T06:45:00" }),
    ];
    expect(jobsForWave(wave, jobs).map((j) => j.id)).toEqual(["1", "2"]);
  });
  it("idle when no run carried the wave; running dominates the mix", () => {
    expect(aggregateStatus([])).toBe("idle");
    expect(aggregateStatus(["delivered", "running", "done"])).toBe("running");
    expect(aggregateStatus(["delivered", "no-report"])).toBe("no-report");
    expect(aggregateStatus(["delivered", "delivered"])).toBe("delivered");
  });
});
