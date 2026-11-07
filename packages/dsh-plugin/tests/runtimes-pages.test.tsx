import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { islandHost, resetIsland } from "../src/client/island";
import { attachSeat } from "../src/client/seat";
import { resetIslandConfig } from "../src/client/config";
import type { IslandRoute } from "../src/client/navigation";

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 64,
        end: (index + 1) * 64,
        size: 64,
      })),
    getTotalSize: () => count * 64,
  }),
}));

afterEach(() => {
  resetIsland();
  document.body.replaceChildren();
  resetIslandConfig();
  vi.unstubAllGlobals();
});

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/** One machine running one agent CLI; its machine id is `local:<daemon>`. */
const RUNTIME = {
  id: "rt-1",
  workspace_id: "ws-1",
  daemon_id: "dmn-1",
  name: "Studio",
  custom_name: null,
  runtime_mode: "local",
  provider: "claude",
  launch_header: "Claude Code",
  status: "online",
  device_info: "darwin arm64",
  metadata: {},
  owner_id: "user-1",
  visibility: "private",
  shared_with: [],
  profile_id: null,
  last_seen_at: "2026-11-03T00:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-11-03T00:00:00Z",
};

const MACHINE_ID = "local:dmn-1";

function stubBackend() {
  const handler = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    if (url.startsWith("/multica/config")) {
      return json({ wsPath: "/multica/ws", workspaceSlug: "demo", problem: null });
    }
    if (url.startsWith("/multica/api/workspaces")) {
      return json([
        {
          id: "ws-1",
          slug: "demo",
          name: "Demo",
          created_at: "2026-01-01T00:00:00Z",
          updated_at: "2026-01-01T00:00:00Z",
        },
      ]);
    }
    if (url.startsWith("/multica/api/me")) {
      return json({ id: "user-1", name: "Ada", email: "ada@example.test" });
    }
    if (url.startsWith("/multica/api/runtimes")) return json([RUNTIME]);
    if (url.startsWith("/multica/api/issues/grouped")) {
      return json({
        groups: [
          { id: "none", assignee_type: null, assignee_id: null, issues: [], total: 0 },
        ],
      });
    }
    if (url === "/multica/api/issues" || url.startsWith("/multica/api/issues?")) {
      return json({ issues: [], total: 0, comments: [] });
    }
    return json([]);
  });
  vi.stubGlobal("fetch", handler);
}

function shadow(): ShadowRoot {
  return (islandHost() as HTMLElement).shadowRoot as ShadowRoot;
}

/** Keep letting React commit until `check` holds (queries land asynchronously). */
async function until(check: () => boolean, timeoutMs = 8000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
  }
  expect(check()).toBe(true);
}

function rows(): HTMLElement[] {
  return [...shadow().querySelectorAll<HTMLElement>("[role='row']")];
}

/** The machine row the shared list draws (its pill names the device). */
function machineRow(): HTMLElement {
  const row = rows().find((candidate) =>
    candidate.textContent?.includes("darwin arm64"),
  );
  expect(row).toBeDefined();
  return row as HTMLElement;
}

function tabs(): string[] {
  return [...shadow().querySelectorAll<HTMLElement>("[data-tab-frame]")].map(
    (frame) => frame.textContent ?? "",
  );
}

async function attach(route: IslandRoute): Promise<void> {
  stubBackend();
  const seat = document.createElement("div");
  document.body.append(seat);
  act(() => attachSeat(seat, route));
  await until(() => shadow().querySelector("[data-tab-strip]") !== null);
}

describe("runtimes pages in the panel (M-174)", () => {
  it("opens a machine's page from the list instead of swallowing the click", async () => {
    await attach({ view: "runtimes" });
    await until(() => rows().some((row) => row.textContent?.includes("darwin arm64")));
    expect(tabs()).toHaveLength(1);

    const link = machineRow().querySelector("a");
    expect(link).not.toBeNull();
    await act(async () => {
      (link as Element).dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true }),
      );
    });

    // The row used to push an address the island refused, so nothing happened.
    // A plain click navigates this tab, the way a row does in the desktop
    // window; only a modifier click opens a second one.
    await until(() => tabs()[0]?.includes("Machine") === true);
    expect(tabs()).toHaveLength(1);
    await until(() => (shadow().textContent ?? "").includes("Studio"));
    expect(shadow().textContent).toContain("Rename machine");
  }, 20000);

  it("renders a machine's page with every runtime it reports", async () => {
    await attach({ view: "runtime", machineId: MACHINE_ID });
    await until(() => (shadow().textContent ?? "").includes("Studio"));
    // The machine header, and the runtime its machine holds.
    expect(shadow().textContent).toContain("1 runtime");
    expect(shadow().textContent).toContain("Built-in");
  });

  it("renders one runtime's settings under its machine", async () => {
    await attach({ view: "runtimeSettings", machineId: MACHINE_ID, runtimeId: "rt-1" });
    await until(() => (shadow().textContent ?? "").includes("Studio"));
    expect(shadow().textContent).toContain("Technical details");
  }, 20000);
});
