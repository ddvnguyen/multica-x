/**
 * The shell-link rules (M-159, M-316): the folder chain every MMX view feeds,
 * workspace matching, session binding, and the toggle plan's `hostPanel`
 * feature detection — including the press that must never navigate. All pure —
 * the dsh faces enter as structural test doubles.
 */
import { describe, expect, it } from "vitest";
import {
  boundSessionId,
  describe as describeReason,
  disabledTooltip,
  folderForSelection,
  folderOfProjectResources,
  HOST_NO_BESIDE_PANEL_TOOLTIP,
  isNoSessionSurfaceError,
  normalizePath,
  NO_SESSION_SURFACE_NOTE,
  noSessionSurfaceError,
  panelToggleTitle,
  planSidebarToggle,
  sessionSurfaceMounted,
  toggleState,
  workspaceForFolder,
  type FolderSelectionInput,
  type ProjectResourceLike,
  type WorkspaceLike,
} from "../src/client/shell-link";

const PROJECTS = [{ id: "p1" }, { id: "p2" }];

function directory(path: string, daemonId = "daemon-a"): ProjectResourceLike {
  return { resource_type: "local_directory", resource_ref: { local_path: path, daemon_id: daemonId } };
}

describe("folderOfProjectResources", () => {
  it("takes the first local_directory ref that is not on another daemon", () => {
    expect(
      folderOfProjectResources(
        [
          { resource_type: "github_repo", resource_ref: { url: "https://github" } },
          directory("/srv/demo"),
        ],
        "DAEMON-A",
      ),
    ).toBe("/srv/demo");
  });

  it("skips refs whose daemon is not this host's", () => {
    expect(folderOfProjectResources([directory("/x", "daemon-b")], "daemon-a")).toBeNull();
  });

  it("filters nothing when the host cannot name its daemon", () => {
    expect(folderOfProjectResources([directory("/x", "daemon-b")], "")).toBe("/x");
  });

  it("skips worktree refs: a runtime copy is not the folder the sidebar Session owns", () => {
    const ref = {
      resource_type: "local_directory",
      resource_ref: { local_path: "/srv/worktree", daemon_id: "daemon-a", execution_mode: "worktree" },
    };
    expect(folderOfProjectResources([ref], "daemon-a")).toBeNull();
  });

  it("ignores non-directory types and empty paths", () => {
    expect(
      folderOfProjectResources([{ resource_type: "local_directory", resource_ref: { local_path: " " } }], "d"),
    ).toBeNull();
  });
});

/** One `resourceFor` over a fixed map: a missing id answers `null`. */
function foldersFrom(map: Record<string, string>): (id: string) => string | null {
  return (id) => map[id] ?? null;
}

function selection(overrides: Partial<FolderSelectionInput> = {}): FolderSelectionInput {
  return {
    projectId: null,
    projects: PROJECTS,
    resourceFor: () => null,
    workspaceFolder: "",
    ...overrides,
  };
}

