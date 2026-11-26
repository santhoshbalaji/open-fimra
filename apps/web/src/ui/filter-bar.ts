import { BreakpointObserver } from '@angular/cdk/layout';
import { NgTemplateOutlet } from '@angular/common';
import {
  ChangeDetectionStrategy, Component, Directive, ElementRef, TemplateRef, ViewEncapsulation, computed, contentChild, inject, input, output,
  viewChildren
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import { map } from 'rxjs';
import { defaultWorkspaceFormat } from '../workspace-format';
import { Icon } from './icon';
import { LiveStatus } from './live-status';
import { SidePanel } from './side-panel';
import { workspacePhone } from './workspace-breakpoint';

export interface ActiveFilter {
  id: string;
  label: string;
}

@Directive({ selector: 'ng-template[fimraFilterControls]' })
export class FilterControls {
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
    <div class="grid gap-2">
      <div class="min-w-0">
        @if (controls(); as controls) {
          @if (phone()) {
            <button #edit type="button" hlmBtn variant="outline" aria-haspopup="dialog"
              [attr.aria-expanded]="panel.open()" (click)="panel.show(edit, summary)">Filters</button>
            <fimra-side-panel #panel="fimraSidePanel" heading="Filters">
              @if (panel.open()) {
                <div class="filter-bar-controls filter-sheet-controls">
                  <ng-container [ngTemplateOutlet]="controls.template" />
                  <button type="button" hlmBtn variant="outline" class="justify-self-start" (click)="panel.close()">Done</button>
                </div>
              }
            </fimra-side-panel>
          } @else {
            <div class="filter-bar-controls"><ng-container [ngTemplateOutlet]="controls.template" /></div>
          }
        }
      </div>
      <div #summary tabindex="-1"
        class="flex flex-wrap items-center gap-2 rounded-control focus-visible:outline-2 focus-visible:outline-focus">
        <p class="text-body text-text-secondary">{{ activeLabel() }}</p>
        <fimra-live-status class="sr-only" [message]="announcement()" />
        @if (filters().length) {
          <ul role="list" aria-label="Active filters" class="flex min-w-0 flex-wrap gap-2">
            @for (filter of filters(); track filter.id) {
              <li class="min-w-0">
                <button #chip type="button" class="filter-bar-chip" (click)="removeFilter(filter.id, $index, summary)">
                  <span class="sr-only">Remove filter </span>
                  <span class="min-w-0 wrap-anywhere">{{ filter.label }}</span>
                  <svg fimraIcon="circle-x" aria-hidden="true" class="size-4 shrink-0"></svg>
                </button>
              </li>
            }
          </ul>
          <button type="button" hlmBtn variant="ghost" (click)="summary.focus(); clearAll.emit()">Clear all filters</button>
        }
      </div>
    </div>
  `
})
export class FilterBar {
  readonly filters = input.required<readonly ActiveFilter[]>();
  readonly remove = output<string>();
  readonly clearAll = output<void>();

  protected readonly controls = contentChild(FilterControls);
  protected readonly phone = toSignal(
    inject(BreakpointObserver).observe(workspacePhone).pipe(map(result => result.matches)),
    { requireSync: true }
  );
  private readonly chips = viewChildren<ElementRef<HTMLButtonElement>>('chip');

  protected readonly activeLabel = computed(() => {
    const count = this.filters().length;
    if (!count) return 'No active filters';
    return count === 1 ? '1 active filter' : `${defaultWorkspaceFormat.number(count)} active filters`;
  });
  protected readonly announcement = computed(() =>
    [this.activeLabel(), ...this.filters().map(filter => filter.label)].join('. '));

  protected removeFilter(id: string, index: number, fallback: HTMLElement) {
    const chips = this.chips();
    const target = chips[index + 1]?.nativeElement ?? chips[index - 1]?.nativeElement ?? fallback;
    target.focus();
    this.remove.emit(id);
  }
}
