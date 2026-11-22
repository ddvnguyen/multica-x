// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { EMPTY_AGENT_DRAFT } from "@multica/core/agents";
import { renderWithI18n } from "../../test/i18n";
import { AgentConfigurationPanel } from "./agent-configuration-panel";

// Probe flag per test: the hook reads only the workspace-scoped boolean
// from the endpoint (M-517 verdict — no published-flag OR).
const probeFlag = vi.hoisted(() => ({ value: false }));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-test" }));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: (options: { queryKey?: readonly unknown[] }) =>
    options?.queryKey?.includes("probe")
      ? { data: probeFlag.value }
      : { data: undefined },
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
  afterEach(() => {
    cleanup();
    probeFlag.value = false;
  });

  it("hides the type selector when the flag is off", () => {
    probeFlag.value = false;
    mountPanel(formProps());
    expect(screen.queryByRole("radiogroup", { name: /agent or role/i })).toBeNull();
    expect(screen.queryByLabelText(/tier role/i)).toBeNull();
  });

  it("shows the type selector and Tier Role in Role mode", () => {
    probeFlag.value = true;
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
