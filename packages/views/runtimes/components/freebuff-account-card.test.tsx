// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { FreebuffAccountCard } from "./freebuff-account-card";

const mockGetFreebuffAccount = vi.hoisted(() => vi.fn());
const mockRequestFreebuffConnect = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getFreebuffAccount: (...args: unknown[]) =>
      mockGetFreebuffAccount(...args),
    requestFreebuffConnect: (...args: unknown[]) =>
      mockRequestFreebuffConnect(...args),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-freebuff",
    name: "Freebuff (ddv-server)",
    provider: "freebuff",
    status: "online",
    metadata: {},
    ...overrides,
  } as AgentRuntime;
}

function renderCard(runtime: AgentRuntime) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <FreebuffAccountCard runtime={runtime} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockGetFreebuffAccount.mockResolvedValue({
    status: {
      status: "active",
      model: "z-ai/glm-5.3-flash",
      admittedAt: "2026-10-21T10:00:00.000Z",
      expiresAt: "2026-10-21T11:00:00.000Z",
      remainingMs: 42 * 60_000,
      credit: {
        balance: 21,
        dailyLimit: 5,
        dailySpent: 1,
        dailyRemaining: 4,
        dailyResetAt: "2026-10-22T00:00:00.000Z",
        walletBalance: 17,
      },
      prices: { "z-ai/glm-5.3-flash": 2 },
    },
    connect: { state: "connected", account: "owner@example.com" },
  });
  mockRequestFreebuffConnect.mockResolvedValue({ state: "requested" });
});

describe("FreebuffAccountCard", () => {
  it("shows the session time, the credit and the price per model", async () => {
    const { container } = renderCard(makeRuntime());

    await waitFor(() => {
      expect(screen.getByText("Freebuff account")).toBeInTheDocument();
    });
    // The card composes each line from several translated fragments, so the
    // assertions look at the rendered text rather than at one text node.
    await waitFor(() => {
      expect(container.textContent).toContain("active");
    });
    expect(container.textContent).toContain("42m left");
    expect(container.textContent).toContain("4 of 5 Freebucks left today");
    expect(container.textContent).toContain("21 Freebucks in total");
    expect(screen.getByText("z-ai/glm-5.3-flash")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("shows the connected account", async () => {
    renderCard(makeRuntime());
    await waitFor(() => {
      expect(screen.getByText(/Connected as owner@example.com/)).toBeInTheDocument();
    });
  });

  it("renders nothing for a runtime that is not a Freebuff runtime", async () => {
    const { container } = renderCard(
      makeRuntime({ id: "rt-other", provider: "codebuddy" }),
    );
    await waitFor(() => {
      expect(mockGetFreebuffAccount).not.toHaveBeenCalled();
    });
    expect(container).toBeEmptyDOMElement();
  });

  it("starts a browser login when the connect button is pressed", async () => {
    mockGetFreebuffAccount.mockResolvedValue({ connect: { state: "" } });
    renderCard(makeRuntime());

    const button = await screen.findByRole("button", {
      name: "Connect to Freebuff",
    });
    await userEvent.click(button);

    await waitFor(() => {
      expect(mockRequestFreebuffConnect).toHaveBeenCalledWith("rt-freebuff");
    });
  });
});
