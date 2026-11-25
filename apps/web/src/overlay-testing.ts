/**
 * jsdom lacks the layout APIs that Helm select lists and dialogs call. Tests that
 * open them install these no-op versions first.
 */
export function stubLayoutApis(): void {
  globalThis.ResizeObserver ??= class {
    observe(): void { /* No layout in jsdom. */ }
    unobserve(): void { /* No layout in jsdom. */ }
    disconnect(): void { /* No layout in jsdom. */ }
  };
  Element.prototype.scrollIntoView ??= () => undefined;
}
