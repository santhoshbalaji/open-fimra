import { Directive, ElementRef, effect, inject, input } from '@angular/core';

/** Draws Lucide shapes on the host SVG with the shared outline style. */
@Directive({
  host: {
    'width': '24', 'height': '24', 'viewBox': '0 0 24 24', 'fill': 'none', 'stroke': 'currentColor',
    'stroke-width': '2', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', '[attr.data-icon]': 'fimraIcon()'
  }
})
export abstract class IconRenderer<Name extends string> {
  protected abstract shapes(name: Name): string;
  readonly fimraIcon = input.required<Name>();

  constructor() {
    const svg = inject<ElementRef<SVGSVGElement>>(ElementRef).nativeElement;
    effect(() => { svg.innerHTML = this.shapes(this.fimraIcon()); });
  }
}
