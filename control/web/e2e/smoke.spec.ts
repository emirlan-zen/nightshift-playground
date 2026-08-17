import { test, expect } from "@playwright/test";

// These drive the real app (Go dev-mode binary + seeded data) by URL and
// visible text/role — never by component structure — so they keep passing
// across the control-page refactor. Seed data is defined in control/dev.go.

test("Sessions lists seeded endpoints with their run state", async ({ page }) => {
  await page.goto("/sessions");
  await expect(page.getByTestId("server-card-title-playground")).toBeVisible();
  await expect(page.getByText("running").first()).toBeVisible();
});

test("+ New session reveals the name field (mint flow entry)", async ({ page }) => {
  await page.goto("/sessions");
  // A running agent's card offers "New session"; clicking it opens the namer.
  await page
    .getByRole("button", { name: /new session/i })
    .first()
    .click();
  await expect(page.getByPlaceholder(/name/i).first()).toBeVisible();
});

test("deep-link to a ticket opens its detail", async ({ page }) => {
  await page.goto("/tickets/playground/20260705-0900-tkt-a1b2");
  // The body text only renders inside the opened detail view (the board card
  // shows just the title), so it uniquely proves the deep-link opened it.
  await expect(page.getByText("Sample open ticket so the board isn't empty in dev.")).toBeVisible();
});

test("deep-link to a night run renders its report as a full page", async ({ page }) => {
  await page.goto("/night/playground/20260705-0500-sweep-ef56");
  await expect(page.getByText(/Dev-mode sample report/i)).toBeVisible();
  // full page (not a modal): a Back control returns to the Night list.
  await expect(page.getByRole("button", { name: /back/i })).toBeVisible();
});

test("/ lands on the simplified Home with flows and manual sessions", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/home/);
  await expect(page.getByText(/Work without babysitting it/i)).toBeVisible();
  await expect(page.getByText(/Add a verified approval workflow/i)).toBeVisible();
  await expect(page.getByRole("heading", { name: /manual sessions/i })).toBeVisible();
});

test("Runs folds the night history in as a wave timeline", async ({ page }) => {
  // /night now redirects into the single Runs surface (ADR-0020); the night
  // history + wave timeline render as a section of /runs.
  await page.goto("/night");
  await expect(page).toHaveURL(/\/runs/);
  // seeded playground waves show as timeline chips (medic delivered, synth done).
  await expect(page.getByText("Night of", { exact: false }).first()).toBeVisible();
  await expect(page.getByTitle(/23:00/).first()).toBeVisible();
});

test("workbench navigation updates the URL (state lives in the URL)", async ({ page }) => {
  await page.goto("/home");
  await page.getByRole("link", { name: /^Runs/ }).click();
  await expect(page).toHaveURL(/\/runs/);
  await page.getByRole("link", { name: /^Automations/ }).click();
  await expect(page).toHaveURL(/\/automations/);
  await page.getByRole("link", { name: /System/ }).click();
  await expect(page).toHaveURL(/\/system/);
});

test("switching from run creation to Automation Studio keeps the workbench mounted", async ({
  page,
}) => {
  await page.goto("/runs/new");
  await page.getByRole("link", { name: "Automations", exact: true }).click();
  await expect(page).toHaveURL(/\/automations$/);
  await expect(page.getByText("// automation studio")).toBeVisible();
  await expect(page.getByText("Run templates")).toBeVisible();
});

test("mobile: the five primary workbench destinations stay directly tappable", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/home");
  await page.getByRole("link", { name: /^Runs/ }).click();
  await expect(page).toHaveURL(/\/runs/);
  await page.getByRole("link", { name: /^Build/ }).click();
  await expect(page).toHaveURL(/\/automations/);
  await page.getByRole("link", { name: /^Inbox/ }).click();
  await expect(page).toHaveURL(/\/inbox/);
});

test("mobile: the Automation step's graph preview never overflows the page", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/runs/new");
  await page.getByLabel("Goal").fill("Phone-created run");
  await page.getByLabel("Acceptance criteria").fill("It ships");
  await page.getByRole("button", { name: /Choose automation/ }).click();
  await expect(page.getByTestId("graph-canvas")).toBeVisible();
  // The graph's intrinsic width must never widen the page (the P0-1 class).
  // On failure, name the widest elements — Linux scrollbars + font metrics
  // surface overflows macOS never shows, and a bare px count isn't actionable.
  const { overflow, offenders } = await page.evaluate(() => {
    const de = document.documentElement;
    const cw = de.clientWidth;
    const bad: string[] = [];
    document.querySelectorAll("main *").forEach((el) => {
      const r = el.getBoundingClientRect();
      if (r.right > cw + 1 && !bad.some((b) => b.includes(el.tagName)))
        bad.push(`${el.tagName}.${String(el.className).slice(0, 60)} right=${Math.round(r.right)}`);
    });
    return {
      overflow: Math.max(
        de.scrollWidth - cw,
        document.body.scrollWidth - document.body.clientWidth,
      ),
      offenders: bad.slice(0, 6),
    };
  });
  expect(
    overflow,
    `automation step overflows by ${overflow}px at 390w; widest: ${offenders.join(" | ")}`,
  ).toBeLessThanOrEqual(1);
});

