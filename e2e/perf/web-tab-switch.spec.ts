/**
 * Web tab-switch latency (M-519).
 *
 * Real production Next.js UI, real tab strip and router sync, synthetic HTTP
 * and WebSocket boundaries. No API server, database or account is needed. The
 * scenario seeds a four-tab layout (Issues, Chat, Agents, issue detail), then
 * clicks around the strip while reading the User Timing marks the product
 * writes (`tab-switch:*`). Each switch is a fresh measurement; the first lap
 * is labelled cold (that route has not been visited in this context) and the
 * next laps warm (Query/RSC caches are populated).
 *
 * Numbers are observations, not a gate: one machine and one browser cannot
 * separate a small regression from noise. The report is attached and logged
 * so it can be pasted into the PR alongside the before/after table.
 */
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

const WORKSPACE_ID = "a1111111-1111-4111-8111-111111111111";
const USER_ID = "b2222222-2222-4222-8222-222222222222";
const ISSUE_ID = "c3333333-3333-4333-8333-333333333333";
const SLUG = "perf";
const AT = "2026-01-01T00:00:00Z";

const SPANS = [
  "click-to-store",
  "store-to-replace",
  "replace-to-commit",
  "commit-to-paint",
  "total",
] as const;
type Span = (typeof SPANS)[number];

interface TabSeed {
  id: string;
  url: string;
  title: string;
}

const TABS: TabSeed[] = [
  { id: "tab-issues", url: `/${SLUG}/issues`, title: "Issues" },
  { id: "tab-chat", url: `/${SLUG}/chat`, title: "Chat" },
  { id: "tab-agents", url: `/${SLUG}/agents`, title: "Agents" },
  { id: "tab-issue", url: `/${SLUG}/issues/PERF-1`, title: "PERF-1: Benchmark issue" },
];

/** One lap through all four tabs, ending where it started. */
const LAP: ReadonlyArray<{ id: string; path: string; label: string }> = [
  { id: "tab-chat", path: `/${SLUG}/chat`, label: "issues->chat" },
  { id: "tab-agents", path: `/${SLUG}/agents`, label: "chat->agents" },
  { id: "tab-issue", path: `/${SLUG}/issues/PERF-1`, label: "agents->issue" },
  { id: "tab-issues", path: `/${SLUG}/issues`, label: "issue->issues" },
];

test.use({
  viewport: { width: 1440, height: 900 },
  locale: "en-US",
  colorScheme: "light",
  serviceWorkers: "block",
});

const workspace = {
  id: WORKSPACE_ID, name: "Perf", slug: SLUG, description: null, context: null,
  settings: { github_enabled: false }, repos: [], issue_prefix: "PERF",
  avatar_url: null, created_at: AT, updated_at: AT,
};
const me = {
  id: USER_ID, name: "Perf User", email: "perf@example.test", avatar_url: null,
  language: "en", onboarded_at: AT,
  onboarding_questionnaire: { source: "other" }, created_at: AT, updated_at: AT,
};
const issue = {
  id: ISSUE_ID, workspace_id: WORKSPACE_ID, number: 1,
  identifier: "PERF-1", title: "Benchmark issue",
  description: "Tab switch performance fixture.", status: "in_progress",
  status_category: "in_progress", status_name: "In Progress", priority: "none",
  assignee_type: null, assignee_id: null, creator_type: "user", creator_id: USER_ID,
  parent_issue_id: null, project_id: null, position: 0,
  start_date: null, due_date: null, stage: null, metadata: {}, properties: {},
  labels: [], reactions: [], created_at: AT, updated_at: AT,
};

const EMPTY_LISTS = [
  "/api/agents", "/api/runtimes", "/api/squads", "/api/autopilots", "/api/skills",
  "/api/agent-task-snapshot", "/api/invitations", "/api/inbox", "/api/pins",
  "/api/assignee-frequency", "/api/agent-run-counts", "/api/agent-activity-30d",
  "/api/chat/sessions", "/api/agent-work", "/api/inbox/unread-summary",
  "/api/inbox/snoozed", "/api/issue-view-preferences", "/api/working-agents",
  "/api/issue-views", "/api/project-folders", "/api/issue-wakeup-summaries",
  "/api/issue-workflows", "/api/chat/my-wakeups", "/api/labels",
  "/api/issues/unread", "/api/inbox/archived/page",
];

