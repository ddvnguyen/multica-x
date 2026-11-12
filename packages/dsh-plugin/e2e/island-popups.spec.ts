import { expect, test, type Page } from "@playwright/test";

/**
 * Popups, tooltips and dismissal (M-148 correction).
 *
 * The lane-z report measured five things on the Agents → Dsh Engineer →
 * Settings inspector: two pickers open at once, a tooltip left floating over
 * the list, Escape doing nothing, and an outside click doing nothing. Each test
 * below drives the same Runtime → Model sequence with the fork's own primitives
 * — a popover whose trigger is wrapped in a tooltip that is open only while the
 * popover is closed, and rows that carry their own tooltips.
 *
 * It runs against all three arrangements of the shell's overlay layer, because
 * that is what separated the working case from lane z:
 *
 * - the layer is there when the island is built (popups join it straight away);
 * - the layer is painted after the plugin warmed the island up (lane z — the
 *   seat has to be adopted, carrying any popup that is already open);
 * - there is no layer at all (the island seats overlays on the document).
 */

const VIEWPORT = { width: 1440, height: 1300 } as const;

type ShellLayer = "present" | "late" | "none";

const ARRANGEMENTS: Array<{ name: string; query: string; layer: ShellLayer }> = [
  { name: "with the shell's overlay layer", query: "", layer: "present" },
  {
    name: "with the overlay layer mounted after warm-up (lane z)",
    query: "&seat=late",
    layer: "late",
  },
  { name: "with no overlay layer at all", query: "&seat=off", layer: "none" },
];

const openPopup = (page: Page, testId: string) => page.getByTestId(testId).filter({ visible: true });

/**
 * Every visible overlay, by slot.
 *
 * Read from all three roots an overlay can live in — the document, the panel's
 * shadow root and the seat's — because "is it gone?" has to be asked where the
 * popup actually rendered.
 */
async function visibleOverlays(page: Page): Promise<{ popovers: number; tooltips: number }> {
  return page.evaluate(() => {
    const roots: (Document | ShadowRoot)[] = [document];
    for (const host of document.querySelectorAll<HTMLElement>("[data-parity-island]")) {
      if (host.shadowRoot !== null) roots.push(host.shadowRoot);
    }
    for (const seat of document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")) {
      if (seat.shadowRoot !== null) roots.push(seat.shadowRoot);
    }
    const visible = (slot: string): number =>
      roots
        .flatMap((root) => [
          ...root.querySelectorAll<HTMLElement>(`[data-slot="${slot}"]:not([data-closed])`),
        ])
        .filter((element) => element.getClientRects().length > 0).length;
    return { popovers: visible("popover-content"), tooltips: visible("tooltip-content") };
  });
}

async function visiblePopups(page: Page): Promise<number> {
  const overlays = await visibleOverlays(page);
  return overlays.popovers + overlays.tooltips;
}

/** Where the open Runtime picker actually rendered. */
async function seatReport(page: Page) {
  return page.evaluate(() => {
    const seats = [...document.querySelectorAll<HTMLElement>("[data-multica-popup-seat]")];
    const holder =
      seats.find(
        (seat) => seat.shadowRoot?.querySelector('[data-testid="runtime-picker"]') != null,
      ) ?? null;
    const panel = document.querySelector<HTMLElement>("[data-parity-island]");
    return {
      seats: seats.length,
      inSeat: holder !== null,
      inPanel: panel?.shadowRoot?.querySelector('[data-testid="runtime-picker"]') != null,
      inShellLayer: holder?.parentElement?.hasAttribute("data-shell-overlay") ?? false,
      position: holder?.style.position ?? null,
    };
  });
}

