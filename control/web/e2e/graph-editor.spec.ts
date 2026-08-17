import { expect, test } from "@playwright/test";

test("builds and saves a scheduled graph with drag and needs-work route", async ({ page }) => {
  await page.goto("/automations/templates/new");
  await page.getByLabel("Template id").fill("graph-ci");
  await page.getByLabel("Name").fill("Graph CI");
  const canvas = page.getByTestId("graph-canvas");
  await page.getByTestId("palette-node-plan").dragTo(canvas);
  await page.getByTestId("palette-node-implement").dragTo(canvas);
  await page.getByTestId("palette-node-review").click();
  await page.getByRole("button", { name: "Edge" }).click();
  await page.getByRole("checkbox", { name: "Recurring night run" }).check();
  await page.getByLabel("Schedule repository").fill("nightshift");
  await page.getByLabel("Schedule goal").fill("Verify graph editing");
  await page.getByRole("button", { name: /Save revision/ }).click();
  await expect(page).toHaveURL(/\/automations$/);
  await expect(page.getByText(/daily 22:00/)).toBeVisible();
});

test("schedules fold into Automations with unified terminology, not flow", async ({ page }) => {
  // ADR-0020 UI audit: /automations/profiles folds into the Automation Studio,
  // and the ≈80%-dead legacy profile graph (and its "…graph language" copy) is
  // removed. The Schedules section must render there in the unified vocabulary.
  await page.goto("/automations/profiles");
  await expect(page).toHaveURL(/\/automations$/);
  await expect(page.getByText(/active for/i)).toBeVisible();
  await expect(page.getByText(/flow graph language/i)).toHaveCount(0);
});

test("edits the canvas with mouse and keyboard: select, delete, undo, toolbar, fullscreen", async ({
  page,
}) => {
  // A new template starts from the default plan → implement → review → validate shape.
  await page.goto("/automations/templates/new");
  await expect(page.getByTestId("graph-node-h:0:0:plan")).toBeVisible();

  // Keyboard: select the first node, Delete removes it, ⌘Z restores it.
  await page.getByTestId("graph-node-h:0:0:plan").click();
  await expect(page.getByTestId("graph-node-h:0:0:plan")).toHaveClass(/selected/);
  await page.keyboard.press("Delete");
  await expect(page.getByTestId("graph-node-h:0:0:plan")).toHaveCount(0);
  await expect(page.getByTestId("graph-node-h:0:0:implement")).toBeVisible();
  await page.keyboard.press("ControlOrMeta+z");
  await expect(page.getByTestId("graph-node-h:0:0:plan")).toBeVisible();
  await expect(page.getByTestId("graph-node-h:1:0:implement")).toBeVisible();

  // Toolbar on the selected node: duplicate-in-parallel fans the stage out. The
  // copy takes the next member slot (ADR-0023) so its runtime can be pinned on
  // its own — hence the `#2` in the id.
  await page.getByTestId("graph-node-h:1:0:implement").click();
  await page.getByLabel("Duplicate Implement in parallel").click();
  await expect(page.getByTestId("graph-node-h:1:1:implement#2")).toBeVisible();

  // Fullscreen overlay: expand, add from the in-canvas node library, Esc exits.
  await page.getByTestId("graph-expand").click();
  await expect(page.getByTestId("graph-canvas")).toHaveClass(/fullscreen/);
  await page.getByTestId("canvas-palette-review").click();
  await expect(page.getByTestId("graph-node-h:4:0:review")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("graph-canvas")).not.toHaveClass(/fullscreen/);
});

test.describe("phone graph", () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test("supports tap fallback and retains a canvas", async ({ page }) => {
    await page.goto("/automations/templates/new");
    await page.getByTestId("palette-node-plan").click();
    await expect(page.getByTestId("graph-canvas")).toBeVisible();
    await expect(page.getByRole("button", { name: /Save revision/ })).toBeVisible();
    await page.reload();
    await expect(page.getByTestId("graph-canvas")).toBeVisible();
  });
});
