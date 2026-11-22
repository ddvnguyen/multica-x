// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { configStore } from "@multica/core/config";
import { EMPTY_AGENT_DRAFT } from "@multica/core/agents";
import { renderWithI18n } from "../../test/i18n";
import { AgentConfigurationPanel } from "./agent-configuration-panel";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  // The flag probe fails closed on no data: flag on = published frontend
  // flag, flag off = probe returns nothing. Tier-role options render empty.
  useQuery: () => ({ data: undefined }),
}));

const formProps = () => ({
  draft: { ...EMPTY_AGENT_DRAFT },
  onChange: vi.fn(),
  runtimes: [],
  runtimesLoading: false,
  members: [],
  currentUserId: "user-1",
  nameError: null,
  onNameChange: vi.fn(),
});

function mountPanel(props: ReturnType<typeof formProps>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <AgentConfigurationPanel {...props} />
    </QueryClientProvider>,
  );
}

describe("AgentConfigurationPanel role type (M-499)", () => {
  beforeEach(() => {
    act(() => {
      configStore.getState().setFeatureFlags({ agent_role_type: true });
    });
  });

  afterEach(() => {
    cleanup();
    act(() => {
      configStore.getState().setFeatureFlags({});
    });
  });

  it("hides the type selector when the flag is off", () => {
    act(() => {
      configStore.getState().setFeatureFlags({});
    });
    mountPanel(formProps());
    expect(screen.queryByRole("radiogroup", { name: /agent or role/i })).toBeNull();
    expect(screen.queryByLabelText(/tier role/i)).toBeNull();
  });

  it("shows the type selector and Tier Role in Role mode", () => {
    mountPanel({
      ...formProps(),
      draft: { ...EMPTY_AGENT_DRAFT, subtype: "role", tierRole: "engineer" },
    });
    expect(
      screen.getByRole("radiogroup", { name: /agent or role/i }),
    ).not.toBeNull();
    expect(screen.getByLabelText(/tier role/i)).not.toBeNull();
  });
});
