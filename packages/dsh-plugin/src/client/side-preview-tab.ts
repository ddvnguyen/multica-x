import type { HostCreateElement } from "./seat";
import { issueIdFromParams } from "./side-preview";
import { publishSidePreview } from "./side-preview-store";

/**
 * The body of dsh's side preview tab (M-336).
 *
 * dsh renders it with the shell's React 18 and hands every tab body the
 * framework-bound `useTabInfo()`, which is where the tab's live navigation
 * params and lifetime signal come from. The element below is empty on purpose:
 * the island owns everything inside it (React 19, its own shadow root), and
 * this component's whole job is to republish the container with the issue the
 * tab is showing.
 *
 * `useTabInfo()` is the shell's own hook — the module that provides it is the
 * shell's, so calling it here runs React 18's dispatcher, not the bundled React
 * 19's. Nothing else in this file calls a hook: a React 19 hook reached from a
 * React 18 render would find no dispatcher at all.
 */

/** The subset of dsh's tab information this body reads. */
interface TabInfoProps {
  useTabInfo?: () => {
    tab: {
      signal: AbortSignal;
      navigation: { params: unknown; revision: number };
    };
  };
}

export function createIssuePreviewBody(createElement: HostCreateElement) {
  return function MulticaIssuePreviewBody(props: TabInfoProps) {
    const info = props.useTabInfo?.();
    const issueId = issueIdFromParams(info?.tab.navigation.params);
    // A ref identity that changes per render is deliberate: React calls the
    // outgoing ref with `null` and the incoming one with the same node, so
    // every render republishes the container (the node is stable, the issue may
    // not be) without ever detaching the portal. `null` is ignored — the tab's
    // own signal withdraws the target when the record disappears.
    const ref = (node: HTMLElement | null): void => {
      if (node === null) return;
      publishSidePreview(node, issueId, info?.tab.signal);
    };
    return createElement("div", {
      ref,
      "data-multica-issue-preview": "",
      style: { height: "100%", minHeight: 0, display: "block" },
    });
  };
}
