import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { WebTabProvider, WebTabStrip } from "./web-tab-shell";

vi.mock("@multica/views/tabs", () => ({
  TabBar: ({ compact }: { compact?: boolean }) => (
    <div data-testid="tab-bar" data-compact={compact ? "true" : "false"} />
  ),
  TabHistoryButtons: () => <div data-testid="tab-history" />,
  ChatTabsInHeaderProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("@/platform/web-tab-navigation", () => ({
  WebTabNavigationProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  WebTabSync: () => null,
  useWebTabProjector: () => () => {},
}));

describe("WebTabStrip", () => {
  it("puts Back/Forward before the compact tab strip, in the header slot", () => {
    render(<WebTabStrip />);

    const strip = document.querySelector('[data-slot="web-tab-strip"]')!;
    const history = screen.getByTestId("tab-history");
    const tabs = screen.getByTestId("tab-bar");

    expect(strip.contains(history)).toBe(true);
    expect(strip.contains(tabs)).toBe(true);
    expect(
      history.compareDocumentPosition(tabs) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    // Same thin strip the shell already shipped: 28px and compact tabs.
    expect(strip.className).toContain("h-7");
    expect(tabs).toHaveAttribute("data-compact", "true");
  });

  it("wraps the dashboard in the tab adapter and the header chat placement", () => {
    render(
      <WebTabProvider>
        <div data-testid="dashboard" />
      </WebTabProvider>,
    );

    expect(screen.getByTestId("dashboard")).toBeInTheDocument();
  });
});
