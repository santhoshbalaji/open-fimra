import { translate } from '../language';
import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { RouterLink, UrlTree } from '@angular/router';
import { workspaceFormat, WorkspaceFormat } from '../workspace-format';

@Component({
  selector: 'fimra-stat-tile',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterLink],
  host: { class: 'block min-w-0' },
  template: `
    <dl class="relative grid h-full content-start gap-1 rounded-card border border-divider bg-surface p-4 wrap-anywhere">
      <dt class="text-body font-semibold">
        @if (route(); as route) {
          <a class="text-action underline underline-offset-2 after:absolute after:inset-0 after:rounded-card
            focus-visible:after:outline-2 focus-visible:after:outline-focus"
            [routerLink]="route" [attr.aria-describedby]="valueId()">{{ msg(label()) }}</a>
        } @else if (href(); as href) {
          <a class="rounded-control text-action underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-focus"
            [href]="href">{{ msg(label()) }}</a>
        } @else { {{ msg(label()) }} }
      </dt>
      <dd class="text-figure tabular-nums" [attr.id]="valueId()">{{ displayValue() }}</dd>
      <dd class="text-body text-text-secondary">{{ msg(scope()) }}</dd>
      @if (freshAt(); as updated) {
        <dd class="text-label text-text-secondary">
          {{ msg(freshnessLabel()) }} <time [attr.datetime]="updated.toISOString()">{{ format().dateTime(updated) }}</time>
        </dd>
      }
    </dl>
  `
})
export class StatTile {
  protected readonly msg = translate;
  readonly value = input.required<number | string>();
  readonly label = input.required<string>();
  readonly scope = input.required<string>();
  readonly freshAt = input<Date>();
  readonly freshnessLabel = input('Updated');
  readonly href = input<string>();
  readonly route = input<UrlTree>();
  readonly valueId = input<string>();
  readonly format = input<Pick<WorkspaceFormat, keyof WorkspaceFormat>>(workspaceFormat);

  protected readonly displayValue = computed(() => {
    const value = this.value();
    return typeof value === 'number' ? this.format().number(value) : translate(value);
  });
}
