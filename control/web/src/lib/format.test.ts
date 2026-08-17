import { describe, it, expect } from "vitest";
import { fmtRunId, fmtBytes, fmtUptime } from "./format";

describe("fmtBytes", () => {
  it("scales to binary units", () => {
    expect(fmtBytes(71 * 2 ** 30)).toBe("71.0 GB");
    expect(fmtBytes(512 * 2 ** 20)).toBe("512 MB");
    expect(fmtBytes(2 ** 40)).toBe("1.0 TB");
    expect(fmtBytes(0)).toBe("0 B");
  });
});

describe("fmtUptime", () => {
  it("renders coarse days/hours/minutes", () => {
    expect(fmtUptime(6 * 86400 + 6 * 3600)).toBe("6d 6h");
    expect(fmtUptime(3600 + 10 * 60)).toBe("1h 10m");
    expect(fmtUptime(40)).toBe("40s");
  });
});

describe("fmtRunId", () => {
  it("humanizes a run id, keeping the disambiguating hash", () => {
    const h = fmtRunId("20260705-0500-sweep-ef56");
    expect(h.label).toBe("Sweep · Jul 5, 05:00");
    expect(h.hash).toBe("ef56");
    expect(h.raw).toBe("20260705-0500-sweep-ef56");
  });

  it("maps the tkt kind to a readable word", () => {
    expect(fmtRunId("20260705-0900-tkt-a1b2").label).toBe("Ticket · Jul 5, 09:00");
  });

  it("passes non-matching ids through unchanged", () => {
    expect(fmtRunId("weird-id").label).toBe("weird-id");
    expect(fmtRunId("weird-id").hash).toBe("");
  });
});
