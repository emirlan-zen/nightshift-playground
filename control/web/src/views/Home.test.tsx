import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { MemoryRouter, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { Flow, Health, Obs, ServerState, TicketGroup } from "@/lib/api";
import { server } from "@/test/msw";
import { Home } from "./Home";

const baseFlow: Flow = {
  id: "flow-home-running",
  agent: "playground",
  repo: "nightshift",
  goal: "Make Home glanceable",
  acceptance: [],
  template: "full-delivery",
  nodeRoles: [],
  created: "2026-07-11T04:00:00Z",
  updated: "2026-07-11T04:10:00Z",
  status: "running",
  batch: "home-test",
  branch: "ui/home-glance",
  base: "origin/main",
  worktreeState: "active",
  round: 0,
  nodeViews: [],
};

const obs: Obs = {
  generated: 1,
  alerts: [{ agent: "playground", kind: "stall", detail: "Needs attention", ts: 1 }],
  agents: [],
  runs: [],
  budget: { nightBudgetUSD: 100, nightSpendUSD: 25, weekSpendUSD: 200, topUpsMinted: 0 },
};

const health: Health = {
  auth: { ok: true, detail: "ok", checkedAt: 1 },
  recentFailures: [],
  forge: [],
};

function Location() {
  return <output data-testid="location">{useLocation().pathname}</output>;
}

function renderHome() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/home"]}>
        <Home />
        <Location />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Home glance strip", () => {
  it("summarizes live work and links every stat to its drill-down", async () => {
    const servers: ServerState[] = [
      {
        company: "playground",
        active: true,
        sessions: [{ instance: "playground", active: true }],
      },
    ];
    const tickets: TicketGroup[] = [
      {
        agent: "playground",
        tickets: [
          {
            id: "review-me",
            agent: "playground",
            title: "Review me",
            status: "review",
            createdBy: "agent",
            created: "2026-07-11T04:00:00Z",
            updated: "2026-07-11T04:10:00Z",
          },
        ],
      },
    ];

    server.use(
      http.get("/api/flows", () =>
        HttpResponse.json([baseFlow, { ...baseFlow, id: "flow-blocked", status: "blocked" }]),
      ),
      http.get("/api/status", () => HttpResponse.json(servers)),
      http.get("/api/tickets", () => HttpResponse.json(tickets)),
      http.get("/api/obs", () => HttpResponse.json(obs)),
      http.get("/api/health", () => HttpResponse.json(health)),
      http.get("/api/profiles/proposals", () =>
        HttpResponse.json([{ name: "faster-night", valid: true }]),
      ),
    );
    renderHome();

    expect(await screen.findByRole("button", { name: "runs: 1 active" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "sessions: 1 live" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "inbox: 4 open" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "budget: 25% tonight" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "budget: 25% tonight" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/health");
  });
});
