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
  ChatTabsInHeaderProvider,
  HeaderChatControls,
  HeaderChatTabs,
  useChatTabsInHeader,
} from "./chat-tabs";
export { TabHistoryButtons } from "./tab-history-buttons";
export {
  selectTabByShortcut,
  useTabSelectionShortcut,
  type TabSelectionShortcutKey,
  type TabSelectionShortcutHost,
} from "./use-tab-selection-shortcut";
export { useTabHistory, type BrowsingHistoryEntry } from "./use-tab-history";
export { useNavigationInputBindings } from "./use-navigation-input-bindings";
export {
  DesktopTabNavigationProvider,
  routeContentLinkPath as routeDesktopContentLinkPath,
  routeIssueDeepLinkPath,
} from "./desktop-navigation";
export type { DesktopNavigationHost } from "./desktop-navigation";
