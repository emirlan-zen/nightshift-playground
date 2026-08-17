import { test, expect, type Locator, type Page } from "@playwright/test";

// Workstream 3 regression guard: NO page may scroll horizontally at a 390px
// phone width. Inbox and the Runs graph were the known offenders; the automation
// studio + template editor were live offenders found during the pass. These
// drive the real dev-mode binary (seeded data) by URL, so they survive refactors.
//
// Every case asserts a page-specific landmark BEFORE measuring overflow, so a
// silent redirect to /home or an error shell can't pass the overflow check while
// rendering the wrong (or no) page. The matrix covers every distinct page layout
// in App.tsx — static pages, the dynamic detail/editor routes, and the legacy
// route aliases.

const PHONE = { width: 390, height: 844 };
// ADR-0020 UI audit requires zero page overflow at BOTH the phone and the
// desktop reference widths, so the route matrix runs at each.
const VIEWPORTS = [
  { name: "390px", size: PHONE },
  { name: "1440px", size: { width: 1440, height: 900 } },
];

async function expectNoHorizontalScroll(page: Page, where: string) {
  // Allow a 1px rounding slack; anything more is a real overflow. Check both the
  // document element and the body — either scrolling the page is a regression.
  const overflow = await page.evaluate(() => {
    const de = document.documentElement;
    return Math.max(
      de.scrollWidth - de.clientWidth,
      document.body.scrollWidth - document.body.clientWidth,
    );
  });
  expect(overflow, `${where} overflows horizontally by ${overflow}px at 390w`).toBeLessThanOrEqual(
    1,
  );
}

type PageCase = {
  route: string;
  // The pathname the app settles on (differs only for redirect aliases).
  settlesTo?: string;
  // A page-specific landmark proving the intended content rendered. Omitted for
  // static pages, which fall back to asserting <main> mounted with real content.
  landmark?: (page: Page) => Locator;
};

// Seeded ids come from internal/control/dev.go for the playground workspace.
const CASES: PageCase[] = [
  // --- static pages -----------------------------------------------------------
  { route: "/home" },
  { route: "/sessions" },
  { route: "/runs" },
  { route: "/runs/new" },
  { route: "/automations" },
  { route: "/automations/templates/new" },
  { route: "/automations/nodes" },
  { route: "/automations/nodes/review" },
  // ADR-0020 UI audit: /automations/profiles and /night folded into their host
  // surfaces, so the bare routes now redirect (settlesTo differs from route).
  { route: "/automations/profiles", settlesTo: "/automations" },
  { route: "/night", settlesTo: "/runs" },
  { route: "/inbox" },
  { route: "/tickets" },
  { route: "/focus" },
  { route: "/system" },
  { route: "/health" },
  { route: "/usage" },
  { route: "/prompts" },
  // --- dynamic detail / editor pages (distinct layouts) -----------------------
  // (the run-detail route uses a timestamp-based id, so it is covered by a
  //  click-through test below rather than a hardcoded URL)
  {
    route: "/night/playground/20260705-0500-sweep-ef56",
    landmark: (p) => p.getByText(/20260705-0500-sweep-ef56/).first(),
  },
  {
    route: "/tickets/playground/20260705-0900-tkt-a1b2",
    landmark: (p) => p.getByText("Wire up dev-mode seed data").first(),
  },
  {
    route: "/focus/products",
    landmark: (p) => p.getByLabel(/products\.md content/i),
  },
  {
    route: "/prompts/global",
    landmark: (p) => p.getByRole("heading", { name: "global" }),
  },
  {
    route: "/automations/templates/refine-adr",
    landmark: (p) => p.getByTestId("graph-canvas"),
  },
  // ADR-0023: the compare-harness editor carries the widest new sidebar content
  // (member pins) plus the scores table, both new overflow candidates.
  {
    route: "/automations/templates/compare-harness",
    landmark: (p) => p.getByTestId("graph-node-h:0:1:attempt#2"),
  },
  {
    route: "/automations/profiles/broad",
    landmark: (p) => p.getByText("broad", { exact: true }).first(),
  },
  // --- legacy route aliases (still mounted; redirect or render the same) ------
  { route: "/servers" },
  { route: "/pipeline", settlesTo: "/automations" },
  {
    route: "/pipeline/broad",
    settlesTo: "/automations/profiles/broad",
    landmark: (p) => p.getByText("broad", { exact: true }).first(),
  },
  { route: "/morning", settlesTo: "/home" },
];

