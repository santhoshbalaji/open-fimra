import { DOCUMENT } from '@angular/common';
import { ChangeDetectionStrategy, Component, ElementRef, inject, input } from '@angular/core';

export interface FieldError {
  fieldId: string;
  message: string;
}

let nextSummary = 0;

@Component({
  selector: 'fimra-field-error-summary',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '[hidden]': 'errors().length === 0',
    tabindex: '-1',
    role: 'group',
    '[attr.aria-labelledby]': 'headingId',
    '[class.block]': 'errors().length > 0',
    class: 'rounded-control border border-destructive p-4 text-text focus-visible:outline-2 focus-visible:outline-ring'
  },
  template: `
    <p [id]="headingId" class="font-semibold">{{ heading() }}</p>
    <ul class="mt-2 list-disc space-y-1 ps-5">
      @for (error of errors(); track $index) {
        <li><a class="rounded-sm text-danger underline underline-offset-2"
          [attr.href]="'#' + error.fieldId" (click)="focusField($event, error.fieldId)">{{ error.message }}</a></li>
      }
    </ul>
  `
})
export class FieldErrorSummary {
  readonly errors = input.required<readonly FieldError[]>();
  readonly heading = input('Check the following fields');

  protected readonly headingId = `fimra-error-summary-${++nextSummary}`;

  private readonly document = inject(DOCUMENT);
  private readonly element = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  /** Call after rendering a new submission's errors. */
  focus() {
    if (this.errors().length) this.element.focus();
  }

  protected focusField(event: MouseEvent, id: string) {
    event.preventDefault();
    this.document.getElementById(id)?.focus();
  }
}
