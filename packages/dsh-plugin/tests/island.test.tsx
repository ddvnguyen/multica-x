import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { isIslandMounted, mountIsland, unmountIsland } from "../src/client/island";
import { shadowStyles } from "../src/client/popup-seat";
import { BOARD_ROUTE, type IslandRoute } from "../src/client/navigation";
import { attachSeat } from "../src/client/seat";

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

  // One row per rail entry: the island has to render each of the five views on
  // its own, inside the same shadow root, with nothing page-level in the shell.
  const entries: Array<{ entry: string; route: IslandRoute; text: string }> = [
    { entry: "issues", route: BOARD_ROUTE, text: "Issues" },
    { entry: "agents", route: { view: "agents" }, text: "Agents" },
    { entry: "skills", route: { view: "skills" }, text: "Skills" },
    { entry: "inbox", route: { view: "inbox" }, text: "Inbox" },
    { entry: "chat", route: { view: "chat" }, text: "Chat" },
  ];

  for (const { entry, route, text } of entries) {
    it(`opens the ${entry} entry inside the shadow root`, async () => {
      stubBackend();
      const host = hostElement();
      act(() => mountIsland(host, { initialRoute: route }));

      await settle(() => {
        const rendered = host.shadowRoot?.textContent ?? "";
        expect(rendered).toContain(text);
        expect(rendered).not.toContain("Connecting to Multica");
        expect(rendered).not.toContain("did not start");
      });

      // Native rendering only: no iframe anywhere in the document, and the
      // island's sheet stays inside its own shadow root.
      expect(host.shadowRoot?.querySelector("iframe")).toBeNull();
      expect(document.querySelectorAll("iframe")).toHaveLength(0);
      expect(host.shadowRoot?.querySelector("style[data-multica-styles]")).not.toBeNull();
      expect(document.querySelector("style[data-multica-styles]")).toBeNull();
    });
  }

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

  it("tears a seat down once the rail moves away from it", async () => {
    stubBackend();
    const seat = document.createElement("div");
    document.body.append(seat);
    mounted.push(seat);

    act(() => attachSeat(seat, BOARD_ROUTE));
    expect(isIslandMounted(seat)).toBe(true);

    // The shell detaches the ref before it removes the node, so the sweep runs a
    // microtask later — after the commit — and only for a node that really left.
    await act(async () => {
      seat.remove();
      attachSeat(null, BOARD_ROUTE);
    });
    expect(isIslandMounted(seat)).toBe(false);
  });

  it("keeps a seat alive across a re-attach", async () => {
    stubBackend();
    const seat = document.createElement("div");
    document.body.append(seat);
    mounted.push(seat);

    act(() => {
      attachSeat(seat, BOARD_ROUTE);
      // A re-render detaches and re-attaches the same node.
      attachSeat(null, BOARD_ROUTE);
      attachSeat(seat, BOARD_ROUTE);
    });
    await act(async () => {});
    expect(isIslandMounted(seat)).toBe(true);
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

describe("popup seat", () => {
  function overlayLayer(): HTMLElement {
    const layer = document.createElement("div");
    layer.dataset.shellOverlay = "";
    document.body.append(layer);
    return layer;
  }

  it("seats the island's portals in the shell's overlay layer", async () => {
    stubBackend();
    const layer = overlayLayer();
    const host = hostElement();
    act(() => mountIsland(host));

    const seat = layer.querySelector<HTMLElement>("[data-multica-popup-seat]");
    expect(seat).not.toBeNull();
    // Zero-size, inert, above the app layer: the shell's own overlay-layer
    // pattern, so the seat itself blocks nothing and steals no clicks.
    expect(seat?.style.position).toBe("absolute");
    expect(seat?.style.width).toBe("0px");
    expect(seat?.style.height).toBe("0px");
    expect(seat?.style.zIndex).toBe("60");
    expect(seat?.style.pointerEvents).toBe("none");

    // The portal container lives inside the seat's shadow root — outside the
    // panel's stacking context, still under the island's stylesheet.
    const seatShadow = seat?.shadowRoot;
    expect(seatShadow?.querySelector("style[data-multica-styles]")).not.toBeNull();
    const portalHost = seatShadow?.querySelector("[data-multica-portals]");
    expect(portalHost).not.toBeNull();
    expect(portalHost?.parentNode).toBe(seatShadow);

    // One portal seat: the panel's own shadow root keeps the page, not the
    // popups — popups must not render below the shell's overlay layer again.
    expect(host.shadowRoot?.querySelector("[data-multica-portals]")).toBeNull();

    await settle(() => {
      expect(host.shadowRoot?.textContent ?? "").toContain("Issues");
    });
  });

  it("follows the shell's theme on the seat", async () => {
    stubBackend();
    const layer = overlayLayer();
    const host = hostElement();
    act(() => mountIsland(host));

    const portalHost = layer
      .querySelector("[data-multica-popup-seat]")
      ?.shadowRoot?.querySelector("[data-multica-portals]");
    expect(portalHost?.classList.contains("dark")).toBe(false);

    act(() => {
      document.documentElement.classList.add("dark");
    });
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, 20);
    await promise;
    expect(portalHost?.classList.contains("dark")).toBe(true);
    act(() => {
      document.documentElement.classList.remove("dark");
    });
  });

  it("removes the seat from the shell's layer on unmount", async () => {
    stubBackend();
    const layer = overlayLayer();
    const host = hostElement();
    act(() => mountIsland(host));
    expect(layer.querySelector("[data-multica-popup-seat]")).not.toBeNull();

    act(() => unmountIsland(host));
    expect(layer.querySelector("[data-multica-popup-seat]")).toBeNull();
    expect(host.shadowRoot?.childNodes.length).toBe(0);
  });

  it("falls back inside the surface when the shell has no overlay layer", async () => {
    stubBackend();
    const host = hostElement();
    act(() => mountIsland(host));

    expect(document.querySelector("[data-multica-popup-seat]")).toBeNull();
    const portalHost = host.shadowRoot?.querySelector("[data-multica-portals]");
    expect(portalHost).not.toBeNull();
    expect(
      portalHost?.parentElement?.classList.contains("multica-island-surface"),
    ).toBe(true);

    await settle(() => {
      expect(host.shadowRoot?.textContent ?? "").toContain("Issues");
    });
  });
});
