import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { configureHostShell, resetHostShell } from "../src/client/shell-runtime";
import type { SidebarRightLike } from "../src/client/shell-link";

// The folder chain reads the view store and a projects query; the wiring under
// test only needs an answer, and the opener's folder handling has its own suite.
vi.mock("../src/client/sidebar-folder", () => ({
  useSidebarFolder: () => ({ folder: "/work/acme", reason: "host" }),
}));

// What the island hands the fork is the branch under test (M-336): capture the
// provider's inputs instead of re-deriving them from a capability probe.
interface PassedProps {
  supportsTabs: boolean;
  supportsPeek: boolean;
  openSidePreview: unknown;
}
const passed: PassedProps[] = [];
vi.mock("../src/fork", () => ({
  IssueOpeningCapabilityProvider: (props: PassedProps & { children: ReactNode }) => {
    passed.push({
      supportsTabs: props.supportsTabs,
      supportsPeek: props.supportsPeek,
      openSidePreview: props.openSidePreview,
    });
    return props.children;
  },
}));

const { SidePreviewCapability } = await import("../src/client/side-preview-opener");

/** A shell face with only what the preview asks for. */
function sidebarRight(): SidebarRightLike {
  return {
    mounted: { getSnapshot: () => undefined, subscribe: () => () => {} },
    toggleExpanded: () => {},
    isExpanded: () => false,
    hostPanel: () => () => {},
    selectHostedSession: () => () => {},
    openTab: () => {},
  };
}

const input = {
  workspaceId: "ws-1",
  projects: undefined,
  projectId: null,
  workspaceFolder: "",
  route: { view: "board" } as const,
  daemonId: "",
};

function mount() {
  render(
    <SidePreviewCapability input={input}>
      <div data-child="" />
    </SidePreviewCapability>,
  );
  expect(passed).toHaveLength(1);
  return passed[0]!;
}

beforeEach(() => {
  passed.length = 0;
});

afterEach(() => {
  resetHostShell();
});

describe("island side preview capability", () => {
  it("opens the host's column when dsh publishes the tab seam", () => {
    configureHostShell({
      sidebarRight: sidebarRight(),
      workspaces: null,
      sessions: null,
      panelInfo: null,
      daemonId: "",
      sidePreviewKind: "multica-issue",
    });

    const props = mount();
    expect(props.supportsTabs).toBe(true);
    // Both open modes stay on offer (M-330); the opener is what sends a press
    // to the column, and is also what keeps the floating panel down.
    expect(props.supportsPeek).toBe(true);
    expect(typeof props.openSidePreview).toBe("function");
  });

  it("falls back to the floating peek on a shell without the seam", () => {
    configureHostShell({
      sidebarRight: sidebarRight(),
      workspaces: null,
      sessions: null,
      panelInfo: null,
      daemonId: "",
      sidePreviewKind: null,
    });

    const props = mount();
    expect(props.supportsTabs).toBe(true);
    expect(props.supportsPeek).toBe(true);
    // No opener: the click keeps its peek meaning and `IssuePeekHost` mounts.
    expect(props.openSidePreview).toBeNull();
  });

  it("falls back the same way before any shell bridge is installed", () => {
    const props = mount();
    expect(props.supportsPeek).toBe(true);
    expect(props.openSidePreview).toBeNull();
  });
});
