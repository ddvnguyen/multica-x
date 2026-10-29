import { expect, test } from "@playwright/test";

/**
 * M-154: the Language row changes the island's language and survives a reload.
 *
 * The row persists through the host's `LocaleAdapter` and then reloads the
 * page — the fork's own confirmation pattern. That reload is the reason the
 * harness is URL-addressed: a page that booted at the board would come back at
 * the board, and the spec could not tell "the language changed" from "the tab
 * was lost".
 *
 * The stored choice is asserted through `localStorage` as well as the rendered
 * copy: the first proves the island owns the preference (`multica.dsh.locale`,
 * not a cookie on dsh's origin), the second proves the fork read it back.
 */

const PREFERENCES = "/route.html?route=settings&query=tab%3Dpreferences";

async function boot(page: import("@playwright/test").Page): Promise<void> {
  await page.waitForFunction(
    () => window.__islandHost?.()?.isConnected === true,
    undefined,
    { timeout: 20_000 },
  );
  await expect(page.getByText("Connecting to Multica…")).toHaveCount(0, {
    timeout: 20_000,
  });
}

test("Preferences language row switches the island to French and back", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1300 });
  await page.goto(PREFERENCES);
  await boot(page);

  // The row renders at all: this page used to throw because no adapter was
  // mounted, so the whole tab was the error boundary (M-154).
  const language = page.locator('[aria-label="Language"]');
  await expect(language).toBeVisible();

  await language.click();
  await page.getByRole("option", { name: "Français" }).click();

  // The row reloads the page; the French copy arriving is the confirmation.
  await expect(page.getByText("Langue").first()).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("nav a", { hasText: "Préférences" })).toHaveCount(1);

  expect(
    await page.evaluate(() => window.localStorage.getItem("multica.dsh.locale")),
  ).toBe("fr");
  // No Multica cookie: dsh's origin is shared and its own language is not ours.
  expect(await page.evaluate(() => document.cookie)).not.toContain("multica-locale");

  // ... and back, through the same row, now labelled in French.
  await page.locator('[aria-label="Langue"]').click();
  await page.getByRole("option", { name: "English" }).click();

  await expect(page.getByText("Language").first()).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("nav a", { hasText: "Preferences" })).toHaveCount(1);
  expect(
    await page.evaluate(() => window.localStorage.getItem("multica.dsh.locale")),
  ).toBe("en");
});
