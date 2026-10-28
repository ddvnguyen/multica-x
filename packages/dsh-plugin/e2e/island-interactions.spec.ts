import { expect, test } from "@playwright/test";
import { clearShellKeys, focusReport, shellKeys } from "./parity/focus";

/**
 * M-148: the behaviours that go with the styling fix.
 *
 * These are the interaction half of the acceptance gate: an overlay stack that
 * closes in the wrong order, a dialog that leaks focus, or a keystroke that
 * reaches dsh's shortcuts while the reader is typing are all defects the style
 * comparison cannot see. Everything runs against the island rendering, with a
 * fake shell that records the keys its document-level handler receives.
 */

const ISLAND = "/parity.html?mode=island&theme=light";

test.beforeEach(async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1300 });
  await page.goto(ISLAND);
});

test("Escape closes the top layer only", async ({ page }) => {
  await page.getByTestId("popover-trigger").click();
  await expect(page.getByTestId("popover-content")).toBeVisible();

  await page.getByTestId("popover-open-dialog").click();
  await expect(page.getByTestId("dialog-content")).toBeVisible();

  await page.keyboard.press("Escape");

  // The dialog is the top layer, so it is the one Escape takes.
  await expect(page.getByTestId("dialog-content")).toHaveCount(0);
  await expect(page.getByTestId("popover-content")).toBeVisible();
});

test("a click outside closes the menu", async ({ page }) => {
  await page.getByTestId("menu-trigger").click();
  await expect(page.getByTestId("menu-content")).toBeVisible();

  // A press on the shell's own background, outside the island entirely.
  await page.mouse.click(4, 4);

  await expect(page.getByTestId("menu-content")).toHaveCount(0);
});

test("opening a dialog from a menu closes the menu", async ({ page }) => {
  await page.getByTestId("menu-trigger").click();
  await expect(page.getByTestId("menu-content")).toBeVisible();

  await page.getByTestId("menu-open-dialog").click();

  await expect(page.getByTestId("dialog-content")).toBeVisible();
  await expect(page.getByTestId("menu-content")).toHaveCount(0);
});

test("Tab stays inside an open dialog", async ({ page }) => {
  await page.getByTestId("dialog-trigger").click();
  await expect(page.getByTestId("dialog-content")).toBeVisible();

  // Base UI's trap is the behaviour; the loop is long enough to leave the
  // dialog several times over if the trap ever stops working.
  for (let step = 0; step < 12; step += 1) {
    await page.keyboard.press("Tab");
    const focus = await focusReport(page);
    expect(focus.insideDialog, `focus escaped after ${step + 1} Tab presses: ${JSON.stringify(focus)}`).toBe(
      true,
    );
  }
});

test("typing in a dialog input never reaches the shell's shortcuts", async ({ page }) => {
  await page.getByTestId("dialog-trigger").click();
  await expect(page.getByTestId("dialog-content")).toBeVisible();

  // The recorder is the fake shell's document-level handler, so it is the same
  // listener a dsh binding would be.
  await clearShellKeys(page);
  await page.getByTestId("dialog-input").click();
  await page.keyboard.type("jj kk");

  // The island's guard sits on the portal container the dialog renders into,
  // not just on the panel: a dialog is not a child of the page that opened it.
  expect(await shellKeys(page)).toEqual([]);

  // Control: the shell's own input still reaches the shell, so the assertion
  // above is about the island boundary and not a recorder that never fires.
  // The dialog is modal, so it has to close before anything outside it can be
  // clicked at all.
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("dialog-content")).toHaveCount(0);
  await page.locator("#shell-input").click();
  await page.keyboard.type("jj");
  expect(await shellKeys(page)).toEqual(["j", "j"]);
});

test("typing in a panel input stays out of the shell too", async ({ page }) => {
  // The menu trigger lives on the island's root, the same tree a page's own
  // search field does.
  await page.getByTestId("menu-trigger").click();
  await expect(page.getByTestId("menu-content")).toBeVisible();

  await clearShellKeys(page);
  await page.keyboard.press("ArrowDown");
  expect(await shellKeys(page)).toEqual([]);
});
