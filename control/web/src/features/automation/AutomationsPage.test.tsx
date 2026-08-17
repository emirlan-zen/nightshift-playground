import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { server } from "@/test/msw";
import { encodeGraphState } from "@/features/graph/urlState";
import { Automations, TemplateLibrary } from "./AutomationsPage";

// The template editor is where an operator declares the two ADR-0023 capabilities
// that change a run's shape: which node may produce nodes at runtime, and which
// engine each member of a compared pair runs on. Both are edits, so both are
// asserted through the real form, not the types.

function renderEditor(id: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/automations/templates/${id}`]}>
        <Routes>
          <Route path="/automations/templates/:id" element={<TemplateLibrary />} />
          <Route path="*" element={<div>elsewhere</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("automation list", () => {
  it("says which automations can grow at runtime and which compare members", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/automations"]}>
          <Automations />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    // Runtime fan-out is visible before opening the automation.
    expect(await screen.findByText("refine emits ≤3 → judge")).toBeInTheDocument();
    // A slotted member reads as a second member of the same role, not a new role.
    expect(screen.getByText("1. Attempt ∥ Attempt #2")).toBeInTheDocument();
  });
});

describe("template editor · runtime fan-out", () => {
  it("declares an emitter on the selected node and prices its worst case", async () => {
    renderEditor("full-delivery");
    fireEvent.click(await screen.findByTestId("graph-node-h:1:0:implement"));
    const toggle = await screen.findByLabelText("Implement produces nodes at runtime");
    fireEvent.click(toggle);

    // Defaults land a valid spec (contestant + judge), so enabling emission never
    // opens on a red form.
    expect(screen.getByLabelText("Emission cap")).toHaveValue(3);
    expect(screen.getByRole("button", { name: "Attempt", pressed: true })).toBeInTheDocument();
    expect(screen.getByLabelText("Fan-in node")).toHaveValue("judge");
    // 5 stage members + 3 emitted + 1 fan-in.
    expect(screen.getByText("9 / 16")).toBeInTheDocument();
  });

  it("refuses a cap wider than an emitter may go", async () => {
    renderEditor("full-delivery");
    fireEvent.click(await screen.findByTestId("graph-node-h:1:0:implement"));
    fireEvent.click(await screen.findByLabelText("Implement produces nodes at runtime"));
    fireEvent.change(screen.getByLabelText("Emission cap"), { target: { value: "9" } });

    expect(await screen.findByText(/may emit between 1 and 8 nodes/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /save revision/i })).toBeDisabled();
  });

  it("refuses an emitted node that itself emits", async () => {
    renderEditor("full-delivery");
    fireEvent.click(await screen.findByTestId("graph-node-h:1:0:implement"));
    fireEvent.click(await screen.findByLabelText("Implement produces nodes at runtime"));
    // Emitting the emitter's own role would make depth unbounded.
    fireEvent.click(screen.getByRole("button", { name: "Implement", pressed: false }));
    expect(
      await screen.findByText(/is itself an emitter — an emitted node may not emit again/),
    ).toBeInTheDocument();
  });
});

describe("template editor · harness comparison", () => {
  it("duplicates a node into a slotted member with its own engine pin", async () => {
    renderEditor("full-delivery");
    fireEvent.click(await screen.findByTestId("graph-node-h:1:0:implement"));
    fireEvent.click(await screen.findByLabelText("Duplicate Implement in parallel"));

    // The copy takes the next slot, so it is addressable on its own.
    fireEvent.click(await screen.findByTestId("graph-node-h:1:1:implement#2"));
    const panel = await screen.findByTestId("member-runtime-panel");
    expect(panel).toHaveTextContent("implement#2");
    fireEvent.change(screen.getByLabelText("Member executor"), { target: { value: "codex" } });
    fireEvent.change(screen.getByLabelText("Member model"), {
      target: { value: "gpt-5.6-sol" },
    });
    // The pin shows on the stage list next to the member it governs.
    await waitFor(() => expect(screen.getByTitle("Runs on codex · gpt-5.6-sol")).toBeVisible());
  });

  it("reads an existing template's member pins back into the form", async () => {
    renderEditor("compare-harness");
    fireEvent.click(await screen.findByTestId("graph-node-h:0:1:attempt#2"));
    const panel = await screen.findByTestId("member-runtime-panel");
    expect(panel).toHaveTextContent("attempt#2");
    expect(screen.getByLabelText("Member executor")).toHaveValue("codex");
    expect(screen.getByLabelText("Member model")).toHaveValue("gpt-5.6-sol");
  });
});

describe("template editor · URL state", () => {
  // A phone refresh restores the in-progress graph from the URL. Rebuilding the
  // stages from `role` alone dropped the `#N` slot, so the second member of a
  // compared pair silently vanished — losing exactly the pin being edited.
  it("restores a slotted member across a refresh", async () => {
    const g = encodeGraphState({
      template: { id: "compare-harness", revision: "new" },
      routes: [],
      nodes: [
        {
          id: "h:0:0:attempt",
          role: "attempt",
          member: "attempt",
          stage: 0,
          order: 0,
          lane: "happy",
        },
        {
          id: "h:0:1:attempt#2",
          role: "attempt",
          member: "attempt#2",
          slot: 2,
          stage: 0,
          order: 1,
          lane: "happy",
        },
        { id: "h:1:0:judge", role: "judge", member: "judge", stage: 1, order: 0, lane: "happy" },
      ],
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[`/automations/templates/compare-harness?g=${g}`]}>
          <Routes>
            <Route path="/automations/templates/:id" element={<TemplateLibrary />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId("graph-node-h:0:1:attempt#2")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("graph-node-h:0:1:attempt#2"));
    expect(await screen.findByTestId("member-runtime-panel")).toHaveTextContent("attempt#2");
  });
});

describe("template editor · judge scores", () => {
  it("tables an automation's scores newest revision first, with the delta", async () => {
    renderEditor("compare-harness");
    await screen.findByText("3ba1f2c");
    const table = screen.getByTestId("automation-scores");
    // Newest revision first; the arrow compares it with the one it replaced.
    const revisions = [...table.querySelectorAll("tbody tr")].map(
      (row) => row.children[2].textContent,
    );
    expect(revisions).toEqual(["3ba1f2c", "9c7de11"]);
    expect(table).toHaveTextContent("3.8");
    // The delta is in percentage POINTS (76% vs 64%), so a judge that changes
    // scale between revisions cannot read as a regression.
    expect(table).toHaveTextContent("▲12");
    // Runtime identity is on the row: an anonymous comparison table compares
    // nothing.
    expect(table).toHaveTextContent("claude");
    expect(table).toHaveTextContent("tests pass; error paths unexercised");
  });

  it("says so plainly when nothing has judged the automation", async () => {
    server.use(http.get("/api/scores", () => HttpResponse.json({ groups: [], rows: [] })));
    renderEditor("compare-harness");
    expect(await screen.findByText(/Nothing has judged this automation yet/)).toBeInTheDocument();
  });
});
