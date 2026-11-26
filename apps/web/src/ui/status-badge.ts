import { Component, input } from '@angular/core';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { Icon, IconName } from './icon';

const icons = {
  overdue: 'triangle-alert', complete: 'check', healthy: 'circle-check', delayed: 'clock',
  quiet: 'bell-off', failing: 'circle-x', step: 'clock', draft: 'file-pen-line', published: 'circle-check',
  'validation-success': 'check', pending: 'clock', failed: 'circle-x', queued: 'list-ordered'
} satisfies Record<string, IconName>;

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
      <svg [fimraIcon]="icons[icon()]" aria-hidden="true" class="size-4"
        [class.fill-warning]="icon() === 'overdue'"></svg>
      {{ label() }}
    </span>
  `
})
export class StatusBadge {
  protected readonly icons = icons;
  readonly icon = input.required<keyof typeof icons>();
  readonly label = input.required<string, string>({ transform: visibleLabel });
}
