import { StrictMode, useState, type CSSProperties } from "react";
import { createRoot } from "react-dom/client";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Command,
  CommandDialog,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@multica/ui/components/ui/command";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { PortalContainerProvider } from "@multica/ui/components/ui/portal-container";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import islandCss from "../../src/client/island.css?inline";
import { ISLAND_CSS, ISLAND_PROPERTY_RULES, createPopupSeat } from "../../src/client/popup-seat";
import { restoreShadowFocusGuards } from "../../src/client/focus-guards";
import { installPropertyRules } from "../../src/client/property-shim";
import { swallowShellShortcuts } from "../../src/client/shell-keys";

/**
 * The M-148 parity harness.
 *
 * One page, two renderings of the *same* compiled stylesheet:
 *
 * - `?mode=reference` puts the fork's CSS in the document, which is how
 *   upstream Multica web serves it and where the browser registers Tailwind's
 *   `@property` rules itself.
 * - `?mode=island` builds the island's boundary — host, shadow root, popup
 *   seat, the document-level property shim — the way `mountIsland` does.
 *
 * The fixtures below are forked primitives with the utility recipes the real
 * surfaces use, plus the mechanism boxes (translate, rotate, scale, blur, ring,
 * gradient, space) whose values Tailwind v4 derives from `--tw-*` initial
 * values. `?theme=dark` and `?shim=off` exist so the spec can compare the two
 * themes and prove the shim is what fixes the island.
 */

type Mode = "reference" | "island";

const params = new URLSearchParams(window.location.search);
const mode: Mode = params.get("mode") === "island" ? "island" : "reference";
const dark = params.get("theme") === "dark";
const shim = params.get("shim") !== "off";

interface Boundary {
  /** Where the fixtures render: the island's root, or a document div. */
  root: HTMLElement;
  /** Where overlays portal to; `null` keeps Base UI's `document.body` default. */
  portalHost: HTMLElement | null;
}

function stage(): HTMLElement {
  const element = document.getElementById("stage");
  if (element === null) throw new Error("parity stage is missing");
  return element;
}

/** The fork's stylesheet applied to the document, as upstream web has it. */
function referenceBoundary(): Boundary {
  const style = document.createElement("style");
  style.dataset.parityStyles = "";
  style.textContent = islandCss;
  document.head.append(style);
  if (dark) document.documentElement.classList.add("dark");

  const surface = document.createElement("div");
  surface.className = "multica-island-surface";
  const root = document.createElement("div");
  root.className = "multica-island-root";
  surface.append(root);
  stage().append(surface);
  return { root, portalHost: null };
}

/**
 * The island's boundary, minus the forked app.
 *
 * This is `mountIsland`'s shape: an inert host, one shadow root carrying the
 * rewritten sheet, the popup seat in the shell's overlay layer when there is
 * one, and the document-level `@property` registration `startIsland` performs.
 */
function islandBoundary(): Boundary {
  if (shim) installPropertyRules(document, ISLAND_PROPERTY_RULES);

  const host = document.createElement("div");
  host.dataset.parityIsland = "";
  stage().append(host);
  const shadow = host.attachShadow({ mode: "open" });

  const style = document.createElement("style");
  style.dataset.multicaStyles = "";
  style.textContent = ISLAND_CSS;

  const surface = document.createElement("div");
  surface.className = "multica-island-surface";
  // `mountIsland` follows the shell's theme by toggling this class on the
  // surface; the fork's `.dark` token block is a class rule, so it lands here
  // rather than on the host.
  if (dark) surface.classList.add("dark");
  const root = document.createElement("div");
  root.className = "multica-island-root";
  root.dataset.parityRoot = "";
  surface.append(root);
  shadow.append(style, surface);

  const seat = createPopupSeat(document);
  const portalHost =
    seat?.portalHost ??
    (() => {
      const fallback = document.createElement("div");
      fallback.className = "multica-island-portals";
      fallback.dataset.multicaPortals = "";
      surface.append(fallback);
      return fallback;
    })();
  if (seat !== null) portalHost.classList.toggle("dark", dark);

  // The guard production installs on the island's root, and on the portal
  // container its overlays render into (a dialog's input is not a child of the
  // panel, so the root's guard alone would let keystrokes out).
  swallowShellShortcuts(root);
  swallowShellShortcuts(portalHost);
  restoreShadowFocusGuards(portalHost);

  return { root, portalHost };
}

