import { expect, test, type Page } from "@playwright/test";

/**
 * M-154: the panel zoom setting.
 *
 * Three things only a real browser can show, and each is asserted here:
 * `zoom` actually scales the surface's layout (jsdom has no layout at all),
 * the level survives a reload (it is a device preference), and a dialog opened
 * at 125 % is still anchored and clickable — the dialogs are portalled outside
 * the zoomed subtree, which is the whole reason zoom stops at the panel.
 */

const PREFERENCES = "/route.html?route=settings&query=tab%3Dpreferences";
const BOARD = "/route.html?route=board";
const ADD_ISSUE = "button:has(svg.lucide-plus)";

async function boot(page: Page): Promise<void> {
  await page.waitForFunction(
    () => window.__islandHost?.()?.isConnected === true,
    undefined,
    { timeout: 20_000 },
  );
  await expect(page.getByText("Connecting to Multica…")).toHaveCount(0, {
    timeout: 20_000,
  });
}

/** The wrapper `IslandApp` puts around the provider tree. */
function surface(page: Page) {
  return page.locator("[data-panel-zoom]").first();
}

async function pickZoom(page: Page, level: string): Promise<void> {
  await page.locator('[aria-label="Panel zoom"]').click();
  await page.getByRole("option", { name: level, exact: true }).click();
}

/** The topmost element at a point, piercing every shadow root on the way down. */
async function hitTest(
  page: Page,
  point: { x: number; y: number },
): Promise<boolean> {
  return page.evaluate(({ x, y }) => {
    let element = document.elementFromPoint(x, y);
    while (element?.shadowRoot != null) {
      const inner = element.shadowRoot.elementFromPoint(x, y);
      if (inner === null || inner === element) break;
      element = inner;
    }
    return element?.closest("[data-hit-probe='primary']") != null;
  }, point);
}

test("zoom scales the panel live, survives a reload, and keeps dialogs anchored", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1300 });
  await page.goto(PREFERENCES);
  await boot(page);

  await expect(surface(page)).toHaveAttribute("data-panel-zoom", "100");
  const before = await page.getByText("Panel zoom").first().boundingBox();
  expect(before).not.toBeNull();

  await pickZoom(page, "125%");

  await expect(surface(page)).toHaveAttribute("data-panel-zoom", "125");
  expect(
    await surface(page).evaluate((element) => getComputedStyle(element).zoom),
  ).toBe("1.25");
  // A known element inside the surface grows with it — a `transform` would
  // leave this box at its unscaled size and only paint it bigger.
  const after = await page.getByText("Panel zoom").first().boundingBox();
  expect(after).not.toBeNull();
  expect(after!.height).toBeGreaterThan(before!.height * 1.15);

  // Reload: the level is a device preference, not page state.
  await page.reload();
  await boot(page);
  await expect(surface(page)).toHaveAttribute("data-panel-zoom", "125");
  expect(
    await page.evaluate(() => window.localStorage.getItem("multica.dsh.zoom")),
  ).toBe("125");

  // A dialog at 125 %: it renders outside the zoomed subtree, so it opens at
  // its own size and takes a click at its own pixels (M-146, re-run zoomed).
  await page.goto(BOARD);
  await boot(page);
  await expect(surface(page)).toHaveAttribute("data-panel-zoom", "125");

  await page.locator(ADD_ISSUE).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  const primary = dialog.getByRole("button", { name: /create/i }).first();
  await expect(primary).toBeVisible();
  await primary.evaluate((element) =>
    element.setAttribute("data-hit-probe", "primary"),
  );
  const box = await primary.boundingBox();
  expect(box).not.toBeNull();
  expect(
    await hitTest(page, {
      x: box!.x + box!.width / 2,
      y: box!.y + box!.height / 2,
    }),
  ).toBe(true);

  await dialog.locator("button:has(svg.lucide-x)").first().click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  // Back to 100 %.
  await page.goto(PREFERENCES);
  await boot(page);
  await pickZoom(page, "100%");
  await expect(surface(page)).toHaveAttribute("data-panel-zoom", "100");
  expect(
    await surface(page).evaluate((element) => getComputedStyle(element).zoom),
  ).toBe("1");
});
