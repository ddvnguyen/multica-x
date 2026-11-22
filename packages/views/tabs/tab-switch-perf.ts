/**
 * User-timing for one tab switch at a time (M-519).
 *
 * A tab click spans two owners: the strip writes the store, and the web
 * router sync projects the URL onto Next.js. A mark written on one side and
 * read on the other has to live in a module both can import, so it lives here
 * in `views/tabs` (no framework API, no store). Desktop mounts the strip too;
 * the marks are harmless there and the perf scenario reads them on web.
 *
 * Names are fixed and cleared at the start of each switch: the scenario
 * clicks one tab at a time and reads the last measurement, so there is no
 * per-switch allocation and no growing list of entries in a long session.
 */

const PREFIX = "tab-switch";

const MARK = {
  click: `${PREFIX}:click`,
  store: `${PREFIX}:store`,
  replace: `${PREFIX}:replace`,
  commit: `${PREFIX}:commit`,
  paint: `${PREFIX}:paint`,
} as const;

type Phase = keyof typeof MARK;

const SPANS: ReadonlyArray<{
  name: string;
  from: Phase;
  to: Phase;
}> = [
  { name: "click-to-store", from: "click", to: "store" },
  { name: "store-to-replace", from: "store", to: "replace" },
  { name: "replace-to-commit", from: "replace", to: "commit" },
  { name: "commit-to-paint", from: "commit", to: "paint" },
  { name: "total", from: "click", to: "paint" },
];

/** URL of the switch in flight, or null when none is pending. */
let pendingTarget: string | null = null;
/** Frames waiting to close the measurement once the route has committed. */
let firstPaintFrame = 0;
let secondPaintFrame = 0;

function available(): boolean {
  return (
    typeof performance !== "undefined" &&
    typeof performance.mark === "function" &&
    typeof performance.measure === "function"
  );
}

/** Compare the location part of two URLs; search/hash ordering must not matter. */
function locationOf(url: string): string {
  const withoutHash = url.split("#", 1)[0] ?? url;
  const question = withoutHash.indexOf("?");
  return question === -1 ? withoutHash : withoutHash.slice(0, question);
}

/** Start a switch: clear the previous sample and mark the click. */
export function beginTabSwitch(targetUrl: string): void {
  pendingTarget = targetUrl;
  cancelPaintFrames();
  if (!available()) return;
  for (const name of Object.values(MARK)) performance.clearMarks(name);
  for (const span of SPANS) performance.clearMeasures(`${PREFIX}:${span.name}`);
  performance.mark(MARK.click);
}

/** The store write that activates the clicked tab has landed. */
export function markTabSwitchStore(): void {
  if (!available() || pendingTarget === null) return;
  performance.mark(MARK.store);
}

/** The router has been asked to navigate to the target URL. */
export function markTabSwitchReplace(): void {
  if (!available() || pendingTarget === null) return;
  performance.mark(MARK.replace);
}

/**
 * The App Router committed `url`. Returns true only for the URL this switch
 * is waiting for, so an unrelated navigation cannot close the measurement.
 */
export function markTabSwitchCommit(url: string): boolean {
  if (pendingTarget === null || locationOf(url) !== locationOf(pendingTarget)) {
    return false;
  }
  pendingTarget = null;
  if (available()) performance.mark(MARK.commit);
  schedulePaint();
  return true;
}

function cancelPaintFrames(): void {
  if (typeof cancelAnimationFrame === "function") {
    if (firstPaintFrame) cancelAnimationFrame(firstPaintFrame);
    if (secondPaintFrame) cancelAnimationFrame(secondPaintFrame);
  }
  firstPaintFrame = 0;
  secondPaintFrame = 0;
}

/**
 * Paint is measured two frames after the commit, not from a React effect: the
 * sync effect can re-run for an unrelated URL update, and its cleanup would
 * otherwise cancel the frames before the page got a chance to paint. The
 * frames live at module scope so a commit closes its own measurement.
 */
function schedulePaint(): void {
  cancelPaintFrames();
  if (typeof requestAnimationFrame !== "function") {
    finishTabSwitchPaint();
    return;
  }
  firstPaintFrame = requestAnimationFrame(() => {
    secondPaintFrame = requestAnimationFrame(() => {
      firstPaintFrame = 0;
      secondPaintFrame = 0;
      finishTabSwitchPaint();
    });
  });
}

/**
 * The new page has painted (called from the second frame after commit).
 * Returns the spans in milliseconds for the caller to log or assert on.
 */
export function finishTabSwitchPaint(): Record<string, number> | null {
  if (!available()) return null;
  performance.mark(MARK.paint);
  const result: Record<string, number> = {};
  for (const span of SPANS) {
    const name = `${PREFIX}:${span.name}`;
    try {
      performance.measure(name, MARK[span.from], MARK[span.to]);
    } catch {
      // A mark from an earlier, abandoned switch is missing: skip the span.
      continue;
    }
    result[span.name] =
      performance.getEntriesByName(name, "measure").at(-1)?.duration ?? 0;
  }
  return result;
}

/** Test-only: drop the in-flight switch and its marks. */
export function resetTabSwitchPerfForTests(): void {
  pendingTarget = null;
  cancelPaintFrames();
  if (!available()) return;
  for (const name of Object.values(MARK)) performance.clearMarks(name);
  for (const span of SPANS) performance.clearMeasures(`${PREFIX}:${span.name}`);
}
