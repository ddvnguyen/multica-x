// @vitest-environment jsdom
import { act, render } from "@testing-library/react"
import { useDefaultLayout } from "react-resizable-panels"
import { afterEach, describe, expect, it, vi } from "vitest"
import { HostStorageProvider } from "@multica/ui/components/ui/host-storage"
import {
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable"
import { useHostStorage } from "@multica/ui/components/ui/host-storage"

/**
 * M-360: a group that mounts at 0px wide (the dsh island's shadow host before
 * first layout) defers its saved layout, then rebuilds from the panels'
 * `defaultSize` on the first real measurement — and `onLayoutChanged` writes
 * those defaults back over the saved layout. The seam re-applies the saved
 * percentages once the group measures non-zero, so the remembered widths win
 * and storage ends with the saved layout, not the defaults.
 *
 * jsdom never lays out, so the test drives the observers directly: the
 * library's own observer gets a first real entry (panels summing to 1544px),
 * then the seam's observer gets its entry. A capturing `ResizeObserver`
 * stands in for the suite stub because the stub delivers empty entry lists,
 * which the library ignores.
 */

const LAYOUT_ID = "multica_test_deferred_layout"
const SAVED = { content: 72.798, sidebar: 27.202 }
const GROUP_WIDTH = 1544

type ObserverRecord = { callback: ResizeObserverCallback; targets: Element[] }
let records: ObserverRecord[]
let animationFrames: FrameRequestCallback[]

function installCapturingObserver(): void {
  records = []
  animationFrames = []
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    animationFrames.push(callback)
    return animationFrames.length
  })
  vi.stubGlobal("cancelAnimationFrame", () => {})
  class CapturingObserver {
    callback: ResizeObserverCallback
    targets: Element[] = []
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback
      records.push(this)
    }
    observe(target: Element): void {
      this.targets.push(target)
    }
    unobserve(): void {}
    disconnect(): void {}
  }
  vi.stubGlobal("ResizeObserver", CapturingObserver)
}

function memoryStore(seed: string | null): { storage: Storage; writes: string[]; value: () => string | null } {
  let value = seed
  const writes: string[] = []
  const storage = {
    get length() {
      return value === null ? 0 : 1
    },
    clear: () => {
      value = null
    },
    getItem: () => value,
    key: () => null,
    removeItem: () => {
      value = null
    },
    setItem: (_key: string, next: string) => {
      writes.push(next)
      value = next
    },
  } as unknown as Storage
  return { storage, writes, value: () => value }
}