describe("folderForSelection", () => {
  it("uses the picked project's folder", () => {
    expect(
      folderForSelection(
        selection({ projectId: "p1", resourceFor: foldersFrom({ p1: "/srv/demo" }) }),
      ),
    ).toEqual({ folder: "/srv/demo", reason: "project" });
  });

  it("disables with the link-a-folder copy when the picked project has no local_directory", () => {
    const resolved = folderForSelection(selection({ projectId: "p2", workspaceFolder: "/srv/demo" }));
    expect(resolved).toEqual({ folder: null, reason: "project-no-folder" });
    expect(disabledTooltip(resolved.reason, resolved.folder)).toBe("Link a folder to this project");
  });

  it("does not replace a picked project with another rung", () => {
    // The header's own picker must never open a folder it does not name.
    const resolved = folderForSelection(
      selection({
        projectId: "p2",
        issueProjectId: "p3",
        workspaceFolder: "/srv/only-all",
        resourceFor: foldersFrom({ p3: "/srv/issue" }),
      }),
    );
    expect(resolved.folder).toBeNull();
  });

  it("keeps working while the project list is loading", () => {
    const resolved = folderForSelection(
      selection({
        projectId: "p1",
        projects: undefined,
        resourceFor: foldersFrom({ p1: "/srv/demo" }),
      }),
    );
    expect(resolved).toEqual({ folder: "/srv/demo", reason: "project" });
  });

  it("uses the viewed issue's project under All projects", () => {
    // The Owner's case: an issue page whose picker still says "All projects".
    const resolved = folderForSelection(
      selection({
        issueProjectId: "p3",
        resourceFor: foldersFrom({ p3: "/srv/issue" }),
      }),
    );
    expect(resolved).toEqual({ folder: "/srv/issue", reason: "issue-project" });
  });

  it("prefers the viewed issue's project over the board's filter", () => {
    const resolved = folderForSelection(
      selection({
        issueProjectId: "p3",
        viewProjectId: "p1",
        resourceFor: foldersFrom({ p1: "/srv/demo", p3: "/srv/issue" }),
      }),
    );
    expect(resolved.folder).toBe("/srv/issue");
  });

  it("uses the one project the board filter names", () => {
    const resolved = folderForSelection(
      selection({ viewProjectId: "p1", resourceFor: foldersFrom({ p1: "/srv/demo" }) }),
    );
    expect(resolved).toEqual({ folder: "/srv/demo", reason: "view-project" });
  });

  it("falls through a named project that has no folder", () => {
    const resolved = folderForSelection(
      selection({
        issueProjectId: "p3",
        viewProjectId: "p1",
        resourceFor: foldersFrom({ p1: "/srv/demo" }),
      }),
    );
    expect(resolved).toEqual({ folder: "/srv/demo", reason: "view-project" });
  });

  it("names the folder-less project when nothing else resolves", () => {
    const resolved = folderForSelection(selection({ issueProjectId: "p3" }));
    expect(resolved).toEqual({ folder: null, reason: "project-no-folder" });
    expect(disabledTooltip(resolved.reason, resolved.folder)).toBe("Link a folder to this project");
  });

  it("falls back to workspaceFolder when no project is named", () => {
    expect(folderForSelection(selection({ workspaceFolder: "/srv/demo" }))).toEqual({
      folder: "/srv/demo",
      reason: "default",
    });
  });

  it("falls through while the named project's resources are still loading", () => {
    // `undefined` is "not known yet", not "no folder": the chain keeps going
    // rather than claiming the project is unlinked.
    const resolved = folderForSelection(
      selection({
        issueProjectId: "p3",
        workspaceFolder: "/srv/only-all",
        resourceFor: () => undefined,
      }),
    );
    expect(resolved).toEqual({ folder: "/srv/only-all", reason: "default" });
  });

  it("asks the reader to choose a project when All projects has no default", () => {
    const resolved = folderForSelection(selection());
    expect(resolved.folder).toBeNull();
    expect(disabledTooltip(resolved.reason, resolved.folder)).toBe("Choose a project");
  });

  it("ignores a blank issue or view project id", () => {
    expect(folderForSelection(selection({ issueProjectId: "  ", viewProjectId: "" }))).toEqual({
      folder: null,
      reason: "no-folder",
    });
  });
});

describe("workspaceForFolder", () => {
  const workspaces: WorkspaceLike[] = [
    { workspaceId: "w-1", path: "/srv/demo/", sessionIds: ["s-1"] },
    { workspaceId: "w-2", path: "/srv/other", sessionIds: [] },
  ];

  it("matches by normalized path", () => {
    expect(workspaceForFolder(workspaces, "/srv//demo")?.workspaceId).toBe("w-1");
  });

  it("matches nothing when no path is equal", () => {
    expect(workspaceForFolder(workspaces, "/srv/demos")).toBeNull();
  });
});

describe("boundSessionId", () => {
  it("keeps the on-screen Session when it belongs to the Workspace", () => {
    expect(boundSessionId("s-1", { sessionIds: ["s-1", "s-2"] })).toBe("s-1");
  });

  it("rebinds to the Workspace's first Session otherwise", () => {
    expect(boundSessionId("s-9", { sessionIds: ["s-1", "s-2"] })).toBe("s-1");
  });

  it("returns null when the Workspace has no Session (caller creates one)", () => {
    expect(boundSessionId(undefined, { sessionIds: [] })).toBeNull();
  });
});

describe("planSidebarToggle", () => {
  it("retargets the hosted column when it draws another Workspace's Session", () => {
    // MMX owns the main column, so nothing selects a Session: the press names
    // the project's own Session and keeps the board where it is.
    expect(
      planSidebarToggle({
        hasHostPanel: true,
        canNameSession: true,
        mountedSessionId: "s-other",
        boundSessionId: "s-1",
      }),
    ).toEqual({ kind: "retarget", sessionId: "s-1" });
  });

  it("flips the hosted column when it already draws the bound Session", () => {
    expect(
      planSidebarToggle({
        hasHostPanel: true,
        canNameSession: true,
        mountedSessionId: "s-1",
        boundSessionId: "s-1",
      }),
    ).toEqual({ kind: "toggle" });
  });

  it("retargets to a Session it will have to create when the Workspace has none", () => {
    expect(
      planSidebarToggle({
        hasHostPanel: true,
        canNameSession: true,
        mountedSessionId: "s-other",
        boundSessionId: null,
      }),
    ).toEqual({ kind: "retarget", sessionId: null });
  });

  it("flips a hosted column it cannot retarget, rather than leaving MMX", () => {
    // A card C build before naming shipped: the column has no way to move, and
    // revealing the Session would replace the board with a Conversation.
    expect(
      planSidebarToggle({
        hasHostPanel: true,
        canNameSession: false,
        mountedSessionId: "s-other",
        boundSessionId: "s-1",
      }),
    ).toEqual({ kind: "toggle" });
  });

  it("never plans a navigation on a host that cannot host the panel", () => {
    // M-316: no `hostPanel` means the only place the Sidebar can be drawn is over
    // an on-screen Session, which replaces MMX with the dsh page. Two shapes of
    // that host — nothing on screen, and somebody else's Session — both refuse.
    for (const mountedSessionId of [undefined, "s-9", "s-1"]) {
      expect(
        planSidebarToggle({
          hasHostPanel: false,
          canNameSession: false,
          mountedSessionId,
          boundSessionId: "s-1",
        }),
      ).toEqual({ kind: "unavailable" });
    }
  });

  it("never plans a navigation on a naming-less host either", () => {
    expect(
      planSidebarToggle({
        hasHostPanel: false,
        canNameSession: true,
        mountedSessionId: undefined,
        boundSessionId: null,
      }),
    ).toEqual({ kind: "unavailable" });
  });
});

