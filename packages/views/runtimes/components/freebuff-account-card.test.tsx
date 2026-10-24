// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AgentRuntime } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { FreebuffAccountCard } from "./freebuff-account-card";

const mockGetFreebuffAccounts = vi.hoisted(() => vi.fn());
const mockAddFreebuffAccount = vi.hoisted(() => vi.fn());
const mockRemoveFreebuffAccount = vi.hoisted(() => vi.fn());
const mockRetryFreebuffAccountLogin = vi.hoisted(() => vi.fn());
const mockSetFreebuffDefaultAccount = vi.hoisted(() => vi.fn());
const mockCopyText = vi.hoisted(() => vi.fn());
const mockOpenExternal = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getFreebuffAccounts: (...args: unknown[]) => mockGetFreebuffAccounts(...args),
    addFreebuffAccount: (...args: unknown[]) => mockAddFreebuffAccount(...args),
    removeFreebuffAccount: (...args: unknown[]) => mockRemoveFreebuffAccount(...args),
    retryFreebuffAccountLogin: (...args: unknown[]) =>
      mockRetryFreebuffAccountLogin(...args),
    setFreebuffDefaultAccount: (...args: unknown[]) =>
      mockSetFreebuffDefaultAccount(...args),
  },
  ApiError: class ApiError extends Error {},
}));

vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: (...args: unknown[]) => mockCopyText(...args),
}));

vi.mock("../../platform", () => ({
  openExternal: (...args: unknown[]) => mockOpenExternal(...args),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

const LINK = "https://freebuff.com/login?auth_code=abc";

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

const connected = {
  id: "a1",
  label: "owner@example.com",
  state: "connected",
  status: {
    status: "active",
    model: "z-ai/glm-5.3-flash",
    remainingMs: 42 * 60_000,
    credit: { balance: 21, dailyLimit: 5, dailyRemaining: 4, walletBalance: 17 },
    prices: { "z-ai/glm-5.3-flash": 2 },
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  mockGetFreebuffAccounts.mockResolvedValue({
    accounts: [connected],
    default_account_id: "a1",
  });
  mockAddFreebuffAccount.mockResolvedValue({ id: "a2", state: "requested" });
  mockRemoveFreebuffAccount.mockResolvedValue(undefined);
  mockRetryFreebuffAccountLogin.mockResolvedValue(undefined);
  mockSetFreebuffDefaultAccount.mockResolvedValue({ accounts: [] });
  mockCopyText.mockResolvedValue(true);
});

describe("FreebuffAccountCard", () => {
  it("shows a connected account with its session, credit and prices", async () => {
    const { container } = renderCard(makeRuntime());

    expect(await screen.findByText("owner@example.com")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
    // Each line is composed from several translated fragments, so the
    // assertions look at the rendered text rather than at one text node.
    expect(container.textContent).toContain("active");
    expect(container.textContent).toContain("42m left");
    expect(container.textContent).toContain("4 of 5 Freebucks left today");
    expect(container.textContent).toContain("21 Freebucks in total");
    expect(screen.getByText("z-ai/glm-5.3-flash")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("lists each model with what an hour costs and how long today's credit lasts", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({
      accounts: [
        {
          ...connected,
          status: {
            credit: { balance: 25, dailyLimit: 25, dailyRemaining: 25 },
            models: [
              { id: "m-glm", name: "GLM 5.3 Flash", price: 20 },
              { id: "m-solar", name: "Solar Mini 4", price: 0, notes: ["Promotional"] },
              { id: "m-slow", name: "MiMo v2.6 Flash Slow", price: 5, recommended: true },
              { id: "m-haiku", name: "Claude Haiku 5.5", price: 100, locked: true },
              { id: "m-new", name: "Brand New Model", price: null },
            ],
          },
        },
      ],
      default_account_id: "a1",
    });
    const { container } = renderCard(makeRuntime());

    expect(await screen.findByText("Models and cost")).toBeInTheDocument();
    const text = container.textContent ?? "";
    expect(text).toContain("20 Freebucks/h");
    expect(text).toContain("about 1.3 h left today");
    expect(text).toContain("5 Freebucks/h");
    expect(text).toContain("about 5 h left today");
    expect(text).toContain("Free");
    expect(text).toContain("Promotional");
    expect(text).toContain("Recommended");
    expect(text).toContain("Needs a paid plan");
    expect(text).toContain("Price not published");
    // Cheapest first, locked models last.
    expect(text.indexOf("Solar Mini 4")).toBeLessThan(text.indexOf("GLM 5.3 Flash"));
    expect(text.indexOf("GLM 5.3 Flash")).toBeLessThan(text.indexOf("Claude Haiku 5.5"));
  });

  it("renders nothing for a runtime that is not a Freebuff runtime", async () => {
    const { container } = renderCard(
      makeRuntime({ id: "rt-other", provider: "codebuddy" }),
    );
    await waitFor(() => {
      expect(mockGetFreebuffAccounts).not.toHaveBeenCalled();
    });
    expect(container).toBeEmptyDOMElement();
  });

  it("invites the user to add the first account", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({ accounts: [] });
    renderCard(makeRuntime());
    expect(await screen.findByText(/No Freebuff account yet/)).toBeInTheDocument();
  });

  it("adds an account when the button is pressed", async () => {
    renderCard(makeRuntime());
    await userEvent.click(await screen.findByRole("button", { name: "Add account" }));
    await waitFor(() => {
      expect(mockAddFreebuffAccount).toHaveBeenCalledWith("rt-freebuff");
    });
  });

  it("shows the sign-in link to copy or open while the sign-in is pending", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({
      accounts: [
        {
          id: "a2",
          state: "awaiting_login",
          login_url: LINK,
          expires_at: "2026-10-24T12:00:00Z",
        },
      ],
    });
    renderCard(makeRuntime());

    const field = await screen.findByLabelText("Sign-in link");
    expect(field).toHaveValue(LINK);

    await userEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(mockCopyText).toHaveBeenCalledWith(LINK);

    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(mockOpenExternal).toHaveBeenCalledWith(LINK, { webTarget: "new-tab" });
  });

  it("removes an account only after a second press", async () => {
    renderCard(makeRuntime());
    await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
    expect(mockRemoveFreebuffAccount).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Confirm remove" }));
    await waitFor(() => {
      expect(mockRemoveFreebuffAccount).toHaveBeenCalledWith("rt-freebuff", "a1");
    });
  });

  it("makes another connected account the default", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({
      accounts: [connected, { id: "a2", label: "two@example.com", state: "connected" }],
      default_account_id: "a1",
    });
    renderCard(makeRuntime());

    await screen.findByText("two@example.com");
    await userEvent.click(screen.getByRole("button", { name: "Make default" }));
    await waitFor(() => {
      expect(mockSetFreebuffDefaultAccount).toHaveBeenCalledWith("rt-freebuff", "a2");
    });
  });

  it("offers a new link for a signed-out account", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({
      accounts: [{ id: "a1", label: "owner@example.com", state: "signed_out" }],
    });
    renderCard(makeRuntime());

    await userEvent.click(await screen.findByRole("button", { name: "Sign in again" }));
    await waitFor(() => {
      expect(mockRetryFreebuffAccountLogin).toHaveBeenCalledWith("rt-freebuff", "a1");
    });
  });

  it("explains a failed sign-in", async () => {
    mockGetFreebuffAccounts.mockResolvedValue({
      accounts: [{ id: "a1", state: "failed", error: "bun not found" }],
    });
    renderCard(makeRuntime());
    expect(await screen.findByText("bun not found")).toBeInTheDocument();
  });
});
