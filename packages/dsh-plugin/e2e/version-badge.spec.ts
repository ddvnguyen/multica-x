import { expect, test, type Page } from "@playwright/test";

/**
 * M-155: one version string where an error report starts.
 *
 * The badge (bottom right, every page) and the Settings row must show the
 * identical stamp — if they can disagree, "which build broke" is exactly the
 * question the string exists to answer. Runs in Chromium against the
 * keep-alive harness: both surfaces live inside shadow roots, and the row is
 * rendered by the same injected `__MMX_BUILD__` the shipped bundle carries.
 *
 * The three pages are the ones an agent actually names in a report: the
 * board, an issue, and Settings itself. Screenshots at 1440×1300 (light and
 * dark) are taken here for the PR evidence.
 */

/** The seeded issue the harness board always shows. */
const BOARD_ROW = "Rail click stays warm";

/** The issue detail's actions menu — proves the route actually changed. */
const ISSUE_ACTIONS =
  "button:has(svg.lucide-ellipsis), button:has(svg.lucide-more-horizontal)";

/** `x.x.x-yyMMMdd.HH.mm`, the Owner's format (M-155). */
const STAMP_SHAPE = /^\d+\.\d+\.\d+-\d{2}[A-Z]{3}\d{2}\.\d{2}\.\d{2}$/;

const SHOT_DIR = process.env.MMX_SHOT_DIR;

async function openBoard(page: Page): Promise<void> {
  await page.setViewportSize({ width: 1440, height: 1300 });
  await page.goto("/keep-alive.html");
  await page.waitForFunction(() => (window.__islandHost?.() ?? null) !== null);
  await page.getByTestId("rail-multica").click();
  await expect(page.getByText(BOARD_ROW)).toBeVisible();
}

async function badgeStamp(page: Page): Promise<string> {
  const badge = page.getByTestId("mmx-badge");
  await expect(badge).toBeVisible();
  return (await badge.textContent())?.trim() ?? "";
}

async function settingsRowStamp(page: Page): Promise<string> {
  await page.getByTestId("rail-multica-settings").click();
  const row = page.getByTestId("mmx-version");
  await expect(row).toBeVisible();
  return (await row.textContent())?.trim() ?? "";
}

async function shoot(page: Page, name: string): Promise<void> {
  if (!SHOT_DIR) return;
  await page.screenshot({ path: `${SHOT_DIR}/${name}.png` });
}

test("badge matches the Settings row from the board", async ({ page }) => {
  await openBoard(page);

  const board = await badgeStamp(page);
  expect(board).toMatch(STAMP_SHAPE);
  await shoot(page, "m155-board-light");

  expect(await settingsRowStamp(page)).toBe(board);
});

test("badge matches the Settings row from an issue", async ({ page }) => {
  await openBoard(page);
  await page.getByText(BOARD_ROW).click();
  await expect(page.locator(ISSUE_ACTIONS).first()).toBeVisible();

  const issue = await badgeStamp(page);
  expect(issue).toMatch(STAMP_SHAPE);
  await shoot(page, "m155-issue-light");

  expect(await settingsRowStamp(page)).toBe(issue);
});

test("Settings row, badge and tooltip agree", async ({ page }) => {
  await openBoard(page);
  await page.getByTestId("rail-multica-settings").click();

  const row = page.getByTestId("mmx-version");
  await expect(row).toBeVisible();
  const stamp = (await row.textContent())?.trim() ?? "";
  expect(stamp).toMatch(STAMP_SHAPE);
  expect(await badgeStamp(page)).toBe(stamp);
  await shoot(page, "m155-settings-light");

  // The tooltip carries what the compact stamp cannot: the UTC build time
  // and the short sha — the two halves of "which commit was this".
  await expect(page.getByTestId("mmx-badge")).toHaveAttribute(
    "title",
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z · \S+$/,
  );
});

test("a dark shell keeps the badge and the row identical", async ({ page }) => {
  // Dark from the first frame: the island reads the shell theme at mount
  // (`island.ts` syncTheme follows the document element), so the class has
  // to be there before the app boots — the same recipe as the parity
  // harness's `?theme=dark`. An init script runs before the parser has
  // built <html>, so the class lands as soon as it exists; toggling after
  // paint races the screenshot and would photograph a light frame.
  await page.addInitScript(() => {
    const markDark = () => document.documentElement.classList.add("dark");
    if (document.documentElement) {
      markDark();
    } else {
      new MutationObserver((_mutations, observer) => {
        if (!document.documentElement) return;
        markDark();
        observer.disconnect();
      }).observe(document, { childList: true });
    }
  });
  await openBoard(page);
  await page.getByTestId("rail-multica-settings").click();

  const row = page.getByTestId("mmx-version");
  await expect(row).toBeVisible();
  const stamp = (await row.textContent())?.trim() ?? "";
  expect(stamp).toMatch(STAMP_SHAPE);
  expect(await badgeStamp(page)).toBe(stamp);
  // The island's own surface followed the shell into dark, or the shot
  // below would be light evidence of a dark claim.
  const dark = await page.evaluate(() => {
    const surface = window
      .__islandHost?.()
      ?.shadowRoot?.querySelector(".multica-island-surface");
    return surface?.classList.contains("dark") ?? false;
  });
  expect(dark).toBe(true);
  await shoot(page, "m155-settings-dark");
});