test("mobile: Direction and System stay reachable from the header", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/home");
  // The phone-editable focus files (ADR-0013) must never require a desktop.
  await page.getByRole("link", { name: "Direction" }).click();
  await expect(page).toHaveURL(/\/focus/);
  await page.getByRole("link", { name: "System" }).click();
  await expect(page).toHaveURL(/\/system/);
});

test("operator can create a task-first refining run", async ({ page }) => {
  await page.goto("/runs/new");
  await page.getByLabel("Goal").fill("Refine ADR-0060 into a buildable decision");
  await page.getByLabel("Repository").selectOption({ label: "playground / example-project" });
  await page
    .getByLabel("Acceptance criteria")
    .fill("Every open question is resolved\nA fresh reviewer finds no blocking ambiguity");
  await page.getByRole("button", { name: /Instant/ }).click();
  await page.getByRole("button", { name: /Choose automation/ }).click();
  // The Automation step previews the composed run as a graph (not just a list).
  await expect(page.getByTestId("graph-canvas")).toBeVisible();
  await page.getByRole("button", { name: /Refine an ADR/ }).click();
  await expect(page.getByTestId("graph-node-h:0:0:refine")).toBeVisible();
  await expect(page.getByText(/Exact node prompt/)).toBeVisible();
  await page.getByRole("button", { name: /Review run/ }).click();
  await page.getByRole("button", { name: "Start unattended run" }).click();
  await expect(page).toHaveURL(/\/runs\/flow-/);
  await expect(page.getByRole("heading", { name: /Refine ADR-0060/ })).toBeVisible();
  await expect(page.getByText("instant handoff")).toBeVisible();
  await expect(page.getByText("Refine", { exact: true }).first()).toBeVisible();
  await page.getByText("Technical details", { exact: true }).click();
  await expect(page.getByText(/nightshift\/flow-/)).toBeVisible();
});

test("Focus tab loads the seeded file, edits, and saves", async ({ page }) => {
  await page.goto("/focus");
  // Seeded products.md renders in the editor (dev seed in control/dev.go).
  const editor = page.getByRole("textbox", { name: /products\.md content/i });
  await expect(editor).toHaveValue(/Promoted bets/);
  // Edit -> dirty state -> save round-trips through PUT /api/focus/products.
  await editor.fill("# Promoted bets\n\nedited from e2e\n");
  await expect(page.getByText("unsaved changes")).toBeVisible();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("saved ✓")).toBeVisible();
  // The projects file is one URL-backed switch away.
  await page.getByRole("button", { name: "projects", exact: true }).click();
  await expect(page).toHaveURL(/\/focus\/projects/);
  await expect(page.getByRole("textbox", { name: /projects\.md content/i })).toHaveValue(
    /Curated projects/,
  );
});

test("Stop is a two-step confirm — first tap arms, second fires", async ({ page }) => {
  await page.goto("/sessions");
  const stop = page.getByRole("button", { name: "Stop", exact: true }).first();
  await stop.click();
  // Arming replaces the label; the session is NOT stopped by the first tap.
  const sure = page.getByRole("button", { name: "Sure?" }).first();
  await expect(sure).toBeVisible();
  await sure.click();
  await expect(page.getByRole("button", { name: "Sure?" })).toHaveCount(0);
});

test("node prompt is inspectable, editable, and versioned", async ({ page }) => {
  await page.goto("/automations/nodes/review");
  const editor = page.getByRole("textbox", { name: "Node prompt body" });
  await expect(editor).toHaveValue(/review the actual diff and artifacts/i);
  await editor.fill("# Node · Review\n\nReview the real diff and prove every finding.\n");
  await page.getByRole("button", { name: /Save prompt revision/ }).click();
  await expect(page.getByText("custom", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "history" }).click();
  await expect(page.getByRole("button", { name: "Restore" })).toBeVisible();
});

