import { Component, input } from '@angular/core';
import { LucideBellOff, LucideCircleCheck, LucideCircleX, LucideCheck, LucideClock, LucideTriangleAlert } from '@lucide/angular';
import { HlmBadge } from '@spartan-ng/helm/badge';

function visibleLabel(value: string) {
  const text = value.trim();
  if (!text) throw new Error('A workflow status needs a visible label.');
  return text;
}

@Component({
  selector: 'fimra-status-badge',
  imports: [HlmBadge, LucideBellOff, LucideCircleCheck, LucideCircleX, LucideCheck, LucideClock, LucideTriangleAlert],
  host: { class: 'inline-flex align-middle mx-1' },
  template: `
    <span hlmBadge variant="outline" class="gap-1.5 text-text">
      @switch (icon()) {
        @case ('overdue') { <svg lucideTriangleAlert aria-hidden="true" class="size-4 fill-warning"></svg> }
        @case ('complete') { <svg lucideCheck aria-hidden="true" class="size-4"></svg> }
        @case ('healthy') { <svg lucideCircleCheck aria-hidden="true" class="size-4"></svg> }
        @case ('delayed') { <svg lucideClock aria-hidden="true" class="size-4"></svg> }
        @case ('quiet') { <svg lucideBellOff aria-hidden="true" class="size-4"></svg> }
        @case ('failing') { <svg lucideCircleX aria-hidden="true" class="size-4"></svg> }
        @case ('step') { <svg lucideClock aria-hidden="true" class="size-4"></svg> }
      }
      {{ label() }}
    </span>
  `
})
export class StatusBadge {
  readonly icon = input.required<'step' | 'complete' | 'overdue' | 'healthy' | 'delayed' | 'quiet' | 'failing'>();
  readonly label = input.required<string, string>({ transform: visibleLabel });
}
