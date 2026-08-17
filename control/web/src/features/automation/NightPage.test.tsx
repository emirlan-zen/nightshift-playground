import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Report } from "@/lib/api";
import type { FlowReportMeta } from "@/lib/night";
import { ReportRow } from "./NightPage";

// P1-3: a flow node's report row must name its run (goal/template/node) and show
// the node verdict, instead of collapsing to the bare "Flow · <date>" fallback.

const flowMeta: FlowReportMeta = {
  goal: "Audit the entire control-plane web UI page by page",
  template: "ui-audit",
  node: "validate",
  verdict: "needs-work",
};

function mkReport(p: Partial<Report>): Report {
  return { id: "20260712-2213-flow-02df", mtime: 1_760_000_000, ...p };
}

describe("ReportRow flow enrichment", () => {
  it("names the flow goal, template, role, and verdict for a flow report with no banner headline", () => {
    render(<ReportRow report={mkReport({})} flow={flowMeta} onOpen={() => {}} />);
    // goal becomes the title (no headline to fall back to a bare run id)
    expect(screen.getByText(/Audit the entire control-plane web UI/)).toBeInTheDocument();
    // sub line carries template + node role; verdict is a badge
    expect(screen.getByText(/ui-audit/)).toBeInTheDocument();
    expect(screen.getByText(/validate/)).toBeInTheDocument();
    expect(screen.getByText("needs-work")).toBeInTheDocument();
    // the indistinct run-id fallback is gone
    expect(screen.queryByText(/^Flow ·/)).not.toBeInTheDocument();
  });

  it("keeps the banner headline as the title and still names the flow in the sub", () => {
    render(
      <ReportRow
        report={mkReport({ headline: "One finding left", tone: "partial", stats: "Findings 8/9" })}
        flow={flowMeta}
        onOpen={() => {}}
      />,
    );
    expect(screen.getByText("One finding left")).toBeInTheDocument();
    expect(screen.getByText("partial")).toBeInTheDocument(); // tone chip preserved
    expect(screen.getByText("needs-work")).toBeInTheDocument(); // verdict chip added
    // goal + template + stats all present in the sub, headline still the title
    expect(screen.getByText(/ui-audit/)).toBeInTheDocument();
    expect(screen.getByText(/Findings 8\/9/)).toBeInTheDocument();
  });

  it("leaves a non-flow report unchanged (headline title, run-id label in sub)", () => {
    render(
      <ReportRow
        report={mkReport({
          id: "20260712-0800-synth-9f3a",
          headline: "Two PRs shipped",
          wave: "synth",
        })}
        onOpen={() => {}}
      />,
    );
    expect(screen.getByText("Two PRs shipped")).toBeInTheDocument();
    // the humanized run-id label is preserved in the sub for non-flow headlined rows
    expect(screen.getByText(/Synth ·/)).toBeInTheDocument();
    expect(screen.getByText(/synth/)).toBeInTheDocument();
  });
});
