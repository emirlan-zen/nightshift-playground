import { describe, it, expect, afterEach, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import type { ReactNode } from "react";
import { server } from "../test/msw";
import { useRunScores } from "./queries";

// A judge delivers WHILE its run is live, so an already-open run page must pick
// the scores up on its own. staleTime alone did not: the query only refetched on
// focus or navigation, so an operator watching a comparison saw an empty card
// until they touched the page.

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const empty = { groups: [], rows: [] };
const scored = {
  groups: [],
  rows: [
    {
      runId: "flow-20260817-1411-3ff49738",
      judgeNode: "judge-r1",
      subject: "attempt-r1",
      dimension: "correctness",
      score: 4,
      max: 5,
      createdAt: 1755400000,
    },
  ],
};

afterEach(() => vi.useRealTimers());

describe("useRunScores", () => {
  it("polls a live run so a judge's scores appear without navigating", async () => {
    let call = 0;
    server.use(
      http.get("/api/scores", () => {
        call++;
        return HttpResponse.json(call === 1 ? empty : scored);
      }),
    );
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { result } = renderHook(() => useRunScores("flow-20260817-1411-3ff49738", true), {
      wrapper,
    });
    await waitFor(() => expect(result.current.data).toEqual(empty));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(21_000);
    });
    await waitFor(() => expect(result.current.data?.rows).toHaveLength(1));
  });

  it("leaves a terminal run alone — its scores cannot change", async () => {
    let call = 0;
    server.use(
      http.get("/api/scores", () => {
        call++;
        return HttpResponse.json(empty);
      }),
    );
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { result } = renderHook(() => useRunScores("flow-20260817-1411-3ff49738", false), {
      wrapper,
    });
    await waitFor(() => expect(result.current.data).toEqual(empty));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(call).toBe(1);
  });
});