for (const vp of VIEWPORTS) {
  for (const c of CASES) {
    test(`no horizontal scroll at ${vp.name}: ${c.route}`, async ({ page }) => {
      await page.setViewportSize(vp.size);
      await page.goto(c.route, { waitUntil: "networkidle" });
      // let the graph canvas / async lists settle
      await page.waitForTimeout(300);

      // 1) The app settled on the intended route — a redirect to /home would fail
      //    here instead of silently passing the overflow check.
      const settled = c.settlesTo ?? c.route;
      expect(new URL(page.url()).pathname, `${c.route} redirected unexpectedly`).toBe(settled);

      // 2) The intended page content actually rendered.
      if (c.landmark) {
        await expect(c.landmark(page), `${c.route} landmark not visible`).toBeVisible();
      } else {
        const main = page.locator("main");
        await expect(main).toBeVisible();
        expect((await main.innerText()).trim().length, `${c.route} main is empty`).toBeGreaterThan(
          20,
        );
      }

      // 3) Only then measure overflow.
      await expectNoHorizontalScroll(page, `${c.route} @ ${vp.name}`);
    });
  }
}

test("Sessions run-history control meets the 44px tap target with visible focus", async ({
  page,
}) => {
  // Workstream 3 regression: the inline "Run history" control was an ~11px hit
  // area with no focus treatment. It must now be >=44px tall and show the focus
  // ring under keyboard focus.
  await page.setViewportSize(PHONE);
  await page.goto("/sessions", { waitUntil: "networkidle" });
  const link = page.getByTestId("run-history-link");
  await expect(link).toBeVisible();
  const box = await link.boundingBox();
  expect(box, "run-history control has a bounding box").not.toBeNull();
  expect(box!.height, "run-history tap target is at least 44px tall").toBeGreaterThanOrEqual(44);
  // Native button ⇒ keyboard-focusable; focusVisible forces the :focus-visible
  // heuristic so the global focus ring (a box-shadow) applies deterministically.
  await link.evaluate((el: HTMLElement) => el.focus({ focusVisible: true }));
  expect(await link.evaluate((el) => el === document.activeElement)).toBe(true);
  const shadow = await link.evaluate((el) => getComputedStyle(el).boxShadow);
  expect(shadow, "focused control shows a visible focus ring").not.toBe("none");
});

test("run detail and its /flows alias render and don't scroll at 390px", async ({ page }) => {
  // The run-detail id is timestamp-based (seeded in dev.go), so reach it through
  // the UI rather than a hardcoded URL. The FlowGraph canvas was a known mobile
  // offender before .flow-graph got an explicit width.
  await page.setViewportSize(PHONE);
  await page.goto("/runs", { waitUntil: "networkidle" });
  await page.getByText("Add a verified approval workflow").first().click();
  await expect(page).toHaveURL(/\/runs\/flow-/);
  await expect(
    page.getByRole("heading", { name: /Add a verified approval workflow/ }),
  ).toBeVisible();
  await page.waitForTimeout(300);
  await expectNoHorizontalScroll(page, "run detail");

  // The /flows/:id legacy alias renders the same read-only run graph.
  const id = new URL(page.url()).pathname.split("/").pop()!;
  await page.goto(`/flows/${id}`, { waitUntil: "networkidle" });
  expect(new URL(page.url()).pathname).toBe(`/flows/${id}`);
  await expect(
    page.getByRole("heading", { name: /Add a verified approval workflow/ }),
  ).toBeVisible();
  await page.waitForTimeout(300);
  await expectNoHorizontalScroll(page, "run detail (/flows alias)");

  // The graph-only run detail must also stay flush at the desktop width, where
  // the graph sits beside the acceptance/guidance sidebar (ADR-0020 audit).
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`/runs/${id}`, { waitUntil: "networkidle" });
  await expect(page.getByTestId("graph-canvas")).toBeVisible();
  await page.waitForTimeout(300);
  await expectNoHorizontalScroll(page, "run detail @ 1440px");
});
