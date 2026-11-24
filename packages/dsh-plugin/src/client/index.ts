import { createElement as bundledCreateElement } from "react";
import type { DshClientContext, DshSidebarRightTabs, DshUiWorkspace } from "../dsh";
import { followDshSessionDeepLink } from "./dsh-deep-link";
import { startIsland } from "./island";
import type { IslandRoute } from "./navigation";
import { FAMILY_ROOTS } from "./navigation";
import { bindShell, startBoundSidebarAutoCollapse } from "./shell";
import { configureHostShell } from "./shell-runtime";
import type {
  SessionsLike,
  SidebarRightLike,
  WorkspacesLike,
} from "./shell-link";
import { familyOfRoute } from "./tabs";
import {
  createRailIcon,
  createSeatComponent,
  type HostCreateElement,
  type RailGlyph,
} from "./seat";
import { startUnreadPoll } from "./unread-store";
import { createSteerComposer, STEER_COMPOSER_ENTRY } from "./steer-composer";
import {
  SIDE_PREVIEW_KIND,
  SIDE_PREVIEW_TAB_ID,
  SIDE_PREVIEW_TITLES,
  canOpenSidePreview,
} from "./side-preview";
import { createIssuePreviewBody } from "./side-preview-tab";

/**
 * Browser face of the Multica panel.
 *
 * One rail row per Multica view, additive and without shadowing:
 *
 * - `sidebar.panellist` (a `list`) renders one sidebar button per registration
 *   and addresses the matching `main` cell by list id. Each entry below is one
 *   of those rows; the sidebar owns the button, the label and the selection
 *   state, which is why the menu is seven rows rather than a dropdown the plugin
 *   would have to position inside someone else's column. The list has no
 *   grouping API, so the grouping is expressed by adjacent order (30–36) and a
 *   shared glyph family, and each label carries the product name because the
 *   collapsed rail shows only a glyph and a tooltip.
 * - `main` (a `keyed` slot) gets one cell per entry: the seat the React 19
 *   island mounts into, opening at that entry's own view. `priority: -100` is
 *   the additive convention the shipped panels use; nothing shipped is replaced.
 *
 * The seventh row is Settings: the fork's own page, with the surfaces this panel
 * does not render hidden and one MOCT group added. It is a rail row and not a
 * route inside another row because a settings page is a destination a reader
 * returns to, and the shell's rail is where destinations live.
 *
 * The rows are also the shell's record of which MMX view is showing: the island
 * selects the row that owns the active tab's family (`client/shell.ts`), so the
 * highlight can never disagree with the tab strip (M-197).
 */
export interface MulticaEntry {
  /** `sidebar.panellist` list id, and the `main` cell key it addresses. */
  id: string;
  /** Rail row label: the button's accessible name and its tooltip. */
  label: string;
  /** Where the island opens when this row is selected. */
  route: IslandRoute;
  /** Glyph drawn inside the shell's rail button. */
  glyph: RailGlyph;
}

/** The seven entries, with Decisions beside Inbox. */
export const MULTICA_ENTRIES: readonly MulticaEntry[] = [
  {
    id: "multica",
    label: "Multica Issues",
    route: FAMILY_ROOTS.issues,
    glyph: "issues",
  },
  {
    id: "multica-agents",
    label: "Multica Agents",
    route: FAMILY_ROOTS.agents,
    glyph: "agents",
  },
  {
    id: "multica-skills",
    label: "Multica Skills",
    route: FAMILY_ROOTS.skills,
    glyph: "skills",
  },
  {
    id: "multica-inbox",
    label: "Multica Inbox",
    route: FAMILY_ROOTS.inbox,
    glyph: "inbox",
  },
  {
    id: "multica-decisions",
    label: "Multica Decisions",
    route: FAMILY_ROOTS.decisions,
    glyph: "decisions",
  },
  {
    id: "multica-chat",
    label: "Multica Chat",
    route: FAMILY_ROOTS.chat,
    glyph: "chat",
  },
  {
    id: "multica-settings",
    label: "Multica Settings",
    route: FAMILY_ROOTS.settings,
    glyph: "settings",
  },
];

/** The entry the panel opened with; kept as the first row's id. */
export const MULTICA_PANEL_ID = "multica";

/** First rail row's order; the entries follow in sequence. */
const FIRST_ORDER = 30;

/**
 * Services dsh resolves before calling `apply`.
 *
 * `layout` is dsh's own ui-layout service: the package already declares that
 * module as a client dependency, and injecting the service is what guarantees
 * `ctx.layout` exists when `apply` binds it (M-197) instead of depending on
 * which plugin was applied first.
 *
 * `sidebarRight`, `workspaces` and `sessions` are the side-panel bridge's
 * sources (M-159). Injecting them documents the edge the loader reads for
 * ordering; the apply body still reads each one tolerantly, so a host built
 * without the right sidebar keeps the rail rows anyway.
 *
 * No session-navigation service is injected (M-316): the toggle opens the panel
 * beside MMX or not at all, and selecting a Session is the shell's navigation —
 * the one thing that would replace MMX with the dsh page.
 */
