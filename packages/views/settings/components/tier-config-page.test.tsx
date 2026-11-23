// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

// The page is a host: it must render the one existing tier component, not a
// second copy of it (M-545).
vi.mock("./tiers-tab", () => ({
  TiersTab: () => <div data-testid="tiers-tab" />,
}));

import { TierConfigPage } from "./tier-config-page";

describe("TierConfigPage", () => {
  it("renders the existing tier config in the Team main panel", () => {
    render(<TierConfigPage />);
    expect(screen.getByTestId("tiers-tab")).toBeInTheDocument();
  });
});
