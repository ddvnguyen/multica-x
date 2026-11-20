"use client"

import * as React from "react"
import * as ResizablePrimitive from "react-resizable-panels"

import { cn } from "@multica/ui/lib/utils"

function layoutsEqual(a: ResizablePrimitive.Layout, b: ResizablePrimitive.Layout): boolean {
  const keys = Object.keys(a)
  if (keys.length !== Object.keys(b).length) return false
  // Pixel-constrained panels round through the group's size, so compare with
  // a tolerance instead of exact equality.
  return keys.every((key) => Math.abs((a[key] ?? 0) - (b[key] ?? 0)) < 0.01)
}

/**
 * Re-applies the saved `defaultLayout` once the group first measures non-zero.
 *
 * `react-resizable-panels` derives the mount layout from the measured group
 * size: a group that mounts at 0px wide (the dsh island's shadow host before
 * first layout) defers, then rebuilds the layout from the panels'
 * `defaultSize` on the first real measurement and fires `onLayoutChanged` —
 * overwriting the saved layout with the defaults (M-360). Re-applying the
 * saved percentages once through the group ref puts the remembered widths
 * back, and the same `onLayoutChanged` path writes them back to storage.
 * A group mounted at full width matches without work, so web and desktop
 * behaviour is unchanged, and a group with no saved layout is left alone.
 */
export function useDeferredLayoutRestore({
  elementRef,
  groupRef,
  savedLayout,
}: {
  elementRef: React.RefObject<HTMLDivElement | null>
  groupRef: React.RefObject<ResizablePrimitive.GroupImperativeHandle | null>
  savedLayout: ResizablePrimitive.Layout | undefined
}): void {
  const savedRef = React.useRef(savedLayout)
  const restoredRef = React.useRef(false)
  React.useLayoutEffect(() => {
    const saved = savedRef.current
    // The saved layout is read once: a later change means another tab wrote
    // storage, and clobbering the live layout with it would fight the user.
    if (!saved || restoredRef.current) return
    const element = elementRef.current
    if (!element) return
    const attempt = (): boolean => {
      if (restoredRef.current) return true
      const handle = groupRef.current
      // The group's own observer has not measured yet; its `setLayout` is a
      // no-op until then, so wait for the next notification.
      if (!handle || element.getBoundingClientRect().width <= 0) return false
      let before: ResizablePrimitive.Layout
      try {
        before = handle.getLayout()
      } catch {
        return false
      }
      if (layoutsEqual(before, saved)) {
        restoredRef.current = true
        return true
      }
      try {
        handle.setLayout(saved)
      } catch {
        return false
      }
      // A successful call changes the layout (or it already matched); a
      // still-deferred call changes nothing — keep waiting for that one.
      try {
        if (!layoutsEqual(handle.getLayout(), before)) restoredRef.current = true
      } catch {
        restoredRef.current = true
      }
      return restoredRef.current
    }
    if (attempt()) return
    // Observer notifications are event-driven (one per resize), so an
    // unrestored group retries without a polling loop. Defer the retry until
    // the next frame so every ResizeObserver callback for the first real
    // measurement (including the panel library's deferred-layout callback)
    // completes before we decide the saved layout is already restored.
    let frame: number | null = null
    const observer = new ResizeObserver(() => {
      if (frame !== null) cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        frame = null
        if (attempt()) observer.disconnect()
      })
    })
    observer.observe(element)
    return () => {
      if (frame !== null) cancelAnimationFrame(frame)
      observer.disconnect()
    }
  }, [elementRef, groupRef])
}

