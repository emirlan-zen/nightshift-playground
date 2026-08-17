import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/msw";
import { api } from "./api";

// Contract tests: the api client is the stable Go<->TS boundary. These assert
// the exact METHOD + URL (incl. query encoding) each call hits, and that
// responses parse into the documented shapes. They are deliberately decoupled
// from any React component, so the control-page refactor can't break them.

// captureCalls installs a catch-all that records every request and returns an
// empty JSON body (fine for both json() and text() consumers here).
function captureCalls() {
  const calls: { method: string; path: string; body?: string }[] = [];
  server.use(
    http.all("*", async ({ request }) => {
      const u = new URL(request.url);
      const body =
        request.method === "POST" || request.method === "PUT" ? await request.text() : undefined;
      calls.push({ method: request.method, path: u.pathname + u.search, body });
      return HttpResponse.json({});
    }),
  );
  return calls;
}

describe("api URL + method contract", () => {
  const cases: [string, () => unknown, string, string][] = [
    ["status", () => api.status(), "GET", "/api/status"],
    ["start", () => api.start("agent-b"), "POST", "/api/start?c=agent-b"],
    ["stop", () => api.stop("agent-b"), "POST", "/api/stop?c=agent-b"],
    [
      "sessionStart default",
      () => api.sessionStart("agent-a"),
      "POST",
      "/api/session/start?c=agent-a",
    ],
    [
      "sessionStart slot encodes spaces",
      () => api.sessionStart("agent-a", "Repo Cleanup"),
      "POST",
      "/api/session/start?c=agent-a&slot=Repo%20Cleanup",
    ],
    [
      "sessionStop",
      () => api.sessionStop("agent-a", "agent-a__x"),
      "POST",
      "/api/session/stop?c=agent-a&instance=agent-a__x",
    ],
    ["sweeps", () => api.sweeps(), "GET", "/api/sweeps"],
    ["setSweep on", () => api.setSweep("p", true), "POST", "/api/sweep?c=p&on=1"],
    ["setSweep off", () => api.setSweep("p", false), "POST", "/api/sweep?c=p&on=0"],
    ["jobs", () => api.jobs("p"), "GET", "/api/jobs?c=p"],
    ["pipeline", () => api.pipeline(), "GET", "/api/pipeline"],
    ["profiles", () => api.profiles(), "GET", "/api/profiles"],
    ["profile", () => api.profile("deep-perf"), "GET", "/api/profiles/deep-perf"],
    [
      "saveProfile",
      () => api.saveProfile("deep", { name: "deep", waves: [] }),
      "PUT",
      "/api/profiles/deep",
    ],
    ["deleteProfile", () => api.deleteProfile("deep"), "DELETE", "/api/profiles/deep"],
    ["activateProfile", () => api.activateProfile("deep"), "POST", "/api/profiles/active"],
    ["runProfile", () => api.runProfile("deep"), "POST", "/api/profiles/deep/run"],
    ["proposals", () => api.proposals(), "GET", "/api/profiles/proposals"],
    ["applyProposal", () => api.applyProposal("x"), "POST", "/api/profiles/proposals/x/apply"],
    ["dismissProposal", () => api.dismissProposal("x"), "DELETE", "/api/profiles/proposals/x"],
    ["queueJob", () => api.queueJob("p", "do it", "2026-07-05T05:00:00Z"), "POST", "/api/job"],
    ["cancelJob", () => api.cancelJob("p", "id1"), "POST", "/api/job/cancel?c=p&id=id1"],
    ["stopRun", () => api.stopRun("p", "id1"), "POST", "/api/run/stop?c=p&id=id1"],
    ["flowCatalog", () => api.flowCatalog(), "GET", "/api/flow-catalog"],
    ["flowTemplates", () => api.flowTemplates(), "GET", "/api/flow-templates"],
    [
      "saveFlowTemplate",
      () =>
        api.saveFlowTemplate("secure", {
          id: "secure",
          name: "Secure",
          description: "x",
          nodes: ["review"],
        }),
      "PUT",
      "/api/flow-templates/secure",
    ],
    [
      "resetFlowTemplate",
      () => api.resetFlowTemplate("secure"),
      "DELETE",
      "/api/flow-templates/secure",
    ],
    [
      "saveNodeRuntime",
      () => api.saveNodeRuntime("review", "xhigh", 120),
      "PUT",
      "/api/node-runtime/review",
    ],
    [
      "resetNodeRuntime",
      () => api.resetNodeRuntime("review"),
      "DELETE",
      "/api/node-runtime/review",
    ],
    ["repos", () => api.repos(), "GET", "/api/repos"],
    ["flows", () => api.flows(), "GET", "/api/flows"],
    ["flow", () => api.flow("flow-1"), "GET", "/api/flows/flow-1"],
    [
      "createFlow",
      () =>
        api.createFlow({
          agent: "p",
          repo: "demo",
          goal: "ship",
          acceptance: [],
          template: "build-feature",
        }),
      "POST",
      "/api/flows",
    ],
    [
      "setFlowDeadline",
      () => api.setFlowDeadline("flow-1", "2026-07-11T12:00:00Z"),
      "PUT",
      "/api/flows/flow-1/deadline",
    ],
    [
      "setFlowGuidance",
      () => api.setFlowGuidance("flow-1", "Prefer the API"),
      "PUT",
      "/api/flows/flow-1/guidance",
    ],
    ["stopFlow", () => api.stopFlow("flow-1"), "POST", "/api/flows/flow-1/stop"],
    ["cleanupFlow", () => api.cleanupFlow("flow-1"), "POST", "/api/flows/flow-1/cleanup"],
    ["ideas", () => api.ideas(), "GET", "/api/ideas"],
    ["idea", () => api.idea("2026-07-11"), "GET", "/api/idea?id=2026-07-11"],
    [
      "promoteIdea",
      () => api.promoteIdea("2026-07-11", "worth a test"),
      "POST",
      "/api/ideas/2026-07-11/promote",
    ],
    ["reports", () => api.reports("p"), "GET", "/api/reports?c=p"],
    ["report", () => api.report("p", "id1"), "GET", "/api/report?c=p&id=id1"],
    ["tickets", () => api.tickets(), "GET", "/api/tickets"],
    ["createTicket", () => api.createTicket("p", "t", "b"), "POST", "/api/ticket"],
    [
      "updateTicket",
      () => api.updateTicket("p", "id", "review", "n"),
      "POST",
      "/api/ticket/update",
    ],
    ["prompts", () => api.prompts(), "GET", "/api/prompts"],
    ["prompt", () => api.prompt("global"), "GET", "/api/prompt?id=global"],
    [
      "promptDocument",
      () => api.promptDocument("node-review"),
      "GET",
      "/api/prompt-document?id=node-review",
    ],
    [
      "savePrompt",
      () => api.savePrompt("node-review", "body"),
      "PUT",
      "/api/prompt-document?id=node-review",
    ],
    [
      "restorePrompt",
      () => api.restorePrompt("node-review", "123"),
      "POST",
      "/api/prompt-document/restore?id=node-review&version=123",
    ],
    ["focus", () => api.focus(), "GET", "/api/focus"],
    ["saveFocus", () => api.saveFocus("products", "# x"), "PUT", "/api/focus/products"],
    ["usage", () => api.usage(), "GET", "/api/usage"],
    ["health", () => api.health(), "GET", "/api/health"],
    ["obs", () => api.obs(), "GET", "/api/obs"],
    ["vitals", () => api.vitals(), "GET", "/api/vitals"],
    ["ledger", () => api.ledger(), "GET", "/api/ledger"],
    ["runScores", () => api.runScores("flow-1"), "GET", "/api/scores?run=flow-1"],
    [
      "automationScores",
      () => api.automationScores("compare harness"),
      "GET",
      "/api/scores?automation=compare%20harness",
    ],
  ];

  it.each(cases)("%s -> %s %s", async (_name, call, method, path) => {
    const calls = captureCalls();
    await call();
    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe(method);
    expect(calls[0].path).toBe(path);
  });

  it("reportBanner builds an <img> URL without fetching", () => {
    expect(api.reportBanner("p", "id 1")).toBe("/api/report/banner?c=p&id=id%201");
  });

  it("POST bodies carry the documented JSON payloads", async () => {
    const calls = captureCalls();
    await api.queueJob("playground", "run sweep", "2026-07-05T05:00:00Z");
    await api.createTicket("playground", "Title", "Body");
    await api.updateTicket("playground", "id1", "review", "note");
    expect(JSON.parse(calls[0].body!)).toEqual({
      agent: "playground",
      prompt: "run sweep",
      at: "2026-07-05T05:00:00Z",
    });
    expect(JSON.parse(calls[1].body!)).toEqual({
      agent: "playground",
      title: "Title",
      body: "Body",
    });
    expect(JSON.parse(calls[2].body!)).toEqual({
      agent: "playground",
      id: "id1",
      status: "review",
      note: "note",
    });
  });

  it("queueJob + createTicket omit optional fields unless provided", async () => {
    const calls = captureCalls();
    // defaults: no effort/minutes/lane keys at all
    await api.queueJob("p", "x", "2026-07-05T05:00:00Z");
    await api.createTicket("p", "t", "b");
    // opts present: keys included
    await api.queueJob("p", "x", "2026-07-05T05:00:00Z", { effort: "xhigh", minutes: 90 });
    await api.createTicket("p", "t", "b", "hunt");
    expect(JSON.parse(calls[0].body!)).toEqual({
      agent: "p",
      prompt: "x",
      at: "2026-07-05T05:00:00Z",
    });
    expect(JSON.parse(calls[1].body!)).toEqual({ agent: "p", title: "t", body: "b" });
    expect(JSON.parse(calls[2].body!)).toEqual({
      agent: "p",
      prompt: "x",
      at: "2026-07-05T05:00:00Z",
      effort: "xhigh",
      minutes: 90,
    });
    expect(JSON.parse(calls[3].body!)).toEqual({ agent: "p", title: "t", body: "b", lane: "hunt" });
  });

  it("saveFocus PUTs the content as the documented JSON payload", async () => {
    const calls = captureCalls();
    await api.saveFocus("projects", "# repos\n");
    expect(calls[0].method).toBe("PUT");
    expect(calls[0].path).toBe("/api/focus/projects");
    expect(JSON.parse(calls[0].body!)).toEqual({ content: "# repos\n" });
  });
});