test("node runtime contract can be customized and reset", async ({ page }) => {
  await page.goto("/automations/nodes/review");
  await page.getByRole("tab", { name: "runtime" }).click();
  await page.getByLabel("Reasoning effort").selectOption("xhigh");
  await page.getByLabel(/Auto-stop ceiling/).fill("120");
  await page.getByRole("button", { name: /Save runtime/ }).click();
  await expect(page.getByRole("button", { name: /Reset defaults/ })).toBeVisible();
});

test("definition proposal shows its why and applies through the versioned path", async ({
  page,
}) => {
  await page.goto("/inbox");
  const proposal = page.getByRole("group", {
    name: "Definition proposal harden-review-node",
  });
  await expect(proposal).toBeVisible();
  await expect(page.getByText(/4 of 5 runs this week needed 2\+ amend rounds/)).toBeVisible();
  await proposal.getByRole("button", { name: "Apply" }).click();
  await expect(proposal).toHaveCount(0);
  // The applied body is now the review node's live prompt.
  await page.goto("/automations/nodes/review");
  await expect(page.getByRole("textbox", { name: "Node prompt body" })).toHaveValue(
    /Reject any finding you cannot reproduce/,
  );
});

test("parallel run detail marks group members and verdicts", async ({ page }) => {
  await page.goto("/runs");
  await page.getByText("Split the notifier", { exact: false }).first().click();
  await expect(page.getByText("∥ parallel").first()).toBeVisible();
  await expect(page.getByText("ok", { exact: true }).first()).toBeVisible();
});

test("a new node role can be defined from the library", async ({ page }) => {
  await page.goto("/automations/nodes/new");
  await page.getByRole("textbox", { name: "Node id" }).fill("benchmark");
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("Benchmark");
  await page
    .getByRole("textbox", { name: "Description" })
    .fill("Measure performance before and after the change.");
  await page.getByRole("textbox", { name: "Output contract" }).fill("Benchmark comparison");
  await page
    .getByRole("textbox", { name: "Role prompt" })
    .fill("# Node · Benchmark\n\nMeasure the hot paths and report deltas with evidence.");
  await page.getByRole("button", { name: /Create node/ }).click();
  await expect(page.getByRole("heading", { name: "Benchmark" })).toBeVisible();
  await expect(page.getByText("operator-defined").first()).toBeVisible();
});

test("review node runs on the codex executor and the runtime picker offers it", async ({
  page,
}) => {
  // ADR-0018: the built-in review node dispatches to the Codex CLI.
  await page.goto("/automations/nodes/review");
  await expect(page.getByText("executor codex")).toBeVisible();
  await page.getByRole("tab", { name: "runtime" }).click();
  const executor = page.getByLabel("Executor");
  await expect(executor).toHaveValue("codex");
  await expect(executor.locator("option")).toHaveText(["claude", "codex"]);
});

test("night composer can queue a codex-executor run", async ({ page }) => {
  // The night composer folded into Runs (ADR-0020). Use the exact "Run" toggle so
  // it doesn't collide with the page's "New run" button.
  await page.goto("/runs");
  await page.getByRole("button", { name: "Run", exact: true }).first().click();
  await page.getByPlaceholder(/Queue a night run/).fill("Audit the preview stack");
  await page.getByLabel("Executor").selectOption("codex");
  await page.getByRole("button", { name: "Queue", exact: true }).click();
  // The queued job landed…
  await expect(page.getByText("Audit the preview stack")).toBeVisible();
  // …and codex jobs now wear the real OpenAI brand mark instead of a text label.
  await expect(page.getByRole("img", { name: "OpenAI" }).first()).toBeVisible();
});

test("new-run form offers the ADR-0019 concurrency picker (default serial)", async ({ page }) => {
  await page.goto("/runs/new");
  await expect(page.getByText(/concurrency/i).first()).toBeVisible();
  await expect(page.getByRole("button", { name: /1 · serial/i })).toBeVisible();
  await page.getByRole("button", { name: "≤3" }).click();
});

test("a scheduled template wears its recurring badge in the studio", async ({ page }) => {
  await page.goto("/automations");
  await expect(page.getByText(/daily 02:30/i).first()).toBeVisible();
});

test("run detail shows the serialized concurrency chip", async ({ page }) => {
  await page.goto("/runs");
  await page.getByText("Add a verified approval workflow").first().click();
  await expect(page.getByText(/serialized/i).first()).toBeVisible();
});

test("run detail renders the ADR-0019 graph with a fan-out summary", async ({ page }) => {
  await page.goto("/runs");
  await page.getByText("Split the notifier").first().click();
  await expect(page.getByTestId("graph-canvas")).toBeVisible();
  await expect(page.getByText(/2 fanned out/i)).toBeVisible();
});
