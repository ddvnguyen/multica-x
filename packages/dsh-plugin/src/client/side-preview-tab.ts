import type { HostCreateElement } from "./seat";
import { sidePreviewTargetFromParams } from "./side-preview";
import { publishSidePreview } from "./side-preview-store";

/**
 * The body of dsh's side preview tab (M-336, generalized in G6).
 */

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
    const target = sidePreviewTargetFromParams(info?.tab.navigation.params);
    const ref = (node: HTMLElement | null): void => {
      if (node === null) return;
      publishSidePreview(node, target, info?.tab.signal);
    };
    return createElement("div", {
      ref,
      "data-multica-issue-preview": "",
      style: { height: "100%", minHeight: 0, display: "block" },
    });
  };
}
