import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import islandCss from "./island.css?inline";
import { IslandBoot } from "./island-app";

/**
 * The React 19 island.
 *
 * dsh's shell is React 18 and this package ships its own React 19, so the two
 * trees must not meet: the shell renders exactly one element here — a host
 * `div` — and this module owns everything below it. That includes the slider
 * between them, a shadow root, which is what keeps Tailwind's preflight and
 * utility classes out of the shell's document.
 *
 * `:root` is rewritten to `:host` because inside a shadow tree `:root` matches
 * nothing: the fork's design tokens would otherwise be injected and never land.
 */

/** Everything the shell needs to tear one island down again. */
interface IslandHandle {
  root: Root;
  theme: MutationObserver | null;
}

export interface MountIslandOptions {
  /** Which place to open first. The board unless a link named one issue. */
  initialRoute?: import("./navigation").IslandRoute;
}

const handles = new WeakMap<HTMLElement, IslandHandle>();

export function shadowStyles(css: string): string {
  return css.replace(/:root\b/g, ":host");
}

/** Is the shell currently dark? dsh exposes both a class and a data attribute. */
export function shellPrefersDark(doc: Document): boolean {
  const root = doc.documentElement;
  return (
    root.classList.contains("dark") ||
    root.dataset.theme === "dark" ||
    (root.dataset.theme === undefined &&
      typeof window !== "undefined" &&
      window.matchMedia?.("(prefers-color-scheme: dark)").matches === true)
  );
}

export function mountIsland(host: HTMLElement, options: MountIslandOptions = {}): void {
  if (handles.has(host)) return;
  const doc = host.ownerDocument;
  const shadow = host.shadowRoot ?? host.attachShadow({ mode: "open" });
  shadow.replaceChildren();

  const style = doc.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = shadowStyles(islandCss);

  const surface = doc.createElement("div");
  surface.className = "multica-island-surface";

  const rootElement = doc.createElement("div");
  rootElement.className = "multica-island-root";
  rootElement.dataset.multicaRoot = "";

  const portalHost = doc.createElement("div");
  portalHost.className = "multica-island-portals";
  portalHost.dataset.multicaPortals = "";

  surface.append(rootElement, portalHost);
  shadow.append(style, surface);

  const syncTheme = (): void => {
    surface.classList.toggle("dark", shellPrefersDark(doc));
  };
  syncTheme();

  // The shell switches theme on the document element; follow it so the panel
  // does not jump to light in a dark shell.
  const theme =
    typeof MutationObserver === "undefined"
      ? null
      : new MutationObserver(syncTheme);
  theme?.observe(doc.documentElement, {
    attributes: true,
    attributeFilter: ["class", "data-theme"],
  });

  // dsh owns document-level shortcuts. Keyboard events are composed, so a
  // keystroke typed here would otherwise reach them and trigger shell actions
  // mid-edit; stopping them at the island boundary keeps the shell's bindings
  // to the shell's own surfaces.
  for (const type of ["keydown", "keyup", "keypress"] as const) {
    rootElement.addEventListener(type, (event) => event.stopPropagation());
  }

  const root = createRoot(rootElement);
  root.render(
    createElement(IslandBoot, { portalHost, initialRoute: options.initialRoute }),
  );
  handles.set(host, { root, theme });
}

export function unmountIsland(host: HTMLElement): void {
  const handle = handles.get(host);
  if (handle === undefined) return;
  handles.delete(host);
  handle.theme?.disconnect();
  handle.root.unmount();
  host.shadowRoot?.replaceChildren();
}

/** True while an island is mounted on this element. For tests and diagnostics. */
export function isIslandMounted(host: HTMLElement): boolean {
  return handles.has(host);
}