export const inject = [
  "slots",
  "layout",
  "sidebarRight",
  "workspaces",
  "sessions",
];

let hostCreateElement: HostCreateElement | null = null;

/** A face without `effect` reads as "no teardown surface". */
type EffectFace = Pick<DshClientContext, "effect">;

/** A face the shell context carries: undefined when the host does not provide it. */
type Face = Record<string, unknown>;

/**
 * Run `fn` under the plugin's page lifetime, or at once when the shell gives
 * the browser context no `effect`.
 *
 * The client `effect` is optional (M-504): `ctx.effect?.(...)` no-ops
 * silently when it is absent, which left the `hostPanel` claims unregistered
 * — the panel then ignored every name while `selectHostedSession` still
 * returned a disposer. Falling back to a direct call keeps the claim; the
 * warn makes the missing teardown loud instead of silent. Page-lifetime
 * registrations (this module's only kind here) outlive nothing by leaking:
 * `apply` runs once per page.
 */
function runClientEffect(face: EffectFace, label: string, fn: () => (() => void) | void): void {
  if (typeof face.effect === "function") {
    face.effect(fn, label);
    return;
  }
  console.warn(`[multica] ${label}: no ctx.effect, running without teardown`);
  fn();
}

/** Read one service off the shell context without assuming it exists. */
function face(ctx: DshClientContext, name: string): unknown {
  const source = ctx as unknown as { get?: (name: string) => unknown } & Face;
  if (typeof source.get === "function") {
    return source.get(name);
  }
  return (source as Face)[name];
}

/**
 * Drop a consumed `dsh_session` from the address bar.
 *
 * The parameter is this plugin's one-shot instruction, not page state: leaving
 * it would re-apply the navigation on every reload, after the reader may have
 * moved on. Everything else in the URL is preserved.
 */
function consumeSessionParam(search: string): void {
  if (typeof location === "undefined" || typeof history === "undefined") return;
  const url = new URL(location.href);
  url.search = search;
  history.replaceState(history.state, "", url);
}

/**
 * Hand this module the shell's `createElement`.
 *
 * The bundle wrapper calls it with `require("react")` from the shell's module
 * table before `apply` runs. Without it the plugin assumes a React 19 host and
 * uses its own bundled `createElement`, which is what the unit tests do.
 */
export function configureHostRuntime(createElement: HostCreateElement | null): void {
  hostCreateElement = createElement;
}

