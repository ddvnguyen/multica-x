#!/usr/bin/env node
/**
 * Rail-click cost, measured on a running dsh lane.
 *
 *   node packages/dsh-plugin/e2e/perf/rail-click.mjs --url http://127.0.0.1:3180
 *
 * Drives the real rail (Issues -> Agents -> Inbox -> Issues) in headless
 * Chromium and reports, per click: time until the island shows that page,
 * whether the "Connecting to Multica…" gate painted, how many `/multica`
 * requests and WebSockets the click caused, and whether the island's style
 * nodes were rebuilt. Three runs, median.
 *
 * The click is issued with `element.click()` inside the page: the shell's own
 * nav overlaps the rail rows in headless, and the cost being measured is the
 * mount, not Playwright's hit-testing. For the same reason this is a plain
 * node script and not a Playwright test — `playwright.config.ts` ignores
 * `e2e/perf/**` so the ordinary suite never runs it.
 *
 * Exit code is 1 when a click never settles, so a broken lane fails loudly
 * instead of reporting a fast number for an empty page.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { chromium } from "@playwright/test";

const ROWS = ["Multica Issues", "Multica Agents", "Multica Inbox", "Multica Issues"];

function arg(name, fallback) {
  const index = process.argv.indexOf(`--${name}`);
  return index === -1 ? fallback : process.argv[index + 1];
}

const url = arg("url", process.env.LANE_URL ?? "http://127.0.0.1:3180/");
const runs = Number(arg("runs", "3"));
const out = arg("out", null);
const shots = arg("shots", null);

/** Runs in the page. Returns what the island currently shows. */
function islandState() {
  const host = document.querySelector("[data-multica-panel]");
  const shadow = host?.shadowRoot ?? null;
  const text = shadow?.querySelector("[data-multica-root]")?.innerText ?? "";
  // The island stylesheet is a `style[data-multica-styles]` sibling of the
  // React root inside the shadow root, not a child of it.
  const styles = shadow ? Array.from(shadow.querySelectorAll("style")) : [];
  return {
    host: host !== null,
    text: text.replace(/\s+/g, " ").trim(),
    gate: text.includes("Connecting to Multica"),
    styleNodes: styles.length,
    styleBytes: styles.reduce((sum, node) => sum + node.textContent.length, 0),
  };
}

async function clickRail(page, label) {
  return page.evaluate((name) => {
    const button = Array.from(document.querySelectorAll("button")).find(
      (candidate) => candidate.textContent.trim() === name && candidate.getBoundingClientRect().width > 0,
    );
    if (!button) return false;
    button.click();
    return true;
  }, label);
}

async function measureRun(browser, index) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("Performance.enable");

  let api = 0;
  let config = 0;
  let sockets = 0;
  page.on("request", (request) => {
    const target = request.url();
    if (target.includes("/multica/config")) config += 1;
    else if (target.includes("/multica/api")) api += 1;
  });
  page.on("websocket", () => (sockets += 1));

  const metrics = async () =>
    Object.fromEntries((await cdp.send("Performance.getMetrics")).metrics.map((m) => [m.name, m.value]));

  await page.goto(url, { waitUntil: "domcontentloaded" });
  await page.waitForFunction(() => document.querySelector("button[aria-label='Multica Issues']") !== null, null, {
    timeout: 60_000,
  });
  // The plugin warms itself in an idle callback; let that land before the
  // first click so the first number is a click cost and not a boot cost.
  await page.waitForTimeout(2000);

  const rows = [];
  let previousText = "";
  for (const [step, label] of ROWS.entries()) {
    const before = await metrics();
    api = 0;
    config = 0;
    sockets = 0;
    const started = Date.now();
    if (!(await clickRail(page, label))) throw new Error(`no rail button labelled ${label}`);

    let sawGate = false;
    let firstNonEmptyMs = null;
    let settled = false;
    let state = null;
    for (let tick = 0; tick < 400; tick += 1) {
      state = await page.evaluate(islandState);
      if (state.gate) sawGate = true;
      if (firstNonEmptyMs === null && state.text.length > 0) firstNonEmptyMs = Date.now() - started;
      // Content that differs from the page we left is the new page, not the
      // old one still on screen while the route swaps.
      if (state.text.length > 20 && !state.gate && state.text !== previousText) {
        settled = true;
        break;
      }
      await page.waitForTimeout(25);
    }
    const clickToContentMs = Date.now() - started;
    const after = await metrics();
    previousText = state?.text ?? previousText;

    if (shots !== null) {
      mkdirSync(shots, { recursive: true });
      await page.screenshot({ path: `${shots}/${index}-${step}-${label.replace(/\s+/g, "")}.png` });
    }

    rows.push({
      step,
      label,
      settled,
      clickToContentMs,
      firstNonEmptyMs,
      sawGate,
      apiCalls: api,
      configFetches: config,
      newSockets: sockets,
      scriptMs: Math.round((after.ScriptDuration - before.ScriptDuration) * 1000),
      heapMB: Number((after.JSHeapUsedSize / 1048576).toFixed(1)),
      styleNodes: state?.styleNodes ?? 0,
      styleKB: Math.round((state?.styleBytes ?? 0) / 1024),
      text: (state?.text ?? "").slice(0, 60),
    });
  }

  await page.close();
  return rows;
}

const median = (values) => {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)];
};

const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH || undefined,
  args: ["--no-sandbox"],
});

const perRun = [];
for (let index = 0; index < runs; index += 1) {
  perRun.push(await measureRun(browser, index));
}
await browser.close();

const rows = ROWS.map((label, step) => {
  const samples = perRun.map((run) => run[step]);
  return {
    label,
    clickToContentMs: median(samples.map((s) => s.clickToContentMs)),
    sawGate: samples.filter((s) => s.sawGate).length,
    apiCalls: median(samples.map((s) => s.apiCalls)),
    configFetches: median(samples.map((s) => s.configFetches)),
    newSockets: median(samples.map((s) => s.newSockets)),
    scriptMs: median(samples.map((s) => s.scriptMs)),
    heapMB: median(samples.map((s) => s.heapMB)),
    styleNodes: median(samples.map((s) => s.styleNodes)),
    styleKB: median(samples.map((s) => s.styleKB)),
    settled: samples.every((s) => s.settled),
  };
});

const header = ["step", "click→content", "gate", "/multica", "config", "new WS", "script", "heap", "style nodes", "CSS"];
const table = rows.map((row) => [
  row.label,
  `${row.clickToContentMs} ms`,
  row.sawGate === 0 ? "no" : `yes (${row.sawGate}/${runs})`,
  String(row.apiCalls),
  String(row.configFetches),
  String(row.newSockets),
  `${row.scriptMs} ms`,
  `${row.heapMB} MB`,
  String(row.styleNodes),
  `${row.styleKB} KB`,
]);
const widths = header.map((cell, column) => Math.max(cell.length, ...table.map((row) => row[column].length)));
const line = (cells) => cells.map((cell, column) => cell.padEnd(widths[column])).join("  ");

console.log(`\n${url}  ·  ${runs} runs  ·  median\n`);
console.log(line(header));
console.log(line(widths.map((width) => "-".repeat(width))));
for (const row of table) console.log(line(row));
console.log("");

if (out !== null) {
  const target = resolve(out);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, JSON.stringify({ url, runs, rows, perRun }, null, 2));
  console.log(`wrote ${target}`);
}

const unsettled = rows.filter((row) => !row.settled);
if (unsettled.length > 0) {
  console.error(`never settled: ${unsettled.map((row) => row.label).join(", ")}`);
  process.exit(1);
}
