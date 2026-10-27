import { useEffect, useMemo, useState } from "react";
import {
  ISLAND_API_BASE,
  islandWebSocketUrl,
  loadIslandConfig,
  type IslandConfig,
} from "./config";
import {
  BOARD_ROUTE,
  createIslandNavigation,
  ISLAND_FAMILIES,
  islandRouteKey,
  type IslandRoute,
} from "./navigation";
import { createIslandStorage } from "./storage";
import { createMoctGroup, HIDDEN_SETTINGS_TABS } from "./moct-settings";
import { UnreadReporter } from "./unread-reporter";
import {
  AgentDetailPage,
  AgentsPage,
  ChatPage,
  CoreProvider,
  createIssueViewStore,
  ErrorBoundary,
  InboxPage,
  IssueDetailRoute,
  IssuesPage,
  NavigationProvider,
  PortalContainerProvider,
  RESOURCES,
  setCurrentWorkspace,
  SettingsPage,
  SkillDetailPage,
  SkillsPage,
  useWorkspaceList,
  ViewStoreProvider,
  WorkspaceSlugProvider,
} from "../fork";

/** Bumping this changes the client identity multica-be records for the panel. */
export const ISLAND_VERSION = "0.1.0";

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
  /** Place to mount at; the board unless a deep link asked for one issue. */
  initialRoute?: IslandRoute;
}

const SUPPORTED = ["en", "zh-Hans", "ko", "ja", "fr"] as const;
type Locale = (typeof SUPPORTED)[number];

/** Follow the shell's document language instead of inventing a preference. */
export function detectLocale(): Locale {
  const candidates =
    typeof navigator === "undefined"
      ? []
      : [...(navigator.languages ?? []), navigator.language];
  for (const candidate of candidates) {
    const tag = (candidate ?? "").toLowerCase();
    if (tag.startsWith("zh")) return "zh-Hans";
    if (tag.startsWith("ko")) return "ko";
    if (tag.startsWith("ja")) return "ja";
    if (tag.startsWith("fr")) return "fr";
    if (tag.startsWith("en")) return "en";
  }
  return "en";
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
  initialRoute,
}: {
  portalHost: HTMLElement;
  initialRoute?: IslandRoute;
}) {
  const [config, setConfig] = useState<IslandConfig | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    loadIslandConfig()
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
      initialRoute={initialRoute}
    />
  );
}

export function IslandApp({
  portalHost,
  preferredSlug,
  wsUrl,
  wsPath,
  problem,
  initialRoute,
}: IslandAppProps) {
  const storage = useMemo(() => createIslandStorage(), []);
  const locale = useMemo(detectLocale, []);
  return (
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
      resources={{ [locale]: RESOURCES[locale] }}
      syncUserLocale={false}
    >
      <PortalContainerProvider container={portalHost}>
        <WorkspaceGate
          preferredSlug={preferredSlug}
          wsPath={wsPath}
          problem={problem}
          initialRoute={initialRoute}
        />
      </PortalContainerProvider>
    </CoreProvider>
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
 */
function WorkspaceGate({
  preferredSlug,
  wsPath,
  problem,
  initialRoute,
}: {
  preferredSlug?: string;
  wsPath: string;
  problem: string | null;
  initialRoute?: IslandRoute;
}) {
  const { workspaces, ready } = useWorkspaceList();
  const workspace = useMemo(() => {
    const preferred = preferredSlug?.trim();
    if (preferred) {
      return workspaces.find((entry) => entry.slug === preferred) ?? null;
    }
    return workspaces[0] ?? null;
  }, [preferredSlug, workspaces]);

  const slug = workspace?.slug;
  const id = workspace?.id;
  useEffect(() => {
    if (slug === undefined || id === undefined) return;
    setCurrentWorkspace(slug, id);
    return () => setCurrentWorkspace(null, null);
  }, [slug, id]);

  if (!ready) return <IslandMessage title="Connecting to Multica…" />;
  if (workspace === null) {
    return (
      <IslandMessage
        title="No Multica workspace"
        body={
          preferredSlug
            ? `The configured workspace "${preferredSlug}" is not readable with this token.`
            : "This token cannot read any workspace on the configured Multica server."
        }
      />
    );
  }
  return (
    <WorkspaceSlugProvider slug={workspace.slug}>
      {/* Beside the app itself, inside CoreProvider's QueryClientProvider:
          the reporter observes the summary key the inbox page reads and
          writes the rail dot's store. */}
      <UnreadReporter workspaceId={workspace.id} />
      <IslandPages
        slug={workspace.slug}
        wsPath={wsPath}
        problem={problem}
        initialRoute={initialRoute}
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
 * the matching page instead of doing nothing. dsh gives a plugin no way to move
 * the shell's rail selection, so the row keeps marking the entry the reader came
 * through while the island follows the reader.
 */
function IslandPage({
  route,
}: {
  /** Settings is mounted by `IslandPages`; it needs props this switch lacks. */
  route: Exclude<IslandRoute, { view: "settings" }>;
}) {
  switch (route.view) {
    case "board":
      return <IssuesPage />;
    case "issue":
      return <IssueDetailRoute routeId={route.issueId} />;
    case "agents":
      return <AgentsPage />;
    case "agent":
      return <AgentDetailPage agentId={route.agentId} />;
    case "skills":
      return <SkillsPage />;
    case "skill":
      return <SkillDetailPage skillId={route.skillId} />;
    case "inbox":
      return <InboxPage />;
    case "chat":
      return <ChatPage />;
  }
}

export function IslandPages({
  slug,
  wsPath,
  problem,
  initialRoute = BOARD_ROUTE,
}: {
  slug: string;
  wsPath: string;
  problem: string | null;
  initialRoute?: IslandRoute;
}) {
  const [route, setRoute] = useState<IslandRoute>(initialRoute);
  const [refused, setRefused] = useState<string | null>(null);
  const viewStore = useState(() => createIssueViewStore("dsh-issues"))[0];
  const moctGroup = useMemo(
    () => createMoctGroup({ workspaceSlug: slug, wsPath, problem }),
    [slug, wsPath, problem],
  );

  const navigation = useMemo(
    () =>
      createIslandNavigation({
        slug,
        route,
        onChange: setRoute,
        onRefused: setRefused,
      }),
    [slug, route],
  );

  useEffect(() => {
    if (refused === null) return;
    // One line, once per destination: a refused path is a deliberate scoping
    // decision (the five views below, or a Q8 surface), not a bug to report per
    // click.
    console.info(
      `[multica] ${refused} is not one of the dsh panel's views (${ISLAND_FAMILIES.join(", ")})`,
    );
  }, [refused]);

  return (
    <ViewStoreProvider store={viewStore}>
      <NavigationProvider value={navigation}>
        <ErrorBoundary resetKeys={[islandRouteKey(route)]}>
          {route.view === "settings" ? (
            // The fork's page, scoped to what this shell can actually render:
            // no billing, integrations, plugins or shortcut reference, plus the
            // one group that only exists here.
            <SettingsPage
              extraGroups={[moctGroup]}
              hiddenTabs={HIDDEN_SETTINGS_TABS}
            />
          ) : (
            <IslandPage route={route} />
          )}
        </ErrorBoundary>
      </NavigationProvider>
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
