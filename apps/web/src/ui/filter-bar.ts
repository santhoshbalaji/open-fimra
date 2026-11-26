import { translate } from '../language';
import { BreakpointObserver } from '@angular/cdk/layout';
import { NgTemplateOutlet } from '@angular/common';
import {
  ChangeDetectionStrategy, Component, Directive, ElementRef, TemplateRef, ViewEncapsulation, computed, contentChild, inject, input, output,
  viewChild, viewChildren
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import { map } from 'rxjs';
import { workspaceFormat } from '../workspace-format';
import { Icon } from './icon';
import { LiveStatus } from './live-status';
import { SidePanel } from './side-panel';

export interface ActiveFilter {
  id: string;
  label: string;
}

@Directive({ selector: 'ng-template[fimraFilterControls]' })
export class FilterControls {
  protected readonly msg = translate;
  readonly template = inject(TemplateRef);
}

@Component({
  selector: 'fimra-filter-bar',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton, Icon, LiveStatus, NgTemplateOutlet, SidePanel],
  host: { class: 'block min-w-0' },
  encapsulation: ViewEncapsulation.None,
  styleUrls: ['./filter-bar.css'],
  template: `
    <div>
      @if (controls(); as controls) {
        <div #editor class="min-w-0">
          @if (compact()) {
            <button #edit type="button" hlmBtn variant="outline" aria-haspopup="dialog"
              [attr.aria-expanded]="panel.open()" [attr.aria-description]="filters().length ? activeLabel() : null"
              (click)="panel.show(edit, summary)">
              {{ msg('Filters') }}
              @if (filters().length) { <span class="filter-count" aria-hidden="true">{{ filters().length }}</span> }
            </button>
            <fimra-side-panel #panel="fimraSidePanel" [heading]=" msg('Filters') " [sheetOnly]="true">
              @if (panel.open()) {
                <div class="filter-bar-controls filter-sheet-controls">
                  <ng-container [ngTemplateOutlet]="controls.template" />
                  <button type="button" hlmBtn variant="outline" class="justify-self-start" (click)="panel.close()">{{ msg('Done') }}</button>
                </div>
              }
            </fimra-side-panel>
          } @else {
            <div class="filter-bar-controls"><ng-container [ngTemplateOutlet]="controls.template" /></div>
          }
        </div>
      }
      <fimra-live-status class="sr-only" [message]="announcement()" />
      <div #summary tabindex="-1"
        [class.mt-2]="controls() && filters().length"
        class="flex flex-wrap items-center gap-2 rounded-control focus-visible:outline-2 focus-visible:outline-focus">
        @if (filters().length) {
          <ul role="list" [attr.aria-label]=" msg('Active filters') " class="flex min-w-0 flex-wrap gap-2">
            @for (filter of filters(); track filter.id) {
              <li class="min-w-0">
                <button #chip type="button" class="filter-bar-chip" (click)="removeFilter(filter.id, $index, summary)">
                  <span class="sr-only">{{ msg('Remove filter') }} </span>
                  <span class="min-w-0 wrap-anywhere">{{ filter.label }}</span>
                  <svg fimraIcon="circle-x" aria-hidden="true" class="size-4 shrink-0"></svg>
                </button>
              </li>
            }
          </ul>
          <button type="button" hlmBtn variant="ghost" (click)="focusEditor(summary); clearAll.emit()">{{ msg('Clear all filters') }}</button>
        }
      </div>
    </div>
  `
})
export class FilterBar {
  protected readonly msg = translate;
  readonly filters = input.required<readonly ActiveFilter[]>();
  readonly remove = output<string>();
  readonly clearAll = output<void>();

  protected readonly controls = contentChild(FilterControls);
  protected readonly compact = toSignal(
    inject(BreakpointObserver).observe('(width < 56rem)').pipe(map(result => result.matches)),
    { requireSync: true }
  );
  private readonly editor = viewChild<ElementRef<HTMLElement>>('editor');
  private readonly chips = viewChildren<ElementRef<HTMLButtonElement>>('chip');

  protected readonly activeLabel = computed(() => {
    const count = this.filters().length;
    if (!count) return translate('No active filters');
    return count === 1 ? translate('1 active filter') : translate('{0} active filters', [workspaceFormat.number(count)]);
  });
  protected readonly announcement = computed(() =>
    [this.activeLabel(), ...this.filters().map(filter => filter.label)].join('. '));

  protected removeFilter(id: string, index: number, fallback: HTMLElement) {
    const chips = this.chips();
    const target = chips[index + 1]?.nativeElement ?? chips[index - 1]?.nativeElement;
    if (target) target.focus();
    else this.focusEditor(fallback);
    this.remove.emit(id);
  }

  protected focusEditor(fallback: HTMLElement): void {
    (this.editor()?.nativeElement.querySelector<HTMLElement>('button, select, input') ?? fallback).focus();
  }
}
