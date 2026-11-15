import { ISLAND_CSS } from "./popup-seat";
import type { SidePreviewTargetRef } from "./side-preview";

export interface SidePreviewTarget {
  readonly key: string;
  readonly container: HTMLElement;
  readonly surface: HTMLElement;
  readonly target: SidePreviewTargetRef | null;
}

interface Entry {
  key: string;
  container: HTMLElement;
  surface: HTMLElement;
  target: SidePreviewTargetRef | null;
  signal: AbortSignal | null;
}

const EMPTY: readonly SidePreviewTarget[] = [];
const entries: Entry[] = [];
const listeners = new Set<() => void>();
let snapshot: readonly SidePreviewTarget[] = EMPTY;
let nextKey = 0;
let flushScheduled = false;
let theme: MutationObserver | null = null;

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

function shellIsDark(doc: Document): boolean {
  const root = doc.documentElement;
  return (
    root.classList.contains("dark") ||
    root.dataset.theme === "dark" ||
    (root.dataset.theme === undefined &&
      doc.defaultView?.matchMedia?.("(prefers-color-scheme: dark)").matches === true)
  );
}

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

function sameTarget(a: SidePreviewTargetRef | null, b: SidePreviewTargetRef | null): boolean {
  if (a === b) return true;
  if (a === null || b === null) return false;
  return a.view === b.view && a.id === b.id;
}

function publish(): void {
  let changed = false;
  const next: SidePreviewTarget[] = [];
  for (let i = 0; i < entries.length; i += 1) {
    const entry = entries[i]!;
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
      target: entry.target,
    });
    const previous = snapshot[i];
    if (
      previous === undefined ||
      previous.key !== entry.key ||
      previous.surface !== entry.surface ||
      !sameTarget(previous.target, entry.target)
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

export function publishSidePreview(
  container: HTMLElement,
  target: SidePreviewTargetRef | null,
  signal?: AbortSignal | null,
): void {
  let entry = entries.find((candidate) => candidate.container === container);
  if (entry === undefined) {
    entry = {
      key: `side-preview-${nextKey++}`,
      container,
      surface: buildSurface(container.ownerDocument, container),
      target,
      signal: null,
    };
    entries.push(entry);
  }
  entry.target = target;
  const lifetime = signal ?? null;
  if (lifetime !== null && entry.signal !== lifetime) {
    entry.signal = lifetime;
    lifetime.addEventListener("abort", () => withdrawSidePreview(container), { once: true });
  }
  syncTheme();
  schedule();
}

export function withdrawSidePreview(container: HTMLElement): void {
  const index = entries.findIndex((candidate) => candidate.container === container);
  if (index === -1) return;
  entries.splice(index, 1);
  syncTheme();
  schedule();
}

export function subscribeSidePreview(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getSidePreviewTargets(): readonly SidePreviewTarget[] {
  return snapshot;
}

export function resetSidePreviewTargets(): void {
  entries.length = 0;
  snapshot = EMPTY;
  theme?.disconnect();
  theme = null;
  for (const listener of listeners) listener();
}

export const SIDE_PREVIEW_CSS = ISLAND_CSS;
