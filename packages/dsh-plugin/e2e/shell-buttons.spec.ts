import { expect, test, type Page } from "@playwright/test";

/**
 * M-159 and M-316: the header's side-panel toggle click through with the shell
 * bridge behind it.
 *
 * jsdom can mount the button, but the press runs across a live sidebar face:
 * whether it flips the column in place (`toggleExpanded`) or names the project's
 * Session for the column beside MMX first (`selectHostedSession`), and what the
 * face rejects (`no session surface is mounted`) are only observable in a real
 * browser. The shell harness page configures a stub bridge that records every
 * face call and renders the shown state, so the spec asserts behaviour through
 * the record — the same discipline the Comments bar and the tab strip specs use.
 *
 * The record also carries the M-316 rule: a press never selects a Session. A
 * host that cannot draw the column beside MMX gets a disabled button and no
 * calls at all, because navigating to the dsh page is the outcome the reader
 * ruled out.
 */

const toggle = '[data-shell-button="toggle"]';

async function stubCalls(page: Page): Promise<string[]> {
  return (await page.evaluate(() => window.__stubCalls)) ?? [];
}

async function openShell(page: Page, query: string): Promise<void> {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(String(error)));
  ;(page as unknown as { __pageErrors?: string[] }).__pageErrors = pageErrors;
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`/shell.html${query}`);
  await page.waitForFunction(
    () => window.__islandHost?.()?.isConnected === true,
    undefined,
    { timeout: 20_000 },
  );
  await expect(page.getByText("Connecting to Multica…")).toHaveCount(0, {
    timeout: 20_000,
  });
  // The board's list request is the last boot network touch; the strip has
  // rendered by the time the shell shows the toggle.
  await expect(page.locator(toggle)).toBeVisible({ timeout: 20_000 });
}

async function expectNoPageErrors(page: Page): Promise<void> {
  expect(
    (page as unknown as { __pageErrors?: string[] }).__pageErrors ?? [],
  ).toEqual([]);
}

test("with card C, the toggle opens and closes the dsh side panel in place", async ({
  page,
}) => {
  await openShell(page, "?hostPanel");

  // Enabled with a folder, and the tooltip names the folder it would show.
  await expect(page.locator(toggle)).toBeEnabled();
  await expect(page.locator(toggle)).toHaveAttribute(
    "title",
    "Open the dsh side panel on /srv/demo",
  );
  await expect(page.locator(toggle)).toHaveAttribute("aria-pressed", "false");

  await page.locator(toggle).click();
  await expect(page.locator("[data-sidebar-expanded]")).toHaveCount(1, {
    timeout: 20_000,
  });
  await expect(page.locator(toggle)).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator(toggle)).toHaveAttribute(
    "title",
    "Close the dsh side panel",
  );

  // In place: one flip of the column, and no Session was selected — MMX keeps
  // the main column, which is the whole point of card C. (`hostPanel` itself is
  // called from `apply`, which this harness page never runs.)
  expect(await stubCalls(page)).toEqual(["toggleExpanded"]);

  await page.locator(toggle).click();
  await expect(page.locator("[data-sidebar-expanded]")).toHaveCount(0, {
    timeout: 20_000,
  });
  expect((await stubCalls(page)).filter((call) => call.startsWith("selectSession"))).toEqual([]);

  await expectNoPageErrors(page);
});

test("with card C, a press on another Workspace's Session retargets the column before opening", async ({
  page,
}) => {
  // The Conversation last selected a Session from another Workspace, and MMX
  // owns the main column: the column would open on that other folder unless the
  // press names the project's Session first.
  await openShell(page, "?hostPanel&otherSession");

  await page.locator(toggle).click();
  await expect(page.locator("[data-sidebar-expanded]")).toHaveCount(1, {
    timeout: 20_000,
  });
  await expect(page.locator("[data-sidebar]")).toHaveAttribute("data-session", "s-1");
  // The name lands first, then the flip — and nothing selects a Session, so MMX
  // never leaves the main column.
  expect(await stubCalls(page)).toEqual(["selectHostedSession s-1", "toggleExpanded"]);
  expect((await stubCalls(page)).filter((call) => call.startsWith("selectSession"))).toEqual([]);

  // Closing goes where the column is: the press is not a retarget.
  await page.locator(toggle).click();
  await expect(page.locator("[data-sidebar-expanded]")).toHaveCount(0, {
    timeout: 20_000,
  });
  expect((await stubCalls(page)).filter((call) => call.startsWith("selectHostedSession"))).toEqual([
    "selectHostedSession s-1",
  ]);

  await expectNoPageErrors(page);
});

test("without hostPanel the toggle is disabled: it never leaves MMX for the dsh page", async ({
  page,
}) => {
  // The pre-M-316 build's only other surface was over an on-screen Session,
  // which replaces the board with a Conversation. The button says what this
  // build cannot do instead, and no face is called.
  await openShell(page, "?noSurface");

  await expect(page.locator(toggle)).toBeDisabled();
  await expect(page.locator(toggle)).toHaveAttribute(
    "title",
    "This dsh build cannot show the side panel next to MMX",
  );
  expect(await stubCalls(page)).toEqual([]);

  await expectNoPageErrors(page);
});

test("on an issue page whose picker says All projects, the press uses the issue's project folder", async ({
  page,
}) => {
  // The Owner's M-316 flow: no project picked, one issue on screen, and the
  // folder comes from that issue's project — not from the "All projects"
  // default, which is `/srv/demo` here.
  await openShell(page, "?issue&hostPanel");

  await expect(page.locator(toggle)).toBeEnabled();
  await expect(page.locator(toggle)).toHaveAttribute(
    "title",
    "Open the dsh side panel on /srv/issue-demo",
  );

  await page.locator(toggle).click();
  await expect(page.locator("[data-sidebar-expanded]")).toHaveCount(1, {
    timeout: 20_000,
  });
  // The issue's folder is not a Workspace this host knows yet, so the press
  // registers it (idempotent), creates its first Session, names that Session
  // for the hosted column and flips it — in that order, and MMX keeps the main
  // column throughout.
  expect(await stubCalls(page)).toEqual([
    "workspaces:create /srv/issue-demo",
    "sessions:create w-1",
    "selectHostedSession s-new",
    "toggleExpanded",
  ]);
  await expect(page.locator("[data-sidebar]")).toHaveAttribute("data-session", "s-new");
  expect((await stubCalls(page)).filter((call) => call.startsWith("selectSession"))).toEqual([]);

  await expectNoPageErrors(page);
});

test("no folder: the toggle is disabled and says what is missing", async ({
  page,
}) => {
  await openShell(page, "?noFolder&hostPanel");

  await expect(page.locator(toggle)).toBeDisabled();
  await expect(page.locator(toggle)).toHaveAttribute("title", "Choose a project");
  expect(await stubCalls(page)).toEqual([]);

  await expectNoPageErrors(page);
});