describe("toggleState", () => {
  it("is enabled on an issue page whose picker says All projects", () => {
    // The Owner's own flow (M-316): the picker has no project, the issue names
    // one, and the press must work on that project's folder.
    const resolved = folderForSelection(
      selection({
        issueProjectId: "p3",
        resourceFor: foldersFrom({ p3: "/srv/issue" }),
      }),
    );
    expect(
      toggleState({ folder: resolved.folder, reason: resolved.reason, canHostPanel: true }),
    ).toEqual({ disabled: false, title: "Open the dsh side panel on /srv/issue" });
  });

  it("names the host's limit first: no view can fix a build without hostPanel", () => {
    expect(
      toggleState({ folder: "/srv/demo", reason: "project", canHostPanel: false }),
    ).toEqual({ disabled: true, title: HOST_NO_BESIDE_PANEL_TOOLTIP });
    expect(
      toggleState({ folder: null, reason: "no-folder", canHostPanel: false }).title,
    ).toBe("This dsh build cannot show the side panel next to MMX");
  });

  it("is disabled with the folder-side copy when no folder resolves", () => {
    expect(toggleState({ folder: null, reason: "no-folder", canHostPanel: true })).toEqual({
      disabled: true,
      title: "Choose a project",
    });
  });

  it("shows the pressed state while a press is in flight", () => {
    expect(
      toggleState({
        folder: "/srv/demo",
        reason: "project",
        canHostPanel: true,
        busy: true,
        expanded: true,
      }).title,
    ).toBe("Close the dsh side panel");
  });
});

describe("panelToggleTitle", () => {
  it("names the fold it opens and the close it performs", () => {
    expect(panelToggleTitle(false, "/srv/demo")).toBe("Open the dsh side panel on /srv/demo");
    expect(panelToggleTitle(true, "/srv/demo")).toBe("Close the dsh side panel");
  });
});

describe("noSessionSurfaceError", () => {
  it("raises the same failure the dsh face raises", () => {
    expect(isNoSessionSurfaceError(noSessionSurfaceError())).toBe(true);
  });
});

describe("sessionSurfaceMounted", () => {
  it("is mounted only when the snapshot carries a Session", () => {
    expect(sessionSurfaceMounted({ mounted: { getSnapshot: () => "s-1" } })).toBe(true);
    expect(sessionSurfaceMounted({ mounted: { getSnapshot: () => undefined } })).toBe(false);
  });

  it("answers false when there is no sidebar at all", () => {
    expect(sessionSurfaceMounted(null)).toBe(false);
    expect(sessionSurfaceMounted(undefined)).toBe(false);
  });
});

describe("isNoSessionSurfaceError", () => {
  it("matches the dsh face's no-session write rejection", () => {
    expect(
      isNoSessionSurfaceError(new Error("sidebarRight: no session surface is mounted")),
    ).toBe(true);
    expect(isNoSessionSurfaceError(new Error("openTab failed: boom"))).toBe(false);
    expect(isNoSessionSurfaceError("not an error")).toBe(false);
  });
});

describe("describe", () => {
  it("explains no-session for a folder the reader follows into dsh", () => {
    expect(describeReason("no-session")).toBe(NO_SESSION_SURFACE_NOTE);
  });

  it("names the host's limit when the panel cannot be hosted beside MMX", () => {
    expect(describeReason("host")).toBe(HOST_NO_BESIDE_PANEL_TOOLTIP);
  });
});

describe("normalizePath", () => {
  it("collapses duplicate separators and a trailing slash", () => {
    expect(normalizePath("/srv//demo/")).toBe("/srv/demo");
    expect(normalizePath(" /srv/demo ")).toBe("/srv/demo");
    expect(normalizePath("")).toBe("");
  });
});
