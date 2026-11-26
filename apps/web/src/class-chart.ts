import { translate } from './language';
import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { detectionSeriesColors, type DetectionSeries } from './dashboard.series';
import { workspaceFormat } from './workspace-format';

@Component({
  selector: 'fimra-class-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'block min-w-0', 'aria-hidden': 'true' },
  template: `
    <ul class="grid gap-3">
      @for (row of rows(); track row.id) {
        <li class="grid gap-1">
          <div class="flex justify-between gap-2 text-body wrap-anywhere">
            <span>{{ row.label }}</span><span class="font-semibold tabular-nums">{{ number(row.count) }}</span>
          </div>
          <div class="h-3 overflow-hidden rounded-control bg-muted">
            <div class="h-full rounded-control" [style.width.%]="100 * row.count / maximum()"
              [style.background-color]="'var(' + colors[row.slot - 1] + ')'"></div>
          </div>
        </li>
      }
    </ul>
    <p class="mt-3 flex justify-between gap-2 border-t border-divider pt-2 text-body font-semibold">
      <span>{{ msg('Total detections') }}</span><span class="tabular-nums">{{ number(total()) }}</span>
    </p>
  `
})
export class ClassChart {
  protected readonly msg = translate;
  readonly rows = input.required<readonly DetectionSeries[]>();
  protected readonly colors = detectionSeriesColors;
  protected readonly total = computed(() => this.rows().reduce((total, row) => total + row.count, 0));
  protected readonly maximum = computed(() => Math.max(1, ...this.rows().map(row => row.count)));
  protected readonly number = workspaceFormat.number;
}
