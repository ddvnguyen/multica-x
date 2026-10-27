import { expect, test, type Page } from "@playwright/test";

/**
 * M-130: the island is built once, detached, and moved between rail entries.
 *
 * The load-bearing assertion is layout, not identity: the warm-up runs while
 * the island has no box at all, so every measurement a page makes at mount
 * (the inbox's virtual list above all) sees width 0. Attaching it has to give
 * those pages a real box and a real row count, or the "keep-alive" is a panel
 * that comes back blank. jsdom has no layout and cannot tell the two apart,
 * so this runs in Chromium against the harness shell.
 */

const BOARD_ROW = "Rail click stays warm";

async function clickRail(page: Page, id: string): Promise<void> {
  await page.getByTestId(`rail-${id}`).click();
}

/**
 * The same rail click, dispatched on the button instead of hit-tested.
 *
 * While an island menu is open, Base UI's own inert layer sits over the page
 * inside the popup seat, so Playwright refuses the click. That layer is the
 * overlay's business (M-116), not this spec's: what is under test here is
 * whether leaving a panel closes what it left open.
 */
async function clickRailDirect(page: Page, id: string): Promise<void> {
  await page.getByTestId(`rail-${id}`).evaluate((button: HTMLElement) => button.click());
}

/** Rows with a real box: a virtual list measured at width 0 renders none. */
async function inboxRows(page: Page): Promise<number> {
  return page.locator("#main [role='button']").evaluateAll(
    (nodes) =>
      nodes.filter((node) => {
        const box = node.getBoundingClientRect();
        return box.width > 120 && box.height > 20;
      }).length,
  );
}

test("keeps the island warm and lays it out again on every rail click", async ({
  page,
}) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(String(error)));

  await page.goto("/keep-alive.html");

  // The warm-up is the production one: `apply` at an idle window, with no
  // cell mounted, so the island exists before anything can claim it.
  await page.waitForFunction(() => (window.__islandHost?.() ?? null) !== null);
  await page.waitForFunction(() =>
    (window.__requests ?? []).some((url) => url.startsWith("/multica/config")),
  );
  const booted = await page.evaluate(() => {
    const host = window.__islandHost?.() ?? null;
    return {
      connected: host?.isConnected ?? null,
      configCalls: (window.__requests ?? []).filter((url) =>
        url.startsWith("/multica/config"),
      ).length,
    };
  });
  expect(booted.connected).toBe(false);
  expect(booted.configCalls).toBe(1);

  // First click: the island moves in and the board has to be laid out.
  await clickRail(page, "multica");
  await expect(page.getByText(BOARD_ROW)).toBeVisible();
  const boardBox = await page.getByText(BOARD_ROW).boundingBox();
  expect(boardBox?.width ?? 0).toBeGreaterThan(40);
  expect(boardBox?.height ?? 0).toBeGreaterThan(8);

  const hostId = await page.evaluate(() => {
    const host = window.__islandHost?.();
    host?.setAttribute("data-probe", "first");
    return host?.getAttribute("data-probe") ?? null;
  });
  expect(hostId).toBe("first");

  // Second click: the same host moves to the other cell, and the virtual list
  // — which measured itself while the island was detached — has to lay out.
  await clickRail(page, "multica-inbox");
  await expect(page.locator("#main [role='button']").first()).toBeVisible();
  expect(await inboxRows(page)).toBe(3);

  // Third click: back to the board, still the same island, no second boot.
  await clickRail(page, "multica");
  await expect(page.getByText(BOARD_ROW)).toBeVisible();
  const again = await page.evaluate(() => {
    const host = window.__islandHost?.() ?? null;
    return {
      same: host?.getAttribute("data-probe") === "first",
      connected: host?.isConnected ?? null,
      inMain: host?.parentElement?.parentElement?.id ?? null,
      configCalls: (window.__requests ?? []).filter((url) =>
        url.startsWith("/multica/config"),
      ).length,
    };
  });
  expect(again).toMatchObject({
    same: true,
    connected: true,
    inMain: "main",
    configCalls: 1,
  });

  expect(failures).toEqual([]);
});

test("keeps one popup seat in the shell's overlay layer across every move", async ({
  page,
}) => {
  await page.goto("/keep-alive.html");
  await page.waitForFunction(() => (window.__islandHost?.() ?? null) !== null);

  await clickRail(page, "multica");
  await expect(page.getByText(BOARD_ROW)).toBeVisible();

  // The board's own filter menu, portalled into the shell's overlay layer —
  // outside the panel, which is why a popup is not something detaching a node
  // takes with it.
  await page.getByRole("button", { name: /filter/i }).first().click();
  await expect(page.locator("[role='menu']")).toBeVisible();
  // One seat per document: a per-attach seat would leave one behind on every
  // rail click, each with its own copy of the island's 292 KB stylesheet.
  await expect(page.locator("[data-multica-popup-seat]")).toHaveCount(1);

  // Leaving closes what the panel had open, and the seat stays where it was
  // for the next panel to portal into. (The press that closes an overlay whose
  // owner outlives the route is covered in `tests/island.test.tsx`, where a
  // portal can be made to survive the switch.)
  await clickRailDirect(page, "multica-inbox");
  await expect(page.locator("[role='menu']")).toHaveCount(0);
  await expect(page.locator("[data-multica-popup-seat]")).toHaveCount(1);

  await clickRailDirect(page, "multica");
  await expect(page.getByText(BOARD_ROW)).toBeVisible();
  await expect(page.locator("[data-multica-popup-seat]")).toHaveCount(1);
});
