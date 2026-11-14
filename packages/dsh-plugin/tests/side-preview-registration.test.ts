import { afterAll, afterEach, describe, expect, it } from "vitest";
import type { DshClientContext, DshSidebarRightTabDefinition } from "../src/dsh";
import { apply } from "../src/client/index";
import { resetIsland } from "../src/client/island";
import type {
  SessionsLike,
  SidebarRightLike,
  WorkspacesLike,
} from "../src/client/shell-link";
import { hostShell, resetHostShell } from "../src/client/shell-runtime";
import { startUnreadPoll } from "../src/client/unread-store";

afterEach(() => {
  resetHostShell();
});

// `apply` warms the island on a timer and starts the unread poller; the same
// teardown seat.test uses keeps neither alive past this file.
afterAll(async () => {
  startUnreadPoll()();
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, 250);
  await promise;
  resetIsland();
});

interface Registration {
  options: Record<string, unknown>;
  component: unknown;
}

function fakeShellFaces(withOpenTab = true) {
  const sidebarRight: SidebarRightLike = {
    mounted: { getSnapshot: () => "s-1", subscribe: () => () => {} },
    toggleExpanded: () => {},
    isExpanded: () => false,
    hostPanel: () => () => {},
    selectHostedSession: () => () => {},
    ...(withOpenTab ? { openTab: () => {} } : {}),
  };
  const workspaces: WorkspacesLike = {
    list: { getSnapshot: () => ({ items: [] }), subscribe: () => () => {} },
    create: async (input) => ({ workspaceId: "w-1", path: input.path, sessionIds: [] }),
  };
  const sessions: SessionsLike = {
    list: { getSnapshot: () => ({ byId: {} }), subscribe: () => () => {} },
    create: async () => "s-1",
  };
  return { sidebarRight, workspaces, sessions };
}

function fakeContext(options: { withTabTypes?: boolean; withOpenTab?: boolean } = {}) {
  const { withTabTypes = true, withOpenTab = true } = options;
  const registrations: Registration[] = [];
  const injected: string[] = [];
  const effects: string[] = [];
  const tabTypes: DshSidebarRightTabDefinition[] = [];
  const ctx = {
    slots: {
      inject(key: string, register: () => unknown) {
        injected.push(key);
        return register();
      },
      register(opts: Record<string, unknown>, component: unknown) {
        registrations.push({ options: opts, component });
        return () => {};
      },
    },
    effect(fn: () => (() => void) | void) {
      return fn();
    },
    ...fakeShellFaces(withOpenTab),
    ...(withTabTypes
      ? {
          sidebarRightTabs: {
            register(definition: DshSidebarRightTabDefinition) {
              tabTypes.push(definition);
              effects.push(definition.id);
              return () => {};
            },
          },
        }
      : {}),
  } as unknown as DshClientContext;
  return { ctx, registrations, injected, tabTypes, effects };
}

describe("side preview registration", () => {
  it("registers one page tab type and its body when the shell has the seam", () => {
    const { ctx, registrations, injected, tabTypes } = fakeContext();
    apply(ctx);

    expect(tabTypes).toHaveLength(1);
    const definition = tabTypes[0]!;
    expect(definition.id).toBe("@multica/dsh-plugin");
    expect(definition.kind).toBe("multica-issue");
    // dsh captures the chip once per tab and never rewrites it, and a page
    // type's only address is `sidebar://<kind>`: the chip names the kind.
    expect(definition.title("sidebar://multica-issue")).toBe("Issue");

    // Stage two: the body is keyed by the same id, so the shell dispatches
    // every tab of the kind to it.
    expect(injected).toContain("sidebar.right.pane.tab");
    const body = registrations.filter((entry) => entry.options.name === "sidebar.right.pane.tab");
    expect(body).toHaveLength(1);
    expect(body[0]?.options.key).toBe("@multica/dsh-plugin");
    expect(typeof body[0]?.component).toBe("function");
  });

  it("gives the island the kind, so `Side preview` is offered", () => {
    const { ctx } = fakeContext();
    apply(ctx);
    expect(hostShell()?.sidePreviewKind).toBe("multica-issue");
  });

  it("registers nothing on a shell without the tab registry", () => {
    const { ctx, registrations, injected, tabTypes } = fakeContext({ withTabTypes: false });
    apply(ctx);

    expect(tabTypes).toHaveLength(0);
    expect(injected).not.toContain("sidebar.right.pane.tab");
    expect(
      registrations.some((entry) => entry.options.name === "sidebar.right.pane.tab"),
    ).toBe(false);
    // The island then offers `New tab` alone — the M-330 state, not an open
    // mode that cannot work.
    expect(hostShell()?.sidePreviewKind).toBeNull();
  });

  it("registers nothing on a shell whose sidebar face has no openTab", () => {
    const { ctx, tabTypes } = fakeContext({ withOpenTab: false });
    apply(ctx);

    expect(tabTypes).toHaveLength(0);
    expect(hostShell()?.sidePreviewKind).toBeNull();
  });
});
