import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { useIssueOpeningStore } from "@multica/core/issues/stores/issue-opening-store";
import {
  defaultStorage,
  setDefaultStorage,
} from "@multica/core/platform";
import type { StorageAdapter } from "@multica/core/types/storage";
import {
  IssueOpeningCapabilityProvider,
  useIssueOpeningCapability,
  type SidePreviewOpenTarget,
} from "./issue-opening-capability";
import { IssuePeekActionsContext, useIssuePeekClick } from "./peek-context";

const nav = vi.hoisted(() => ({
  openInNewTab: undefined as ((path: string, issueId: string, opts?: { activate?: boolean }) => boolean) | undefined,
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    push: vi.fn(),
    replace: vi.fn(),
    openInNewTab: nav.openInNewTab,
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));

function Probe() {
  const capability = useIssueOpeningCapability();
  return (
    <div
      data-testid="capability"
      data-open-mode={capability.openMode}
      data-supports-tabs={capability.supportsTabs ? "yes" : "no"}
      data-supports-peek={capability.supportsPeek ? "yes" : "no"}
      data-supports-page={capability.supportsPage ? "yes" : "no"}
      data-side-preview={capability.openSidePreview === null ? "none" : "host"}
    />
  );
}

function renderDefaultProvider() {
  return render(
    <IssueOpeningCapabilityProvider>
      <Probe />
    </IssueOpeningCapabilityProvider>,
  );
}

/** A card-shaped target: the click handler the views give every issue link. */
function ClickProbe({ onOpened }: { onOpened: (target: SidePreviewOpenTarget) => boolean }) {
  return (
    <IssueOpeningCapabilityProvider supportsTabs supportsPeek openSidePreview={onOpened}>
      <IssueLink />
    </IssueOpeningCapabilityProvider>
  );
}

/** Inside the provider on purpose: the hook reads the capability it is given. */
function IssueLink() {
  const click = useIssuePeekClick();
  return (
    <button data-testid="issue-link" onClick={(event) => click("issue-1", event)}>
      M-1
    </button>
  );
}

beforeEach(() => {
  useIssueOpeningStore.persist.clearStorage();
  useIssueOpeningStore.setState({ openMode: "page", hasUserChoice: false });
});

/**
 * The MMX wiring (M-336): the island shows both open modes and supplies the
 * host's own column as where a Side preview lands.
 */
describe("host-owned side preview", () => {
  beforeEach(() => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "peek", hasUserChoice: true });
    });
  });

  it("offers Side preview and exposes the host opener", () => {
    render(
      <IssueOpeningCapabilityProvider supportsTabs supportsPeek openSidePreview={() => true}>
        <Probe />
      </IssueOpeningCapabilityProvider>,
    );
    const probe = screen.getByTestId("capability");
    expect(probe).toHaveAttribute("data-open-mode", "peek");
    expect(probe).toHaveAttribute("data-supports-peek", "yes");
    expect(probe).toHaveAttribute("data-side-preview", "host");
  });

  it("sends a plain click to the host column instead of an in-view peek", () => {
    const open = vi.fn(() => true);
    render(<ClickProbe onOpened={open} />);
    const link = screen.getByTestId("issue-link");
    fireEvent.click(link);
    expect(open).toHaveBeenCalledWith("issue-1");
  });

  it("falls back to New tab when the host owns the column but cannot open it", () => {
    // Seam present, no folder: the island's opener declines, and the click must
    // not die silently (M-336) — it keeps its other meaning, a new tab.
    const open = vi.fn(() => false);
    const openInNewTab = vi.fn(() => true);
    nav.openInNewTab = openInNewTab;
    try {
      render(<ClickProbe onOpened={open} />);
      const link = screen.getByTestId("issue-link");
      fireEvent.click(link);
      expect(open).toHaveBeenCalledWith("issue-1");
      expect(openInNewTab).toHaveBeenCalledWith("/acme/issues/issue-1", "issue-1", { activate: true });
    } finally {
      nav.openInNewTab = undefined;
    }
  });

  it("routes the click to the in-view peek when the host has no opener and no peek host", () => {
    const open = vi.fn(() => false);
    render(
      <IssueOpeningCapabilityProvider supportsTabs supportsPeek>
        <IssueLink />
      </IssueOpeningCapabilityProvider>,
    );
    const link = screen.getByTestId("issue-link");
    const event = fireEvent.click(link);
    expect(open).not.toHaveBeenCalled();
    // No host column and no fork peek: nothing handles the press.
    expect(event).toBe(true);
  });
});

