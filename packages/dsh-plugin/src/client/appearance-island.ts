import {
  APPEARANCE_STORAGE_KEY,
  APPEARANCE_THEME_ATTR,
  INSTALLED_THEMES_STORAGE_KEY,
  THEME_OPTION_ATTR_PREFIX,
  activeTheme,
  appearanceRootVariables,
  appearanceStylesheet,
  appearanceThemeAttributes,
  appearanceThemeCss,
  selectAppearance,
  useAppearanceStore,
  useInstalledThemesStore,
  useThemeSafeMode,
} from "../fork";

const THEME_CSS_ELEMENT_ID = "multica-theme-css";
const STYLE_ELEMENT_ID = "multica-appearance";

function clearThemeAttributes(root: HTMLElement) {
  root.removeAttribute(APPEARANCE_THEME_ATTR);
  for (const name of root.getAttributeNames()) {
    if (name.startsWith(THEME_OPTION_ATTR_PREFIX)) root.removeAttribute(name);
  }
}

function writeStyle(root: ShadowRoot, id: string, css: string) {
  let style = root.getElementById(id) as HTMLStyleElement | null;
  if (!css) {
    style?.remove();
    return;
  }
  if (!style) {
    style = document.createElement("style");
    style.id = id;
    root.appendChild(style);
  }
  if (style.textContent !== css) style.textContent = css;
}

function orderStyles(root: ShadowRoot) {
  const theme = root.getElementById(THEME_CSS_ELEMENT_ID);
  const adjustments = root.getElementById(STYLE_ELEMENT_ID);
  if (theme && adjustments && adjustments.compareDocumentPosition(theme) & Node.DOCUMENT_POSITION_FOLLOWING) {
    root.appendChild(adjustments);
  }
}

function syncModeClasses(root: HTMLElement) {
  const dark = root.classList.contains("dark");
  if (root.classList.contains("theme-dark") !== dark) root.classList.toggle("theme-dark", dark);
  if (root.classList.contains("theme-light") === dark) root.classList.toggle("theme-light", !dark);
}

export function applyAppearanceTo(root: HTMLElement, shadowRoot: ShadowRoot) {
  const prefs = selectAppearance(useAppearanceStore.getState());
  const safeMode = useThemeSafeMode.getState().active;
  const theme = activeTheme(prefs, safeMode);

  clearThemeAttributes(root);
  for (const [name, value] of Object.entries(appearanceThemeAttributes(theme, prefs))) {
    if (value !== null) root.setAttribute(name, value);
  }

  const variables = appearanceRootVariables(prefs);
  for (const [name, value] of Object.entries(variables)) {
    if (value === null) root.style.removeProperty(name);
    else root.style.setProperty(name, value);
  }

  syncModeClasses(root);

  writeStyle(shadowRoot, THEME_CSS_ELEMENT_ID, appearanceThemeCss(theme));
  writeStyle(shadowRoot, STYLE_ELEMENT_ID, appearanceStylesheet(theme, prefs));
  orderStyles(shadowRoot);
}

export function mountAppearanceIsland(
  host: HTMLElement,
  shadowRoot: ShadowRoot,
  popupHost?: HTMLElement,
  popupShadowRoot?: ShadowRoot,
): () => void {
  const applyAll = () => {
    applyAppearanceTo(host, shadowRoot);
    if (popupHost && popupShadowRoot) applyAppearanceTo(popupHost, popupShadowRoot);
  };
  applyAll();

  const unsubAppearance = useAppearanceStore.subscribe(applyAll);
  const unsubInstalled = useInstalledThemesStore.subscribe(applyAll);
  const unsubSafe = useThemeSafeMode.subscribe(applyAll);

  const onStorage = (event: StorageEvent) => {
    if (event.key === APPEARANCE_STORAGE_KEY) void useAppearanceStore.persist.rehydrate();
    if (event.key === INSTALLED_THEMES_STORAGE_KEY) void useInstalledThemesStore.persist.rehydrate();
  };
  window.addEventListener("storage", onStorage);

  // Safe mode: Shift held during load.
  let hold: number | undefined;
  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Shift" || hold !== undefined) return;
    hold = window.setTimeout(() => useThemeSafeMode.getState().enter(), 700);
  };
  const onKeyUp = (event: KeyboardEvent) => {
    if (event.key !== "Shift") return;
    window.clearTimeout(hold);
    hold = undefined;
  };
  window.addEventListener("keydown", onKeyDown);
  window.addEventListener("keyup", onKeyUp);
  const stopListening = window.setTimeout(() => window.removeEventListener("keydown", onKeyDown), 3000);

  return () => {
    unsubAppearance();
    unsubInstalled();
    unsubSafe();
    window.removeEventListener("storage", onStorage);
    window.clearTimeout(stopListening);
    window.clearTimeout(hold);
    window.removeEventListener("keydown", onKeyDown);
    window.removeEventListener("keyup", onKeyUp);
  };
}
