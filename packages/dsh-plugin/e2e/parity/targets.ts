/**
 * What the parity harness measures (M-148).
 *
 * Each surface names the elements whose computed styles are compared between
 * the two renderings of the same stylesheet — the fork's own CSS applied to a
 * document (how upstream Multica web serves it) and the island's shadow roots.
 * The elements are chosen for the mechanisms Tailwind v4 expresses through
 * `@property`: `border` reads `--tw-border-style`, `shadow-*` and `ring-*` build
 * their lists from `--tw-shadow*`, `translate-*`/`rotate-*`/`scale-*` from the
 * transform variables, `blur-*` from `--tw-blur`, gradients from
 * `--tw-gradient-*`, `space-*` from `--tw-space-y-reverse`. A surface that only
 * looked at colours would pass while every border in the panel disappeared.
 *
 * The properties are the five the card names plus the two that make a mismatch
 * legible (`color`, `border-top-width`), and the mechanism-specific ones below.
 */

/** The style list every box-shaped target is compared on. */
const BOX = [
  "border-top-style",
  "border-top-width",
  "box-shadow",
  "border-top-left-radius",
  "font-family",
  "background-color",
  "color",
] as const;

/** The five in the card, for targets that are text rather than a box. */
const TEXT = ["font-family", "background-color", "color", "border-top-style"] as const;

export const STYLE_PROPERTIES = [
  ...new Set([
    ...BOX,
    ...TEXT,
    // Tailwind v4 writes these three as their own properties, not as
    // `transform`, and each one is assembled from `--tw-*` initial values.
    "translate",
    "rotate",
    "scale",
    "filter",
    "background-image",
    "margin-top",
    "border-bottom-style",
  ]),
] as const;

export type StyleProperty = (typeof STYLE_PROPERTIES)[number];

export interface ParityTarget {
  /** The fixture's `data-testid`, and the property list it is judged on. */
  testId: string;
  properties: readonly StyleProperty[];
}

export interface ParitySurface {
  id: string;
  /**
   * The control that opens the surface, when it is an overlay. Static surfaces
   * need none — their targets are on screen as the page loads.
   */
  trigger?: string;
  targets: readonly ParityTarget[];
}

export const PARITY_SURFACES: readonly ParitySurface[] = [
  {
    id: "board",
    targets: [
      { testId: "board-column", properties: BOX },
      { testId: "board-card", properties: BOX },
      { testId: "board-card-title", properties: TEXT },
      { testId: "board-badge", properties: BOX },
    ],
  },
  {
    id: "issue",
    targets: [
      { testId: "issue-header", properties: [...BOX, "border-bottom-style"] },
      { testId: "issue-title", properties: TEXT },
      { testId: "issue-body", properties: BOX },
      { testId: "issue-text", properties: TEXT },
      { testId: "issue-action", properties: BOX },
    ],
  },
  {
    id: "mechanisms",
    targets: [
      { testId: "fx-translate", properties: ["translate", "border-top-style"] },
      { testId: "fx-rotate", properties: ["rotate", "border-top-style"] },
      { testId: "fx-scale", properties: ["scale", "border-top-style"] },
      { testId: "fx-blur", properties: ["filter", "border-top-style"] },
      { testId: "fx-ring", properties: ["box-shadow", "border-top-style"] },
      { testId: "fx-gradient", properties: ["background-image", "border-top-style"] },
      { testId: "fx-space-second", properties: ["margin-top", "border-top-style"] },
    ],
  },
  {
    id: "dialog",
    trigger: "dialog-trigger",
    targets: [
      { testId: "dialog-content", properties: BOX },
      { testId: "dialog-title", properties: TEXT },
      { testId: "dialog-input", properties: BOX },
      { testId: "dialog-submit", properties: BOX },
    ],
  },
  {
    id: "menu",
    trigger: "menu-trigger",
    targets: [
      { testId: "menu-content", properties: BOX },
      { testId: "menu-item", properties: BOX },
      { testId: "menu-separator", properties: ["background-color", "border-top-style"] },
      { testId: "menu-shortcut", properties: TEXT },
    ],
  },
  {
    id: "popover",
    trigger: "popover-trigger",
    targets: [
      { testId: "popover-content", properties: BOX },
      { testId: "popover-item", properties: BOX },
      { testId: "popover-status-dot", properties: [...BOX, "box-shadow"] },
      { testId: "popover-trigger", properties: BOX },
    ],
  },
  {
    id: "toast",
    targets: [
      { testId: "toast", properties: BOX },
      { testId: "toast-title", properties: TEXT },
      { testId: "toast-body", properties: TEXT },
      { testId: "toast-action", properties: BOX },
    ],
  },
  {
    id: "palette",
    trigger: "palette-trigger",
    targets: [
      { testId: "palette-content", properties: BOX },
      { testId: "palette-input", properties: BOX },
      { testId: "palette-item", properties: BOX },
      { testId: "palette-shortcut", properties: TEXT },
    ],
  },
];
