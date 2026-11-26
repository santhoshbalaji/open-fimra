import { translate } from './language';
import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { FieldErrorSummary } from './ui/field-error-summary';
import { StatusBadge } from './ui/status-badge';

export type LayerStatus = 'pending' | 'ready' | 'failed' | 'published' | 'retired';
export interface LayerProblem { kind: string; featureRef: string | null; message: string; blocksPublish: boolean }

const states = {
  pending: ['pending', 'Importing'],
  ready: ['validation-success', 'Checks passed'],
  failed: ['failed', 'Import failed'],
  published: ['published', 'Published'],
  retired: ['complete', 'Retired']
} as const;

@Component({
  selector: 'fimra-layer-status',
  imports: [StatusBadge],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `<fimra-status-badge class="mx-0!" [icon]="badge().icon" [label]="badge().label" />`
})
export class LayerStatusBadge {
  protected readonly msg = translate;
  readonly status = input<LayerStatus>();
  readonly blocked = input(false);
  readonly readFailed = input(false);
  readonly readyLabel = input<string>(states.ready[1]);
  readonly alreadyPublished = input(false);

  protected readonly badge = computed(() => {
    const status = this.status();
    const published = this.alreadyPublished() && status !== 'published';
    const [icon, label] = this.blocked() ? ['overdue', translate('Needs a corrected file')] as const :
      status ? states[status] : published ? states.published : ['draft', translate('Not uploaded')] as const;
    const text = status === 'ready' && !this.blocked() ? this.readyLabel() : label;
    return {
      icon: this.readFailed() ? 'overdue' as const : icon,
      label: (published && status ? 'Published. New upload: ' : '') + text
    };
  });
}

const guidance: Record<string, string> = {
  invalid_archive: 'Export the dataset again and include all files that belong to it.',
  archive_limit: 'Export a smaller area or save the layer as a GeoPackage.',
  unreadable_file: 'Open the source in your GIS and export it again in a supported format.',
  choose_layer: 'Enter the layer name from your GIS in the Source layer field.',
  unknown_layer: 'Check the Source layer field against the layer name in your GIS.',
  unknown_field: 'Check the field names below against the file’s attribute table.',
  missing_code: 'Fill in the code in the file’s attribute table and check the Code field mapping.',
  missing_name: 'Fill in the name in the file’s attribute table and check the Name field mapping.',
  invalid_code: 'Use a code with letters or numbers.',
  duplicate_path: 'Give each unit under the same parent its own code.',
  missing_parent_code: 'Fill in the published parent’s code and check the Parent code field mapping.',
  ambiguous_parent_code: 'Use a parent code that identifies one published unit.',
  unknown_parent_code: 'Use the code of a published unit at the level above.',
  parent_outside: 'Check the parent code and the unit’s position against the published parent boundary.',
  non_polygon: 'Export this layer as polygons.',
  empty_geometry: 'Draw the missing shape or remove the empty feature.',
  invalid_geometry: 'Repair the shape in your GIS and export the file again.',
  unusable_geometry: 'Check the shape and its coordinate system, then export it again.',
  out_of_bounds: 'Check the source projection and export the layer again.',
  overlapping_units: 'Edit the listed boundaries so neighboring units share an edge without overlapping.',
  unit_gap: 'Align the neighboring boundaries to close the gap.',
  no_features: 'Export a layer that contains usable features.',
  feature_limit: 'Export fewer features.',
  coordinate_limit: 'Simplify the shapes or export a smaller area.'
};

@Component({
  selector: 'fimra-layer-checks',
  imports: [FieldErrorSummary, HlmBadge],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'grid gap-3' },
  template: `
    @if (summary()) {
      <fimra-field-error-summary [heading]=" msg('Layer problems to fix') " [errors]="blockers()" />
    }
    @if (needsReplacement()) {
      <p [id]="idPrefix() + '-repair'" class="font-medium text-danger">
        {{ msg('Correct these problems in the source file or upload settings, then choose and upload a replacement.') }}
      </p>
    }
    <ng-content select="[layerRepairAction]" />
    @if (items().length) {
      <ul class="grid gap-2">
        @for (item of items(); track item.id) {
          <li [id]="item.id" tabindex="-1"
            class="grid gap-1 rounded-control border border-border px-3 py-2 wrap-anywhere focus-visible:outline-2 focus-visible:outline-ring"
            [class.border-s-danger]="item.problem.blocksPublish" [class.border-s-4]="item.problem.blocksPublish">
            <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
              @if (item.problem.blocksPublish) {
                <span hlmBadge variant="destructive">{{ msg('Blocks publishing') }}</span>
              } @else {
                <span hlmBadge variant="outline">{{ msg('Review') }}</span>
              }
              @if (item.problem.featureRef) { <span class="text-sm text-muted-foreground">{{ item.problem.featureRef }}</span> }
            </div>
            <p>{{ item.problem.message }}</p>
            @if (item.problem.blocksPublish && guidance[item.problem.kind]; as repair) {
              <p class="text-sm text-muted-foreground">{{ repair }}</p>
            }
          </li>
        }
      </ul>
    }
    @if (!summary() && reviewCount(); as count) {
      <p class="text-sm text-muted-foreground">
        {{ msg('{0} {1} to review that do not block publishing.', [count, count === 1 ? 'item' : 'items']) }}
      </p>
    }
  `
})
export class LayerChecks {
  protected readonly msg = translate;
  readonly idPrefix = input.required<string>();
  readonly problems = input.required<readonly LayerProblem[]>();
  readonly failed = input(false);
  /** Setup already has a summary across all layers. */
  readonly summary = input(true);

  protected readonly guidance = guidance;
  protected readonly items = computed(() => this.problems()
    .map((problem, index) => ({ problem, id: this.idPrefix() + '-problem-' + index }))
    .filter(item => this.summary() || item.problem.blocksPublish)
    .sort((a, b) => Number(b.problem.blocksPublish) - Number(a.problem.blocksPublish)));
  protected readonly blockers = computed(() => this.items().filter(item => item.problem.blocksPublish)
    .map(({ problem, id }) => ({ fieldId: id, message: [problem.featureRef, problem.message].filter(Boolean).join(' · ') })));
  protected readonly needsReplacement = computed(() => this.failed() || this.blockers().length > 0);
  protected readonly reviewCount = computed(() => this.problems().filter(problem => !problem.blocksPublish).length);
}