const boundary = mode === "island" ? islandBoundary() : referenceBoundary();

/** What a dsh hotkey handler would see: keys that reached the document. */
const shellKeys: string[] = [];
Object.assign(window, { __shellKeys: shellKeys });
document.addEventListener("keydown", (event) => shellKeys.push(event.key));

function CreateIssueDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="dialog-content" className="rounded-xl border bg-background shadow-lg">
        <DialogHeader>
          <DialogTitle data-testid="dialog-title" className="text-lg font-semibold">
            New issue
          </DialogTitle>
          <DialogDescription data-testid="dialog-description" className="text-sm text-muted-foreground">
            An issue belongs to this workspace.
          </DialogDescription>
        </DialogHeader>
        <Input data-testid="dialog-input" placeholder="Issue title" />
        <DialogFooter>
          <Button data-testid="dialog-submit">Create issue</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function BoardSurface() {
  return (
    <section
      data-testid="board-column"
      className="flex w-72 flex-col gap-2 rounded-lg border border-border bg-muted/40 p-2"
    >
      <div data-testid="board-card" className="rounded-lg border bg-card p-3 shadow-sm">
        <p data-testid="board-card-title" className="text-sm font-medium text-foreground">
          Fix the rail row
        </p>
        <p className="text-xs text-muted-foreground">MUL-1</p>
      </div>
      <div className="rounded-lg border bg-card p-3 shadow-sm">
        <Badge data-testid="board-badge" variant="outline">
          todo
        </Badge>
      </div>
    </section>
  );
}

function IssueSurface() {
  return (
    <section className="flex w-96 flex-col gap-2">
      <header
        data-testid="issue-header"
        className="flex items-center justify-between gap-2 border-b border-border pb-2"
      >
        <h2 data-testid="issue-title" className="text-xl font-semibold tracking-tight">
          Fix the rail row
        </h2>
        <Button data-testid="issue-action" variant="outline" size="sm">
          Open
        </Button>
      </header>
      <article data-testid="issue-body" className="rounded-xl border bg-background p-4 shadow-lg">
        <p data-testid="issue-text" className="text-sm text-muted-foreground">
          Body copy.
        </p>
      </article>
    </section>
  );
}

/** One box per `--tw-*` initial value Tailwind needs registered. */
function MechanismBoxes() {
  return (
    <section className="flex flex-wrap items-center gap-3">
      <div data-testid="fx-translate" className="translate-x-2 -translate-y-1 rounded border bg-card p-2 text-xs">
        translate
      </div>
      <div data-testid="fx-rotate" className="rotate-3 rounded border bg-card p-2 text-xs">
        rotate
      </div>
      <div data-testid="fx-scale" className="scale-95 rounded border bg-card p-2 text-xs">
        scale
      </div>
      <div data-testid="fx-blur" className="blur-[2px] rounded border bg-card p-2 text-xs">
        blur
      </div>
      <div data-testid="fx-ring" className="rounded border bg-card p-2 text-xs ring-4 ring-brand">
        ring
      </div>
      <div
        data-testid="fx-gradient"
        className="rounded bg-linear-to-r from-primary to-secondary p-2 text-xs text-primary-foreground"
      >
        gradient
      </div>
      <div data-testid="fx-space" className="space-y-2 rounded border bg-card p-2 text-xs">
        <p>space</p>
        <p data-testid="fx-space-second">second</p>
      </div>
    </section>
  );
}

