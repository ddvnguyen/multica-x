import { expect, test, type Page } from "@playwright/test";

/**
 * M-146: modal-store dialogs render inside the island.
 *
 * The fork's pages do not own their dialogs — they call `useModalStore.open`
 * and `ModalRegistry` renders whatever it names. The island mounted no
 * registry, so on lane z every dialog (create issue, delete confirm, set
 * parent) was a click that did nothing. These assertions run against the
 * production `apply` entry in the keep-alive harness, in a real Chromium:
 * the dialog portals into a shadow root, and jsdom has neither layout nor
 * shadow-DOM hit-testing, so neither the portal target nor "the button is
 * clickable" can be told apart there.
 */

/** The board column's add-issue button: the island's only "New issue". */
const ADD_ISSUE = "button:has(svg.lucide-plus)";

/** The issue detail header's actions menu trigger (icon-only). */
const ACTIONS_MENU =
  "button:has(svg.lucide-ellipsis), button:has(svg.lucide-more-horizontal)";

async function openBoard(page: Page): Promise<void> {
  await page.goto("/keep-alive.html");
  await page.waitForFunction(() => (window.__islandHost?.() ?? null) !== null);
  await page.getByTestId("rail-multica").click();
  await expect(page.getByText("Rail click stays warm")).toBeVisible();
}

/**
 * The topmost element at a point, piercing every shadow root on the way down.
 *
 * `document.elementFromPoint` stops at the first shadow host, and the island's
 * dialogs are two roots deep (the island's surface, then the popup seat in the
 * shell's overlay layer). Walking `shadowRoot.elementFromPoint` is what the
 * browser itself does for hit testing.
 */
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

test("opens the create-issue dialog from the board, and its close control closes it", async ({
  page,
}) => {
  const failures: string[] = [];
  page.on("pageerror", (error) => failures.push(String(error)));

  await openBoard(page);

  await page.locator(ADD_ISSUE).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  // Typing proves the dialog is a live form, not a painted backdrop.
  await dialog.getByRole("textbox").first().fill("Dialog from the island");
  await expect(dialog.getByRole("textbox").first()).toContainText(
    "Dialog from the island",
  );

  // This fork's create dialog has no "Cancel" button: its close control is the
  // X in the header (create-issue.tsx:973), which is the same `onClose`.
  await dialog.locator("button:has(svg.lucide-x)").first().click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(failures).toEqual([]);
});

test("the dialog's primary button receives the click at its own pixels", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1300 });
  await openBoard(page);

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
  const centre = {
    x: box!.x + box!.width / 2,
    y: box!.y + box!.height / 2,
  };
  expect(await hitTest(page, centre)).toBe(true);

  // The screenshot the card asks for: the dialog open in a 1300-high window,
  // measured once the entry animation has settled so it is not shot mid-fade.
  await dialog.getByRole("textbox").first().fill("Dialog from the island");
  await page.waitForTimeout(400);
  await page.screenshot({
    path: process.env.M146_SHOT ?? "m146-create-issue-dialog.png",
  });
});

test("the delete confirm opens from the issue page and Esc closes it", async ({
  page,
}) => {
  await page.goto("/keep-alive.html");
  await page.waitForFunction(() => (window.__islandHost?.() ?? null) !== null);
  await page.getByTestId("rail-multica-issue").click();
  await expect(page.getByText("MUL-1").first()).toBeVisible();

  await page.locator(ACTIONS_MENU).first().click();
  await page.getByRole("menuitem", { name: /delete/i }).first().click();

  // The delete confirm is an AlertDialog, not a Dialog: it reports itself as
  // `alertdialog` so a screen reader announces it as a decision, not a panel.
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
});

test("leaving the panel closes the dialog it had open", async ({ page }) => {
  await openBoard(page);

  await page.locator(ADD_ISSUE).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();

  // The dialog's own backdrop covers the rail, so the rail click is dispatched
  // rather than hit-tested: what is under test is whether leaving closes what
  // the panel left open, not the overlay's click ownership (M-116).
  await page
    .getByTestId("rail-multica-inbox")
    .evaluate((button: HTMLElement) => button.click());
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await page.getByTestId("rail-multica").click();
  await expect(page.getByText("Rail click stays warm")).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
