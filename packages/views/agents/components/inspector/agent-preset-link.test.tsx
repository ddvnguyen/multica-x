// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import enSettings from "../../../locales/en/settings.json";
import { AgentPresetLink } from "./agent-preset-link";

vi.mock("@multica/core/api", () => ({
  api: { getWorkspace: vi.fn(), listAgentPresets: vi.fn() },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ settings: () => "/ws/settings" }),
}));
vi.mock("../../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href}>{children}</a>
  ),
}));

const preset = {
  id: "preset-1",
  workspace_id: "ws-1",
  name: "Architect Sonnet",
  agent_id: "agent-1",
  agent_name: "Architect",
  tier_id: "tier-1",
  tier_name: "Sonnet High",
  tier_enabled: true,
};

function renderLink() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider locale="en" resources={{ en: { settings: enSettings } }}>
        <AgentPresetLink agentId="agent-1" />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

describe("AgentPresetLink", () => {
  beforeEach(() => {
    vi.mocked(api.getWorkspace).mockResolvedValue({
      settings: { agent_presets_enabled: true },
    } as never);
    vi.mocked(api.listAgentPresets).mockResolvedValue([preset]);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("names the presets bound to this agent and links to Settings → Tiers", async () => {
    renderLink();
    expect(
      await screen.findByText(/Presets using this agent/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Architect Sonnet/)).toBeInTheDocument();
    const link = screen.getByRole("link", { name: /Manage in Settings/ });
    expect(link).toHaveAttribute("href", "/ws/settings?tab=tiers");
  });

  it("ignores presets bound to other agents", async () => {
    vi.mocked(api.listAgentPresets).mockResolvedValue([
      { ...preset, id: "other", agent_id: "agent-2", name: "Other" },
    ]);
    renderLink();
    expect(
      await screen.findByText(/No presets for this agent/),
    ).toBeInTheDocument();
  });
});
