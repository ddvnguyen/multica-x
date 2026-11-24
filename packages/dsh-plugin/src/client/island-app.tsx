import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useStore } from "zustand";
import {
  islandConfig,
  ISLAND_API_BASE,
  islandWebSocketUrl,
  type IslandConfig,
} from "./config";
import {
  BOARD_ROUTE,
  createIslandNavigation,
  ISLAND_FAMILIES,
  islandPath,
  islandRouteKey,
  parseIslandRoute,
  type IslandRoute,
} from "./navigation";
import { createIslandStorage } from "./storage";
import { createTabsStore, familyOfRoute } from "./tabs";
import {
  createIslandScrollRestorationAdapter,
  watchScrollMemento,
} from "./memento-capture";
import { selectShellPanel } from "./shell";
import { TabBar } from "./tab-bar";
import { ProjectPicker } from "./project-picker";
import { readProjectScope, writeProjectScope } from "./project-scope";
import { createIslandLocaleAdapter, resolveIslandLocale } from "./locale";
import { PanelZoomRow } from "./zoom-row";
import { useZoom } from "./zoom-store";
import { runtimesDeviceTab } from "./moct-runtimes";
import { ShellButtons } from "./shell-buttons";
import { UnreadReporter } from "./unread-reporter";
import { SteerReporter } from "./steer-reporter";
import { DecisionsBadgeReporter } from "./decisions-badge-reporter";
import { AskPlanToastReporter } from "./ask-plan-toast-reporter";
import { IslandVersionBadge } from "./version-badge";
import { SidePreviewCapability } from "./side-preview-opener";
import { SidePreviewPortals } from "./side-preview-portals";
import { hostShell } from "./shell-runtime";
import {
  AgentDetailPage,
  createMoctGroup,
  createAgentMemorySections,
  HIDDEN_SETTINGS_TABS,
  useCurrentMember,
  AgentsPage,
  ChatPage,
  CoreProvider,
  DecisionsPage,
  createIssueViewStore,
  ErrorBoundary,
  InboxPage,
  IssueDetailRoute,
  IssuesPage,
  RuntimeDetailPage,
  RuntimeSettingsPage,
  mmxBuild,
  ModalRegistry,
  SearchCommand,
  PageChromeProvider,
  NavigationProvider,
  PortalContainerProvider,
  ScrollRestorationProvider,
  HostStorageProvider,
  SidebarProvider,
  projectListOptions,
  RESOURCES,
  setCurrentWorkspace,
  SettingsPage,
  SkillDetailPage,
  SkillsPage,
  Toaster,
  useWorkspaceList,
  ViewStoreProvider,
  WorkspaceSlugProvider,
} from "../fork";

/**
 * The stamped build identity multica-be records for the panel (M-155):
 * `x.x.x-yyMMMdd.HH.mm`, the same string the Settings row and the bottom-right
 * badge show, so a server log line or API identity points at the exact build
 * that produced an error. "dev" only survives where a host built the island
 * without injecting a build — no real deployment does.
 */
export const ISLAND_VERSION = mmxBuild()?.stamp ?? "dev";

/**
 * A rail click the island still has to apply (M-552).
 *
 * The shell mounts a row's cell without saying why, so the click itself is
 * recorded by the row (`requestRailEntry`) and handed to the island with the
 * next attach. `nonce` makes two clicks on the same row distinct events.
 */
export interface RailEntryRequest {
  route: IslandRoute;
  nonce: number;
}

export interface IslandAppProps {
  /** Where Base UI portals render, inside the island's shadow root. */
  portalHost: HTMLElement;
  /** Workspace the panel opens; empty selects the first one the token sees. */
  preferredSlug?: string;
  /** Origin-less WebSocket URL on the dsh origin. */
  wsUrl: string;
  /** Same-origin path of the realtime channel, shown in settings. */
  wsPath: string;
  /** Why the host cannot proxy, when it cannot; shown in settings. */
  problem: string | null;
  /** Default folder for "All projects" (M-159); empty means none configured. */
  workspaceFolder?: string;
  /** Entry the seat asked for; the board unless a deep link named one issue. */
  route?: IslandRoute;
  /** True while a seat holds the island. */
  attached?: boolean;
  /** A rail click this attach has to apply; absent on every other mount. */
  railRequest?: RailEntryRequest | null;
}

/**
 * Boot gate.
 *
 * The workspace and the realtime path come from the host half, which is the
 * only face that knows its own configuration. The provider tree is built once
 * those arrive, so `CoreProvider` never has to change its WebSocket URL under a
 * live connection.
 */