describe("api response parsing + errors", () => {
  it("parses the status array shape from the default fixture", async () => {
    const s = await api.status();
    expect(Array.isArray(s)).toBe(true);
    expect(s[0].company).toBe("playground");
    expect(s[0].sessions[0].instance).toBe("playground");
  });

  it("throws with the server's error text on a non-2xx", async () => {
    server.use(http.get("/api/status", () => HttpResponse.text("boom", { status: 500 })));
    await expect(api.status()).rejects.toThrow("boom");
  });

  it("parses the focus doc + health forge shapes from the default fixtures", async () => {
    const f = await api.focus();
    expect(f.files.map((x) => x.id)).toEqual(["products", "projects"]);
    expect(f.files[0].modifiedAt).toBeGreaterThan(0);
    const h = await api.health();
    expect(h.forge).toEqual([]);
  });

  it("saveFocus surfaces the server's rejection text (bad id / oversized)", async () => {
    server.use(
      http.put("/api/focus/:id", () => HttpResponse.text("unknown focus file", { status: 404 })),
    );
    await expect(api.saveFocus("products", "x")).rejects.toThrow("unknown focus file");
  });
});

// ADR-0023 contract: emit-nodes, per-member runtime, and judge scores are served
// by the Go control plane and consumed here. These lock the exact JSON the
// server promises, so a rename on either side fails a test instead of silently
// rendering an empty surface.
describe("ADR-0023 emit + scores contract", () => {
  it("templates carry emitters and per-member runtime pins", async () => {
    const templates = await api.flowTemplates();
    const compare = templates.find((t) => t.id === "compare-harness")!;
    expect(compare.stages).toEqual([["attempt", "attempt#2"], ["judge"]]);
    expect(compare.memberRuntime).toEqual({
      attempt: { executor: "claude" },
      "attempt#2": { executor: "codex", model: "gpt-5.6-sol" },
    });
    const fanout = templates.find((t) => t.id === "explore-fanout")!;
    expect(fanout.emitters).toEqual([
      { node: "refine", max: 3, roles: ["attempt"], fanIn: "judge" },
    ]);
  });

  it("a run query returns aggregates plus rows; unknown scores stay null", async () => {
    const res = await api.runScores("flow-20260817-1411-3ff49738");
    expect(res.groups[0]).toMatchObject({
      role: "implement",
      dimension: "correctness",
      promptRev: "3ba1f2c",
      avg: 3.8,
      n: 5,
      max: 5,
    });
    // Newest prompt revision first — the delta arrow reads groups in that order.
    expect(res.groups.map((g) => g.promptRev)).toEqual(["3ba1f2c", "9c7de11"]);
    expect(res.rows).toHaveLength(2);
    expect(res.rows[0]).toMatchObject({
      judgeNode: "judge",
      subject: "attempt",
      score: 4,
      subjectExecutor: "codex",
      subjectModel: "gpt-5.6-sol",
    });
    // `unknown` is persisted as null, never coerced to 0.
    expect(res.rows[1].score).toBeNull();
  });

  it("an automation query returns aggregates only", async () => {
    const res = await api.automationScores("compare-harness");
    expect(res.groups.length).toBeGreaterThan(0);
    expect(res.rows).toEqual([]);
  });

  it("surfaces the server's 400 when the query names neither subject", async () => {
    server.use(http.get("/api/scores", () => HttpResponse.text("bad query", { status: 400 })));
    await expect(api.runScores("flow-1")).rejects.toThrow("bad query");
  });

  it("ledger nodes carry the scores about them", async () => {
    const runs = await api.ledger();
    const node = runs[0].nodes[0];
    expect(node.scores).toEqual([
      { dimension: "correctness", score: 4, max: 5, rationale: "tests pass" },
      { dimension: "depth", score: null, max: 5 },
    ]);
  });
});
