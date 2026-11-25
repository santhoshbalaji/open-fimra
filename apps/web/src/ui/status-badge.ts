import { Component, input } from '@angular/core';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { Icon } from './icon';

function visibleLabel(value: string) {
  const text = value.trim();
  if (!text) throw new Error('A workflow status needs a visible label.');
  return text;
}

@Component({
  selector: 'fimra-status-badge',
  imports: [HlmBadge, Icon],
  host: { class: 'inline-flex align-middle mx-1' },
  template: `
    <span hlmBadge variant="outline" class="gap-1.5 text-text">
      @switch (icon()) {
        @case ('overdue') { <svg fimraIcon="triangle-alert" aria-hidden="true" class="size-4 fill-warning"></svg> }
        @case ('complete') { <svg fimraIcon="check" aria-hidden="true" class="size-4"></svg> }
        @case ('healthy') { <svg fimraIcon="circle-check" aria-hidden="true" class="size-4"></svg> }
        @case ('delayed') { <svg fimraIcon="clock" aria-hidden="true" class="size-4"></svg> }
        @case ('quiet') { <svg fimraIcon="bell-off" aria-hidden="true" class="size-4"></svg> }
        @case ('failing') { <svg fimraIcon="circle-x" aria-hidden="true" class="size-4"></svg> }
        @case ('step') { <svg fimraIcon="clock" aria-hidden="true" class="size-4"></svg> }
      }
      {{ label() }}
    </span>
  `
})
export class StatusBadge {
  readonly icon = input.required<'step' | 'complete' | 'overdue' | 'healthy' | 'delayed' | 'quiet' | 'failing'>();
  readonly label = input.required<string, string>({ transform: visibleLabel });
}