/**
 * The island on a dsh too old to publish the tab seam (M-330, M-336): both open
 * modes stay on offer and `Side preview` is the fork's floating peek, because
 * there is no host column to open it in.
 */
describe("host without a side column", () => {
  beforeEach(() => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "peek", hasUserChoice: true });
    });
  });

  it("keeps Side preview and exposes no host opener", () => {
    render(
      <IssueOpeningCapabilityProvider supportsTabs supportsPeek>
        <Probe />
      </IssueOpeningCapabilityProvider>,
    );
    const probe = screen.getByTestId("capability");
    expect(probe).toHaveAttribute("data-open-mode", "peek");
    expect(probe).toHaveAttribute("data-supports-peek", "yes");
    expect(probe).toHaveAttribute("data-side-preview", "none");
  });

  it("routes the click to the in-view peek", () => {
    const open = vi.fn();
    render(
      <IssuePeekActionsContext.Provider
        value={{ open, toggle: vi.fn(), close: vi.fn(), publishColumns: vi.fn() }}
      >
        <IssueOpeningCapabilityProvider supportsTabs supportsPeek>
          <IssueLink />
        </IssueOpeningCapabilityProvider>
      </IssuePeekActionsContext.Provider>,
    );
    fireEvent.click(screen.getByTestId("issue-link"));
    expect(open).toHaveBeenCalledWith("issue-1");
  });
});

/**
 * Regression test for M-331: web and desktop mount the provider with
 * defaults (no tab support, peek on), so the capability must pass the
 * persisted preference through. Before the fix the web/desktop trees had no
 * provider at all and fell back to the hardcoded `openMode: "page"`, which
 * silently ignored a saved Side preview choice.
 */
describe("default issue opening capability (web/desktop wiring)", () => {
  it("passes a saved Side preview choice through", () => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "peek", hasUserChoice: true });
    });
    renderDefaultProvider();
    const probe = screen.getByTestId("capability");
    expect(probe).toHaveAttribute("data-open-mode", "peek");
    expect(probe).toHaveAttribute("data-supports-peek", "yes");
    expect(probe).toHaveAttribute("data-supports-tabs", "no");
  });

  it("keeps Full page when that is the saved choice", () => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "page", hasUserChoice: true });
    });
    renderDefaultProvider();
    expect(screen.getByTestId("capability")).toHaveAttribute(
      "data-open-mode",
      "page",
    );
  });

  it("falls back to Full page for a legacy stored tab choice", () => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "tab", hasUserChoice: true });
    });
    renderDefaultProvider();
    expect(screen.getByTestId("capability")).toHaveAttribute(
      "data-open-mode",
      "page",
    );
  });

  it("does not migrate the stored choice without tab support", () => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "peek", hasUserChoice: true });
    });
    renderDefaultProvider();
    expect(useIssueOpeningStore.getState().openMode).toBe("peek");
  });
});

/**
 * The web dashboard (M-480): a tab strip *and* an in-place full page, so New
 * tab is a third choice beside Full page and Side preview. Full page stays the
 * default for anyone who never chose, and nothing rewrites a stored choice.
 */