function mergeElementRefs<T>(
  ...refs: Array<React.Ref<T> | undefined>
): React.RefCallback<T> {
  return (value: T | null) => {
    for (const ref of refs) {
      if (typeof ref === "function") {
        ref(value)
      } else if (ref) {
        ref.current = value
      }
    }
  }
}

 function ResizablePanelGroup({
   className,
  elementRef: elementRefProp,
  groupRef,
  defaultLayout,
   ...props
 }: ResizablePrimitive.GroupProps) {
  // The seam restores the saved layout itself: every page (issue detail,
  // project detail, object pages, builder) fixes the deferred-mount overwrite
  // without wiring anything at each call site.
  const elementRef = React.useRef<HTMLDivElement | null>(null)
  const groupHandleRef = React.useRef<ResizablePrimitive.GroupImperativeHandle | null>(null)
  const [, forceUpdate] = React.useReducer((count: number) => count + 1, 0)
  const handleGroupRef = React.useCallback(
    (handle: ResizablePrimitive.GroupImperativeHandle | null) => {
      groupHandleRef.current = handle
      if (typeof groupRef === "function") {
        groupRef(handle)
      } else if (groupRef) {
        groupRef.current = handle
      }
      // The imperative handle appears after mount; re-render so the restore
      // hook below sees it.
      forceUpdate()
    },
    [groupRef],
  )
  useDeferredLayoutRestore({ elementRef, groupRef: groupHandleRef, savedLayout: defaultLayout })
  return (
    <ResizablePrimitive.Group
      data-slot="resizable-panel-group"
      // The library otherwise injects `*, *:hover { cursor: … !important }`
      // while dragging, and narrows it to a one-way arrow the moment a panel
      // hits a bound — so the cursor flips between two icons mid-drag. It is
      // truthful, but it reads as a glitch. The handle carries `cursor-col-resize`
      // instead, which stays put.
      disableCursor
      elementRef={mergeElementRefs(elementRef, elementRefProp)}
      groupRef={handleGroupRef}
      defaultLayout={defaultLayout}
      className={cn(
        "flex h-full w-full aria-[orientation=vertical]:flex-col",
        // A drag captures the pointer, which then travels across the panels —
        // away from the handle that carries the cursor. `:has` puts the same
        // cursor on the whole group for as long as a separator is active, which
        // is what the library's global rule was doing before `disableCursor`
        // turned it off. Without this the cursor reverts to a text caret the
        // moment the pointer leaves the 8px handle.
        "[&:has([data-separator=active])_*]:cursor-col-resize [&:has([data-separator=active])]:cursor-col-resize",
        "aria-[orientation=vertical]:[&:has([data-separator=active])_*]:cursor-row-resize aria-[orientation=vertical]:[&:has([data-separator=active])]:cursor-row-resize",
        className
      )}
      {...props}
    />
  )
}

function ResizablePanel({ ...props }: ResizablePrimitive.PanelProps) {
  return <ResizablePrimitive.Panel data-slot="resizable-panel" {...props} />
}

function ResizableHandle({
  withHandle,
  className,
  ...props
}: ResizablePrimitive.SeparatorProps & {
  withHandle?: boolean
}) {
  return (
    <ResizablePrimitive.Separator
      data-slot="resizable-handle"
      className={cn(
        // The only cursor source: the group opts out of the library's own
        // (see ResizablePanelGroup). One icon for hover and for the whole drag.
        "cursor-col-resize aria-[orientation=horizontal]:cursor-row-resize",
        "relative flex w-0 items-center justify-center before:absolute before:inset-y-0 before:left-1/2 before:w-px before:-translate-x-1/2 before:bg-transparent before:transition-colors hover:before:bg-foreground/15 data-[separator=active]:before:bg-foreground/15 after:absolute after:inset-y-0 after:left-1/2 after:w-2 after:-translate-x-1/2 focus-visible:outline-hidden aria-[orientation=horizontal]:h-0 aria-[orientation=horizontal]:w-full aria-[orientation=horizontal]:before:inset-x-0 aria-[orientation=horizontal]:before:inset-y-auto aria-[orientation=horizontal]:before:h-px aria-[orientation=horizontal]:before:w-full aria-[orientation=horizontal]:before:translate-x-0 aria-[orientation=horizontal]:after:left-0 aria-[orientation=horizontal]:after:h-2 aria-[orientation=horizontal]:after:w-full aria-[orientation=horizontal]:after:translate-x-0 aria-[orientation=horizontal]:after:-translate-y-1/2",
        className
      )}
      {...props}
    >
      {withHandle && (
        <div className="z-10 flex h-6 w-1 shrink-0 rounded-lg bg-border" />
      )}
    </ResizablePrimitive.Separator>
  )
}

export { ResizableHandle, ResizablePanel, ResizablePanelGroup }
