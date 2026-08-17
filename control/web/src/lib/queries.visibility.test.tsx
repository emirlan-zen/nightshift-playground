import { describe, it, expect, beforeEach } from "vitest";
import { renderHook, waitFor, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import type { ReactNode } from "react";
import { server } from "../test/msw";
import { useObs, useServers, useServersUnfiltered, useTickets, useUsage } from "./queries";
import { resetVisibilityForTests, setAgentHidden, setPresenting } from "./visibility";

// Presentation mode is enforced in the query layer so every view inherits it.
// These tests pin that contract: with the eye on, hidden agents vanish from
// each agent-scoped hook while the raw cache stays complete.

beforeEach(() => {
  localStorage.clear();
  resetVisibilityForTests();
  for (const agent of ["agent-a", "agent-b", "agent-c"]) setAgentHidden(agent, true);
});

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const mixedStatus = [
  { company: "playground", active: true, sessions: [] },
  { company: "agent-a", active: true, sessions: [] },
  { company: "agent-b", active: false, sessions: [] },
];

describe("presentation mode in the query layer", () => {
  it("useServers drops hidden agents only while presenting", async () => {
    server.use(http.get("/api/status", () => HttpResponse.json(mixedStatus)));
    const { result } = renderHook(() => useServers(false), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.map((s) => s.company)).toEqual([
      "playground",
      "agent-a",
      "agent-b",
    ]);

    act(() => setPresenting(true));
    await waitFor(() => expect(result.current.data!.map((s) => s.company)).toEqual(["playground"]));

    act(() => setPresenting(false));
    await waitFor(() => expect(result.current.data!).toHaveLength(3));
  });

  it("useServersUnfiltered keeps the full list for the settings card", async () => {
    server.use(http.get("/api/status", () => HttpResponse.json(mixedStatus)));
    setPresenting(true);
    const { result } = renderHook(() => useServersUnfiltered(), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!).toHaveLength(3);
  });

  it("useTickets hides company groups, including dotted session variants", async () => {
    server.use(
      http.get("/api/tickets", () =>
        HttpResponse.json([
          { agent: "playground", tickets: [] },
          { agent: "agent-b.2", tickets: [] },
          { agent: "agent-c", tickets: [] },
        ]),
      ),
    );
    setPresenting(true);
    const { result } = renderHook(() => useTickets(), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.map((g) => g.agent)).toEqual(["playground"]);
  });

  it("useObs filters alerts, per-agent stats, and runs", async () => {
    server.use(
      http.get("/api/obs", () =>
        HttpResponse.json({
          generated: 0,
          alerts: [
            { agent: "agent-a", kind: "stall", detail: "x", ts: 1 },
            { agent: "playground", kind: "stall", detail: "y", ts: 2 },
          ],
          agents: [
            { agent: "agent-a", sessions: 1, turns: 1, toolCalls: 1, toolErrors: 0, lastTs: 1 },
          ],
          runs: [
            {
              agent: "gov/playground/ratchet",
              runId: "r1",
              started: 1,
              report: false,
              status: "ok",
            },
            { agent: "agent-b", runId: "r2", started: 1, report: false, status: "ok" },
          ],
          budget: { nightBudgetUSD: 0, nightSpendUSD: 0, weekSpendUSD: 0, topUpsMinted: 0 },
        }),
      ),
    );
    setPresenting(true);
    const { result } = renderHook(() => useObs(false), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    const obs = result.current.data!;
    expect(obs.alerts.map((a) => a.agent)).toEqual(["playground"]);
    expect(obs.agents).toEqual([]);
    expect(obs.runs.map((r) => r.agent)).toEqual(["gov/playground/ratchet"]);
  });

  it("useUsage hides per-agent rows but keeps totals/days/models", async () => {
    const sums = { in: 1, out: 1, cacheRead: 0, cacheCreate: 0, total: 2, messages: 1, costUSD: 1 };
    server.use(
      http.get("/api/usage", () =>
        HttpResponse.json({
          windowDays: 14,
          generated: 0,
          totals: sums,
          days: [{ ...sums, date: "2026-08-15" }],
          models: [],
          agents: [
            { ...sums, agent: "playground" },
            { ...sums, agent: "agent-a" },
          ],
        }),
      ),
    );
    setPresenting(true);
    const { result } = renderHook(() => useUsage(false), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.agents.map((a) => a.agent)).toEqual(["playground"]);
    expect(result.current.data!.days).toHaveLength(1);
    expect(result.current.data!.totals.total).toBe(2);
  });
});
