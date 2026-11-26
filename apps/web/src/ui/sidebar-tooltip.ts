import { Directive, ElementRef, inject } from '@angular/core';

@Directive({
  selector: '[fimraSidebarTooltip]',
  host: {
    '(window:keydown.escape)': 'dismiss()',
    '(blur)': 'reset()',
    '(pointerleave)': 'reset()'
  }
})
export class SidebarTooltip {
  private readonly trigger = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  protected dismiss(): void {
    if (this.trigger.matches('.sidebar-collapsed :is(:hover, :focus-visible)')) {
      this.trigger.setAttribute('data-tooltip-dismissed', '');
    }
  }

  protected reset(): void {
    this.trigger.removeAttribute('data-tooltip-dismissed');
  }
}
