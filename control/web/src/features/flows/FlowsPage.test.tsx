import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Flow } from "@/lib/api";
import { server } from "@/test/msw";
import { Flows } from "./FlowsPage";

afterEach(() => vi.restoreAllMocks());

const flow: Flow = {
  id: "flow-20260711-1000-a1b2c3d4",
  agent: "playground",
  repo: "nightshift",
  goal: "Make the live run delightful on a phone",
  acceptance: ["Node detail survives refresh"],
  template: "full-delivery",
  nodeRoles: ["plan", "implement"],
  deadline: "2099-07-11T19:00:00Z",
  created: "2026-07-11T09:00:00Z",
  updated: "2026-07-11T10:00:00Z",
  status: "running",
  batch: "flow-test",
  branch: "ui/live-run-graph",
  base: "origin/main",
  worktreeState: "active",
  round: 0,
  nodeViews: [
    {
      id: "plan",
      role: "plan",
      jobId: "plan-job",
      name: "Plan",
      description: "Choose the highest-impact phone interaction.",
      output: "A focused plan",
      effort: "high",
      minutes: 60,
      promptId: "node-plan",
      promptRevision: "abc123",
      state: "delivered",
    },
    {
      id: "implement",
      role: "implement",
      jobId: "implement-job",
      afterId: "plan",
      name: "Implement",
      description: "Build and prove the phone experience.",
      output: "A verified branch",
      effort: "xhigh",
      minutes: 120,
      promptId: "node-implement",
      promptRevision: "def456",
      startedAt: "2099-07-11T10:00:00Z",
      state: "running",
    },
  ],
};

function Location() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

function BackButton() {
  const navigate = useNavigate();
  return <button onClick={() => navigate(-1)}>Back in test</button>;
}

