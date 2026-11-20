import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { AppBottomBar } from "./bottom-bar";

interface TeamStatus {
  working: { id: string }[];
  stuck: { id: string }[];
  byAgent: Map<string, { queuedCount: number }>;
}

const h = vi.hoisted(() => ({
  chat: { floatingChatEnabled: true },
  isMobile: false,
  workspace: { id: "ws-test" } as { id: string } | null,
  team: { working: [], stuck: [], byAgent: new Map() } as TeamStatus,
  // The shell's chat placement: the web SPA puts the sessions in the header
  // strip and drops the footer's chat (M-413).
  chatTabsInHeader: false,
  trialVisible: false,
}));

vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (state: typeof h.chat) => unknown) => selector(h.chat),
    { getState: () => h.chat },
  ),
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => h.workspace,
  useWorkspacePaths: () => ({ chat: () => "/acme/chat", agents: () => "/acme/agents" }),
}));
vi.mock("@multica/ui/hooks/use-mobile", () => ({ useIsMobile: () => h.isMobile }));
vi.mock("./panels/team-status", () => ({ useTeamAgentStatus: () => h.team }));
vi.mock("./app-shell-context", () => ({ useAppShell: () => null }));
vi.mock("../chat/components/chat-bar", () => ({ ChatBar: () => <div data-testid="chat-bar" /> }));
vi.mock("../tabs/chat-tabs", () => ({ useChatTabsInHeader: () => h.chatTabsInHeader }));
vi.mock("../billing/trial-status", () => ({
  TrialStatus: () => (h.trialVisible ? <a href="/acme/settings">Trial</a> : null),
  useTrialStatusVisible: () => h.trialVisible,
}));

function renderBar(pathname = "/acme/issues") {
  const adapter: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname,
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  return renderWithI18n(
    <NavigationProvider value={adapter}>
      <AppBottomBar />
    </NavigationProvider>,
  );
}

beforeEach(() => {
  h.chat.floatingChatEnabled = true;
  h.isMobile = false;
  h.workspace = { id: "ws-test" };
  h.team = { working: [], stuck: [], byAgent: new Map() };
  h.chatTabsInHeader = false;
  h.trialVisible = false;
});

describe("AppBottomBar", () => {
  it("shows quick chat and no team status while the team is idle", () => {
    renderBar();

    expect(screen.getByRole("contentinfo", { name: "Status bar" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-bar")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("keeps quick chat without a working-agent entry when agents are running", () => {
    h.team.working = [{ id: "a1" }, { id: "a2" }, { id: "a3" }, { id: "a4" }];
    renderBar();

    expect(screen.queryByText("4 agents working")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-bar")).toBeInTheDocument();
  });

  it("adds up the tasks waiting on offline runtimes across agents", () => {
    h.team.stuck = [{ id: "a1" }, { id: "a2" }];
    h.team.byAgent = new Map([
      ["a1", { queuedCount: 2 }],
      ["a2", { queuedCount: 1 }],
    ]);
    renderBar();

    expect(
      screen.getByRole("link", { name: "3 tasks waiting on an offline runtime" }),
    ).toHaveAttribute("href", "/acme/agents");
  });

  it("drops quick chat on the Chat tab, which already shows it full-page", () => {
    renderBar("/acme/chat");
    expect(screen.queryByTestId("chat-bar")).not.toBeInTheDocument();
  });

  it("renders no footer at all when the header hosts the chat and nothing is stuck", () => {
    h.chatTabsInHeader = true;
    expect(renderBar().container).toBeEmptyDOMElement();
  });

  it("keeps the status row when the header hosts the chat, without a second chat bar", () => {
    h.chatTabsInHeader = true;
    h.team.stuck = [{ id: "a1" }];
    h.team.byAgent = new Map([["a1", { queuedCount: 1 }]]);
    renderBar();

    expect(screen.queryByTestId("chat-bar")).not.toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "1 task waiting on an offline runtime" }),
    ).toBeInTheDocument();
  });

  it("keeps a trial notice as the bar's only content", () => {
    h.chatTabsInHeader = true;
    h.trialVisible = true;
    renderBar();

    expect(screen.getByRole("link", { name: "Trial" })).toBeInTheDocument();
    expect(screen.queryByTestId("chat-bar")).not.toBeInTheDocument();
  });

  it("renders nothing when the chat preference is off and no status shows", () => {
    h.chat.floatingChatEnabled = false;
    expect(renderBar().container).toBeEmptyDOMElement();
  });

  it("renders nothing on a narrow window or without a workspace", () => {
    h.isMobile = true;
    expect(renderBar().container).toBeEmptyDOMElement();

    h.isMobile = false;
    h.workspace = null;
    expect(renderBar().container).toBeEmptyDOMElement();
  });
});