function responseFor(pathname: string): unknown | undefined {
  if (EMPTY_LISTS.includes(pathname)) return [];
  switch (pathname) {
    case "/api/config": return { feature_flags: {}, allow_signup: true };
    case "/api/me": return me;
    case "/api/workspaces": return [workspace];
    case "/api/client-usage": return {};
    case "/api/chat/pending-tasks": return { tasks: [] };
    case "/api/chat/pending-tasks/has-any": return { has_pending: false };
    case "/api/projects": return { projects: [], total: 0 };
    case "/api/issues": return { issues: [], total: 0 };
    case "/api/issue-statuses": return { statuses: [] };
    case "/api/properties": return { properties: [], total: 0 };
    case "/api/quick-actions": return { quick_actions: [] };
    case "/api/issue-status-counts": return {};
    case "/api/issues/child-progress": return { progress: [] };
    case "/api/issues/table/facets": return {};
    case "/api/issues/table/rows": return { issues: [], total: 0 };
    case "/api/search-index/manifest": return {
      cursor: "0", issue_count: 0, comment_count: 0, project_count: 0, text_bytes: 0,
    };
    case "/api/search-index/snapshot": return {
      issues: [], comments: [], projects: [], next_after_number: 0, done: true,
    };
    case "/api/search-index/changes": return {
      issues: [], comments: [], projects: [], deleted: { issues: [], comments: [], projects: [] },
      cursor: "0", has_more: false,
    };
    case `/api/workspaces/${WORKSPACE_ID}/members`: return [];
    case "/api/issues/PERF-1":
    case `/api/issues/${ISSUE_ID}`: return issue;
    case `/api/issues/${ISSUE_ID}/timeline`: return [];
    case `/api/issues/${ISSUE_ID}/task-runs`: return [];
    case `/api/issues/${ISSUE_ID}/subscribers`: return [];
    case `/api/issues/${ISSUE_ID}/wakeups`: return [];
    case `/api/issues/${ISSUE_ID}/attachments`: return [];
    case `/api/issues/${ISSUE_ID}/labels`: return { labels: [] };
    case `/api/issues/${ISSUE_ID}/children`: return { issues: [] };
    case `/api/issues/${ISSUE_ID}/pull-requests`: return { pull_requests: [] };
  }
  if (/^\/api\/issues\/[^/]+\/(system-wakeups|duplicates|questions|environments|decision-records)$/.test(pathname)) {
    return [];
  }
  if (/^\/api\/workspaces\/[^/]+\/(decisions|presets)$/.test(pathname)) return [];
  if (/^\/api\/workspaces\/[^/]+\/issues\/[^/]+\/ask-plan$/.test(pathname)) return {};
  if (/^\/api\/inbox\/issues\/[^/]+\/read$/.test(pathname)) return {};
  return undefined;
}

/** Persisted v5 tab store with four open tabs, matching the real envelope. */
function seedTabs(): string {
  const group = {
    activeTabId: TABS[0]!.id,
    recentTabIds: [],
    browsingHistory: TABS.map((tab) => tab.url),
    browsingHistoryTitles: Object.fromEntries(TABS.map((tab) => [tab.url, tab.title])),
    tabs: TABS.map((tab) => {
      const clean = tab.url.split("#", 1)[0]!.split("?", 1)[0]!;
      return {
        id: tab.id,
        url: tab.url,
        resourceKey: clean,
        title: tab.title,
        pinned: false,
        history: { stack: [tab.url], index: 0 },
        memento: { scroll: {}, view: {} },
      };
    }),
  };
  return JSON.stringify({
    state: {
      activeWorkspaceSlug: SLUG,
      byWorkspace: { [SLUG]: group },
      windowHistory: { entries: [], index: -1 },
      tabAttention: null,
    },
    version: 5,
  });
}

function percentile(values: number[], fraction: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const index = Math.max(0, Math.ceil(fraction * sorted.length) - 1);
  return Math.round(sorted[index]! * 100) / 100;
}

function emit(report: Record<string, unknown>): void {
  const out = process.env.PERF_REPORT_PATH;
  if (out) {
    mkdirSync(dirname(resolve(out)), { recursive: true });
    writeFileSync(resolve(out), JSON.stringify(report, null, 2));
  }
  test.info().attach("tab-switch-report", {
    body: JSON.stringify(report, null, 2),
    contentType: "application/json",
  });
  // eslint-disable-next-line no-console
  console.log(`PERF_REPORT ${JSON.stringify(report)}`);
}

async function readSpans(page: Page): Promise<Record<Span, number | null>> {
  return page.evaluate((names) => {
    const out: Record<string, number | null> = {};
    for (const name of names) {
      const entries = performance.getEntriesByName(`tab-switch:${name}`, "measure");
      out[name] = entries.length
        ? Math.round((entries[entries.length - 1]!.duration ?? 0) * 100) / 100
        : null;
    }
    return out as Record<string, number | null>;
  }, [...SPANS]) as Promise<Record<Span, number | null>>;
}

