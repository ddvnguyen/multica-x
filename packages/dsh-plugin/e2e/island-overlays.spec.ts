import { expect, test, type Locator, type Page } from "@playwright/test";

/**
 * M-116: island overlays must receive clicks even when a shell overlay
 * covers the same pixels.
 *
 * The harness (`harness/main.tsx`) builds the island boundary from the
 * production `createPopupSeat` and paints a blocking card into the fake
 * shell's overlay layer — the dsh-workbuddy geometry that made every island
 * popup lose its clicks on lane z. jsdom cannot reproduce shadow-DOM
 * hit-testing, so these assertions run in a real Chromium: Playwright's own
 * click performs a hit-target check, which is the behaviour under test.
 */

const PICKED = "Claude";

/** Park the shell's blocking card exactly over a locator's box. */
async function coverWithCard(page: Page, locator: Locator): Promise<void> {
  const box = await locator.boundingBox();
  if (box === null) throw new Error("cover target has no box");
  await page.evaluate(({ x, y, width, height }) => {
    const card = document.getElementById("blocking-card");
    if (card === null) throw new Error("harness card is missing");
    Object.assign(card.style, {
      left: `${x}px`,
      top: `${y}px`,
      width: `${width}px`,
      height: `${height}px`,
    });
  }, box);
}

/**
 * The blocking assertion from the plan: a popup opened from the island lands
 * at its trigger, measured against the popup seat's new origin.
 */
async function expectLandsUnderTrigger(
  page: Page,
  triggerId: string,
  contentId: string,
): Promise<void> {
  const trigger = await page.getByTestId(triggerId).boundingBox();
  // The entry animation transforms the popup; measure it settled, which is
  // where it lives and what the positioner computed.
  await page.waitForTimeout(300);
  const content = await page.getByTestId(contentId).boundingBox();
  expect(trigger).not.toBeNull();
  expect(content).not.toBeNull();
  // side=bottom, align=center: below the trigger, overlapping it horizontally.
  expect(content!.y).toBeGreaterThanOrEqual(trigger!.y + trigger!.height - 2);
  expect(content!.x).toBeLessThan(trigger!.x + trigger!.width);
  expect(content!.x + content!.width).toBeGreaterThan(trigger!.x);
}

/**
 * Base UI's select aligns the selected row with the trigger
 * (`alignItemWithTrigger`), so its popup covers the trigger row by design.
 * Anchored means: same horizontal band as the trigger, top at the trigger —
 * not parked at the seat's origin.
 */
async function expectAnchoredToTrigger(
  page: Page,
  triggerId: string,
  contentId: string,
): Promise<void> {
  const trigger = await page.getByTestId(triggerId).boundingBox();
  await page.waitForTimeout(300);
  const content = await page.getByTestId(contentId).boundingBox();
  expect(trigger).not.toBeNull();
  expect(content).not.toBeNull();
  expect(content!.x).toBeLessThanOrEqual(trigger!.x + 4);
  expect(content!.x + content!.width).toBeGreaterThanOrEqual(
    trigger!.x + trigger!.width - 4,
  );
  expect(content!.y).toBeGreaterThanOrEqual(trigger!.y - 2);
  expect(content!.y).toBeLessThanOrEqual(trigger!.y + trigger!.height + 8);
}

/**
 * With the card covering the popup, the seat — not the card — must own those
 * pixels: the document-level hit test retargets to the seat host across the
 * shadow boundary, and inside the seat's shadow root it resolves to the item.
 */
async function expectSeatWinsPixels(page: Page, itemTestId: string): Promise<void> {
  const result = await page.evaluate((id) => {
    const seat = document.querySelector<HTMLElement>("[data-multica-popup-seat]");
    if (seat === null || seat.shadowRoot === null) return { error: "no popup seat" };
    const item = seat.shadowRoot.querySelector<HTMLElement>(
      `[data-testid="${id}"]`,
    );
    if (item === null) return { error: `item ${id} not inside the seat` };
    const rect = item.getBoundingClientRect();
    const cx = rect.x + rect.width / 2;
    const cy = rect.y + rect.height / 2;
    const documentHit = document.elementFromPoint(cx, cy);
    const seatHit = seat.shadowRoot.elementFromPoint(cx, cy);
    return {
      documentHitIsSeat:
        documentHit !== null &&
        documentHit.closest("[data-multica-popup-seat]") !== null,
      seatHitIsItem: seatHit !== null && (seatHit === item || item.contains(seatHit)),
    };
  }, itemTestId);
  expect(result).toEqual({ documentHitIsSeat: true, seatHitIsItem: true });
}

async function expectPicked(page: Page): Promise<void> {
  await expect(page.getByTestId("picked")).toHaveText(`picked: ${PICKED}`);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/");
});

test("the shell card owns the island's pixels until an overlay opens", async ({ page }) => {
  // Precondition of the bug, mirrored from the lane-z evidence: the card in
  // the shell's overlay layer beats everything in the app layer below it.
  const card = page.locator("#blocking-card");
  const box = await card.boundingBox();
  expect(box).not.toBeNull();
  const hit = await page.evaluate(({ x, y }) => {
    const element = document.elementFromPoint(x, y);
    return element?.closest("#blocking-card") !== null && element !== null;
  }, { x: box!.x + box!.width / 2, y: box!.y + box!.height / 2 });
  expect(hit).toBe(true);
});

test("a Popover opened from the island is clickable under the shell card", async ({ page }) => {
  await page.getByTestId("popover-trigger").click();
  const content = page.getByTestId("popover-content");
  await expect(content).toBeVisible();
  await expectLandsUnderTrigger(page, "popover-trigger", "popover-content");

  await coverWithCard(page, content);
  await expectSeatWinsPixels(page, `popover-item-${PICKED}`);

  // Real mouse click: Playwright's hit-target check fails if the card wins.
  await page.getByTestId(`popover-item-${PICKED}`).click();
  await expectPicked(page);
});

test("a Select opened from the island is clickable under the shell card", async ({ page }) => {
  await page.getByTestId("select-trigger").click();
  const content = page.getByTestId("select-content");
  await expect(content).toBeVisible();
  await expectAnchoredToTrigger(page, "select-trigger", "select-content");

  await coverWithCard(page, content);
  await expectSeatWinsPixels(page, `select-item-${PICKED}`);

  await page.getByTestId(`select-item-${PICKED}`).click();
  await expectPicked(page);
});

test("a DropdownMenu opened from the island is clickable under the shell card", async ({ page }) => {
  await page.getByTestId("dropdown-trigger").click();
  const content = page.getByTestId("dropdown-content");
  await expect(content).toBeVisible();
  await expectLandsUnderTrigger(page, "dropdown-trigger", "dropdown-content");

  await coverWithCard(page, content);
  await expectSeatWinsPixels(page, `dropdown-item-${PICKED}`);

  await page.getByTestId(`dropdown-item-${PICKED}`).click();
  await expectPicked(page);
});
