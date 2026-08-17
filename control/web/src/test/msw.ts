import { setupServer } from "msw/node";
import { http, HttpResponse } from "msw";
import type {
  ServerState,
  SweepMap,
  Job,
  Report,
  TicketGroup,
  PromptsDoc,
  FocusDoc,
  IdeaCollection,
  Usage,
  Health,
  Obs,
  Pipeline,
  Vitals,
  ProfilesDoc,
  Proposal,
  Flow,
  FlowCatalog,
  RepoOption,
  PromptDocument,
  LedgerRun,
  ScoresResponse,
} from "@/lib/api";

// Default handlers return minimal but shape-valid fixtures for every GET
// endpoint, so mounting any view in a test never hits the network. Individual
// tests override with server.use(...) when they need to assert or vary a
// response. Fixtures mirror the Go json tags — the same contract api.ts locks.

const status: ServerState[] = [
  {
    company: "playground",
    active: true,
    ttl_until: 1783271926,
    sessions: [{ instance: "playground", active: true, ttl_until: 1783271926 }],
  },
];
const sweeps: SweepMap = { playground: true };
const jobs: Job[] = [];
const reports: Report[] = [];
const tickets: TicketGroup[] = [{ agent: "playground", tickets: [] }];
const prompts: PromptsDoc = { groups: [] };
const usage: Usage = {
  windowDays: 14,
  generated: 0,
  totals: { in: 0, out: 0, cacheRead: 0, cacheCreate: 0, total: 0, messages: 0, costUSD: 0 },
  days: [],
  models: [],
  agents: [],
};
const health: Health = {
  auth: { ok: true, detail: "ok", checkedAt: 0 },
  recentFailures: [],
  forge: [],
};
const focus: FocusDoc = {
  files: [
    { id: "products", content: "# bets\n", modifiedAt: 1751700000 },
    { id: "projects", content: "# repos\n", modifiedAt: 1751700000 },
  ],
};
const ideas: IdeaCollection = {
  files: [{ id: "2026-07-11", title: "A promising bet", modifiedAt: 1751700000 }],
};
const obs: Obs = {
  generated: 0,
  alerts: [],
  agents: [],
  runs: [],
  budget: {
    nightBudgetUSD: 1800,
    nightSpendUSD: 0,
    weekSpendUSD: 0,
    topUpsMinted: 0,
    lastAdjust: "2026-07-05",
    limitHitNight: "2026-07-05",
    spendAtHitUSD: 1800,
  },
};
const pipeline: Pipeline = {
  agent: "playground",
  source: "builtin",
  waves: [
    { name: "medic", time: "23:00", effort: "medium", minutes: 50, noTickets: true },
    { name: "exec", time: "00:45", effort: "xhigh", minutes: 420, perTicket: true },
    { name: "synth", time: "08:00", effort: "medium", minutes: 45, noTickets: true },
  ],
};

