import { screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";
import { DashboardLayout } from "./dashboard-layout";

// The shell is a frame: what matters here is where the header slot lands, not
// the workspace machinery around it.
vi.mock("./dashboard-guard", () => ({
  DashboardGuard: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../common/appearance-sync", () => ({ AppearanceSync: () => null }));
vi.mock("../modals/registry", () => ({ ModalRegistry: () => null }));
vi.mock("../onboarding", () => ({ SourceBackfillModal: () => null }));
vi.mock("../billing/trial-notices", () => ({ WorkspaceTrialNotices: () => null }));
vi.mock("./app-shell-context", () => ({
  AppShellProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("./app-rail", () => ({ AppRail: () => <nav data-testid="rail" /> }));
vi.mock("./bottom-bar", () => ({ AppBottomBar: () => <footer data-testid="bottom-bar" /> }));
vi.mock("./compact-nav-sheet", () => ({ CompactNavSheet: () => null }));
vi.mock("./navigation-progress", () => ({ NavigationProgress: () => null }));
vi.mock("./route-data-prefetch", () => ({
  ShellRouteDataPrefetch: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("./space-panel", () => ({ SpacePanel: () => <aside data-testid="space-panel" /> }));
vi.mock("./workspace-presence-prefetch", () => ({ WorkspacePresencePrefetch: () => null }));
vi.mock("./global-shortcuts", () => ({ GlobalShortcuts: () => null }));
vi.mock("@multica/ui/components/ui/sidebar", () => ({
  SidebarProvider: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

describe("DashboardLayout", () => {
  it("renders the header above the content card, spanning the section panel", () => {
    renderWithI18n(
      <DashboardLayout header={<div data-testid="header" />}>
        <div data-testid="page" />
      </DashboardLayout>,
    );

    const header = screen.getByTestId("header");
    const card = document.querySelector("[data-mc='page']")!;
    const panel = screen.getByTestId("space-panel");

    // The card holds the section panel and the page; the header is outside it,
    // so it crosses the panel's column instead of starting at the page.
    expect(card.contains(panel)).toBe(true);
    expect(card.contains(header)).toBe(false);
    expect(card.parentElement).toBe(header.parentElement);
    expect(
      header.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("renders no header for a host that does not pass one", () => {
    renderWithI18n(
      <DashboardLayout>
        <div data-testid="page" />
      </DashboardLayout>,
    );

    expect(screen.queryByTestId("header")).not.toBeInTheDocument();
    expect(screen.getByTestId("page")).toBeInTheDocument();
  });
});