function renderFlow(entry: string, withHistory = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter
        initialEntries={withHistory ? ["/before", entry] : [entry]}
        initialIndex={withHistory ? 1 : 0}
      >
        <Routes>
          <Route
            path="/runs/:id"
            element={
              <>
                <Flows />
                <Location />
                <BackButton />
              </>
            }
          />
          <Route path="*" element={<Location />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("create-form graph preview", () => {
  async function toAutomationStep() {
    renderFlow("/runs/new");
    fireEvent.change(await screen.findByLabelText(/goal/i), {
      target: { value: "Ship the thing" },
    });
    fireEvent.change(screen.getByLabelText(/acceptance criteria/i), {
      target: { value: "It works" },
    });
    fireEvent.click(screen.getByRole("button", { name: /choose automation/i }));
  }

  it("previews the selected template as a graph on the Automation step", async () => {
    await toAutomationStep();
    // The template's happy path renders as graph nodes (one per catalog role).
    expect(await screen.findByTestId("graph-canvas")).toBeInTheDocument();
    expect(screen.getByTestId("graph-node-h:0:0:refine")).toBeInTheDocument();
    expect(screen.getByTestId("graph-node-h:4:0:validate")).toBeInTheDocument();
  });

  // A stage-only automation (compare-harness, explore-attempts) has no `nodes`
  // — the server sends null — so reading the sequence off that field crashed
  // the whole Automation step before any graph could render.
  it("lists a stage-only automation by its stage members", async () => {
    await toAutomationStep();
    expect(await screen.findByTestId("graph-canvas")).toBeInTheDocument();
    expect(screen.getByText("attempt → attempt#2 → judge")).toBeInTheDocument();
  });

  it("tapping a graph node opens its definition detail", async () => {
    await toAutomationStep();
    fireEvent.click(await screen.findByTestId("graph-node-h:1:0:implement"));
    // The sticky detail panel switches to the tapped node's catalog definition.
    expect(screen.getByText("Build it in the worktree.", { selector: "p" })).toBeInTheDocument();
  });

  it("prices the sessions an automation may add at runtime", async () => {
    renderFlow("/runs/new");
    fireEvent.change(await screen.findByLabelText(/goal/i), {
      target: { value: "Explore three approaches" },
    });
    fireEvent.change(screen.getByLabelText(/acceptance criteria/i), {
      target: { value: "One approach wins" },
    });
    fireEvent.click(screen.getByRole("button", { name: /choose automation/i }));
    // The fixture's explore-fanout template lets refine emit up to 3 attempts,
    // fanned in by a judge — 4 sessions beyond its listed stages.
    fireEvent.click(await screen.findByText("Explore then judge"));
    fireEvent.click(screen.getByRole("button", { name: /review run/i }));
    const envelope = await screen.findByTestId("envelope-emissions");
    expect(envelope).toHaveTextContent("up to 4");
  });

  it("re-shapes the graph when the sequence is customized", async () => {
    await toAutomationStep();
    await screen.findByTestId("graph-canvas");
    fireEvent.click(screen.getByRole("button", { name: "Remove validate" }));
    // Custom sequences mint one stage per node with no routes (backend parity),
    // so the last remaining node sits at stage 3 and validate is gone.
    expect(screen.queryByTestId("graph-node-h:4:0:validate")).not.toBeInTheDocument();
    expect(screen.getByTestId("graph-node-h:3:0:amend")).toBeInTheDocument();
    expect(screen.getByText("custom · sequential")).toBeInTheDocument();
  });
});

describe("live flow graph", () => {
  it("stores a tapped graph node in the URL and reveals nearby detail", async () => {
    server.use(http.get("/api/flows/:id", () => HttpResponse.json(flow)));
    renderFlow(`/runs/${flow.id}`);

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Implement, running. Open node details",
      }),
    );

    expect(screen.getByTestId("location")).toHaveTextContent("?node=implement-job");
    expect(screen.getByTestId("graph-node-detail")).toHaveTextContent(
      "Build and prove the phone experience.",
    );
  });

  it("orients the read-only run graph toward its editable automation", async () => {
    server.use(http.get("/api/flows/:id", () => HttpResponse.json(flow)));
    renderFlow(`/runs/${flow.id}`);

    // The run graph is a live, read-only view; its shape is fixed for a run.
    // "Edit automation" is the bridge to the definition (the template editor).
    fireEvent.click(await screen.findByRole("button", { name: /edit automation/i }));
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/automations/templates/full-delivery",
    );
  });

  it("restores selected node detail from a deep-link", async () => {
    server.use(http.get("/api/flows/:id", () => HttpResponse.json(flow)));
    renderFlow(`/runs/${flow.id}?node=implement-job`);

    expect(await screen.findByTestId("graph-node-detail")).toHaveTextContent("Implement");
    // findByRole: the xyflow canvas measures nodes async before showing them,
    // unlike the detail panel which renders in the first pass.
    expect(
      await screen.findByRole("button", { name: "Implement, running. Open node details" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("shows each parallel node's own countdown in the graph", async () => {
    const now = new Date("2099-07-11T11:00:00Z").getTime();
    vi.spyOn(Date, "now").mockReturnValue(now);
    const parallelFlow: Flow = {
      ...flow,
      nodeViews: [
        flow.nodeViews[0],
        {
          ...flow.nodeViews[1],
          id: "implement-a",
          jobId: "implement-a-job",
          name: "Implement A",
          startedAt: "2099-07-11T10:00:00Z",
        },
        {
          ...flow.nodeViews[1],
          id: "implement-b",
          jobId: "implement-b-job",
          name: "Implement B",
          startedAt: "2099-07-11T10:30:00Z",
        },
      ],
    };
    server.use(http.get("/api/flows/:id", () => HttpResponse.json(parallelFlow)));
    renderFlow(`/runs/${flow.id}`);

    expect(await screen.findByTestId("graph-node-implement-a")).toHaveTextContent("1h 00m left");
    expect(screen.getByTestId("graph-node-implement-b")).toHaveTextContent("1h 30m left");
    expect(
      screen.getByRole("button", { name: "Implement A, running. Open node details" }),
    ).not.toHaveAccessibleName(/left/i);
  });

  // ADR-0023: a run may create nodes the automation never listed, and a judge
  // may score them. Both have to be visible on the run, or the operator cannot
  // tell what the run decided or how well it went.
  it("marks an emitted node and shows the scores about it", async () => {
    const emittedFlow: Flow = {
      ...flow,
      emitters: [{ node: "plan", max: 3, roles: ["implement"], fanIn: "implement" }],
      nodeViews: [
        flow.nodeViews[0],
        { ...flow.nodeViews[1], emittedBy: "plan", worktree: "/wt/implement" },
      ],
    };
    server.use(
      http.get("/api/flows/:id", () => HttpResponse.json(emittedFlow)),
      http.get("/api/scores", () =>
        HttpResponse.json({
          groups: [],
          rows: [
            {
              runId: emittedFlow.id,
              judgeNode: "judge",
              subject: "implement",
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
              runId: emittedFlow.id,
              judgeNode: "judge",
              subject: "plan",
              dimension: "depth",
              score: null,
              max: 5,
              createdAt: 1755400100,
            },
          ],
        }),
      ),
    );
    renderFlow(`/runs/${flow.id}?node=implement-job`);

    const detail = await screen.findByTestId("graph-node-detail");
    expect(detail).toHaveTextContent("emitted by Plan");
    // The scores about the selected node, with both identities recorded.
    const scored = await screen.findByTestId("node-scores");
    expect(scored).toHaveTextContent("correctness");
    expect(scored).toHaveTextContent("4/5");
    expect(scored).toHaveTextContent("gpt-5.6-sol");
    expect(scored).toHaveTextContent("judged by claude-fable-5");
    // The run-level card lists every judged subject; an unknown stays unknown.
    const runScores = screen.getByTestId("run-scores");
    expect(runScores).toHaveTextContent("unknown");
  });

  it("hides the score surfaces when nothing judged the run", async () => {
    server.use(
      http.get("/api/flows/:id", () => HttpResponse.json(flow)),
      http.get("/api/scores", () => HttpResponse.json({ groups: [], rows: [] })),
    );
    renderFlow(`/runs/${flow.id}?node=implement-job`);
    await screen.findByTestId("graph-node-detail");
    expect(screen.queryByTestId("run-scores")).toBeNull();
    expect(screen.queryByTestId("node-scores")).toBeNull();
  });

  it("replaces graph selection state so Back leaves the run", async () => {
    server.use(http.get("/api/flows/:id", () => HttpResponse.json(flow)));
    renderFlow(`/runs/${flow.id}`, true);

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Implement, running. Open node details",
      }),
    );
    expect(screen.getByTestId("location")).toHaveTextContent("?node=implement-job");

    fireEvent.click(screen.getByRole("button", { name: "Back in test" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/before");
  });
});
