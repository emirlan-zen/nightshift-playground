// A tiny module-level dirty-navigation guard. BrowserRouter has no useBlocker,
// so views with unsaved state register a message here and the AppShell nav
// asks before navigating away. One editor at a time is plenty for this app.

let guardMsg: string | null = null;

export function setNavGuard(msg: string | null) {
  guardMsg = msg;
}

/** true = ok to navigate (no guard, or the operator confirmed the discard). */
export function confirmNav(): boolean {
  if (!guardMsg) return true;
  return window.confirm(guardMsg);
}
