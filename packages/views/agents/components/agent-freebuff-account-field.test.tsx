// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach } from "vitest";
import userEvent from "@testing-library/user-event";
import { render, cleanup, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { FreebuffAccount } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";

const getFreebuffAccounts = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (orig) => ({
  ...(await orig<typeof import("@multica/core/api")>()),
  api: { getFreebuffAccounts },
}));

import { AgentFreebuffAccountField } from "./agent-freebuff-account-field";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const account = (over: Partial<FreebuffAccount>): FreebuffAccount => ({
  id: "acc-1",
  state: "connected",
  label: "Team A",
  ...over,
});

const accounts = (list: FreebuffAccount[]) => ({
  accounts: list,
  default_account_id: null,
});

function renderField(
  props: Partial<React.ComponentProps<typeof AgentFreebuffAccountField>> = {},
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentFreebuffAccountField
          runtimeProvider="freebuff"
          runtimeId="rt-1"
          value=""
          onChange={vi.fn()}
          {...props}
        />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function openDropdown() {
  const trigger = screen
    .getAllByRole("combobox")
    .at(-1) as HTMLElement | undefined;
  if (!trigger) throw new Error("freebuff account trigger not rendered");
  fireEvent.click(trigger);
}

afterEach(() => {
  cleanup();
  getFreebuffAccounts.mockReset();
});

describe("AgentFreebuffAccountField", () => {
  it("is absent on non-freebuff runtimes, without fetching", () => {
    getFreebuffAccounts.mockResolvedValue(accounts([]));
    renderField({ runtimeProvider: "freebuff-desktop" });
    expect(screen.queryByText("Account")).toBeNull();
    expect(getFreebuffAccounts).not.toHaveBeenCalled();
  });

  it("is absent while the runtime has no connected account to offer", async () => {
    getFreebuffAccounts.mockResolvedValue(
      accounts([account({ state: "awaiting_login" })]),
    );
    renderField();
    await vi.waitFor(() => expect(getFreebuffAccounts).toHaveBeenCalled());
    expect(screen.queryByText("Account")).toBeNull();
  });

  it("appears once an account is connected and shows the runtime default", async () => {
    getFreebuffAccounts.mockResolvedValue(accounts([account({})]));
    renderField();
    expect(await screen.findByText("Account")).toBeTruthy();
    expect(screen.getByText("Runtime default")).toBeTruthy();

    // Options render only once the trigger is opened.
    openDropdown();
    expect(await screen.findByText("Team A")).toBeTruthy();
  });

  it("choosing an account calls onChange with its id", async () => {
    const onChange = vi.fn();
    getFreebuffAccounts.mockResolvedValue(
      accounts([account({}), account({ id: "acc-2", label: "Team B" })]),
    );
    renderField({ onChange });
    await screen.findByText("Account");
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByText("Team B"));
    expect(onChange).toHaveBeenCalledWith("acc-2");
  });

  it("choosing the runtime default calls onChange with an empty id", async () => {
    const onChange = vi.fn();
    getFreebuffAccounts.mockResolvedValue(accounts([account({})]));
    renderField({ value: "acc-1", onChange });
    await screen.findByText("Account");
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByText("Runtime default"));
    expect(onChange).toHaveBeenCalledWith("");
  });

  it("still names the bound account after it stopped being connected", async () => {
    getFreebuffAccounts.mockResolvedValue(accounts([account({ state: "signed_out" })]));
    renderField({ value: "acc-1" });
    expect(await screen.findByText("Team A")).toBeTruthy();
  });

  it("falls back to the raw id when the bound account vanished entirely", async () => {
    getFreebuffAccounts.mockResolvedValue(accounts([]));
    renderField({ value: "ghost" });
    expect(await screen.findByText("ghost")).toBeTruthy();
  });
});