const profiles: ProfilesDoc = {
  active: "",
  effective: "",
  profiles: [],
  builtin: {
    name: "builtin",
    waves: pipeline.waves.map((w) => ({ ...w, prompt: w.name + ".md" })),
  },
};
const proposals: Proposal[] = [];
const flows: Flow[] = [];
const flowCatalog: FlowCatalog = {
  templates: [
    {
      id: "full-delivery",
      name: "Full delivery",
      description: "Refine through validation.",
      nodes: ["refine", "implement", "review", "amend", "validate"],
    },
    // ADR-0023: two members of one role in one stage, each with its own runtime
    // pin — the harness-comparison shape the graph must render with two icons.
    {
      id: "compare-harness",
      name: "Compare harnesses",
      description: "Same prompt, two engines, one judge.",
      // No `nodes`: the server sends null for a stage-only automation.
      stages: [["attempt", "attempt#2"], ["judge"]],
      memberRuntime: {
        attempt: { executor: "claude" },
        "attempt#2": { executor: "codex", model: "gpt-5.6-sol" },
      },
      builtin: true,
    },
    // ADR-0023: a declared emitter — refine may fan out up to 3 attempts at
    // runtime, judged by the fan-in node.
    {
      id: "explore-fanout",
      name: "Explore then judge",
      description: "Refine emits attempts, a judge scores them.",
      stages: [["refine"], ["judge"]],
      emitters: [{ node: "refine", max: 3, roles: ["attempt"], fanIn: "judge" }],
    },
  ],
  nodes: [
    {
      id: "refine",
      name: "Refine",
      description: "Refine the task.",
      effort: "high",
      minutes: 90,
      output: "Decision-ready report",
      promptId: "node-refine",
      prompt: "Refine the task.",
      defaultPrompt: "Refine the task.",
      promptSource: "built-in",
      promptRevision: "abc123",
      runtimeSource: "built-in",
      runtimeRevision: "runtime123",
      defaultEffort: "high",
      defaultMinutes: 90,
    },
    {
      id: "implement",
      name: "Implement",
      description: "Build it in the worktree.",
      effort: "xhigh",
      minutes: 120,
      output: "A verified branch",
      promptId: "node-implement",
      prompt: "Build it.",
      defaultPrompt: "Build it.",
      promptSource: "built-in",
      promptRevision: "def456",
      runtimeSource: "built-in",
      runtimeRevision: "runtime456",
      defaultEffort: "xhigh",
      defaultMinutes: 120,
    },
    {
      id: "attempt",
      name: "Attempt",
      description: "Do the task exactly as stated.",
      effort: "high",
      minutes: 90,
      output: "Deliverable per the run goal",
      promptId: "node-attempt",
      prompt: "Attempt it.",
      defaultPrompt: "Attempt it.",
      promptSource: "built-in",
      promptRevision: "att001",
      runtimeSource: "built-in",
      runtimeRevision: "runtimeatt",
      defaultEffort: "high",
      defaultMinutes: 90,
    },
    {
      id: "judge",
      name: "Judge",
      description: "Score the work against a rubric.",
      effort: "high",
      minutes: 45,
      output: "Scores + verdict",
      promptId: "node-judge",
      prompt: "Judge it.",
      defaultPrompt: "Judge it.",
      promptSource: "built-in",
      promptRevision: "jud001",
      runtimeSource: "built-in",
      runtimeRevision: "runtimejud",
      defaultEffort: "high",
      defaultMinutes: 45,
    },
  ],
};
const ledger: LedgerRun[] = [
  {
    id: "flow-20260817-1411-3ff49738",
    agent: "playground",
    repo: "nightshift",
    template: "compare-harness",
    automationRevision: "3ba1f2c",
    status: "complete",
    rounds: 1,
    created: "2026-08-17T09:00:00Z",
    updated: "2026-08-17T14:00:00Z",
    nodes: [
      {
        id: "attempt",
        role: "attempt",
        state: "delivered",
        verdict: "ok",
        scores: [
          { dimension: "correctness", score: 4, max: 5, rationale: "tests pass" },
          { dimension: "depth", score: null, max: 5 },
        ],
      },
    ],
  },
];
const scores: ScoresResponse = {
  groups: [
    {
      role: "implement",
      dimension: "correctness",
      promptRev: "3ba1f2c",
      executor: "claude",
      avg: 3.8,
      pct: 76,
      n: 5,
      max: 5,
      latestRationales: ["tests pass; error paths unexercised"],
    },
    {
      role: "implement",
      dimension: "correctness",
      promptRev: "9c7de11",
      executor: "claude",
      avg: 3.2,
      pct: 64,
      n: 4,
      max: 5,
    },
  ],
  rows: [
    {
      runId: "flow-20260817-1411-3ff49738",
      judgeNode: "judge",
      subject: "attempt",
      dimension: "correctness",
      score: 4,
      max: 5,
      rationale: "tests pass; error paths unexercised",
      judgeExecutor: "claude",
      judgeModel: "claude-fable-5",
      subjectExecutor: "codex",
      subjectModel: "gpt-5.6-sol",
      createdAt: 1755400000,
    },
    {
      runId: "flow-20260817-1411-3ff49738",
      judgeNode: "judge",
      subject: "attempt#2",
      dimension: "correctness",
      score: null,
      max: 5,
      rationale: "could not run the branch",
      judgeExecutor: "claude",
      subjectExecutor: "codex",
      createdAt: 1755400100,
    },
  ],
};
const repos: RepoOption[] = [{ agent: "playground", path: "demo", name: "demo" }];
const promptDocument: PromptDocument = {
  id: "node-refine",
  label: "Refine",
  description: "Refine the task.",
  path: "/tmp/node-refine.md",
  body: "Refine the task.",
  defaultBody: "Refine the task.",
  editable: true,
  source: "built-in",
  revision: "abc123",
  updatedAt: 0,
  versions: [],
};

const vitals: Vitals = {
  generated: 0,
  uptimeSec: 540072,
  load1: 1.8,
  load5: 2.3,
  load15: 1.4,
  cpuCount: 8,
  memTotalBytes: 16 * 2 ** 30,
  memUsedBytes: 9 * 2 ** 30,
  swapTotalBytes: 0,
  swapUsedBytes: 0,
  diskTotalBytes: 160 * 2 ** 30,
  diskUsedBytes: 71 * 2 ** 30,
  ok: true,
};

