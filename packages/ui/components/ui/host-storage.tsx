"use client"

import * as React from "react"

import type { WidthStorage } from "./sidebar"

/**
 * Where a host-owned surface keeps the widths its pages resize.
 *
 * The web app and the desktop own their origin, so their pages persist widths
 * in `localStorage` directly and mount no provider. A host that shares its
 * origin with another app — the dsh island beside the dsh shell — provides its
 * namespaced adapter here (`packages/dsh-plugin/src/client/storage.ts`), and
 * every width consumer below reads through it instead of touching the bare
 * keys (M-360).
 *
 * `undefined` means "no host": consumers fall back to their own browser
 * default, which is why the value is not defaulted to `localStorage` here —
 * a consumer that wants to disable persistence outright passes `null` itself.
 */
const HostStorageContext = React.createContext<WidthStorage | undefined>(
  undefined
)

export function HostStorageProvider({
  storage,
  children,
}: {
  storage?: WidthStorage
  children: React.ReactNode
}) {
  return (
    <HostStorageContext.Provider value={storage}>
      {children}
    </HostStorageContext.Provider>
  )
}

/** The host's width store, or `undefined` on an origin this app owns. */
export function useHostStorage(): WidthStorage | undefined {
  return React.use(HostStorageContext)
}
