import { expect, test, type Page } from "@playwright/test";

// ADR-0023 end-to-end: the three v2 capabilities as an operator meets them —
// a run that produced its own nodes, an automation that runs one prompt on two
// engines, and judge scores by prompt revision. These drive the real dev-mode
// binary against internal/control/dev.go's seed, so they assert the whole chain
// (Go handler → api.ts → canvas) rather than a fixture.
//
// Seeded ids are timestamp-based, so each test finds its subject through the API
// instead of hardcoding an id: the seed's SHAPE is the contract, not its names.

async function runWithEmittedNodes(page: Page): Promise<string> {
  const flows = await (await page.request.get("/api/flows")).json();
  for (const flow of flows) {
    const detail = await (await page.request.get(`/api/flows/${flow.id}`)).json();
    if ((detail.nodeViews ?? []).some((n: { emittedBy?: string }) => n.emittedBy)) return flow.id;
  }
  throw new Error("dev seed has no run with emitted nodes (ADR-0023 dev-seed contract)");
}

async function automationWithScores(page: Page): Promise<string> {
  const templates = await (await page.request.get("/api/flow-templates")).json();
  for (const t of templates) {
    const res = await page.request.get(`/api/scores?automation=${encodeURIComponent(t.id)}`);
    if (!res.ok()) continue;
    const scores = await res.json();
    if ((scores.groups ?? []).length) return t.id;
  }
  throw new Error("dev seed has no scored automation (ADR-0023 dev-seed contract)");
}

test("a run shows the nodes it produced itself, and who judged them", async ({ page }) => {
  const id = await runWithEmittedNodes(page);
  await page.goto(`/runs/${id}`, { waitUntil: "networkidle" });
  await expect(page.getByTestId("graph-canvas")).toBeVisible();

  // The canvas marks runtime-emitted nodes, so the shape of the run reads as
  // "what it decided", not just "what the automation listed".
  await expect(page.getByText("emitted").first()).toBeVisible();

  // Tapping an emitted node names the emitter that produced it.
  await page.locator('[data-testid^="graph-node-"]').filter({ hasText: "emitted" }).first().click();
  const detail = page.getByTestId("graph-node-detail");
  await expect(detail).toBeVisible();
  await expect(detail.getByText(/emitted by/i)).toBeVisible();
});

test("an automation compares two engines on one prompt", async ({ page }) => {
  await page.goto("/automations/templates/compare-harness", { waitUntil: "networkidle" });
  await expect(page.getByTestId("graph-canvas")).toBeVisible();

  // Two members of one role in one stage — the second carries a slot suffix, so
  // its runtime is pinned separately while the prompt revision stays shared.
  await expect(page.getByTestId("graph-node-h:0:0:attempt")).toBeVisible();
  const second = page.getByTestId("graph-node-h:0:1:attempt#2");
  await expect(second).toBeVisible();
  // One card per engine, each with its own brand mark.
  await expect(page.getByRole("img", { name: /Claude/ }).first()).toBeVisible();
  await expect(page.getByRole("img", { name: /Codex/ }).first()).toBeVisible();

  // Selecting the slotted member opens its own runtime pins.
  await second.click();
  const panel = page.getByTestId("member-runtime-panel");
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("attempt#2");
  await expect(page.getByLabel("Member executor")).toHaveValue("codex");
});

test("an automation's judge scores read by prompt revision", async ({ page }) => {
  const id = await automationWithScores(page);
  await page.goto(`/automations/templates/${id}`, { waitUntil: "networkidle" });
  const table = page.getByTestId("automation-scores");
  await expect(table).toBeVisible();
  await expect(table.locator("tbody tr").first()).toBeVisible();
  // At least two revisions of the same (node, dimension) so the delta is real.
  expect(await table.locator("tbody tr").count()).toBeGreaterThan(1);
  await expect(table).toContainText("Prompt");
});

test("declaring runtime fan-out is an edit on the canvas", async ({ page }) => {
  await page.goto("/automations/templates/new", { waitUntil: "networkidle" });
  await page.getByTestId("graph-node-h:0:0:plan").click();
  await page.getByLabel("Plan produces nodes at runtime").check();
  await expect(page.getByLabel("Emission cap")).toHaveValue("3");
  // The card states the budget the node will be told about in its prompt.
  await expect(page.getByText(/emits ≤3 →/).first()).toBeVisible();

  // A cap the run budget cannot hold is refused before the save round-trip.
  await page.getByLabel("Emission cap").fill("9");
  await expect(page.getByText(/may emit between 1 and 8 nodes/)).toBeVisible();
  await expect(page.getByRole("button", { name: /Save revision/ })).toBeDisabled();
});

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("the v2 surfaces stay flush at 390px", async ({ page }) => {
    const overflow = async (where: string) => {
      await page.waitForTimeout(300);
      const px = await page.evaluate(() => {
        const de = document.documentElement;
        return Math.max(
          de.scrollWidth - de.clientWidth,
          document.body.scrollWidth - document.body.clientWidth,
        );
      });
      expect(px, `${where} overflows by ${px}px`).toBeLessThanOrEqual(1);
    };
    const scored = await automationWithScores(page);
    await page.goto(`/automations/templates/${scored}`, { waitUntil: "networkidle" });
    await expect(page.getByTestId("automation-scores")).toBeVisible();
    await overflow("scored automation @ 390px");

    const id = await runWithEmittedNodes(page);
    await page.goto(`/runs/${id}`, { waitUntil: "networkidle" });
    await expect(page.getByTestId("graph-canvas")).toBeVisible();
    await overflow("emitted run @ 390px");
  });
});