export function IslandBoot({
  portalHost,
  route,
  attached = false,
  railRequest = null,
}: {
  portalHost: HTMLElement;
  route?: IslandRoute;
  /** True while a seat holds the island; see `MountIslandOptions.attached`. */
  attached?: boolean;
  /** A rail click to apply on this attach; see `RailEntryRequest`. */
  railRequest?: RailEntryRequest | null;
}) {
  const [config, setConfig] = useState<IslandConfig | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    islandConfig()
      .then((next) => {
        if (!cancelled) setConfig(next);
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setError(cause instanceof Error ? cause.message : String(cause));
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (error !== null) {
    return (
      <IslandMessage
        title="Multica did not start"
        body={`${error}. Check the plugin row's serverUrl and token.`}
      />
    );
  }
  if (config === null) return <IslandMessage title="Connecting to Multica…" />;
  if (config.problem !== null) {
    return (
      <IslandMessage
        title="Multica is not configured"
        body={`${config.problem}. Set the plugin row's serverUrl and token (or MULTICA_DSH_SERVER_URL and MULTICA_DSH_TOKEN).`}
      />
    );
  }
  return (
    <IslandApp
      portalHost={portalHost}
      preferredSlug={config.workspaceSlug}
      wsUrl={islandWebSocketUrl(config.wsPath, window.location)}
      wsPath={config.wsPath}
      problem={config.problem}
      workspaceFolder={config.workspaceFolder}
      route={route}
      attached={attached}
      railRequest={railRequest}
    />
  );
}

export function IslandApp({
  portalHost,
  preferredSlug,
  wsUrl,
  wsPath,
  problem,
  workspaceFolder = "",
  route,
  attached = false,
  railRequest = null,
}: IslandAppProps) {
  const storage = useMemo(() => createIslandStorage(), []);
  // The adapter is what the fork's Preferences page reads its Language row
  // through; without it `useLocaleAdapter` throws and the page renders as the
  // error boundary (M-154). `syncUserLocale` stays off: the Language row still
  // PATCHes `/api/me` itself, and the dsh shell's own language is not ours.
  const localeAdapter = useMemo(
    () => createIslandLocaleAdapter(storage),
    [storage],
  );
  const locale = useMemo(
    () => resolveIslandLocale(localeAdapter),
    [localeAdapter],
  );
  const zoom = useZoom();
  return (
    // Zoom scales the panel's own surface, not the shell and not the overlays:
    // popups are portalled outside this tree (`popup-seat.ts`), so they keep
    // their size and their anchoring at every level. Percentage heights still
    // resolve, because the containing block is expressed in the zoomed space.
    <div
      className="flex min-h-0 flex-1 flex-col"
      style={{ zoom: zoom / 100 }}
      data-panel-zoom={zoom}
    >
      <CoreProvider
        apiBaseUrl={ISLAND_API_BASE}
        wsUrl={wsUrl}
        storage={storage}
        // Cookie mode: the host proxy authenticates every request and the
        // WebSocket upgrade with its own token, so the island sends nothing and
        // the sliding-renewal flow (a cookie-session concern) stays off.
        cookieAuth
        identity={{ platform: "dsh", version: ISLAND_VERSION, os: detectOs() }}
        locale={locale}
        localeAdapter={localeAdapter}
        resources={{ [locale]: RESOURCES[locale] }}
        syncUserLocale={false}
      >
        <PortalContainerProvider container={portalHost}>
          <WorkspaceGate
            preferredSlug={preferredSlug}
            wsPath={wsPath}
            problem={problem}
            workspaceFolder={workspaceFolder}
            route={route}
            attached={attached}
            railRequest={railRequest}
          />
        </PortalContainerProvider>
      </CoreProvider>
    </div>
  );
}

function detectOs(): string {
  const platform =
    typeof navigator === "undefined" ? "" : navigator.platform.toLowerCase();
  if (platform.includes("mac")) return "macos";
  if (platform.includes("win")) return "windows";
  if (platform.includes("linux")) return "linux";
  return "unknown";
}

/**
 * Resolve the workspace the panel speaks as.
 *
 * The fork's pages read their workspace from a slug context and address every
 * query by the workspace id resolved from it, so one workspace has to be
 * chosen before either page mounts. `setCurrentWorkspace` is the same
 * platform-owned pointer the web layout maintains, and it is what puts
 * `X-Workspace-Slug` on the proxied requests.
 *
 * This deployment has exactly one workspace (M-173): the host's
 * `workspaceSlug` is the only source, and a slug it cannot read is an error
 * the reader has to fix — never a reason to quietly open a different
 * workspace's data under the same header.
 */
function WorkspaceGate({
  preferredSlug,
  wsPath,
  problem,
  workspaceFolder,
  route,
  attached,
  railRequest,
}: {
  preferredSlug?: string;
  wsPath: string;
  problem: string | null;
  workspaceFolder?: string;
  route?: IslandRoute;
  attached: boolean;
  railRequest: RailEntryRequest | null;
}) {
  const { workspaces, ready } = useWorkspaceList();
  const workspace = useMemo(() => {
    const preferred = preferredSlug?.trim();
    if (!preferred) return null;
    return workspaces.find((entry) => entry.slug === preferred) ?? null;
  }, [preferredSlug, workspaces]);

  const slug = workspace?.slug;
  const id = workspace?.id;
  // The pointer every descendant request is stamped from, so it has to be set
  // before the first descendant mounts. A passive effect here does not do that:
  // child effects run before the parent's, so the pages' first queries — and
  // the reporter's unread summary — used to leave without `X-Workspace-Slug`,
  // and the server answered 400 to each of them until react-query retried
  // (M-198). A layout effect runs after the DOM is committed and before any
  // passive effect in the tree, and `armed` keeps the children out of the
  // render that precedes it, so nothing below ever queries while the pointer
  // still belongs to another workspace.
  const [armed, setArmed] = useState<{ slug: string; id: string } | null>(null);
  useLayoutEffect(() => {
    if (slug === undefined || id === undefined) return;
    setCurrentWorkspace(slug, id);
    setArmed({ slug, id });
    return () => setCurrentWorkspace(null, null);
  }, [slug, id]);

  if (!ready) return <IslandMessage title="Connecting to Multica…" />;
  if (workspace === null) {
    return (
      <IslandMessage
        title="No Multica workspace"
        body={
          preferredSlug?.trim()
            ? `The configured workspace "${preferredSlug}" is not readable with this token.`
            : "This deployment has no Multica workspace configured."
        }
      />
    );
  }
  // One frame, before the layout effect above has armed this workspace: the
  // pages wait here rather than query against a pointer that is not theirs.
  if (armed === null || armed.slug !== workspace.slug || armed.id !== workspace.id) {
    return <IslandMessage title="Connecting to Multica…" />;
  }
  return (
    <WorkspaceSlugProvider slug={workspace.slug}>
      {/* Beside the app itself, inside CoreProvider's QueryClientProvider:
          the reporters observe live state and write the rail dot's store. */}
      <UnreadReporter workspaceId={workspace.id} />
      {/* Which running MMX turn owns each dsh session (M-136): the dsh chat's
          composer takeover reads this store, and this observer is what keeps
          it current from the same snapshot query presence already loads. */}
      <SteerReporter workspaceId={workspace.id} />
      {/* M-362: the fork's Toaster, mounted once at the island root. No
          permission prompt on load. */}
      <Toaster />
      <IslandPages
        slug={workspace.slug}
        workspaceId={workspace.id}
        wsPath={wsPath}
        problem={problem}
        workspaceFolder={workspaceFolder}
        route={route}
        attached={attached}
        railRequest={railRequest}
      />
    </WorkspaceSlugProvider>
  );
}

/**
 * The places the island renders without host-supplied props, plus their detail
 * depth. Settings is the one entry mounted separately: it needs the MOCT group
 * and the hidden-page list above.
 *
 * The rail row decides where an island opens; from there the fork's pages
 * navigate the way they do in the web app, so a link between two of these views
 * (an inbox row falling back to its issue, an agent's "message" link) lands on
 * the matching page instead of doing nothing. The island keeps the shell's rail
 * selection on the row that owns the active tab's family (`selectShellPanel`),
 * so a tab that moves the reader into another family moves the highlight with
 * it (M-197).
 */
function IslandPage({
  route,
  projectId,
  /** The agent page's host sections (the MOCT Memory tab); absent, a stock page. */
  extraAgentSections,
}: {
  /** Settings is mounted by `IslandPages`; it needs props this switch lacks. */
  route: Exclude<
    IslandRoute,
    { view: "settings" } | { view: "runtime" } | { view: "runtime-settings" }
  >;
  projectId: string | null;
  extraAgentSections?: ReturnType<typeof createAgentMemorySections>;
}) {
  switch (route.view) {
    case "board":
      return <IssuesPage />;
    case "issue":
      return <IssueDetailRoute routeId={route.issueId} />;
    case "agents":
      return <AgentsPage />;
    case "agent":
      return (
        <AgentDetailPage
          agentId={route.agentId}
          extraSections={extraAgentSections}
        />
      );
    case "skills":
      return <SkillsPage />;
    case "skill":
      return <SkillDetailPage skillId={route.skillId} />;
    case "inbox":
      return <InboxPage />;
    case "decisions":
      return <DecisionsPage projectId={projectId} />;
    case "chat":
      return <ChatPage />;
    default:
      // `settings`, `runtime` and `runtime-settings` are mounted by
      // `IslandPages`, beside this switch: Settings needs the MOCT group and
      // hidden-page list this switch lacks.
      return null;
  }
}

export function IslandPages({
  slug,
  workspaceId,
  wsPath,
  problem,
  workspaceFolder = "",
  route: entryRoute = BOARD_ROUTE,
  attached = false,
  railRequest = null,
}: {
  slug: string;
  workspaceId: string;
  wsPath: string;
  problem: string | null;
  /** Default folder for "All projects" (M-159); empty means none configured. */
  workspaceFolder?: string;
  route?: IslandRoute;
  /** True while a seat holds the island; the rail only follows a seated tab. */
  attached?: boolean;
  /** A rail click to apply, recorded by the row itself (M-552). */
  railRequest?: RailEntryRequest | null;
}) {
  const storage = useMemo(() => createIslandStorage(), []);
  // One workspace, one tab set (M-173): the persisted key no longer varies by
  // slug, so switching the host's slug can never strand a second history.
  const tabsStore = useMemo(() => createTabsStore(storage, slug), [storage, slug]);

  // The global project filter: the header picker's selection, read back from
  // storage on mount and reconciled against the live list below. The fork's
  // own project query is shared with the pages, so the picker costs no request
  // of its own once a page has loaded the list.
  const { data: projects } = useQuery(projectListOptions(workspaceId));
  const [projectId, setProjectId] = useState<string | null>(() =>
    readProjectScope(storage, undefined),
  );
  useEffect(() => {
    const resolved = readProjectScope(storage, projects);
    if (resolved !== projectId) setProjectId(resolved);
  }, [projectId, projects, storage]);
  function selectProject(next: string | null) {
    setProjectId(next);
    writeProjectScope(storage, next);
  }

  // A rail click is an event (M-552): the row records it and `attachIsland`
  // only replays it on the attach it caused, never on a re-attach of the same
  // cell. Every mount that is not that event leaves the reader's tab alone.
  useEffect(() => {
    if (railRequest === null) return;
    tabsStore.getState().railEntry(railRequest.route);
  }, [tabsStore, railRequest]);

  // A deep link is a destination, not a row: it opens (or focuses) its own
  // tab once per entry, and a re-attach of the same entry changes nothing.
  const openedEntry = useRef<string | null>(null);
  useEffect(() => {
    const key = islandRouteKey(entryRoute);
    if (openedEntry.current === key) return;
    openedEntry.current = key;
    const isRailEntry = !("issueId" in entryRoute || "agentId" in entryRoute || "skillId" in entryRoute) && (entryRoute.query === undefined || entryRoute.query === "");
    const store = tabsStore.getState();
    // A row with no tab of its own and no tab at all seeds the strip from the
    // entry: there is nothing of the reader's to change, and a restored session
    // must not start with an empty strip. A mount that would move an existing
    // tab of another family stays inert — only a recorded click may do that.
    if (!isRailEntry) store.openOrFocus(islandPath(slug, entryRoute));
    else if (store.tabs.length === 0) store.railEntry(entryRoute);
  }, [tabsStore, entryRoute, slug]);

  // Only what the page shows: scroll-memento and view-state commits write the
  // store on every scroll tick, and a whole-store subscription re-rendered the
  // island (and its page) for each of them (M-552).
  const activeId = useStore(tabsStore, (s) => s.activeId);
  const mountGeneration = useStore(tabsStore, (s) => s.mountGeneration);
  const activeUrl = useStore(
    tabsStore,
    (s) => s.tabs.find((t) => t.id === s.activeId)?.url ?? null,
  );
  const route: IslandRoute =
    activeUrl !== null ? (parseIslandRoute(activeUrl) ?? entryRoute) : entryRoute;

  // The rail and the strip must never disagree (M-197): whatever the active
  // tab shows — after a tab click, Back/Forward across tabs, a closed tab, or
  // a restored session — the sidebar selects the row that owns its family.
  // Only while a seat holds the island: the warm-up build has no seat, and the
  // shell is showing whatever the reader left it on.
  const family = familyOfRoute(route);
  useEffect(() => {
    if (!attached) return;
    selectShellPanel(family);
  }, [attached, family]);

  const viewStore = useState(() => createIssueViewStore("dsh-issues"))[0];
  const { role } = useCurrentMember(workspaceId);
  const canManageMemory = role === "owner" || role === "admin";

  const moctGroup = useMemo(
    () => createMoctGroup({ apiBasePath: ISLAND_API_BASE, canManageMemory, workspaceSlug: slug, workspaceId, wsPath, workspaceFolder, problem }),
    [slug, workspaceId, wsPath, workspaceFolder, problem, canManageMemory],
  );
  const agentMemorySections = useMemo(
    () => createAgentMemorySections({ apiBasePath: ISLAND_API_BASE, canManageMemory, workspaceSlug: slug, workspaceId, wsPath, workspaceFolder, problem }),
    [slug, workspaceId, wsPath, workspaceFolder, problem, canManageMemory],
  );

  const navigation = useMemo(
    () =>
      createIslandNavigation({
        slug,
        tabs: tabsStore,
        onRefused: setRefusedState,
      }),
    [slug, tabsStore],
  );

  const [refused, setRefused] = useState<string | null>(null);
  function setRefusedState(v: string) {
    setRefused(v);
  }

  useEffect(() => {
    if (refused === null) return;
    console.info(
      `[multica] ${refused} is not one of the dsh panel's views (${ISLAND_FAMILIES.join(", ")})`,
    );
  }, [refused]);

  // What a re-mounted tab's views come back as (M-174): the capture pass
  // above serves offsets from the store at mount, straight through the fork's
  // own restoration protocol — no rival scroll store to keep in step.
  const restoration = useMemo(
    () => createIslandScrollRestorationAdapter(tabsStore),
    [tabsStore],
  );
  const pageRootRef = useRef<HTMLDivElement | null>(null);
  useEffect(
    () => watchScrollMemento(tabsStore, () => pageRootRef.current),
    [tabsStore],
  );

  // Reload (M-174) = remount + mark the current page's queries stale.
  // `type: "active"` is exactly the mounted observers — the current page and
  // its shared header queries — and `refetchType: "none"` avoids fetching
  // into the tree that is about to unmount; the remounted page refetches its
  // now-stale queries. desktop's Coordinator does the same on generation
  // change.
  const queryClient = useQueryClient();
  const seenGeneration = useRef(mountGeneration);
  useEffect(() => {
    if (mountGeneration === seenGeneration.current) return;
    seenGeneration.current = mountGeneration;
    queryClient.invalidateQueries({ type: "active", refetchType: "none" });
  }, [mountGeneration, queryClient]);

  return (
    <ViewStoreProvider store={viewStore}>
      {/* Every page's width owner reads through one store: the sidebar widths
          (M-330), the resizable split layouts and the rich-table columns
          (M-360). `storage` is the island's namespaced adapter, so none of
          them touch the bare keys the shell's origin already holds. */}
      <HostStorageProvider storage={storage}>
      <NavigationProvider value={navigation}>
        {/* M-362: toasts agent_question / plan_review inbox:new rows only,
            deduped by item id. Inside the provider: the toast action routes
            through useIntentNavigate. */}
        <AskPlanToastReporter />
        <PageChromeProvider titleInHostChrome projectId={projectId}>
          <DecisionsBadgeReporter workspaceId={workspaceId} projectId={projectId} />
          <TabBar
            tabsStore={tabsStore}
            navigation={navigation}
            slug={slug}
            projectPicker={
              <ProjectPicker
                projects={projects ?? []}
                projectId={projectId}
                onSelect={selectProject}
              />
            }
            headerExtras={
              <ShellButtons
                workspaceId={workspaceId}
                projects={projects}
                projectId={projectId}
                workspaceFolder={workspaceFolder}
                route={route}
              />
            }
          />
          {/* M-330: the island had no width owner, so every split sized through
              `--sidebar-width` / `--list-column-width` lost its resize edge
              (`ListColumn` renders none without a `SidebarProvider`) — the
              Inbox list column the Owner reported. The provider carries the
              two widths and the drag commit; `contents` keeps the pages as
              direct children of the island's flex column, and inheritance of
              the custom properties follows the DOM tree, not the box tree.
              Widths persist in the island's namespaced store, never the bare
              keys the shell's origin already holds. */}
          <SidebarProvider storage={storage} className="contents">
          {/* `Side preview` in MMX is dsh's right column (M-336): the plugin
              registers a tab type there, and a peek-mode click opens the issue
              into it. A dsh build without the tab registry has no column to
              open, so there the floating peek is the preview instead of a
              second mode that cannot work (M-330). The choice is the
              capability's, not the click's: `openSidePreview` non-null keeps
              `IssuePeekHost` down, so the two previews never both exist. */}
          <SidePreviewCapability
            input={{
              workspaceId,
              projects: projects ?? undefined,
              projectId,
              workspaceFolder,
              route,
              daemonId: hostShell()?.daemonId ?? "",
            }}
          >
            <ScrollRestorationProvider adapter={restoration}>
            <ErrorBoundary
              resetKeys={[
                activeId ?? "none",
                islandRouteKey(route),
                mountGeneration,
              ]}
            >
              {/* The page the active tab mounts into. The key re-mounts the
                  page on reload (mountGeneration), and `contents` keeps every
                  layout property the pages had as direct children of the
                  island's flex column. The capture pass reads only this
                  element, so the strip and rail never pollute a capture. */}
              <div
                ref={pageRootRef}
                key={`${activeId ?? "none"}:${mountGeneration}`}
                className="contents"
                data-island-page-root=""
              >
              {route.view === "runtime" ? (
                // M-349: a runtime row pushes `/<slug>/runtimes/<machine>`; the
                // island renders the fork's own detail page instead of
                // refusing the path. The Settings rail row stays selected: the
                // family of this route is settings.
                <RuntimeDetailPage runtimeId={route.machineId} />
              ) : route.view === "runtime-settings" ? (
                <RuntimeSettingsPage
                  machineId={route.machineId}
                  runtimeId={route.runtimeId}
                />
              ) : route.view === "settings" ? (
                // The fork's page, scoped to what this shell can actually render:
                // no billing, integrations, plugins or shortcut reference, plus the
                // one group that only exists here.
                <SettingsPage
                  extraGroups={[moctGroup]}
                  // Runtimes (M-174): the panel's only door to what the
                  // workspace runs on. The desktop keeps this page in its
                  // sidebar; a dsh panel has no such navigation, so it takes
                  // the fork's host-supplied device slot instead.
                  extraDeviceTabs={[runtimesDeviceTab]}
                  hiddenTabs={HIDDEN_SETTINGS_TABS}
                  preferencesExtraRows={<PanelZoomRow />}
                />
              ) : (
                <IslandPage
                  route={route}
                  projectId={projectId}
                  extraAgentSections={agentMemorySections}
                />
              )}
              </div>
            </ErrorBoundary>
            </ScrollRestorationProvider>
          </SidePreviewCapability>
          </SidebarProvider>
          {/* The fork's pages ask for a dialog through `useModalStore` and one
              component renders whatever it names; the web and desktop layouts
              mount it, and the island had no equivalent, so every dialog in the
              panel (create issue, delete confirm, set parent, …) was a no-op
              (M-146). It sits here rather than beside `UnreadReporter` because
              the dialogs navigate — `CreateIssueDialog` calls `useNavigation()`
              (`packages/views/modals/create-issue.tsx:244`) and would throw
              outside `NavigationProvider`. Inside `IslandPages` it is also
              outside the route switch, so a dialog survives a page change; the
              portal container comes from `IslandApp`, so it renders into the
              island's shadow root and not the shell's document. */}
          <ModalRegistry />
          {/* The side preview's content, portalled into the dsh tab's container
              (M-336). It sits here, under every provider the side stack reads,
              because the portal renders into another React tree's DOM. */}
          <SidePreviewPortals />
          <SearchCommand />
          {/* The build stamp (M-155): a sibling of the route switch, so it
              survives page changes and renders even when the boundary shows
              its fallback — the one moment an error report needs it most. */}
          <IslandVersionBadge />
        </PageChromeProvider>
      </NavigationProvider>
      </HostStorageProvider>
    </ViewStoreProvider>
  );
}

function IslandMessage({ title, body }: { title: string; body?: string }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-8 text-center">
      <p className="text-body font-medium text-foreground">{title}</p>
      {body === undefined ? null : (
        <p className="max-w-sm text-caption text-muted-foreground">{body}</p>
      )}
    </div>
  );
}
