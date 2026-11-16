"use client";

import { TabBar as SharedTabBar } from "@multica/views/tabs";
import { parseIssueWindowPath } from "../../../shared/issue-window";

/** Desktop tab strip: the shared strip with detached-window support. */
export function TabBar() {
  return (
    <SharedTabBar
      host={{
        parseIssueWindowPath: (url) => parseIssueWindowPath(url),
        openIssueWindow: ({ path, title }) => {
          void window.desktopAPI.openIssueWindow({ path, title });
        },
      }}
    />
  );
}