for (const arrangement of ARRANGEMENTS) {
  test.describe(arrangement.name, () => {
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize(VIEWPORT);
      await page.goto(`/parity.html?mode=island&theme=light${arrangement.query}`);
    });

    test("opening the model picker closes the runtime picker", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);
      expect(await visiblePopups(page)).toBe(1);

      await page.getByTestId("model-picker-trigger").click();

      // Runtime is outside the Model popover, so the press on its trigger is an
      // outside press for Runtime: it has to close rather than stack.
      await expect(openPopup(page, "model-picker")).toHaveCount(1);
      await expect(openPopup(page, "runtime-picker")).toHaveCount(0);
      expect(await visiblePopups(page)).toBe(1);
    });

    test("a row's tooltip shows while hovering and goes away on mouse leave", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);

      await page.getByTestId("runtime-row-lane-z").hover();
      await expect(openPopup(page, "picker-tooltip")).toHaveCount(1);

      // Away from the row and away from the popover: the tooltip must not be
      // left floating over the list.
      await page.mouse.move(1200, 1200);
      await expect(openPopup(page, "picker-tooltip")).toHaveCount(0);
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);
    });

    test("Escape closes the open picker", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);

      // The pointer is deliberately left on the trigger: the click that opened
      // the picker is what puts it there, and lane z's leftover was a tooltip
      // raised by exactly that stale hover once the picker went away.
      await page.keyboard.press("Escape");

      await expect(openPopup(page, "runtime-picker")).toHaveCount(0);
      await expect(openPopup(page, "picker-tooltip")).toHaveCount(0);
      // No popover anywhere. A trigger tooltip is allowed here and only here:
      // Escape hands focus back to the trigger, and Base UI raises the focused
      // control's own tooltip — a hint on the chip, not the dismissed stack.
      expect((await visibleOverlays(page)).popovers).toBe(0);

      // Off the trigger and out of focus, nothing at all is left behind.
      await page.mouse.move(1200, 1200);
      await page.keyboard.press("Tab");
      await expect.poll(() => visiblePopups(page)).toBe(0);
    });

    test("Escape closes a picker whose row tooltip is open", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);
      await page.getByTestId("runtime-row-lane-z").hover();
      await expect(openPopup(page, "picker-tooltip")).toHaveCount(1);

      await page.keyboard.press("Escape");

      // Top layer first: the row's tooltip goes, the picker stays. Lane z left
      // both on screen forever because Escape never reached Base UI at all.
      await expect(openPopup(page, "picker-tooltip")).toHaveCount(0);
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);

      await page.mouse.move(1200, 1200);
      await page.keyboard.press("Escape");

      await expect(openPopup(page, "runtime-picker")).toHaveCount(0);
      expect((await visibleOverlays(page)).popovers).toBe(0);
    });

    test("a click outside closes the picker", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);

      // Inside the island, on something that is neither the trigger nor the
      // popup: lane z needed this to dismiss, not just a press on the shell.
      await page.getByTestId("board-card").click();

      await expect(openPopup(page, "runtime-picker")).toHaveCount(0);
      expect(await visiblePopups(page)).toBe(0);
    });

    test("popups render in the island's popup seat, never in the panel", async ({ page }) => {
      await page.getByTestId("runtime-picker-trigger").click();
      await expect(openPopup(page, "runtime-picker")).toHaveCount(1);

      // Lane z's layer is painted by the shell after the plugin booted; the
      // seat has to end up in it however late it arrives.
      if (arrangement.layer === "late") {
        await page.evaluate(() => {
          (window as unknown as { __mountShellLayer: () => void }).__mountShellLayer();
        });
        await expect.poll(async () => (await seatReport(page)).inShellLayer).toBe(true);
      }

      const placement = await seatReport(page);

      // A popup inside the panel's shadow root loses every click to the shell's
      // overlay layer (M-116) and hides the press's root ancestor from Base UI's
      // outside-press check, which is what left Runtime open under Model (M-148).
      expect(placement.inPanel).toBe(false);
      expect(placement.inSeat).toBe(true);
      expect(placement.seats).toBe(1);
      if (arrangement.layer === "none") {
        expect(placement.inShellLayer).toBe(false);
        expect(placement.position).toBe("fixed");
      } else {
        expect(placement.inShellLayer).toBe(true);
        expect(placement.position).toBe("absolute");
      }
    });
  });
}

test.describe("with the overlay layer mounted after warm-up (lane z)", () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize(VIEWPORT);
    await page.goto("/parity.html?mode=island&theme=light&seat=late");
  });

  test("an open popup moves into the layer when the shell paints it", async ({ page }) => {
    await page.getByTestId("runtime-picker-trigger").click();
    await expect(openPopup(page, "runtime-picker")).toHaveCount(1);
    const before = await seatReport(page);
    expect(before.inShellLayer).toBe(false);

    await page.evaluate(() => {
      (window as unknown as { __mountShellLayer: () => void }).__mountShellLayer();
    });

    // The seat is adopted, and the popup that was already open is the same
    // element in the same container: React portals follow the container, so the
    // layer appearing never closes or rebuilds an open picker.
    await expect.poll(async () => (await seatReport(page)).inShellLayer).toBe(true);
    const after = await seatReport(page);
    expect(after.inSeat).toBe(true);
    expect(after.seats).toBe(1);
    expect(await visiblePopups(page)).toBe(1);

    // …and it dismisses from its new home too.
    await page.getByTestId("model-picker-trigger").click();
    await expect(openPopup(page, "model-picker")).toHaveCount(1);
    await expect(openPopup(page, "runtime-picker")).toHaveCount(0);
  });
});
