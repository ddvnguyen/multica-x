/**
 * The one module through which this plugin reaches the Multica fork.
 *
 * `scripts/check-fork-entry.mjs` fails the build when any other file under
 * `packages/dsh-plugin/src` imports an `@multica/*` specifier, and it also
 * checks that every specifier here is a public package export. That is the
 * provenance gate: the plugin depends on the fork's published surface, not on
 * whatever a file happens to export, so a refactor of the fork's internals can
 * never silently reach into the island.
 *
 * When the island needs another fork capability, add it here rather than at the
 * call site. Keep the list short: every entry is surface the island must keep
 * working across fork updates.
 */

// Providers and platform state.
export { CoreProvider, setCurrentWorkspace } from "@multica/core/platform";
export { WorkspaceSlugProvider } from "@multica/core/paths";
export { useWorkspaceList } from "@multica/core/workspace";

// Navigation and view state.
export { NavigationProvider } from "@multica/views/navigation";
export type { NavigationAdapter } from "@multica/views/navigation";
export { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
export { createIssueViewStore } from "@multica/core/issues/stores/view-store";

// The two surfaces this panel renders.
export {
  IssuesPage,
  IssueDetailRoute,
} from "@multica/views/issues/components";

// Shared presentation the island has to own: translations, the error boundary,
// and the portal seat that keeps overlays inside the island's shadow root.
export { RESOURCES } from "@multica/views/locales";
export { ErrorBoundary } from "@multica/ui/components/common/error-boundary";
export { PortalContainerProvider } from "@multica/ui/components/ui/portal-container";
