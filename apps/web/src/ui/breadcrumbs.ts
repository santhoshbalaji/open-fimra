import { ChangeDetectionStrategy, Component, input } from '@angular/core';

export interface BreadcrumbLink {
  label: string;
  href: string;
}

@Component({
  selector: 'fimra-breadcrumbs',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <nav aria-label="Breadcrumb">
      <ol class="flex flex-wrap items-center gap-x-2 gap-y-1 text-body">
        @for (link of ancestors(); track $index) {
          <li class="flex min-w-0 items-center gap-2 wrap-anywhere">
            <a class="rounded-control text-action underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-focus"
              [href]="link.href">{{ link.label }}</a>
            <span aria-hidden="true" class="text-text-secondary">/</span>
          </li>
        }
        <li aria-current="page" class="min-w-0 wrap-anywhere font-medium">{{ current() }}</li>
      </ol>
    </nav>
  `
})
export class Breadcrumbs {
  readonly ancestors = input<readonly BreadcrumbLink[]>([]);
  readonly current = input.required<string>();
}
