import { translate } from '../language';
import { Component, computed, input } from '@angular/core';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { Icon, IconName } from './icon';

const icons = {
  overdue: 'triangle-alert', complete: 'check', healthy: 'circle-check', delayed: 'clock',
  quiet: 'bell-off', failing: 'circle-x', step: 'clock', draft: 'file-pen-line', published: 'circle-check',
  'validation-success': 'check', locked: 'lock', pending: 'clock', failed: 'circle-x', queued: 'list-ordered',
  unread: 'bell'
} satisfies Record<string, IconName>;

type Tone = 'urgent' | 'active' | 'success' | 'muted' | 'warning';
const workflow = new Map<string, [IconName, Tone]>([
  ['new', ['circle-alert', 'urgent']],
  ['acknowledged', ['check', 'active']],
  ['team sent', ['chevron-right', 'active']],
  ['reached site', ['circle-check', 'active']],
  ['controlled', ['check', 'success']],
  ['extinguished', ['circle-check', 'success']],
  ['first report', ['file-pen-line', 'active']],
  ['final report', ['list-ordered', 'success']],
  ['verified', ['eye', 'success']],
  ['closed', ['lock', 'success']],
  ['false alarm', ['bell-off', 'muted']],
  ['duplicate', ['layers', 'muted']],
  ['controlled burn', ['sun', 'success']],
  ['not reachable', ['circle-x', 'warning']]
]);

function visibleLabel(value: string) {
  const text = value.trim();
  if (!text) throw new Error('A workflow status needs a visible label.');
  return text;
}

@Component({
  selector: 'fimra-status-badge',
  imports: [HlmBadge, Icon],
  host: { class: 'inline-flex align-middle mx-1' },
  styles: `
    [data-tone=urgent] { --badge-accent: var(--color-danger); }
    [data-tone=active] { --badge-accent: var(--color-action); }
    [data-tone=success] { --badge-accent: var(--color-success-text); }
    [data-tone=muted] { --badge-accent: var(--color-text-secondary); }
    [data-tone=warning] { --badge-accent: var(--color-warning); }
    [data-tone] {
      border-color: color-mix(in srgb, var(--badge-accent) 55%, var(--color-surface));
      background-color: color-mix(in srgb, var(--badge-accent) 12%, var(--color-surface));
    }
    [data-tone] svg { color: var(--badge-accent); }
  `,
  template: `
    <span hlmBadge variant="outline" class="h-auto min-h-5 max-w-full gap-1.5 whitespace-normal wrap-anywhere text-text"
      [attr.data-tone]="appearance()[1]">
      <svg [fimraIcon]="appearance()[0]" aria-hidden="true" class="size-4 shrink-0"></svg>
      {{ msg(label()) }}
    </span>
  `
})
export class StatusBadge {
  protected readonly msg = translate;
  readonly icon = input.required<keyof typeof icons>();
  readonly label = input.required<string, string>({ transform: visibleLabel });
  protected readonly appearance = computed<[IconName, Tone | null]>(() => {
    const icon = this.icon();
    if (icon === 'step' || icon === 'complete') {
      return workflow.get(this.label().toLowerCase()) ?? [icons[icon], icon === 'complete' ? 'success' : null];
    }
    return [icons[icon], icon === 'overdue' ? 'warning' : null];
  });
}
