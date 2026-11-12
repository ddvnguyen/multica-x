import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page, type TestInfo } from "@playwright/test";

/**
 * M-124: the MOCT Memory page in a real browser.
 *
 * Two things only a browser can check, and one thing the card asks for:
 *
 * - the fork's picker selects on pointer events, so the filter wiring Base UI
 *   owns cannot be exercised in jsdom;
 * - the rows and the record page are laid out by the island's stylesheet inside
 *   a shadow root, where nothing about jsdom's rendering says whether they are
 *   visible at all;
 * - the card's evidence is a screenshot of the page as the shell draws it.
 *
 * The harness (`harness/memory.tsx`) mounts the production `IslandApp` over an
 * in-page fake of moct-be, so every request here is the one the panel really
 * makes and is recorded for the assertions.
 */

const PENDING_RULE = "Always run the package suite before pushing";
const ACTIVE_CONVENTION = "Lane z keeps its own database";

interface FetchCall {
  url: string;
  method: string;
  body: unknown;
}

/** Where the evidence set is written; Playwright's own output otherwise. */
const SHOTS = process.env["MOCT_MEMORY_SHOTS"] ?? "";

async function shot(page: Page, testInfo: TestInfo, name: string): Promise<void> {
  const path = SHOTS === "" ? testInfo.outputPath(`${name}.png`) : join(SHOTS, `${name}.png`);
  if (SHOTS !== "") mkdirSync(SHOTS, { recursive: true });
  await page.screenshot({ path });
  await testInfo.attach(name, { path, contentType: "image/png" });
}

async function callsTo(page: Page, path: string): Promise<FetchCall[]> {
  const calls = await page.evaluate(() => (window as unknown as { __moctCalls: FetchCall[] }).__moctCalls);
  return calls.filter((call) => call.url.startsWith(path));
}

/** The list reads, newest last, with the query each one carried. */
async function listReads(page: Page): Promise<URLSearchParams[]> {
  return (await callsTo(page, "/moct/team/v1/memory?")).map(
    (call) => new URLSearchParams(call.url.split("?")[1] ?? ""),
  );
}

test("the Memory entry lists, filters, decides and versions through the seam", async ({
  page,
}, testInfo) => {
  await page.goto("/memory.html");

  // 1. The page opens on the queue that needs a human: pending only.
  await expect(page.getByText(PENDING_RULE)).toBeVisible();
  await expect(page.getByText("Waiting for approval").first()).toBeVisible();
  await expect(page.getByText("2 of 2 records")).toBeVisible();
  // The decided record is not in the pending queue.
  await expect(page.getByText(ACTIVE_CONVENTION)).toHaveCount(0);
  const first = await listReads(page);
  expect(first).toHaveLength(1);
  expect(first[0]?.get("status")).toBe("pending");
  expect(first[0]?.get("limit")).toBe("20");
  expect(page.getByText("Reachable")).toBeVisible();

  // The evidence is the queue itself, so the shot is taken where the records
  // are, not at the top of the page.
  await page.getByText(PENDING_RULE).scrollIntoViewIfNeeded();
  await shot(page, testInfo, "memory-pending-desktop");

  // 2. The status filter reaches moct and comes back with the whole memory.
  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "All" }).click();
  await expect(page.getByText(ACTIVE_CONVENTION)).toBeVisible();
  await expect(page.getByText("5 of 5 records")).toBeVisible();
  const filtered = await listReads(page);
  expect(filtered).toHaveLength(2);
  expect(filtered[1]?.get("status")).toBe("all");

  // 3. The type filter is a moct parameter too, and the count follows it.
  await page.getByRole("combobox", { name: "Type" }).click();
  await page.getByRole("option", { name: "Decision" }).click();
  await expect(page.getByText(PENDING_RULE)).toHaveCount(0);
  await expect(page.getByText("M-103 acceptance run")).toBeVisible();
  await expect(page.getByText("2 of 2 records")).toBeVisible();
  expect((await listReads(page))[2]?.get("types")).toBe("decision");

  // 4. Search is submitted, not typed at: one request per submit.
  const before = (await listReads(page)).length;
  await page.getByLabel("Search memory").fill("acceptance");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.getByText("1 of 1 records")).toBeVisible();
  const searched = await listReads(page);
  expect(searched).toHaveLength(before + 1);
  expect(searched[searched.length - 1]?.get("q")).toBe("acceptance");

  await shot(page, testInfo, "memory-filtered-desktop");

  // 5. One record: its body, its attributes, its versions, and the decision.
  await page.getByRole("button", { name: "Open M-103 acceptance run" }).click();
  await expect(page.getByText("M-103 acceptance run: LLM-turn memory_note OK")).toBeVisible();
  await expect(page.getByText("m103-acceptance")).toBeVisible();
  await expect(page.getByText("v1")).toBeVisible();
  await page.getByText("Decision").first().scrollIntoViewIfNeeded();
  const versionsRead = await callsTo(page, "/moct/team/v1/memory/mem_decision_pending/versions");
  expect(versionsRead).toHaveLength(1);
  const get = await callsTo(page, "/moct/team/v1/memory/get");
  expect(get[0]?.body).toEqual({ ids: ["mem_decision_pending"] });

  await shot(page, testInfo, "memory-record-desktop");

  // 6. Approving writes to moct through the seam, as the host's human: the
  //    browser sends the version it was shown and nothing else.
  await page.getByRole("button", { name: "Approve M-103 acceptance run" }).click();
  await expect(page.getByText("Approved. The record injects from here on.")).toBeVisible();
  const decisions = (await callsTo(page, "/moct/team/v1/memory/mem_decision_pending/approve")).filter(
    (call) => call.method === "POST",
  );
  expect(decisions).toHaveLength(1);
  expect(decisions[0]?.body).toEqual({ baseVersion: 1 });
  // The page follows the answer: the new version, and no decision left to make.
  await expect(page.getByText("version 2")).toBeVisible();
  await expect(page.getByRole("button", { name: "Approve M-103 acceptance run" })).toHaveCount(0);
  await expect(page.getByText("v2")).toBeVisible();

  await shot(page, testInfo, "memory-approved-desktop");

  // 7. Back on the list the row moved with it, in the page's own state.
  await page.getByRole("button", { name: "Memory" }).click();
  await expect(page.getByText("M-103 acceptance run")).toBeVisible();
  await expect(page.getByText("1 of 1 records")).toBeVisible();

  // 8. The narrow panel the shell can hand this page on a phone-sized window:
  //    the record list, its count and the way into a record all stay usable.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("button", { name: "Search" })).toBeVisible();
  await page.getByText("M-103 acceptance run").scrollIntoViewIfNeeded();
  await shot(page, testInfo, "memory-narrow");
  await expect(page.getByRole("button", { name: "Open M-103 acceptance run" })).toBeVisible();
});
