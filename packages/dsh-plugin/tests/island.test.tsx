import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { isIslandMounted, mountIsland, shadowStyles, unmountIsland } from "../src/client/island";

/**
 * The island is a second React tree inside a shadow root, so these tests drive
 * it the way the shell does: mount into a host element and read the shadow DOM
 * back. The API is a stub in front of the `/multica` proxy path — the proxy's
 * own forwarding is covered in `proxy.test.ts`.
 */

const requests: string[] = [];
const mounted: HTMLElement[] = [];

afterEach(() => {
  // The island owns listeners and a live WebSocket; leaving one mounted would
  // leak that work into the next test in this file.
  for (const host of mounted.splice(0)) unmountIsland(host);
  document.body.replaceChildren();
  requests.length = 0;
  vi.unstubAllGlobals();
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

const WORKSPACE = {
  id: "ws-1",
  slug: "demo",
  name: "Demo",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const ISSUE = {
  id: "11111111-1111-4111-8111-111111111111",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Fix the rail row",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 1000,
  stage: null,
  start_date: null,
  due_date: null,
  metadata: {},
  properties: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function stubBackend(
  options: { workspaces?: unknown[]; problem?: string | null; issues?: unknown[] } = {},
) {
  const handler = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    requests.push(url);
    if (url.startsWith("/multica/config")) {
      return json({
        wsPath: "/multica/ws",
        workspaceSlug: "",
        problem: options.problem ?? null,
      });
    }
    if (url.startsWith("/multica/api/workspaces")) {
      return json(options.workspaces ?? [WORKSPACE]);
    }
    if (url.startsWith("/multica/api/me")) {
      return json({ id: "user-1", name: "Ada", email: "ada@example.test" });
    }
    if (url.startsWith("/multica/api/issues/grouped")) {
      return json({
        groups: [
          {
            id: "none",
            assignee_type: null,
            assignee_id: null,
            issues: options.issues ?? [],
            total: (options.issues ?? []).length,
          },
        ],
      });
    }
    if (url.startsWith("/multica/api/issues")) {
      const single = options.issues?.length === 1 ? options.issues[0] : null;
      if (single !== null && !url.includes("/comments") && /issues\/[^/?]+$/.test(url.split("?")[0] ?? "")) {
        return json(single);
      }
      return json({ issues: options.issues ?? [], total: (options.issues ?? []).length, comments: [] });
    }
    if (
      url.startsWith("/multica/api/pins") ||
      url.startsWith("/multica/api/projects") ||
      url.startsWith("/multica/api/issue-statuses") ||
      url.startsWith("/multica/api/labels")
    ) {
      return json([]);
    }
    if (url.includes("assignee-frequency") || url.includes("/agents") || url.includes("/members")) {
      return json([]);
    }
    // Object-shaped singletons the board reads on mount.
    if (url.startsWith("/multica/api/config") || url.startsWith("/multica/api/onboarding")) {
      return json({});
    }
    // Everything else the fork reads on mount is a list; an empty list is the
    // shape those queries parse, so one entry keeps the page rendering instead
    // of tripping over a `{}` that no `.map` accepts.
    return json([]);
  });
  vi.stubGlobal("fetch", handler);
  return handler;
}

function hostElement(): HTMLElement {
  const host = document.createElement("div");
  document.body.append(host);
  mounted.push(host);
  return host;
}

async function settle(assertion: () => void, timeoutMs = 3000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      assertion();
      return;
    } catch {
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 20));
      });
    }
  }
  assertion();
}

describe("shadowStyles", () => {
  it("retargets the fork's :root tokens at the shadow host", () => {
    expect(shadowStyles(':root { --background: red; }\n.foo:rootbar { color: red }')).toBe(
      ':host { --background: red; }\n.foo:rootbar { color: red }',
    );
  });
});

describe("mountIsland", () => {
  it("renders the fork's board inside its own shadow root", async () => {
    stubBackend();
    const host = hostElement();
    act(() => mountIsland(host));

    const shadow = host.shadowRoot;
    expect(shadow).not.toBeNull();
    expect(shadow?.querySelector("style[data-multica-styles]")).not.toBeNull();
    expect(shadow?.querySelector("[data-multica-root]")).not.toBeNull();
    expect(shadow?.querySelector("[data-multica-portals]")).not.toBeNull();

    // Styles are shadow-local: the document itself carries no island sheet.
    expect(document.querySelector("style[data-multica-styles]")).toBeNull();

    await settle(() => {
      expect(host.shadowRoot?.textContent ?? "").not.toContain("Connecting to Multica");
      expect(host.shadowRoot?.textContent ?? "").not.toContain("did not start");
    });

    // The workspace resolved from the stub backend and the board mounted under
    // the panel's own chrome: a page header plus the view's own filter row.
    await settle(() => {
      const text = host.shadowRoot?.textContent ?? "";
      expect(text).toContain("Issues");
    });
    expect(requests.some((url) => url.startsWith("/multica/api/workspaces"))).toBe(true);
  });

  it("renders one issue from the backend inside the panel", async () => {
    stubBackend({ issues: [ISSUE] });
    const host = hostElement();
    act(() => mountIsland(host, { initialRoute: { view: "issue", issueId: ISSUE.identifier } }));

    await settle(() => {
      const text = host.shadowRoot?.textContent ?? "";
      expect(text).toContain(ISSUE.identifier);
      expect(text).toContain(ISSUE.title);
    });
    expect(requests.some((url) => url.includes("issues"))).toBe(true);
  });

  it("explains a host that is not configured instead of rendering an empty page", async () => {
    stubBackend({ problem: "token is not set" });
    const host = hostElement();
    act(() => mountIsland(host));

    await settle(() => {
      const text = host.shadowRoot?.textContent ?? "";
      expect(text).toContain("Multica is not configured");
      expect(text).toContain("token is not set");
    });
  });

  it("reports a token that cannot see a workspace", async () => {
    stubBackend({ workspaces: [] });
    const host = hostElement();
    act(() => mountIsland(host));

    await settle(() => {
      expect(host.shadowRoot?.textContent ?? "").toContain("No Multica workspace");
    });
  });

  it("is idempotent per host and tears down cleanly", async () => {
    stubBackend();
    const host = hostElement();
    act(() => mountIsland(host));
    expect(isIslandMounted(host)).toBe(true);
    act(() => mountIsland(host));
    expect(host.shadowRoot?.querySelectorAll("[data-multica-root]")).toHaveLength(1);

    act(() => unmountIsland(host));
    expect(isIslandMounted(host)).toBe(false);
    expect(host.shadowRoot?.childNodes.length).toBe(0);
  });
});