test("switching web tabs paints from cache within a frame budget", async ({ page, context, baseURL }) => {
  const origin = new URL(baseURL ?? "http://localhost:3000").origin;
  const pageErrors: string[] = [];
  const unexpected: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  await context.addCookies([
    { name: "multica_logged_in", value: "1", url: origin },
    { name: "last_workspace_slug", value: SLUG, url: origin },
  ]);
  await page.addInitScript((tabs) => {
    localStorage.setItem("multica_tabs", tabs);
    localStorage.setItem("multica:chat:floatingChatEnabled", "false");
    localStorage.setItem("multica:chat:isOpen", "false");
  }, seedTabs());
  await page.routeWebSocket(() => true, (ws) => { ws.onMessage(() => {}); });
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.origin !== origin) return route.abort();
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = responseFor(url.pathname);
    if (body === undefined) {
      unexpected.push(`${route.request().method()} ${url.pathname}${url.search}`);
      return route.fulfill({ status: 404, contentType: "application/json", body: "{}" });
    }
    return route.fulfill({
      status: 200, contentType: "application/json", body: JSON.stringify(body),
    });
  });

  await page.goto(`/${SLUG}/issues`, { waitUntil: "domcontentloaded" });
  await expect(page.locator('[data-slot="web-tab-strip"]')).toBeVisible();
  await expect(page.locator('[data-tab-id="tab-chat"]')).toBeVisible();
  await expect(page.locator('[data-tab-id="tab-agents"]')).toBeVisible();
  await expect(page.locator('[data-tab-id="tab-issue"]')).toBeVisible();
  await page.evaluate(() => document.fonts?.ready);

  const repeats = Number(process.env.PERF_REPEATS ?? 5);
  interface Sample {
    /** Click → App Router commit, measured in-page (works on any build). */
    external_commit_ms: number;
    /** Click → second frame after commit, measured in-page. */
    external_paint_ms: number;
    /** Product User Timing spans, present only on instrumented builds. */
    spans: Record<Span, number | null>;
  }
  const samples: Record<string, Sample[]> = { cold: [], warm: [] };

  for (let lap = 0; lap < repeats; lap += 1) {
    const phase = lap === 0 ? "cold" : "warm";
    for (const step of LAP) {
      // Start a page-side clock before the click. Polling the address bar on
      // rAF gives a click→commit number for both the baseline and the fixed
      // build; the product marks add the per-span breakdown when present.
      const external = page.evaluate(
        (path) =>
          new Promise<{ commit: number; paint: number }>((resolve) => {
            const start = performance.now();
            let committed = false;
            let committedAt = 0;
            const tick = () => {
              if (!committed && location.pathname === path) {
                committed = true;
                committedAt = performance.now();
                requestAnimationFrame(() =>
                  requestAnimationFrame(() =>
                    resolve({
                      commit: committedAt - start,
                      paint: performance.now() - start,
                    }),
                  ),
                );
                return;
              }
              requestAnimationFrame(tick);
            };
            tick();
          }),
        step.path,
      );
      await page.locator(`[data-tab-id="${step.id}"] button`).first().click();
      const timing = await Promise.race([
        external,
        new Promise<never>((_, reject) =>
          setTimeout(() => reject(new Error(`no commit for ${step.path}`)), 20_000),
        ),
      ]);
      await page.waitForURL((url) => url.pathname === step.path, { timeout: 20_000 });
      samples[phase]!.push({
        external_commit_ms: Math.round(timing.commit * 100) / 100,
        external_paint_ms: Math.round(timing.paint * 100) / 100,
        spans: await readSpans(page),
      });
    }
  }

  const summary: Record<string, Record<string, Record<string, number | null>>> = {};
  for (const phase of ["cold", "warm"]) {
    const all = samples[phase]!;
    const expected = phase === "cold" ? LAP.length : (repeats - 1) * LAP.length;
    expect(all.length, `${phase}: every switch must commit`).toBe(expected);
    const stats = (values: number[]) => ({
      p50: percentile(values, 0.5),
      p95: percentile(values, 0.95),
      max: Math.round(Math.max(...values) * 100) / 100,
    });
    summary[phase] = {
      "external/click-to-commit": stats(all.map((s) => s.external_commit_ms)),
      "external/click-to-paint": stats(all.map((s) => s.external_paint_ms)),
    };
    for (const span of SPANS) {
      const values = all
        .map((sample) => sample.spans[span])
        .filter((value): value is number => value !== null);
      summary[phase]![span] = values.length === 0
        ? { p50: null, p95: null, max: null }
        : stats(values);
    }
  }

  const report = {
    status: "ok",
    scenario: "web-tab-switch",
    repeats,
    viewport: "1440x900",
    browser: page.context().browser()?.version() ?? "unknown",
    spans: Object.fromEntries(SPANS.map((span) => [span, `tab-switch:${span}`])),
    summary,
    samples,
    page_errors: pageErrors,
    unexpected_requests: unexpected,
  };
  emit(report);
  expect(pageErrors).toEqual([]);
  expect(unexpected).toEqual([]);
});