describe("web dashboard (tabs and an in-place page)", () => {
  function renderWeb(children: ReactNode = <Probe />) {
    return render(
      <IssueOpeningCapabilityProvider supportsTabs supportsPage>
        {children}
      </IssueOpeningCapabilityProvider>,
    );
  }

  it("offers all three modes, with Full page still the default", () => {
    renderWeb();
    const probe = screen.getByTestId("capability");
    expect(probe).toHaveAttribute("data-supports-page", "yes");
    expect(probe).toHaveAttribute("data-supports-peek", "yes");
    expect(probe).toHaveAttribute("data-supports-tabs", "yes");
    expect(probe).toHaveAttribute("data-open-mode", "page");
  });

  it("passes each stored choice through, New tab included", () => {
    for (const mode of ["page", "peek", "tab"] as const) {
      act(() => {
        useIssueOpeningStore.setState({ openMode: mode, hasUserChoice: true });
      });
      const { unmount } = renderWeb();
      expect(screen.getByTestId("capability")).toHaveAttribute(
        "data-open-mode",
        mode,
      );
      unmount();
    }
  });

  it("leaves a stored Full page alone", () => {
    act(() => {
      useIssueOpeningStore.setState({ openMode: "page", hasUserChoice: true });
    });
    renderWeb();
    expect(useIssueOpeningStore.getState().openMode).toBe("page");
  });

  it("opens an activated header tab on a plain click in New tab mode", () => {
    const openInNewTab = vi.fn(() => true);
    nav.openInNewTab = openInNewTab;
    act(() => {
      useIssueOpeningStore.setState({ openMode: "tab", hasUserChoice: true });
    });
    try {
      renderWeb(<IssueLink />);
      expect(fireEvent.click(screen.getByTestId("issue-link"))).toBe(false);
      expect(openInNewTab).toHaveBeenCalledWith(
        "/acme/issues/issue-1",
        "issue-1",
        { activate: true },
      );
    } finally {
      nav.openInNewTab = undefined;
    }
  });

  it("opens a background tab on Cmd/Ctrl+Click in New tab mode", () => {
    const openInNewTab = vi.fn(() => true);
    nav.openInNewTab = openInNewTab;
    act(() => {
      useIssueOpeningStore.setState({ openMode: "tab", hasUserChoice: true });
    });
    try {
      renderWeb(<IssueLink />);
      const link = screen.getByTestId("issue-link");
      fireEvent.click(link, { metaKey: true });
      expect(openInNewTab).toHaveBeenLastCalledWith(
        "/acme/issues/issue-1",
        "issue-1",
        { activate: false },
      );
    } finally {
      nav.openInNewTab = undefined;
    }
  });

  it("keeps the plain link in Full page mode", () => {
    const openInNewTab = vi.fn(() => true);
    nav.openInNewTab = openInNewTab;
    act(() => {
      useIssueOpeningStore.setState({ openMode: "page", hasUserChoice: true });
    });
    try {
      renderWeb(<IssueLink />);
      // Not claimed: the card's own link navigates in place.
      expect(fireEvent.click(screen.getByTestId("issue-link"))).toBe(true);
      expect(openInNewTab).not.toHaveBeenCalled();
    } finally {
      nav.openInNewTab = undefined;
    }
  });

  it("opens the side preview on Shift+Click from New tab", () => {
    const toggle = vi.fn();
    act(() => {
      useIssueOpeningStore.setState({ openMode: "tab", hasUserChoice: true });
    });
    render(
      <IssuePeekActionsContext.Provider
        value={{ open: vi.fn(), toggle, close: vi.fn(), publishColumns: vi.fn() }}
      >
        <IssueOpeningCapabilityProvider supportsTabs supportsPage>
          <IssueLink />
        </IssueOpeningCapabilityProvider>
      </IssuePeekActionsContext.Provider>,
    );
    expect(fireEvent.click(screen.getByTestId("issue-link"), { shiftKey: true })).toBe(false);
    expect(toggle).toHaveBeenCalledWith("issue-1");
  });
});

/**
 * Regression test for M-336's peek clobber: on the dsh island, CoreProvider
 * installs the `multica.dsh.*` adapter after this module's persist hydration
 * has already read (and missed) the bare origin. Before the fix, the
 * capability's migration effect then saw the defaults and wrote "tab" over a
 * Side preview choice it never read — the choice died on every reload.
 */
describe("late host adapter (the island's boot order)", () => {
  const ISLAND_PREFIX = "multica.dsh.";

  /** What the island's CoreProvider installs: a prefixed view of the origin. */
  function islandAdapter(): StorageAdapter {
    return {
      getItem: (key) => localStorage.getItem(ISLAND_PREFIX + key),
      setItem: (key, value) => {
        localStorage.setItem(ISLAND_PREFIX + key, value);
      },
      removeItem: (key) => {
        localStorage.removeItem(ISLAND_PREFIX + key);
      },
    };
  }

  afterEach(async () => {
    setDefaultStorage(defaultStorage);
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    localStorage.clear();
  });

  it("keeps a stored Side preview choice the migration never read", async () => {
    localStorage.setItem(
      ISLAND_PREFIX + "multica_issue_opening",
      JSON.stringify({ state: { openMode: "peek" }, version: 0 }),
    );
    // CoreProvider's install; the store re-reads on the deferred notification.
    await act(async () => {
      setDefaultStorage(islandAdapter());
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(useIssueOpeningStore.getState()).toMatchObject({
      openMode: "peek",
      hasUserChoice: true,
    });

    render(
      <IssueOpeningCapabilityProvider supportsTabs supportsPeek openSidePreview={() => true}>
        <Probe />
      </IssueOpeningCapabilityProvider>,
    );
    expect(screen.getByTestId("capability")).toHaveAttribute(
      "data-open-mode",
      "peek",
    );
    // The migration effect ran and did not rewrite what it now has read.
    expect(
      JSON.parse(localStorage.getItem(ISLAND_PREFIX + "multica_issue_opening")!)
        .state,
    ).toEqual({ openMode: "peek" });
  });
});
