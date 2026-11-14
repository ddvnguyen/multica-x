import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import { useIssueOpeningStore } from "@multica/core/issues/stores/issue-opening-store";
import {
  IssueOpeningCapabilityProvider,
  useIssueOpeningCapability,
} from "./issue-opening-capability";
import { useIssuePeekClick } from "./peek-context";

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    push: vi.fn(),
    replace: vi.fn(),
    openInNewTab: undefined,
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
function ClickProbe({ onOpened }: { onOpened: (issueId: string) => boolean }) {
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

  it("leaves the click alone when the host cannot open the column", () => {
    const open = vi.fn(() => false);
    render(<ClickProbe onOpened={open} />);
    const link = screen.getByTestId("issue-link");
    const event = fireEvent.click(link);
    expect(open).toHaveBeenCalledWith("issue-1");
    // Unhandled: the press keeps whatever meaning the host gives it.
    expect(event).toBe(true);
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
