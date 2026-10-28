/**
 * Keep keystrokes inside the island.
 *
 * dsh owns document-level shortcuts. Keyboard events are composed, so a
 * keystroke typed in the panel would otherwise reach the shell and trigger its
 * bindings mid-edit — including in a dialog's input, which is why the guard sits
 * on the island's root rather than on whichever panel happens to be focused.
 *
 * Its own module because the parity harness drives this guard directly, and
 * importing `island.ts` there would pull the whole forked app into the harness
 * page for three lines of listener.
 */
export function swallowShellShortcuts(rootElement: HTMLElement): void {
  for (const type of ["keydown", "keyup", "keypress"] as const) {
    rootElement.addEventListener(type, (event) => event.stopPropagation());
  }
}
