/**
 * Keep the shell's shortcuts out of the island.
 *
 * dsh owns document-level shortcuts. Keyboard events are composed, so a
 * keystroke typed in the panel would otherwise reach the shell and trigger its
 * bindings mid-edit — including in a dialog's input, which is why the guard sits
 * on the island's root rather than on whichever panel happens to be focused.
 *
 * `Escape` is the exception, and it has to be: Base UI dismisses every layer it
 * owns — popover, tooltip, dialog, menu — from a `keydown` listener on the
 * document, so stopping the key here left the island's popups with no keyboard
 * exit at all (M-148). Escape is also not a shortcut the shell types into a
 * field: it is the key that closes whatever is on top, in the shell and in the
 * panel alike.
 *
 * Its own module because the parity harness drives this guard directly, and
 * importing `island.ts` there would pull the whole forked app into the harness
 * page for a few lines of listener.
 */
export function swallowShellShortcuts(rootElement: HTMLElement): void {
  for (const type of ["keydown", "keyup", "keypress"] as const) {
    rootElement.addEventListener(type, (event) => {
      if (event.key === "Escape") return;
      event.stopPropagation();
    });
  }
}