function MenuSurface({ onOpenDialog }: { onOpenDialog: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger
        data-testid="menu-trigger"
        className="rounded-lg border border-border px-3 py-1.5 text-sm"
      >
        Actions…
      </DropdownMenuTrigger>
      <DropdownMenuContent
        data-testid="menu-content"
        className="w-56 rounded-md border bg-popover p-1 shadow-md"
      >
        <DropdownMenuGroup>
          <DropdownMenuLabel>Issue</DropdownMenuLabel>
          <DropdownMenuItem data-testid="menu-item">Rename</DropdownMenuItem>
        </DropdownMenuGroup>
        <DropdownMenuSeparator data-testid="menu-separator" />
        <DropdownMenuItem data-testid="menu-shortcut-item">
          Archive
          <DropdownMenuShortcut data-testid="menu-shortcut">⌘⌫</DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuItem data-testid="menu-open-dialog" onClick={onOpenDialog}>
          New issue…
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function StatusPicker({ onOpenDialog }: { onOpenDialog: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        data-testid="popover-trigger"
        className="inline-flex items-center gap-2 rounded-lg border border-border px-3 py-1.5 text-sm"
      >
        <span
          data-testid="popover-status-dot"
          className="size-2.5 rounded-full bg-success ring-2 ring-success/40"
        />
        In progress
      </PopoverTrigger>
      <PopoverContent
        data-testid="popover-content"
        keepMounted
        className="w-60 rounded-lg bg-popover shadow-md ring-1 ring-border"
      >
        <p className="px-1 text-xs text-muted-foreground">Status</p>
        <button
          type="button"
          data-testid="popover-item"
          className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted"
        >
          <span className="size-2 rounded-full bg-warning" />
          Todo
        </button>
        <button
          type="button"
          data-testid="popover-open-dialog"
          className="rounded-sm px-2 py-1.5 text-left text-sm hover:bg-muted"
          onClick={onOpenDialog}
        >
          New issue…
        </button>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The toast, as the fork renders one.
 *
 * The island mounts no `Toaster`, so there is no live toast to compare: this is
 * sonner's own recipe (`cn-toast` plus the `--normal-*` variables the fork's
 * Toaster sets) built from the same primitives, which is what the styles are
 * judged on.
 */
function ToastSurface() {
  return (
    <div
      data-testid="toast"
      className="cn-toast flex w-80 flex-col gap-1 rounded-lg border bg-popover p-3 shadow-lg"
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius)",
        } as CSSProperties
      }
    >
      <p data-testid="toast-title" className="text-sm font-medium">
        Issue created
      </p>
      <p data-testid="toast-body" className="text-xs text-muted-foreground">
        MUL-1 was added to the board.
      </p>
      <Button data-testid="toast-action" size="sm" variant="outline" className="self-start">
        Undo
      </Button>
    </div>
  );
}

function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <>
      <Button
        data-testid="palette-trigger"
        variant="outline"
        size="sm"
        onClick={() => onOpenChange(true)}
      >
        Search…
      </Button>
      <CommandDialog open={open} onOpenChange={onOpenChange} title="Command palette" description="Search">
        <Command data-testid="palette-content" className="rounded-xl bg-popover">
          <CommandInput data-testid="palette-input" placeholder="Type a command…" />
          <CommandList>
            <CommandGroup heading="Actions">
              <CommandItem data-testid="palette-item">
                Create issue
                <CommandShortcut data-testid="palette-shortcut">⌘C</CommandShortcut>
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </CommandDialog>
    </>
  );
}

function ParityFixtures() {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const openDialog = () => setDialogOpen(true);

  return (
    <div className="flex flex-col gap-4 p-4">
      <BoardSurface />
      <IssueSurface />
      <MechanismBoxes />
      <div className="flex items-center gap-3">
        <Button data-testid="dialog-trigger" onClick={openDialog}>
          New issue
        </Button>
        <MenuSurface onOpenDialog={openDialog} />
        <StatusPicker onOpenDialog={openDialog} />
        <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      </div>
      <ToastSurface />
      <CreateIssueDialog open={dialogOpen} onOpenChange={setDialogOpen} />
    </div>
  );
}

createRoot(boundary.root).render(
  <StrictMode>
    <PortalContainerProvider container={boundary.portalHost}>
      <ParityFixtures />
    </PortalContainerProvider>
  </StrictMode>,
);
