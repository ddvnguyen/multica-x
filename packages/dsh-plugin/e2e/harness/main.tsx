import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { PortalContainerProvider } from "@multica/ui/components/ui/portal-container";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { createPopupSeat, ISLAND_CSS } from "../../src/client/popup-seat";

/**
 * M-116 harness: the island boundary against a fake dsh shell.
 *
 * Builds what `mountIsland` builds — host, shadow root, surface, and the
 * popup seat from the production `createPopupSeat` — and renders the fork's
 * three overlay primitives through the island's portal container. The fake
 * shell paints a blocking card into its overlay layer, the way the
 * dsh-workbuddy notice parked over the Runtime picker on lane z.
 */

const RUNTIMES = ["Claude", "DeepSeek Harness", "Pi"];

// The shell's own layout: an app column below the overlay layer, and the
// layer's children forced back to pointer-events:auto — the pattern the seat
// mirrors.
const shellStyle = document.createElement("style");
shellStyle.textContent = `
  body { margin: 0; font-family: sans-serif; }
  #shell-app { position: absolute; inset: 0; padding: 32px; }
  #island-host { width: 640px; height: 480px; }
  [data-shell-overlay] { position: absolute; inset: 0; z-index: 20; pointer-events: none; }
  [data-shell-overlay] > * { pointer-events: auto; }
  #blocking-card {
    position: fixed; z-index: 30; left: 40px; top: 420px;
    width: 320px; height: 60px;
    background: #fff; border: 1px solid #999; padding: 8px;
  }
`;
document.head.append(shellStyle);

const app = document.createElement("div");
app.id = "shell-app";

const layer = document.createElement("div");
layer.dataset.shellOverlay = "";
const card = document.createElement("div");
card.id = "blocking-card";
card.innerHTML = "<code>New version available</code>";
layer.append(card);

const host = document.createElement("div");
host.id = "island-host";
app.append(host);
document.body.append(app, layer);

// The island boundary, as mountIsland builds it.
const shadow = host.attachShadow({ mode: "open" });
const style = document.createElement("style");
style.dataset.multicaStyles = "";
style.textContent = ISLAND_CSS;
const surface = document.createElement("div");
surface.className = "multica-island-surface";
const rootElement = document.createElement("div");
rootElement.className = "multica-island-root";
surface.append(rootElement);
shadow.append(style, surface);

const seat = createPopupSeat(document);
if (seat === null) {
  throw new Error("harness requires a [data-shell-overlay] layer");
}

function OverlayDemo() {
  const [picked, setPicked] = useState("nothing");
  return (
    <div
      style={{
        padding: 16,
        display: "flex",
        flexDirection: "column",
        gap: 24,
        alignItems: "flex-start",
      }}
    >
      <div data-testid="picked">picked: {picked}</div>

      <Popover>
        <PopoverTrigger
          data-testid="popover-trigger"
          className="rounded-lg border border-border px-3 py-2"
        >
          Runtime…
        </PopoverTrigger>
        <PopoverContent data-testid="popover-content">
          {RUNTIMES.map((runtime) => (
            <button
              key={runtime}
              type="button"
              data-testid={`popover-item-${runtime}`}
              onClick={() => setPicked(runtime)}
            >
              {runtime}
            </button>
          ))}
        </PopoverContent>
      </Popover>

      <Select
        items={RUNTIMES.map((runtime) => ({ value: runtime, label: runtime }))}
        onValueChange={(value) => value && setPicked(String(value))}
      >
        <SelectTrigger data-testid="select-trigger">
          <SelectValue placeholder="Runtime…" />
        </SelectTrigger>
        <SelectContent data-testid="select-content">
          {RUNTIMES.map((runtime) => (
            <SelectItem key={runtime} value={runtime} data-testid={`select-item-${runtime}`}>
              {runtime}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <DropdownMenu>
        <DropdownMenuTrigger
          data-testid="dropdown-trigger"
          className="rounded-lg border border-border px-3 py-2"
        >
          Menu…
        </DropdownMenuTrigger>
        <DropdownMenuContent data-testid="dropdown-content">
          {RUNTIMES.map((runtime) => (
            <DropdownMenuItem
              key={runtime}
              data-testid={`dropdown-item-${runtime}`}
              onClick={() => setPicked(runtime)}
            >
              {runtime}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

createRoot(rootElement).render(
  <StrictMode>
    <PortalContainerProvider container={seat.portalHost}>
      <OverlayDemo />
    </PortalContainerProvider>
  </StrictMode>,
);