function Split() {
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: LAYOUT_ID,
    storage: useHostStorage(),
  })
  return (
    <ResizablePanelGroup
      orientation="horizontal"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel id="content" minSize="50%">
        content
      </ResizablePanel>
      <ResizablePanel
        id="sidebar"
        defaultSize={320}
        minSize={260}
        maxSize={420}
        collapsible
        groupResizeBehavior="preserve-pixel-size"
      >
        sidebar
      </ResizablePanel>
    </ResizablePanelGroup>
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("deferred layout restore", () => {
  it("re-applies the saved layout after a 0px mount overwrites it", async () => {
    installCapturingObserver()
    const { storage, writes, value } = memoryStore(JSON.stringify(SAVED))
    const host = document.createElement("div")
    document.body.append(host)

    let unmount!: () => void
    await act(async () => {
      ;({ unmount } = render(
        <HostStorageProvider storage={storage}>
          <Split />
        </HostStorageProvider>,
        { container: host },
      ))
    })

    const group = host.querySelector<HTMLElement>('[data-group="true"]')!
    expect(group).not.toBeNull()
    // The mount measured 0px (jsdom never lays out); the saved layout still
    // applies to the panels, which is the pre-measurement state.
    expect(
      (host.querySelector('[data-panel][id="sidebar"]') as HTMLElement).style
        .flexGrow,
    ).toBe("27.202")

    // The island's first real layout: panels summing to the group width.
    const panels = [...group.querySelectorAll<HTMLElement>("[data-panel]")]
    for (const panel of panels) {
      const width = panel.getAttribute("id") === "sidebar" ? 320 : 1224
      Object.defineProperty(panel, "offsetWidth", {
        configurable: true,
        get: () => width,
      })
    }
    group.getBoundingClientRect = () =>
      ({
        left: 0,
        right: GROUP_WIDTH,
        width: GROUP_WIDTH,
        top: 0,
        bottom: 100,
        height: 100,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      }) as DOMRect

    // Effect ordering puts the seam's observer after the library's live one:
    // the stale registration observes the group first, the seam second, the
    // live library registration last.
    const watching = records.filter((record) => record.targets.includes(group))
    expect(watching.length).toBeGreaterThanOrEqual(2)
    const library = watching[watching.length - 1]!
    const seam = watching[watching.length - 2]!

    // The library's first real measurement rebuilds from `defaultSize` and
    // commits the defaults over the saved layout.
    await act(async () => {
      library.callback(
        [{ target: group } as unknown as ResizeObserverEntry],
        library as unknown as ResizeObserver,
      )
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, 250)
      await promise
    })
    expect(writes).toContain(
      JSON.stringify({ content: 79.275, sidebar: 20.725 }),
    )

    // The seam's observer then re-applies the saved percentages; storage ends
    // with the remembered widths, not the rebuilt defaults.
    await act(async () => {
      seam.callback(
        [{ target: group } as unknown as ResizeObserverEntry],
        seam as unknown as ResizeObserver,
      )
      animationFrames.splice(0).forEach((callback) => callback(0))
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, 300)
      await promise
    })
    expect(
      (host.querySelector('[data-panel][id="sidebar"]') as HTMLElement).style
        .flexGrow,
    ).toBe("27.202")
    expect(value()).toBe(JSON.stringify(SAVED))

    await act(async () => {
      unmount()
    })
    host.remove()
  })

  it("keeps the saved layout when reopened before the deferred measurement callback", async () => {
    installCapturingObserver()
    const { storage, value } = memoryStore(JSON.stringify(SAVED))
    const host = document.createElement("div")
    document.body.append(host)

    const mount = async () => {
      const container = document.createElement("div")
      host.append(container)
      let unmount!: () => void
      await act(async () => {
        ;({ unmount } = render(
          <HostStorageProvider storage={storage}>
            <Split />
          </HostStorageProvider>,
          { container },
        ))
      })
      const group = container.querySelector<HTMLElement>('[data-group="true"]')!
      const panels = [...group.querySelectorAll<HTMLElement>("[data-panel]")]
      for (const panel of panels) {
        const width = panel.getAttribute("id") === "sidebar" ? 320 : 1224
        Object.defineProperty(panel, "offsetWidth", {
          configurable: true,
          get: () => width,
        })
      }
      group.getBoundingClientRect = () =>
        ({
          left: 0,
          right: GROUP_WIDTH,
          width: GROUP_WIDTH,
          top: 0,
          bottom: 100,
          height: 100,
          x: 0,
          y: 0,
          toJSON: () => ({}),
        }) as DOMRect
      const watching = records.filter((record) => record.targets.includes(group))
      return { container, group, unmount, library: watching[watching.length - 1]!, seam: watching[watching.length - 2]! }
    }

    // First open: let the library measure and the seam restore, then close it.
    let opened = await mount()
    await act(async () => {
      opened.library.callback(
        [{ target: opened.group } as unknown as ResizeObserverEntry],
        opened.library as unknown as ResizeObserver,
      )
      opened.seam.callback(
        [{ target: opened.group } as unknown as ResizeObserverEntry],
        opened.seam as unknown as ResizeObserver,
      )
      animationFrames.splice(0).forEach((callback) => callback(0))
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, 300)
      await promise
    })
    expect(value()).toBe(JSON.stringify(SAVED))
    await act(async () => opened.unmount())
    opened.container.remove()

    // Reopen at 0px. The seam observer can be delivered before the panel
    // library's deferred observer; restoration must wait until the next frame.
    records = []
    animationFrames = []
    opened = await mount()
    await act(async () => {
      opened.seam.callback(
        [{ target: opened.group } as unknown as ResizeObserverEntry],
        opened.seam as unknown as ResizeObserver,
      )
      opened.library.callback(
        [{ target: opened.group } as unknown as ResizeObserverEntry],
        opened.library as unknown as ResizeObserver,
      )
      animationFrames.splice(0).forEach((callback) => callback(0))
      const { promise, resolve } = Promise.withResolvers<void>()
      setTimeout(resolve, 300)
      await promise
    })
    expect(value()).toBe(JSON.stringify(SAVED))

    await act(async () => opened.unmount())
    opened.container.remove()
    host.remove()
  })
})
