import type { IncomingMessage, ServerResponse } from "node:http";
import type { Duplex } from "node:stream";

/**
 * The two dsh contracts this package compiles against, stated structurally.
 *
 * The plugin is loaded by dsh, not linked against it: the host face is handed
 * the real Cordis `ctx`, and the browser face is handed the shell's slot
 * registry. Declaring only the members used here keeps `@deepseek-ai/*` out of
 * this repository's dependency graph while still failing the build if the call
 * shapes drift, and the pinned reference for both is dsh 0.2.0-rc.2
 * (`packages/host/webserver/src/index.ts`, `packages/client/ui-slots`).
 */

/** One named HTTP route a host plugin may add. */
export interface DshHttpRoute {
  /** `exact` matches the pathname verbatim; `prefix` also matches `path/...`. */
  kind: "exact" | "prefix";
  path: string;
  handler: (req: IncomingMessage, res: ServerResponse) => void | Promise<void>;
}

/** One exact-path HTTP upgrade route, owned by a single protocol. */
export interface DshUpgradeRoute {
  path: string;
  handler: (req: IncomingMessage, socket: Duplex, head: Buffer) => void;
}

/** What dsh's Connection offers: refuse a request that is not this browser. */
export interface DshConnection {
  requestRejection(request: {
    headers: Record<string, string | string[] | undefined>;
  }): number | undefined;
}

/** The host half of a dsh plugin's context. */
export interface DshHostContext {
  /** Run `fn` under the plugin's lifetime; the returned disposer runs on unload. */
  effect(fn: () => (() => void) | void, label: string): unknown;
  webServer: {
    register(route: DshHttpRoute): () => void;
    registerUpgrade(route: DshUpgradeRoute): () => void;
  };
  /** Present in the shipped Web composition; absent in other hosts. */
  connection?: DshConnection;
}

/** One additive slot registration, in the shape dsh's slot registry takes. */
export interface DshSlotRegistration {
  name: string;
  /** Required for `list` slots, which address cells by id. */
  id?: string;
  /** Required for `keyed` slots, which dispatch on `entryKey`. */
  key?: string;
  order?: number;
  priority?: number;
  label?: string;
  /**
   * Chain-slot selector: return the value this entry is elected for, or `null`
   * to decline. Entries are tried in ascending `priority`; the first non-null
   * result wins and reaches the component as its `matched` prop.
   */
  select?: (owner: unknown) => unknown;
}

/**
 * The layout service's selection source (`ctx.layout.panelInfo`).
 *
 * A `HostObservable` in dsh's own terms: read the snapshot, subscribe to its
 * invalidation. `activePanelId` is the selected rail row, `null` for the
 * Conversation.
 */
export interface DshPanelInfo {
  getSnapshot(): { readonly activePanelId: string | null };
  subscribe(listener: () => void): () => void;
}

/**
 * The layout service (`ctx.layout`, provided by ui-layout).
 *
 * Declared as the three members this plugin calls: the selection source, the
 * panel selection itself, and the sidebar toggle. `selectPanel` throws for a
 * `main` key that is not registered; the island's rail rows are.
 */
export interface DshLayout {
  readonly panelInfo: DshPanelInfo;
  selectPanel(panelId: string | null): void;
  toggleSidebar(): void;
}

/**
 * One page-tab type a shell plugin contributes (dsh's `sidebarRightTabs`).
 *
 * Stage one of dsh's two-stage tab registration: the static face — what the
 * kind is called, what its chip says, and whether every open is its own page.
 * Stage two is the keyed `sidebar.right.pane.tab` body registered under the
 * same `id`. `title` is captured into the layout record when the tab opens and
 * is never rewritten, so it names the kind, not the content.
 */
export interface DshSidebarRightTabDefinition {
  /** This implementation's identity; the `key` its body registers under. */
  id: string;
  /** Type discriminator `openTab` names. */
  kind: string;
  /** Each open by kind creates independent content. */
  multiple?: boolean;
  /** Keep a visited body mounted through hiding and Session switches. */
  keepMounted?: boolean;
  /** Priority band; `extension` (the default) outranks shipped viewers. */
  priority?: "extension" | "builtin" | "fallback";
  /** The tab chip's text, captured at open time. */
  title: (address: string) => string;
}

/**
 * The shell's tab-type registry (dsh `ctx.sidebarRightTabs`).
 *
 * Absent on a host built before dsh published plugin tab types; every read of
 * it in this plugin is tolerant, so such a host keeps the rail rows and offers
 * `New tab` alone (M-336).
 */
export interface DshSidebarRightTabs {
  /** Register one type for the caller's lifetime; returns its disposer. */
  register(definition: DshSidebarRightTabDefinition): () => void;
}

/** The browser half of a dsh plugin's context. */
export interface DshClientContext {
  slots: {
    /** Contribute once the slot is live; the callback's effects follow it. */
    inject(key: string, register: () => unknown): unknown;
    register(options: DshSlotRegistration, component: unknown): unknown;
  };
  /**
   * Panel geometry and selection, from dsh's ui-layout. Absent in a host built
   * without it, which leaves the rail agreement and the auto-collapse inert
   * (M-197) instead of failing the panel.
   */
  layout?: DshLayout;
  /**
   * Run `fn` under the plugin's page lifetime, when the shell gives the
   * browser context one (the host face always has it). Optional: the absence
   * reads as "no teardown surface", which only shortens hostPanel's cleanup.
   */
  effect?(fn: () => (() => void) | void, label: string): unknown;
  /**
   * dsh's tab-type registry, when the build ships one (0.2.2+). Absent on an
   * older shell: the plugin then offers `New tab` alone rather than failing to
   * mount, so it is read through the tolerant `face()` helper.
   */
  sidebarRightTabs?: DshSidebarRightTabs;
}

/** The module values every dsh client bundle hands to the shell's loader. */
export interface DshClientModule {
  apply(ctx: DshClientContext): void;
  inject: string[];
  default: DshClientModule;
}
