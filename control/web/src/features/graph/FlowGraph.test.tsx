import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FlowGraph } from "./FlowGraph";
import { edgeTraveled } from "./model";
import type { GraphDraft } from "./types";
import type { FlowNodeDefinition } from "@/features/flows/api";

const catalog = [
  { id: "plan", name: "Plan", effort: "high", minutes: 60, executor: "claude" },
  { id: "implement", name: "Implement", effort: "xhigh", minutes: 240 },
] as unknown as FlowNodeDefinition[];

const draft = (extra?: Partial<GraphDraft["nodes"][number]>[]): GraphDraft => ({
  template: { id: "t", revision: "r1" },
  routes: [],
  nodes: [
    { id: "h:0:0:plan", role: "plan", stage: 0, order: 0, lane: "happy", ...(extra?.[0] ?? {}) },
    {
      id: "h:1:0:implement",
      role: "implement",
      stage: 1,
      order: 0,
      lane: "happy",
      ...(extra?.[1] ?? {}),
    },
  ],
});

describe("FlowGraph canvas", () => {
  it("expands to a fullscreen overlay via the button, F, and Escape", async () => {
    render(<FlowGraph draft={draft()} catalog={catalog} mode="edit" />);
    const canvas = screen.getByTestId("graph-canvas");
    expect(canvas.className).not.toContain("fullscreen");
    fireEvent.click(screen.getByTestId("graph-expand"));
    expect(canvas.className).toContain("fullscreen");
    fireEvent.keyDown(window, { key: "Escape" });
    expect(canvas.className).not.toContain("fullscreen");
    fireEvent.keyDown(window, { key: "f" });
    expect(canvas.className).toContain("fullscreen");
    // Typing in a field must never toggle the canvas.
    fireEvent.keyDown(window, { key: "Escape" });
    const input = document.createElement("input");
    document.body.appendChild(input);
    fireEvent.keyDown(input, { key: "f" });
    expect(canvas.className).not.toContain("fullscreen");
  });

  it("surfaces per-node runtime on template cards and a toolbar on the selected node", async () => {
    const onDuplicate = vi.fn();
    const onRoute = vi.fn();
    const onRemove = vi.fn();
    render(
      <FlowGraph
        draft={draft()}
        catalog={catalog}
        mode="edit"
        selected="h:0:0:plan"
        onDuplicateNode={onDuplicate}
        onAddRouteFrom={onRoute}
        onRemoveNode={onRemove}
      />,
    );
    // Runtime chips: effort · minutes straight from the catalog definition.
    expect(await screen.findByText("high · 60m")).toBeInTheDocument();
    expect(screen.getByText("xhigh · 240m")).toBeInTheDocument();
    fireEvent.click(await screen.findByLabelText("Duplicate Plan in parallel"));
    fireEvent.click(screen.getByLabelText("Add exception route from Plan"));
    fireEvent.click(screen.getByLabelText("Remove Plan from the canvas"));
    expect(onDuplicate).toHaveBeenCalledWith("h:0:0:plan");
    expect(onRoute).toHaveBeenCalledWith("h:0:0:plan");
    expect(onRemove).toHaveBeenCalledWith("h:0:0:plan");
  });

  it("marks the path actually taken on run graphs", async () => {
    const { container } = render(
      <FlowGraph
        draft={draft([{ state: "delivered" }, { state: "running" }])}
        catalog={catalog}
        mode="run"
      />,
    );
    await waitFor(() => expect(container.querySelector(".react-flow__edge")).toBeTruthy());
    expect(container.querySelector(".react-flow__edge.traveled")).toBeTruthy();
  });

  it("keeps run-mode canvases read-only: no toolbar, no runtime chips overriding live state", () => {
    render(<FlowGraph draft={draft([{ state: "delivered" }])} catalog={catalog} mode="run" />);
    expect(screen.queryByLabelText(/Duplicate/)).toBeNull();
    expect(screen.queryByText("high · 60m")).toBeNull();
  });
});

describe("ADR-0023 graph markers", () => {
  it("shows an emitter's budget on the template card and per-member runtime", async () => {
    render(
      <FlowGraph
        draft={draft([
          { emits: { max: 3, roles: ["implement"], fanIn: "implement" } },
          { slot: 2, member: "implement#2", executor: "codex", effort: "high", minutes: 45 },
        ])}
        catalog={catalog}
        mode="edit"
      />,
    );
    expect(await screen.findByText(/emits ≤3 → implement/)).toBeInTheDocument();
    // The member pin wins over the definition's xhigh · 240m default.
    expect(screen.getByText("high · 45m")).toBeInTheDocument();
    expect(screen.getByText("#2")).toBeInTheDocument();
  });

  it("marks a runtime-emitted node on the run graph and announces its slot", async () => {
    render(
      <FlowGraph
        draft={draft([
          { state: "delivered" },
          { state: "running", slot: 2, emittedBy: "h:0:0:plan" },
        ])}
        catalog={catalog}
        mode="run"
      />,
    );
    expect(await screen.findByText("emitted")).toBeInTheDocument();
    expect(screen.getByLabelText(/Implement slot 2, running/)).toBeInTheDocument();
    // The card itself is marked, so provenance survives a glance at the shape.
    expect(screen.getByTestId("graph-node-h:1:0:implement").className).toContain("emitted");
    expect(screen.getByTestId("graph-node-h:0:0:plan").className).not.toContain("emitted");
  });
});

describe("edgeTraveled", () => {
  it("only counts a delivered source as traveled", () => {
    expect(edgeTraveled("delivered")).toBe(true);
    for (const s of ["running", "waiting", "skipped", "stopped", undefined])
      expect(edgeTraveled(s)).toBe(false);
  });
});
