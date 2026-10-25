"use client"

import * as React from "react"

/**
 * Where overlay primitives portal to.
 *
 * Portals default to `document.body`, which is the right answer for an ordinary
 * page but wrong for a surface that renders its styles inside a shadow root or
 * another scoped container: the popup leaves the container and arrives
 * unstyled. A host that owns such a container provides it here, and every
 * primitive below passes it to Base UI's `container` prop.
 *
 * `null` means "no host container" and keeps Base UI's own default.
 */
const PortalContainerContext = React.createContext<HTMLElement | null>(null)

export function PortalContainerProvider({
  container,
  children,
}: {
  container: HTMLElement | null
  children: React.ReactNode
}) {
  return (
    <PortalContainerContext.Provider value={container}>
      {children}
    </PortalContainerContext.Provider>
  )
}

/** The host container overlays portal into, or `null` for `document.body`. */
export function usePortalContainer(): HTMLElement | null {
  return React.use(PortalContainerContext)
}
