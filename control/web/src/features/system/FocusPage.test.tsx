import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { http, HttpResponse } from "msw";
import { server } from "../../test/msw";
import { Focus } from "./FocusPage";

// Regression for the promote-then-clobber bug (#60 review finding): the Focus
// editor seeds its textarea from file.content once. When a Promote appends a
// block to products.md and qk.focus refetches, the editor MUST adopt the new
// content (when it has no unsaved edits) — otherwise it looks dirty with stale
// text and a Save overwrites the appended block.

function renderFocus() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/focus/products"]}>
        <Routes>
          <Route path="/focus/:id" element={<Focus />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Focus editor sync after promotion", () => {
  it("adopts the appended block, stays clean, and saves the synced content", async () => {
    let products = "# bets\n";
    let savedBody = "";
    server.use(
      http.get("/api/focus", () =>
        HttpResponse.json({
          files: [
            { id: "products", content: products, modifiedAt: 1751700000 },
            { id: "projects", content: "# repos\n", modifiedAt: 1751700000 },
          ],
        }),
      ),
      http.get("/api/ideas", () =>
        HttpResponse.json({
          files: [{ id: "2026-07-11", title: "Idea one", modifiedAt: 1751700000 }],
        }),
      ),
      http.get("/api/idea", () =>
        HttpResponse.json({
          id: "2026-07-11",
          title: "Idea one",
          modifiedAt: 1751700000,
          content: "# Idea one\n\nbody\n",
        }),
      ),
      http.post("/api/ideas/:id/promote", () => {
        products = products.replace(/\n*$/, "\n") + "\n## Idea one\n\n_Promoted from backlog._\n";
        return HttpResponse.json({ id: "products", content: products, modifiedAt: 1751700001 });
      }),
      http.put("/api/focus/:id", async ({ request }) => {
        const b = (await request.json()) as { content: string };
        products = b.content;
        savedBody = b.content;
        return HttpResponse.json({ id: "products", content: products, modifiedAt: 1751700002 });
      }),
    );

    renderFocus();

    const ta = (await screen.findByLabelText("products.md content")) as HTMLTextAreaElement;
    expect(ta.value).toBe("# bets\n");
    expect(screen.queryByText(/unsaved changes/i)).not.toBeInTheDocument();

    // Expand the idea row (its list query resolves separately), then promote it.
    fireEvent.click(await screen.findByText("Idea one"));
    fireEvent.click(await screen.findByRole("button", { name: /Promote/ }));

    // The editor adopts the refetched, appended content...
    await waitFor(() =>
      expect((screen.getByLabelText("products.md content") as HTMLTextAreaElement).value).toContain(
        "## Idea one",
      ),
    );
    // ...and is NOT marked dirty (this is what prevents a clobbering Save).
    expect(screen.queryByText(/unsaved changes/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Save/ })).toBeDisabled();

    // Now the operator makes a genuine edit on top and saves: the persisted
    // content must contain BOTH the promoted block AND the edit — proving the
    // save is built on the synced base, never the stale pre-promotion text.
    const synced = (screen.getByLabelText("products.md content") as HTMLTextAreaElement).value;
    fireEvent.change(screen.getByLabelText("products.md content"), {
      target: { value: synced + "\nhand edit\n" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Save/ }));

    await waitFor(() => {
      expect(savedBody).toContain("## Idea one");
      expect(savedBody).toContain("hand edit");
    });
  });
});
