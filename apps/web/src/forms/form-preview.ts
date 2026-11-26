import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import type { FormDefinition } from './form-model';
import { FormPresentation } from './form-presentation';

@Component({
  selector: 'fimra-form-preview',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [FormPresentation],
  template: '<fimra-form-presentation [definition]="definition()" mode="preview" />'
})
export class FormPreview {
  readonly form = input.required<FormDefinition>();
  protected readonly definition = computed(() => ({ ...this.form() }));
}
