import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { defaultWorkspaceFormat, WorkspaceFormat } from '../workspace-format';

@Component({
  selector: 'fimra-stat-tile',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'block min-w-0' },
  template: `
    <dl class="grid h-full content-start gap-1 rounded-card border border-divider bg-surface p-4 wrap-anywhere">
      <dt class="text-body font-semibold">
        @if (href(); as href) {
          <a class="rounded-control text-action underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-focus"
            [href]="href">{{ label() }}</a>
        } @else { {{ label() }} }
      </dt>
      <dd class="text-figure tabular-nums">{{ displayValue() }}</dd>
      <dd class="text-body text-text-secondary">{{ scope() }}</dd>
      <dd class="text-label text-text-secondary">
        {{ freshnessLabel() }} <time [attr.datetime]="freshAt().toISOString()">{{ format().dateTime(freshAt()) }}</time>
      </dd>
    </dl>
  `
})
export class StatTile {
  readonly value = input.required<number | string>();
  readonly label = input.required<string>();
  readonly scope = input.required<string>();
  readonly freshAt = input.required<Date>();
  readonly freshnessLabel = input('Updated');
  readonly href = input<string>();
  readonly format = input<WorkspaceFormat>(defaultWorkspaceFormat);

  protected readonly displayValue = computed(() => {
    const value = this.value();
    return typeof value === 'number' ? this.format().number(value) : value;
  });
}
