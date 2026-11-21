"use client";

import { Suspense } from "react";
import { DashboardLayout } from "@multica/views/layout";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { SearchCommand } from "@multica/views/search";
import { IssueOpeningCapabilityProvider } from "@multica/views/issues/issue-opening-capability";
import { FloatingChat } from "@multica/views/chat/floating-chat";
import { StarPrompt } from "@multica/views/star-prompt";
import { WhatsNewDeck } from "@multica/views/whats-new";
import { WebNotificationBridge } from "@/components/web-notification-bridge";
import {
  WebTabProvider,
  WebTabShell,
  WebTabStrip,
} from "@/components/web-tab-shell";
import { WorkspaceDocumentTitle } from "@/platform/workspace-document-title";

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <>
      {/* useSearchParams requires a Suspense boundary in the app router. Sits
          outside DashboardLayout so the tab is named while the guard is still
          resolving the workspace. */}
      <Suspense fallback={null}>
        <WorkspaceDocumentTitle />
      </Suspense>
      {/* The tab adapter wraps the whole dashboard, not just the page: the
          strip mounts in the layout's header slot, and the rail and section
          panel navigate through the same sessions the strip shows (M-413). */}
      <WebTabProvider>
        {/* The dashboard is a tabbed host with an in-place full page as well,
            so all three open modes stay on offer (M-480). Inside the tab
            provider on purpose: the capability's New tab opener is the web tab
            adapter's `openInNewTab`, which writes through `useTabStore.openTab`
            and so reuses the tab already showing the issue. */}
        <IssueOpeningCapabilityProvider supportsTabs supportsPage>
          <DashboardLayout
            header={<WebTabStrip />}
            loadingIndicator={<MulticaIcon mode="think" size={40} />}
            extra={
              <>
                <SearchCommand />
                <WebNotificationBridge />
                <FloatingChat />
                <StarPrompt />
                <WhatsNewDeck />
              </>
            }
          >
            <WebTabShell>{children}</WebTabShell>
          </DashboardLayout>
        </IssueOpeningCapabilityProvider>
      </WebTabProvider>
    </>
  );
}
