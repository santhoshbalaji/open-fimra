/** Focus a connected target, falling back when the preferred target has gone or cannot take focus. */
export function restoreOverlayFocus(preferred: HTMLElement | null, fallback?: HTMLElement | null): void {
  for (const target of [preferred, fallback]) {
    if (!target?.isConnected) continue;
    target.focus({ preventScroll: true });
    if (target.ownerDocument.activeElement === target) return;
  }
}
