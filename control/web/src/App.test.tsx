import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { App } from "./App";

// Smoke + routing-config tests. These assert the STABLE routing contract (which
// URL the app resolves to) and that the shell mounts — not any view's internal
// markup, which the control-page refactor will churn. Deep user flows live in
// the Playwright e2e suite.

function renderAt(path: string) {
  window.history.pushState({}, "", path);
  return render(<App />);
}

describe("App routing", () => {
  it("mounts the shell and redirects / to /home", async () => {
    renderAt("/");
    // shell chrome is present (stable landmarks, not view internals)
    expect(await screen.findByRole("heading", { name: /nightshift/i })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /overview/i }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: /runs/i }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: /automations|build/i }).length).toBeGreaterThan(0);
    expect(window.location.pathname).toBe("/home");
  });

  it("redirects an unknown path to /home", async () => {
    renderAt("/does-not-exist");
    await screen.findByRole("heading", { name: /nightshift/i });
    expect(window.location.pathname).toBe("/home");
  });

  it("keeps a valid deep-link path (e.g. /tickets)", async () => {
    renderAt("/tickets");
    await screen.findByRole("heading", { name: /nightshift/i });
    expect(window.location.pathname).toBe("/tickets");
  });

  it("keeps a report deep-link (/night/:agent/:id)", async () => {
    renderAt("/night/playground/20260705-0500-sweep-ef56");
    await screen.findByRole("heading", { name: /nightshift/i });
    expect(window.location.pathname).toBe("/night/playground/20260705-0500-sweep-ef56");
  });

  it("keeps a first-class flow deep-link", async () => {
    renderAt("/flows/flow-20260710-1200-a1b2c3d4");
    await screen.findByRole("heading", { name: /nightshift/i });
    expect(window.location.pathname).toBe("/flows/flow-20260710-1200-a1b2c3d4");
  });
});
