/**
 * @vitest-environment jsdom
 *
 * Board columns can be collapsed into a slim strip (Trello's collapsed list):
 * the column keeps its place and its count, and stops showing cards. A
 * workspace admin can also add a list (a status) and rename a custom one
 * straight from the board.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { getIssueSurfaceViewStore } from "@multica/core/issues/stores/surface-view-store";
import type { Issue, IssueStatusEntry } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssueContextMenuProvider } from "../actions/issue-actions-context-menu";
import { BoardView } from "./board-view";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

vi.mock("@multica/core/properties", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/properties")>()),
  propertyListOptions: () => ({
    queryKey: ["properties"],
    queryFn: async () => [],
  }),
  useSetIssueProperty: () => ({ mutate: () => {} }),
  useUnsetIssueProperty: () => ({ mutate: () => {} }),
}));

vi.mock("@multica/core/workspace/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/hooks")>()),
  useActorName: () => ({ getActorName: () => "Someone" }),
}));

vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "viewer-1" } }),
}));

vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  isAgentRuntimeBound: () => true,
  useAgentPresenceDetail: () => ({ availability: "offline", workload: null }),
}));

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
    useWorkspacePaths: () => actual.paths.workspace("acme"),
  };
});

vi.mock("../../navigation", () => ({
  AppLink: ({ children, ...props }: React.ComponentProps<"a">) => (
    <a {...props}>{children}</a>
  ),
  useNavigation: () => ({
    push: () => {},
    openInNewTab: () => {},
    getShareableUrl: (path: string) => `https://app.example${path}`,
    pathname: "/",
  }),
  resolveClickIntent: () => "push",
  useIntentNavigate: () => () => {},
}));

/** The workspace status catalog the board resolves labels and entries from. */
const CATALOG: Record<string, IssueStatusEntry> = {
  todo: {
    id: "status-todo",
    workspace_id: "ws-1",
    key: "todo",
    name: "To Do",
    description: "",
    category: "unstarted",
    color: "#8b8b8b",
    icon: null,
    is_system: true,
    position: 1,
    archived_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
  qa: {
    id: "status-qa",
    workspace_id: "ws-1",
    key: "qa",
    name: "QA",
    description: "",
    category: "started",
    color: "#3366ff",
    icon: null,
    is_system: false,
    position: 1,
    archived_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  },
};

vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => ({
    statuses: Object.values(CATALOG),
    activeStatuses: Object.values(CATALOG),
    categoryOf: (key: string) => CATALOG[key]?.category ?? "unstarted",
    labelOf: (key: string) => CATALOG[key]?.name ?? key,
    entryOf: (key: string) => CATALOG[key],
    colorOf: (key: string) => CATALOG[key]?.is_system ? null : CATALOG[key]?.color ?? null,
    iconOf: () => null,
    inCategory: () => [],
    isLoaded: true,
    isPending: false,
    isError: false,
    retry: () => {},
    hasCustomStatuses: true,
  }),
}));

/** Flipped per test: only a workspace admin may add or rename a list. */
const adminState = { isAdmin: true };

vi.mock("../../workflows/permissions", () => ({
  useWorkflowPermissions: () => ({
    isAdmin: adminState.isAdmin,
    canManageProject: adminState.isAdmin,
  }),
}));

function makeIssue(id: string, status: string): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title: `Task ${id}`,
    description: null,
    status,
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: null,
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

class ObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}

describe("Board column collapsing, adding and renaming", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    vi.stubGlobal("IntersectionObserver", ObserverStub);
    vi.stubGlobal("ResizeObserver", ObserverStub);
    adminState.isAdmin = true;
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  function render({ statuses = ["todo"] }: { statuses?: string[] } = {}) {
    const store = getIssueSurfaceViewStore(
      `board-collapse-${Math.floor(Math.random() * 1e9)}`,
    );
    const issues = statuses.map((status) => makeIssue(status, status));
    const result = renderWithI18n(
      <QueryClientProvider client={queryClient}>
        <ViewStoreProvider store={store}>
          <IssueContextMenuProvider>
            <BoardView
              issues={issues}
              visibleStatuses={statuses}
              onMoveIssue={() => {}}
            />
          </IssueContextMenuProvider>
        </ViewStoreProvider>
      </QueryClientProvider>,
    );
    return { store, ...result };
  }

  it("collapses a column into a strip that keeps the count and drops the cards", () => {
    const { container } = render({ statuses: ["todo"] });

    expect(screen.getByText("Task todo")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Collapse list" }));

    expect(screen.queryByText("Task todo")).toBeNull();
    // The strip still names the column and still shows how much is in it.
    expect(screen.getByRole("button", { name: /Expand/ })).toBeTruthy();
    expect(screen.getByText("Todo")).toBeTruthy();
    // A collapsed column renders no cards, so nothing is droppable into it.
    expect(container.querySelectorAll("[data-board-card]")).toHaveLength(0);
  });

  it("expands a collapsed column again", () => {
    render({ statuses: ["todo"] });

    fireEvent.click(screen.getByRole("button", { name: "Collapse list" }));
    fireEvent.click(screen.getByRole("button", { name: /Expand/ }));

    expect(screen.getByText("Task todo")).toBeTruthy();
  });

  it("offers adding a list to a workspace admin", async () => {
    render({ statuses: ["todo"] });

    fireEvent.click(screen.getByRole("button", { name: "Add list" }));

    // The board reuses the settings page's status editor, so a list added
    // here is created exactly the way it is created in Settings.
    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(screen.getByText("New status")).toBeTruthy();
  });

  it("does not offer adding a list to a non-admin", () => {
    adminState.isAdmin = false;
    render({ statuses: ["todo"] });

    expect(screen.queryByRole("button", { name: "Add list" })).toBeNull();
  });

  it("renames a custom list through the shared status editor", async () => {
    render({ statuses: ["qa"] });

    fireEvent.click(screen.getByRole("button", { name: "Column actions" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rename list" }));

    expect(await screen.findByRole("dialog")).toBeTruthy();
    expect(screen.getByText("Edit status")).toBeTruthy();
  });

  it("locks renaming a built-in list", async () => {
    render({ statuses: ["todo"] });

    fireEvent.click(screen.getByRole("button", { name: "Column actions" }));
    const rename = await screen.findByRole("menuitem", { name: "Rename list" });

    await waitFor(() => expect(rename).toHaveAttribute("aria-disabled", "true"));
    expect(rename.getAttribute("title")).toBe("Built-in lists cannot be renamed.");
  });
});
