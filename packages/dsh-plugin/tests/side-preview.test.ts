import { describe, expect, it } from "vitest";
import {
  SIDE_PREVIEW_KIND,
  canOpenSidePreview,
  createSidePreviewOpener,
  issueIdFromParams,
} from "../src/client/side-preview";
import type {
  SessionsLike,
  SidebarRightLike,
  WorkspaceLike,
  WorkspacesLike,
} from "../src/client/shell-link";

type TabOptions = Parameters<NonNullable<SidebarRightLike["openTab"]>>[1];

interface Opened {
  kind: string;
  options?: TabOptions;
}

/** A sidebar face with the hosted-panel and tab seams the preview needs. */
function fakeSidebar(
  openTab: SidebarRightLike["openTab"] = () => {},
  overrides: Partial<SidebarRightLike> = {},
) {
  let mounted: string | undefined = "s-1";
  const names: string[] = [];
  const opened: Opened[] = [];
  const sidebarRight: SidebarRightLike = {
    mounted: {
      getSnapshot: () => mounted,
      subscribe: () => () => {},
    },
    toggleExpanded: () => {},
    isExpanded: () => true,
    hostPanel: () => () => {},
    selectHostedSession: (sessionId: string) => {
      names.push(sessionId);
      mounted = sessionId;
      return () => {};
    },
    openTab: (kind: string, options?: TabOptions) => {
      opened.push({ kind, options });
      openTab(kind, options);
    },
    ...overrides,
  };
  return { sidebarRight, names, opened };
}

function fakeWorkspaces(created: string[], items: WorkspaceLike[] = []) {
  const workspaces: WorkspacesLike = {
    list: {
      getSnapshot: () => ({ items }),
      subscribe: () => () => {},
    },
    create: async (input: { path: string }) => {
      created.push(input.path);
      return { workspaceId: "w-new", path: input.path, sessionIds: [] };
    },
  };
  return workspaces;
}

function fakeSessions(created: string[]) {
  const sessions: SessionsLike = {
    list: {
      getSnapshot: () => ({ byId: { "s-1": { id: "s-1" } } }),
      subscribe: () => () => {},
    },
    create: async () => {
      created.push("session");
      return "s-new";
    },
  };
  return sessions;
}

describe("issueIdFromParams", () => {
  it("reads a non-empty string id out of dsh's navigation params", () => {
    expect(issueIdFromParams({ issueId: "iss-1" })).toBe("iss-1");
  });

  it("is null for a tab with no params, a blank id or a non-string", () => {
    expect(issueIdFromParams(undefined)).toBeNull();
    expect(issueIdFromParams(null)).toBeNull();
    expect(issueIdFromParams({})).toBeNull();
    expect(issueIdFromParams({ issueId: "   " })).toBeNull();
    expect(issueIdFromParams({ issueId: 42 })).toBeNull();
    expect(issueIdFromParams("iss-1")).toBeNull();
  });
});

describe("canOpenSidePreview", () => {
  it("needs both the tab-type registry and openTab", () => {
    const { sidebarRight } = fakeSidebar();
    expect(canOpenSidePreview(sidebarRight, true)).toBe(true);
    expect(canOpenSidePreview(sidebarRight, false)).toBe(false);
    expect(canOpenSidePreview(null, true)).toBe(false);
  });

  it("is false on a shell whose face has no openTab", () => {
    const { sidebarRight } = fakeSidebar(undefined, { openTab: undefined });
    expect(canOpenSidePreview(sidebarRight, true)).toBe(false);
  });
});

describe("createSidePreviewOpener", () => {
  it("names the folder's Session and opens one revealing page tab", async () => {
    const { sidebarRight, names, opened } = fakeSidebar();
    const workspaceCreates: string[] = [];
    const sessionCreates: string[] = [];
    const open = createSidePreviewOpener({
      sidebarRight,
      workspaces: fakeWorkspaces(workspaceCreates),
      sessions: fakeSessions(sessionCreates),
      folder: "/work/acme",
    });

    await open("iss-7");

    expect(workspaceCreates).toEqual(["/work/acme"]);
    expect(sessionCreates).toHaveLength(1);
    expect(names).toEqual(["s-new"]);
    // One tab, revealed rather than duplicated, carrying the issue the body
    // reads back out of `navigation.params` (M-336).
    expect(opened).toEqual([
      {
        kind: SIDE_PREVIEW_KIND,
        options: { params: { issueId: "iss-7" }, revealIfOpened: true },
      },
    ]);
  });

  it("keeps the Workspace and Session a project already has", async () => {
    const { sidebarRight, names } = fakeSidebar();
    const workspaceCreates: string[] = [];
    const sessionCreates: string[] = [];
    const open = createSidePreviewOpener({
      sidebarRight,
      workspaces: fakeWorkspaces(workspaceCreates, [
        { workspaceId: "w-1", path: "/work/acme", sessionIds: ["s-1"] },
      ]),
      sessions: fakeSessions(sessionCreates),
      folder: "/work/acme/",
    });

    await open("iss-7");

    expect(workspaceCreates).toEqual([]);
    expect(sessionCreates).toEqual([]);
    // The column already edits that Session, so there is nothing to retarget:
    // the same rule the header's toggle follows (M-316).
    expect(names).toEqual([]);
  });

  it("refuses a shell without openTab and a view with no folder", async () => {
    const headless = fakeSidebar(undefined, { openTab: undefined });
    await expect(
      createSidePreviewOpener({
        sidebarRight: headless.sidebarRight,
        workspaces: fakeWorkspaces([]),
        sessions: fakeSessions([]),
        folder: "/work/acme",
      })("iss-1"),
    ).rejects.toThrow(/cannot host the side preview/);
    await expect(
      createSidePreviewOpener({
        sidebarRight: fakeSidebar().sidebarRight,
        workspaces: fakeWorkspaces([]),
        sessions: fakeSessions([]),
        folder: null,
      })("iss-1"),
    ).rejects.toThrow(/names no project folder/);
  });

  it("refuses a build that cannot draw the column beside MMX", async () => {
    const { sidebarRight } = fakeSidebar(undefined, { hostPanel: undefined });
    await expect(
      createSidePreviewOpener({
        sidebarRight,
        workspaces: fakeWorkspaces([]),
        sessions: fakeSessions([]),
        folder: "/work/acme",
      })("iss-1"),
    ).rejects.toThrow(/cannot show the side panel next to MMX/);
  });
});
