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
const MOCT_API = "/multica/api/workspaces/11111111-1111-4111-8111-111111111111/moct";

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
  return (await callsTo(page, `${MOCT_API}/team/v1/memory?`)).map(
    (call) => new URLSearchParams(call.url.split("?")[1] ?? ""),
  );
}

test("the Memory entry lists, filters, decides and versions through the seam", async ({
  page,
}, testInfo) => {
  // A tall window: the evidence shots must read at least 1300px high.
  await page.setViewportSize({ width: 1440, height: 1600 });
  await page.goto("/memory.html");

  // 1. The page opens on the whole memory: every status, moct's own default.
  await expect(page.getByText(PENDING_RULE)).toBeVisible();
  await expect(page.getByText(ACTIVE_CONVENTION)).toBeVisible();
  await expect(page.getByText("5 of 5 records")).toBeVisible();
  const first = await listReads(page);
  expect(first).toHaveLength(1);
  expect(first[0]?.get("status")).toBe("all");
  expect(first[0]?.get("limit")).toBe("20");
  await expect(page.getByText("Reachable")).toBeVisible();

  // The evidence is the whole memory itself, so the shot is taken where the
  // records are, not at the top of the page.
  await page.getByText(PENDING_RULE).scrollIntoViewIfNeeded();
  await shot(page, testInfo, "memory-pending-desktop");

  // 2. The queue that needs a human is one choice away: pending only.
  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "Waiting for approval" }).click();
  await expect(page.getByText(PENDING_RULE)).toBeVisible();
  // The decided record is not in the pending queue.
  await expect(page.getByText(ACTIVE_CONVENTION)).toHaveCount(0);
  await expect(page.getByText("2 of 2 records")).toBeVisible();
  const filtered = await listReads(page);
  expect(filtered).toHaveLength(2);
  expect(filtered[1]?.get("status")).toBe("pending");

  // Back to the whole memory before narrowing by type.
  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "All" }).click();
  await expect(page.getByText("5 of 5 records")).toBeVisible();
  await page.getByRole("combobox", { name: "Type", exact: true }).click();
  await page.getByRole("option", { name: "Decision" }).click();
  await expect(page.getByText(PENDING_RULE)).toHaveCount(0);
  await expect(page.getByText("M-103 acceptance run", { exact: true })).toBeVisible();
  await expect(page.getByText("2 of 2 records")).toBeVisible();
  const typed = await listReads(page);
  expect(typed[typed.length - 1]?.get("types")).toBe("decision");

  // 4. Search is submitted, not typed at: one request per submit.
  const before = (await listReads(page)).length;
  await page.getByLabel("Search memory").fill("acceptance");
  await page.getByLabel("Records").getByRole("button", { name: "Search" }).click();
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
  const versionsRead = await callsTo(page, `${MOCT_API}/team/v1/memory/mem_decision_pending/versions`);
  expect(versionsRead).toHaveLength(1);
  const get = await callsTo(page, `${MOCT_API}/team/v1/memory/get`);
  expect(get[0]?.body).toEqual({ ids: ["mem_decision_pending"] });

  await shot(page, testInfo, "memory-record-desktop");

  // 6. Approving writes to moct through the seam, as the host's human: the
  //    browser sends the version it was shown and nothing else.
  await page.getByRole("button", { name: "Approve M-103 acceptance run" }).click();
  await expect(page.getByText("Approved. The record injects from here on.")).toBeVisible();
  const decisions = (await callsTo(page, `${MOCT_API}/team/v1/memory/mem_decision_pending/approve`)).filter(
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
  await page.getByRole("button", { name: "Back to Memory" }).click();
  await expect(page.getByText("M-103 acceptance run", { exact: true })).toBeVisible();
  await expect(page.getByText("1 of 1 records")).toBeVisible();

  // 8. The Owner files a note from the same screen: title, body, type and the
  //    workspace home, and the row renders saved and active at once.
  await page.getByLabel("Note title").fill("Keep main green");
  await page.getByLabel("Note body").fill("Suite before push");
  await page.getByRole("button", { name: "Save note" }).click();
  await page.getByRole("button", { name: "Confirm save note" }).click();
  await expect(page.getByText("Saved. It is active now — a human note needs no approval.")).toBeVisible();
  const created = (await callsTo(page, `${MOCT_API}/team/v1/memory`)).filter(
    (call) => call.method === "POST" && call.url === `${MOCT_API}/team/v1/memory`,
  );
  expect(created).toHaveLength(1);
  expect(created[0]?.body).toEqual({
    type: "rule",
    title: "Keep main green",
    body: "Suite before push",
    homeKind: "team",
    homeRef: "",
    attrs: { level: "must" },
  });

  // 9. Deleting is permanent and says so: the confirm names retract as the
  //    way back, and the row leaves the list on the purge answer. The list is
  //    still narrowed to decisions matching "acceptance", so clear both first.
  await page.getByRole("combobox", { name: "Type", exact: true }).click();
  await page.getByRole("option", { name: "Any type" }).click();
  await page.getByLabel("Search memory").fill("");
  await page.getByLabel("Records").getByRole("button", { name: "Search" }).click();
  await page.getByRole("button", { name: "Open Keep main green" }).click();
  await expect(page.getByText(/Purged text is gone and cannot be restored/)).toBeVisible();
  await page.getByRole("button", { name: "Delete Keep main green" }).click();
  await page.getByRole("button", { name: "Delete Keep main green permanently" }).click();
  const purged = (await callsTo(page, `${MOCT_API}/team/v1/memory/mem_created_6/purge`)).filter(
    (call) => call.method === "POST",
  );
  expect(purged).toHaveLength(1);
  // Back on the list with the row dropped, not lingering as purged.
  await expect(page.getByText("5 of 5 records")).toBeVisible();
  await expect(page.getByRole("button", { name: "Open Keep main green" })).toHaveCount(0);

  // 10. The narrow panel the shell can hand this page on a phone-sized window:
  //    the record list, its count and the way into a record all stay usable.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByLabel("Records").getByRole("button", { name: "Search" })).toBeVisible();
  await page.getByText("M-103 acceptance run").scrollIntoViewIfNeeded();
  await shot(page, testInfo, "memory-narrow");
  await expect(page.getByRole("button", { name: "Open M-103 acceptance run" })).toBeVisible();
});
