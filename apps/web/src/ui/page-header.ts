import { ChangeDetectionStrategy, Component, input, type TemplateRef } from '@angular/core';

/** Routed screens can contribute view-child templates to the shell's header. */
export interface PageHeaderSlots {
  readonly pageFreshness?: () => TemplateRef<unknown> | undefined;
  readonly pagePrimary?: () => TemplateRef<unknown> | undefined;
  readonly pageSecondary?: () => TemplateRef<unknown> | undefined;
}

@Component({
  selector: 'fimra-page-header',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { class: 'block min-w-0' },
  styleUrl: './workspace-target.css',
  template: `
    <header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
      <div class="page-heading min-w-0 flex-1 basis-64 wrap-anywhere">
        @if (headingLevel() === 1) {
          <h1 tabindex="-1" class="w-fit max-w-full text-display">{{ title() }}</h1>
        } @else {
          <div role="heading" [attr.aria-level]="headingLevel()" class="text-display">{{ title() }}</div>
        }
        @if (scope()) { <p class="mt-1 text-body font-medium">{{ scope() }}</p> }
        <ng-content select="[pageFreshness]" />
      </div>
      <div class="page-actions flex min-w-0 flex-wrap items-center gap-2 empty:hidden">
        <ng-content select="[pageSecondary]" />
        <ng-content select="[pagePrimary]" />
      </div>
    </header>
  `
})
export class PageHeader {
  readonly title = input.required<string>();
  readonly scope = input('');
  readonly headingLevel = input<1 | 2 | 3 | 4 | 5 | 6>(1);
}
