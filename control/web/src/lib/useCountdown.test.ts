import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { countdownAt, useCountdown } from "./useCountdown";

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("countdownAt", () => {
  const now = Date.parse("2026-07-11T06:00:00.000Z");

  it("formats a sub-hour deadline with seconds", () => {
    expect(countdownAt("2026-07-11T06:12:04.000Z", now)).toEqual({
      label: "12m 04s",
      done: false,
    });
  });

  it("keeps long deadlines compact", () => {
    expect(countdownAt("2026-07-11T08:03:44.000Z", now)).toEqual({
      label: "2h 03m",
      done: false,
    });
  });

  it("stops at zero and rejects invalid targets", () => {
    expect(countdownAt("2026-07-11T05:59:59.000Z", now)).toEqual({
      label: "00m 00s",
      done: true,
    });
    expect(countdownAt("not-a-date", now)).toBeNull();
  });
});

describe("useCountdown", () => {
  it("ticks once a second and clears its timer on unmount", () => {
    vi.useFakeTimers();
    const now = Date.parse("2026-07-11T06:00:00.000Z");
    vi.setSystemTime(now);
    const clearInterval = vi.spyOn(window, "clearInterval");

    const { result, unmount } = renderHook(() => useCountdown(new Date(now + 2_000)));
    expect(result.current?.label).toBe("00m 02s");

    act(() => vi.advanceTimersByTime(1_000));
    expect(result.current?.label).toBe("00m 01s");

    unmount();
    expect(clearInterval).toHaveBeenCalled();
  });
});