export function apply(ctx: DshClientContext): void {
  // The side-panel bridge (M-159): the sidebar faces, read tolerantly so a host
  // without the right sidebar still reaches the rail registration below.
  const sidebarRight = (face(ctx, "sidebarRight") ?? null) as SidebarRightLike | null;
  const workspaces = (face(ctx, "workspaces") ?? null) as WorkspacesLike | null;
  const sessions = (face(ctx, "sessions") ?? null) as SessionsLike | null;
  const tabTypes = (face(ctx, "sidebarRightTabs") ?? null) as DshSidebarRightTabs | null;
  const layoutFace = ctx.layout ?? null;
  configureHostShell({
    sidebarRight,
    workspaces,
    sessions,
    daemonId: "",
    sidePreviewKind: canOpenSidePreview(sidebarRight, tabTypes !== null)
      ? SIDE_PREVIEW_KIND
      : null,
  });

  // "Open session" on a Multica run row lands here (M-132 E1). The server's
  // link names the run's own Session; dsh reads no URL for itself, so this is
  // the page's one chance to select it. Tolerant like every other face read: a
  // host without ui-workspace keeps the app exactly as the link found it.
  const uiWorkspace = (face(ctx, "uiWorkspace") ?? null) as DshUiWorkspace | null;
  const deepLinkDone = followDshSessionDeepLink({
    search: typeof location === "undefined" ? "" : location.search,
    sessions,
    navigation: uiWorkspace,
    replaceSearch: consumeSessionParam,
  });
  if (deepLinkDone !== null) {
    runClientEffect(ctx, "multica: dsh_session deep link", () => deepLinkDone);
  }

  // Card C's opt-in: the right sidebar may render beside a hosted `main`
  // panel. Register every MMX panel id under the plugin's page lifetime — the
  // disposer is the plugin's teardown — so an MMX view keeps the side panel
  // next to it. A shell without `ctx.effect` still gets the claims at once
  // (M-504: skipping them left the gate closed and every name ignored); only
  // the teardown is lost.
  // Hosting only keeps the column drawn: which Session's folder it edits is
  // named per press by the toggle's `retarget` plan, because the project (and
  // so the folder) changes while MMX stays in the main column. A face without
  // `hostPanel` has nowhere to draw the column without leaving MMX, so the
  // toggle disables itself there (M-316) instead of putting a Session on
  // screen.
  if (sidebarRight !== null && typeof sidebarRight.hostPanel === "function") {
    runClientEffect(ctx, "multica: sidebarRight.hostPanel for the seven MMX panels", () => {
      const disposers = MULTICA_ENTRIES.map((entry) =>
        sidebarRight.hostPanel?.(entry.id) ?? null,
      ).filter((dispose): dispose is () => void => dispose !== null);
      return () => {
        for (const dispose of disposers) dispose();
      };
    });
  }

  // The rail dot's baseline count. The browser half has no teardown path —
  // `apply` returns void and the slot registrations below are unregistered by
  // the shell, not by this module — so the poller lives as long as the page;
  // the store's own guard makes a repeated `apply` share it rather than start
  // a second timer.
  startUnreadPoll();

  // The shell's own two surfaces this plugin keeps in step with the island
  // (M-197): the rail row that must mark the active tab's family, and the
  // sidebar that collapses while an MMX view is showing. One binding, read by
  // the island and by the auto-collapse subscription below.
  const layout = layoutFace;
  if (layout !== null) {
    bindShell({
      layout,
      panelOfFamily: new Map(
        MULTICA_ENTRIES.map((entry) => [familyOfRoute(entry.route), entry.id]),
      ),
    });
    // Page-lifetime, like the rail dot's poller: `apply` returns void and the
    // shell unloads a plugin with the page.
    startBoundSidebarAutoCollapse();
  }

  const createElement = hostCreateElement ?? (bundledCreateElement as HostCreateElement);

  // The side preview's tab type (M-336, G6 generalized): one page type in
  // dsh's own right column. The chip is generic because one tab serves
  // different targets; the identifier belongs in the body's header.
  if (tabTypes !== null && canOpenSidePreview(sidebarRight, true)) {
    runClientEffect(
      ctx,
      "multica: sidebarRightTabs.register for the side preview",
      () =>
        tabTypes.register({
          id: SIDE_PREVIEW_TAB_ID,
          kind: SIDE_PREVIEW_KIND,
          title: () => SIDE_PREVIEW_TITLES.title,
        }),
    );
    ctx.slots.inject("sidebar.right.pane.tab", () =>
      ctx.slots.register(
        { name: "sidebar.right.pane.tab", key: SIDE_PREVIEW_TAB_ID },
        createIssuePreviewBody(createElement),
      ),
    );
  }

  // The dsh chat's composer for an MMX-owned session (M-136).
  //
  // The selector is the whole safety story: it declines unless exactly one
  // *running* MMX turn pins this session and negotiated `task-supplement-v1`
  // with permission for this viewer. A session with no owner, two owners, or a
  // run that cannot take a message keeps the shell's composer and M-142's
  // banner.
  ctx.slots.inject("conversation.composer", () =>
    ctx.slots.register(
      { ...STEER_COMPOSER_ENTRY },
      createSteerComposer(createElement),
    ),
  );

  MULTICA_ENTRIES.forEach((entry, index) => {
    // The row's own click is what tells the island a rail entry was picked
    // (M-552); the seat's mount alone no longer replays it.
    const railIcon = createRailIcon(createElement, entry.glyph, entry.route);
    const seat = createSeatComponent(createElement, {
      panelId: entry.id,
      route: entry.route,
    });

    ctx.slots.inject("sidebar.panellist", () =>
      ctx.slots.register(
        {
          name: "sidebar.panellist",
          id: entry.id,
          order: FIRST_ORDER + index,
          label: entry.label,
        },
        railIcon,
      ),
    );

    ctx.slots.inject("main", () =>
      ctx.slots.register(
        {
          name: "main",
          key: entry.id,
          priority: -100,
        },
        seat,
      ),
    );
  });

  warmIsland();
}

/** One warm-up per page: `apply` runs once, and a retry would only duplicate it. */
let warming = false;

/**
 * Build the island ahead of the first rail click.
 *
 * The shell keeps only the selected `main` cell mounted, so the island is not
 * created until something claims a seat — and that claim is the click that used
 * to pay for the whole boot: config, workspace, ~30 queries, a WebSocket and
 * 292 KB of stylesheet. `apply` runs during shell boot, so the build waits for
 * an idle window instead of competing with the shell's first paint. A browser
 * without an idle scheduler falls back to a short timer; the island is created
 * detached either way, and `attachIsland` picks up whatever is already there.
 */
function warmIsland(): void {
  if (warming || typeof document === "undefined") return;
  warming = true;
  const idle = (globalThis as { requestIdleCallback?: (fn: () => void, options?: { timeout: number }) => unknown })
    .requestIdleCallback;
  if (typeof idle === "function") {
    idle(() => startIsland(document), { timeout: 2000 });
    return;
  }
  setTimeout(() => startIsland(document), 200);
}
