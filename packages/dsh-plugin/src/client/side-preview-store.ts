import { ISLAND_CSS } from "./popup-seat";

/**
 * The bridge between a dsh tab body and the island that paints it (M-336).
 *
 * The tab body is shell React 18 and the side stack is the island's React 19
 * inside a shadow root, so the two never share a tree: the body publishes "this
 * container is showing this issue" and the island portals its side stack into
 * the container. One store, one direction.
 *
 * The container is not the element the body renders — it is a surface the body
 * *creates inside* it, carrying the island's stylesheet in its own shadow root.
 * A portal that landed in the shell's light DOM would arrive unstyled: the
 * island's Tailwind sheet lives in the island's shadow root, and the fork's
 * design tokens are declared there as `:host` rules (`popup-seat.ts`). The
 * surface follows the shell's theme the same way the island does.
 *
 * Publication is deferred by a microtask on purpose: the body writes during its
 * own render, and notifying a React 19 root synchronously from another root's
 * render is the one thing React forbids. A flush later in the same task is a
 * normal external-store update.
 */

/** One container the island should portal into, and what it shows. */
export interface SidePreviewTarget {
  /** Stable identity of this container for the portal's key. */
  readonly key: string;
  /** The body's element, which addresses this target. */
  readonly container: HTMLElement;
  /** The styled element inside it — where the portal renders. */
  readonly surface: HTMLElement;
  /** The issue to draw, or `null` while the tab has no params yet. */
  readonly issueId: string | null;
}

interface Entry {
  key: string;
  container: HTMLElement;
  surface: HTMLElement;
  issueId: string | null;
  signal: AbortSignal | null;
}

const EMPTY: readonly SidePreviewTarget[] = [];
const entries: Entry[] = [];
const listeners = new Set<() => void>();
let snapshot: readonly SidePreviewTarget[] = EMPTY;
let nextKey = 0;
let flushScheduled = false;
let theme: MutationObserver | null = null;

/** The surface classes the fork's tokens and utilities need. */
function buildSurface(doc: Document, container: HTMLElement): HTMLElement {
  const shadow = container.shadowRoot ?? container.attachShadow({ mode: "open" });
  shadow.replaceChildren();
  const style = doc.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = ISLAND_CSS;
  const surface = doc.createElement("div");
  surface.className = "multica-island-surface";
  surface.dataset.multicaSidePreview = "";
  Object.assign(surface.style, {
    display: "block",
    height: "100%",
    overflowY: "auto",
    background: "var(--background, transparent)",
    color: "var(--foreground, inherit)",
    font: "inherit",
  });
  shadow.append(style, surface);
  return surface;
}

/** The shell's own dark-mode signal; dsh exposes a class and a data attribute. */
function shellIsDark(doc: Document): boolean {
  const root = doc.documentElement;
  return (
    root.classList.contains("dark") ||
    root.dataset.theme === "dark" ||
    (    root.dataset.theme === undefined &&
      doc.defaultView?.matchMedia?.("(prefers-color-scheme: dark)").matches === true)
  );
}

/** Paint every surface from the shell's theme, now and on every switch. */
function syncTheme(): void {
  for (const entry of entries) {
    const doc = entry.container.ownerDocument;
    entry.surface.classList.toggle("dark", shellIsDark(doc));
    if (theme === null && typeof MutationObserver !== "undefined") {
      theme = new MutationObserver(syncTheme);
      theme.observe(doc.documentElement, {
        attributes: true,
        attributeFilter: ["class", "data-theme"],
      });
    }
  }
  if (entries.length === 0) {
    theme?.disconnect();
    theme = null;
  }
}

function publish(): void {
  const next: SidePreviewTarget[] = [];
  let changed = snapshot.length !== 0;
  for (let i = 0; i < entries.length; i += 1) {
    const entry = entries[i]!;
    // A closed tab removes its element without telling us; the element going
    // away IS the notification, so the stale entry drops here.
    if (!entry.container.isConnected) {
      entries.splice(i, 1);
      i -= 1;
      changed = true;
      continue;
    }
    next.push({
      key: entry.key,
      container: entry.container,
      surface: entry.surface,
      issueId: entry.issueId,
    });
    const previous = snapshot[i];
    if (
      previous === undefined ||
      previous.key !== entry.key ||
      previous.issueId !== entry.issueId
    ) {
      changed = true;
    }
  }
  if (snapshot.length !== next.length) changed = true;
  if (!changed) return;
  snapshot = next.length === 0 ? EMPTY : next;
  for (const listener of listeners) listener();
}

function schedule(): void {
  if (flushScheduled) return;
  flushScheduled = true;
  queueMicrotask(() => {
    flushScheduled = false;
    publish();
  });
}

/**
 * Publish one tab body's container.
 *
 * Called on every render of the body, which is what makes a reveal that hands
 * the tab another issue reach the island: the body re-renders, republishes its
 * container with the new id, and the portal paints the new issue in place.
 *
 * @param container - the element the body rendered.
 * @param issueId - the issue the tab is showing; `null` when it has no params.
 * @param signal - the tab occurrence's lifetime; aborting withdraws the target.
 */
export function publishSidePreview(
  container: HTMLElement,
  issueId: string | null,
  signal?: AbortSignal | null,
): void {
  let entry = entries.find((candidate) => candidate.container === container);
  if (entry === undefined) {
    entry = {
      key: `side-preview-${nextKey++}`,
      container,
      surface: buildSurface(container.ownerDocument, container),
      issueId,
      signal: null,
    };
    entries.push(entry);
  }
  entry.issueId = issueId;
  const lifetime = signal ?? null;
  if (lifetime !== null && entry.signal !== lifetime) {
    entry.signal = lifetime;
    lifetime.addEventListener("abort", () => withdrawSidePreview(container), { once: true });
  }
  syncTheme();
  schedule();
}

/** Withdraw one container — the tab closed, or the plugin unloaded. */
export function withdrawSidePreview(container: HTMLElement): void {
  const index = entries.findIndex((candidate) => candidate.container === container);
  if (index === -1) return;
  entries.splice(index, 1);
  syncTheme();
  schedule();
}

/** The island's subscription; `getSidePreviewTargets` is the stable snapshot. */
export function subscribeSidePreview(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The targets to portal into, reference-stable between flushes. */
export function getSidePreviewTargets(): readonly SidePreviewTarget[] {
  return snapshot;
}

/** Drop every target. Tests only: a page reload is the real teardown. */
export function resetSidePreviewTargets(): void {
  entries.length = 0;
  snapshot = EMPTY;
  theme?.disconnect();
  theme = null;
  for (const listener of listeners) listener();
}

/** The sheet a preview surface carries, so a harness can assert it landed. */
export const SIDE_PREVIEW_CSS = ISLAND_CSS;