export const handlers = [
  http.get("/api/status", () => HttpResponse.json(status)),
  http.get("/api/vitals", () => HttpResponse.json(vitals)),
  http.get("/api/sweeps", () => HttpResponse.json(sweeps)),
  http.get("/api/jobs", () => HttpResponse.json(jobs)),
  http.get("/api/pipeline", () => HttpResponse.json(pipeline)),
  http.get("/api/profiles/proposals", () => HttpResponse.json(proposals)),
  http.get("/api/profiles", () => HttpResponse.json(profiles)),
  http.get("/api/profiles/:name", () => HttpResponse.json(profiles.builtin)),
  http.get("/api/reports", () => HttpResponse.json(reports)),
  http.get("/api/report", () => HttpResponse.text("# report")),
  http.get("/api/tickets", () => HttpResponse.json(tickets)),
  http.get("/api/prompts", () => HttpResponse.json(prompts)),
  http.get("/api/prompt", () => HttpResponse.text("prompt body")),
  http.get("/api/prompt-document", () => HttpResponse.json(promptDocument)),
  http.get("/api/focus", () => HttpResponse.json(focus)),
  http.get("/api/ideas", () => HttpResponse.json(ideas)),
  http.get("/api/idea", () => HttpResponse.json({ ...ideas.files[0], content: "# idea\n" })),
  http.post("/api/ideas/:id/promote", () => HttpResponse.json(focus.files[0])),
  http.get("/api/usage", () => HttpResponse.json(usage)),
  http.get("/api/health", () => HttpResponse.json(health)),
  http.get("/api/obs", () => HttpResponse.json(obs)),
  http.get("/api/flow-catalog", () => HttpResponse.json(flowCatalog)),
  http.get("/api/repos", () => HttpResponse.json(repos)),
  http.get("/api/flows", () => HttpResponse.json(flows)),
  http.get("/api/ledger", () => HttpResponse.json(ledger)),
  http.get("/api/proposals", () => HttpResponse.json([])),
  // Exactly one of run= | automation= (the server 400s otherwise); an automation
  // query answers with aggregates only, a run query also with its rows.
  http.get("/api/scores", ({ request }) => {
    const url = new URL(request.url);
    const run = url.searchParams.get("run");
    const automation = url.searchParams.get("automation");
    if (Boolean(run) === Boolean(automation))
      return HttpResponse.text("exactly one of run|automation", { status: 400 });
    return HttpResponse.json(run ? scores : { groups: scores.groups, rows: [] });
  }),
  http.get("/api/flows/:id", () => HttpResponse.json(flows[0] ?? null)),
  // writes: accept and 204
  http.post("/api/start", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/stop", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/session/start", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/session/stop", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/sweep", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/job", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/job/cancel", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/run/stop", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/ticket", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/ticket/update", () => new HttpResponse(null, { status: 200 })),
  http.put("/api/focus/:id", () =>
    HttpResponse.json({ id: "products", content: "x", modifiedAt: 1751700001 }),
  ),
  http.put("/api/profiles/:name", () => new HttpResponse(null, { status: 200 })),
  http.delete("/api/profiles/:name", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/profiles/active", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/profiles/:name/run", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/profiles/proposals/:name/apply", () => new HttpResponse(null, { status: 200 })),
  http.delete("/api/profiles/proposals/:name", () => new HttpResponse(null, { status: 200 })),
  http.post("/api/flows", () => HttpResponse.json(flows[0] ?? null)),
  http.get("/api/flow-templates", () => HttpResponse.json(flowCatalog.templates)),
  http.put("/api/flow-templates/:id", () => HttpResponse.json(flowCatalog.templates[0])),
  http.delete("/api/flow-templates/:id", () => new HttpResponse(null, { status: 204 })),
  http.put("/api/node-runtime/:id", () =>
    HttpResponse.json({ id: "refine", effort: "high", minutes: 90, source: "custom" }),
  ),
  http.delete("/api/node-runtime/:id", () => new HttpResponse(null, { status: 204 })),
  http.put("/api/prompt-document", () => HttpResponse.json(promptDocument)),
  http.post("/api/prompt-document/restore", () => HttpResponse.json(promptDocument)),
  http.put("/api/flows/:id/deadline", () => HttpResponse.json(flows[0] ?? null)),
  http.put("/api/flows/:id/guidance", () => HttpResponse.json(flows[0] ?? null)),
  http.post("/api/flows/:id/stop", () => HttpResponse.json(flows[0] ?? null)),
  http.post("/api/flows/:id/cleanup", () => HttpResponse.json(flows[0] ?? null)),
];

export const server = setupServer(...handlers);
