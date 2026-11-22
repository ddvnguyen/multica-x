export {
  useTabStore,
  getActiveTab,
  useActiveGroup,
  useActiveTabIdentity,
  useActiveTabUrl,
  useActiveTabHistory,
  useActiveBrowsingHistory,
  useActiveBrowsingHistoryTitles,
  splitTabUrl,
  resourceKeyForUrl,
  browsingHistoryKeyForUrl,
  sanitizeTabPath,
  scrollMementoKey,
  emptyMemento,
  mergePersistedTabs,
  type TabSession,
  type Tab,
  type TabMemento,
  type ScrollMementoEntry,
  type WorkspaceTabGroup,
} from "@multica/core/tabs";
export {
  createWindowTabStorage,
  mergeTabGroups,
  type WindowTabStorage,
} from "@multica/core/tabs";
export { TabBar, type TabBarHostCapabilities } from "./tab-bar";
export {
  beginTabSwitch,
  markTabSwitchStore,
  markTabSwitchReplace,
  markTabSwitchCommit,
  finishTabSwitchPaint,
  resetTabSwitchPerfForTests,
} from "./tab-switch-perf";
export {
  ChatTabLeading,
  ChatTabsInHeaderProvider,
  HeaderChatControls,
  chatSessionHref,
  openChatSessionTab,
  useChatTabState,
  useChatTabsInHeader,
} from "./chat-tabs";
export { TabHistoryButtons } from "./tab-history-buttons";
export {
  selectTabByShortcut,
  useTabSelectionShortcut,
  type TabSelectionShortcutKey,
  type TabSelectionShortcutHost,
} from "./use-tab-selection-shortcut";
export {
  useTabHistory,
  useWindowTabHistory,
  type BrowsingHistoryEntry,
} from "./use-tab-history";
export { useNavigationInputBindings } from "./use-navigation-input-bindings";
export {
  DesktopTabNavigationProvider,
  routeContentLinkPath as routeDesktopContentLinkPath,
  routeIssueDeepLinkPath,
} from "./desktop-navigation";
export type { DesktopNavigationHost } from "./desktop-navigation";
