"use client";

import { createContext, useContext, useMemo, type ReactNode } from "react";

interface PageChromeContextValue {
  titleInHostChrome: boolean;
  /** Host-selected project every scoped page filters to; null means all. */
  projectId: string | null;
}

const PageChromeContext = createContext<PageChromeContextValue>({
  titleInHostChrome: false,
  projectId: null,
});

/**
 * The page scope a host owns.
 *
 * A host that shows several Multica surfaces under one global control (the dsh
 * island's project picker) cannot express that choice as per-page URL state, so
 * it passes the project down here and every page that supports a project scope
 * narrows to it. Absent, `null` keeps each page on its own workspace scope —
 * exactly what the web app does today.
 */
export interface PageScope {
  projectId: string | null;
}

export function PageChromeProvider({
  titleInHostChrome = false,
  projectId = null,
  children,
}: {
  titleInHostChrome?: boolean;
  projectId?: string | null;
  children: ReactNode;
}) {
  const value = useMemo<PageChromeContextValue>(
    () => ({ titleInHostChrome, projectId }),
    [projectId, titleInHostChrome],
  );
  return (
    <PageChromeContext.Provider value={value}>
      {children}
    </PageChromeContext.Provider>
  );
}

export function usePageChrome(): PageChromeContextValue {
  return useContext(PageChromeContext);
}

/** The host-owned project filter, read by pages that can narrow to one project. */
export function usePageScope(): PageScope {
  const { projectId } = useContext(PageChromeContext);
  return { projectId };
}
