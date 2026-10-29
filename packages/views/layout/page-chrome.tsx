"use client";

import { createContext, useContext, type ReactNode } from "react";

interface PageChromeContextValue {
  titleInHostChrome: boolean;
}

const PageChromeContext = createContext<PageChromeContextValue>({
  titleInHostChrome: false,
});

export function PageChromeProvider({
  titleInHostChrome = false,
  children,
}: {
  titleInHostChrome?: boolean;
  children: ReactNode;
}) {
  return (
    <PageChromeContext.Provider value={{ titleInHostChrome }}>
      {children}
    </PageChromeContext.Provider>
  );
}

export function usePageChrome(): PageChromeContextValue {
  return useContext(PageChromeContext);
}
