// @vitest-environment jsdom
import { createRef } from "react"
import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog"
import { PortalContainerProvider } from "@multica/ui/components/ui/portal-container"

/**
 * A host that renders its styles inside a scoped container (a shadow root, say)
 * must also keep overlays there, or the popup arrives unstyled. These tests pin
 * the two halves of that contract: a provided container wins, and no provider
 * leaves Base UI's default alone.
 */

function Overlay() {
  return (
    <Dialog open>
      <DialogContent>
        <DialogTitle>Property</DialogTitle>
      </DialogContent>
    </Dialog>
  )
}

describe("PortalContainerProvider", () => {
  it("portals an overlay into the provided container", () => {
    const seat = document.createElement("div")
    document.body.append(seat)

    const { container } = render(
      <PortalContainerProvider container={seat}>
        <Overlay />
      </PortalContainerProvider>,
    )

    expect(seat.querySelector("[data-slot='dialog-content']")).not.toBeNull()
    // The provider's own tree stayed empty: nothing fell back to document.body.
    expect(container.querySelector("[data-slot='dialog-content']")).toBeNull()
  })

  it("leaves the overlay on document.body without a provider", () => {
    const { container } = render(<Overlay />)

    expect(screen.getByText("Property")).toBeInTheDocument()
    expect(container.querySelector("[data-slot='dialog-content']")).toBeNull()
  })

  it("reads the container through a ref-backed host element", () => {
    const ref = createRef<HTMLDivElement>()
    const seat = document.createElement("div")
    document.body.append(seat)

    render(
      <PortalContainerProvider container={seat}>
        <div ref={ref}>
          <Overlay />
        </div>
      </PortalContainerProvider>,
    )

    expect(ref.current?.querySelector("[data-slot='dialog-content']")).toBeNull()
    expect(seat.querySelector("[data-slot='dialog-content']")).not.toBeNull()
  })
})
