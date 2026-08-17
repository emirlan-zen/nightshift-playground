import { describe, it, expect, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../../test/msw";
import { resetVisibilityForTests, setAgentHidden, setPresenting } from "../../lib/visibility";
import { App } from "../../App";

// Regression: the Health page used to fetch /api/obs through its own raw
// useQuery instead of the filtered useObs() hook, so alerts, the night ledger,
// and per-agent health kept showing private agents while presenting (caught
// live 2026-08-15). This renders the real page and pins the filtered path.

const obsWithPrivateAgents = {
  generated: 0,
  alerts: [
    { agent: "agent-a", kind: "stall", detail: "agent-a wedged", ts: 1754800000 },
    { agent: "playground", kind: "stall", detail: "playground wedged", ts: 1754800001 },
  ],
  agents: [
    { agent: "agent-b", sessions: 2, turns: 10, toolCalls: 5, toolErrors: 0, lastTs: 1754800000 },
    {
      agent: "playground",
      sessions: 3,
      turns: 20,
      toolCalls: 9,
      toolErrors: 1,
      lastTs: 1754800001,
    },
  ],
  runs: [
    { agent: "agent-a", runId: "r-nam", started: 1754800000, report: true, status: "delivered" },
    { agent: "playground", runId: "r-pg", started: 1754800001, report: true, status: "delivered" },
  ],
  budget: { nightBudgetUSD: 1800, nightSpendUSD: 0, weekSpendUSD: 0, topUpsMinted: 0 },
};

beforeEach(() => {
  localStorage.clear();
  resetVisibilityForTests();
  for (const agent of ["agent-a", "agent-b"]) setAgentHidden(agent, true);
  server.use(http.get("/api/obs", () => HttpResponse.json(obsWithPrivateAgents)));
});

function renderHealth() {
  window.history.pushState({}, "", "/health");
  return render(<App />);
}

describe("Health page under presentation mode", () => {
  it("shows every agent when not presenting", async () => {
    renderHealth();
    expect(await screen.findByText("Per-agent health")).toBeInTheDocument();
    expect(screen.getByText("agent-b")).toBeInTheDocument();
    expect(screen.getAllByText(/agent-a/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/playground/).length).toBeGreaterThan(0);
  });

  it("hides private agents from alerts, ledger, and per-agent health while presenting", async () => {
    setPresenting(true);
    renderHealth();
    expect(await screen.findByText("Per-agent health")).toBeInTheDocument();
    // playground rows still render on every section
    expect(screen.getByText("playground wedged")).toBeInTheDocument();
    expect(screen.getAllByText(/playground/).length).toBeGreaterThan(0);
    // no trace of the private agents anywhere on the page
    expect(screen.queryByText(/agent-a/)).not.toBeInTheDocument();
    expect(screen.queryByText(/agent-b/)).not.toBeInTheDocument();
  });
});
