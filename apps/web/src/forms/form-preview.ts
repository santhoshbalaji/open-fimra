import { NgTemplateOutlet } from '@angular/common';
import { Component, computed, input } from '@angular/core';
import { FormDefinition, FormItem, alertSources, choicesOf, describeCondition, isContainer, textOf, typeName } from './form-model';

/**
 * Shows a form version read-only, as the people filling it in will see it,
 * with the conditions in plain English. Give it `values` to show a
 * submission's answers with the version it was filled in on. It has no
 * dependencies beyond the form model, so other screens can share it.
 */
@Component({
  selector: 'fimra-form-preview',
  imports: [NgTemplateOutlet],
  template: `
    @let f = form();
    <article class="grid gap-4" [attr.lang]="language()" aria-label="Preview of the form">
      <header class="grid gap-1">
        <h3 class="text-title font-semibold">{{ text(f.title) }}</h3>
        @if (f.description) { <p class="text-muted-foreground">{{ text(f.description) }}</p> }
        <p class="text-label text-muted-foreground">{{ f.id }}, version {{ f.version }}</p>
      </header>
      @for (section of f.sections; track section.key) {
        <section class="preview-section grid gap-3 rounded-card border border-border bg-card p-4" [attr.aria-labelledby]="'preview-' + section.key">
          <h4 class="text-subhead font-semibold" [id]="'preview-' + section.key">{{ text(section.title) }}</h4>
          @if (section.description) { <p class="text-muted-foreground">{{ text(section.description) }}</p> }
          @if (section.showWhen) { <p class="preview-condition text-label text-muted-foreground">Shown when {{ condition(section.showWhen) }}</p> }
          <ng-container [ngTemplateOutlet]="itemList" [ngTemplateOutletContext]="{ $implicit: section.items, answers: values() }" />
        </section>
      }
    </article>

    <ng-template #itemList let-items let-answers="answers">
      <div class="grid gap-3">
        @for (item of asItems(items); track item.key) {
          @if (isContainer(item)) {
            <fieldset class="grid gap-3 rounded-control border border-border p-3">
              <legend class="px-1 font-semibold">{{ text(item.title) }}</legend>
              @if (item.showWhen) { <p class="preview-condition text-label text-muted-foreground">Shown when {{ condition(item.showWhen) }}</p> }
              @if (item.type === 'repeat') {
                <p class="text-label text-muted-foreground">{{ repeatWords(item) }}</p>
                @if (answers) {
                  @for (entry of entries(answers, item.key); track $index) {
                    <div class="grid gap-3 border-l-2 border-border pl-3" [attr.aria-label]="text(asText(item['itemLabel'])) + ' ' + ($index + 1)">
                      <p class="font-semibold">{{ text(asText(item['itemLabel'])) || 'Entry' }} {{ $index + 1 }}</p>
                      <ng-container [ngTemplateOutlet]="itemList" [ngTemplateOutletContext]="{ $implicit: item.items, answers: entry }" />
                    </div>
                  } @empty { <p class="text-muted-foreground">No entries.</p> }
                } @else {
                  <ng-container [ngTemplateOutlet]="itemList" [ngTemplateOutletContext]="{ $implicit: item.items, answers: null }" />
                }
              } @else {
                <ng-container [ngTemplateOutlet]="itemList" [ngTemplateOutletContext]="{ $implicit: item.items, answers: answers }" />
              }
            </fieldset>
          } @else {
            <div class="preview-field grid gap-1">
              <p class="font-medium">
                {{ text(item.label) }}@if (item.required) {<span class="text-danger" aria-label="required"> *</span>}
              </p>
              @if (item.hint) { <p class="text-label text-muted-foreground">{{ text(item.hint) }}</p> }
              @if (answers) {
                <p class="preview-answer rounded-control bg-muted px-2 py-1">{{ answer(item, answers) }}</p>
              } @else {
                <p class="preview-control rounded-control border border-dashed border-border px-2 py-1 text-muted-foreground">{{ control(item) }}</p>
              }
              @if (item.showWhen) { <p class="preview-condition text-label text-muted-foreground">Shown when {{ condition(item.showWhen) }}</p> }
              @if (item['requireWhen']) { <p class="text-label text-muted-foreground">Required when {{ condition(asCondition(item['requireWhen'])) }}</p> }
            </div>
          }
        }
      </div>
    </ng-template>
  `
})
export class FormPreview {
  readonly form = input.required<FormDefinition>();
  readonly language = input('en');
  /** A submission's answers by field key, to show instead of empty controls. */
  readonly values = input<Record<string, unknown> | null>(null);
  readonly isContainer = isContainer;
  private readonly definition = computed(() => this.form());

  text(value: Record<string, string> | undefined): string { return textOf(value, this.language()); }
  asItems(items: unknown): FormItem[] { return items as FormItem[]; }
  asText(value: unknown): Record<string, string> | undefined { return value as Record<string, string> | undefined; }
  asCondition(value: unknown) { return value as FormItem['showWhen']; }
  condition(condition: FormItem['showWhen']): string { return describeCondition(this.definition(), condition); }

  repeatWords(item: FormItem): string {
    const min = item['minRepeats'] as number | undefined, max = item['maxRepeats'] as number | undefined;
    const label = (textOf(item['itemLabel'] as Record<string, string> | undefined, this.language()) || 'entry').toLowerCase();
    if (min && max) return `Add from ${min} to ${max} of: ${label}.`;
    if (min) return `Add at least ${min} of: ${label}.`;
    if (max) return `Add up to ${max} of: ${label}.`;
    return `Add one for each ${label}.`;
  }

  entries(answers: Record<string, unknown>, key: string): Record<string, unknown>[] {
    const list = answers[key];
    return Array.isArray(list) ? list as Record<string, unknown>[] : [];
  }

  /** What the empty control will be, in words. */
  control(item: FormItem): string {
    switch (item.type) {
      case 'single-choice': case 'multiple-choice':
        return (item.type === 'single-choice' ? 'Pick one: ' : 'Pick any: ') +
          choicesOf(this.definition(), item).map(c => textOf(c.label, this.language())).join(', ');
      case 'number': case 'calculated': {
        const unit = item['unit'] ? ' ' + String(item['unit']) : '';
        return (item.type === 'calculated' ? 'Worked out from other answers' : 'A number') + (unit ? ', in' + unit : '');
      }
      case 'alert-value':
        return 'From the alert: ' + (alertSources.find(s => s.value === item['source'])?.label ?? String(item['source']));
      default:
        return typeName(item.type);
    }
  }

  /** An answer in words. Shapes and attachments are summarised. */
  answer(item: FormItem, answers: Record<string, unknown>): string {
    const value = answers[item.key];
    if (value === undefined || value === null) return 'Not answered';
    const choice = (key: unknown) => textOf(choicesOf(this.definition(), item).find(c => c.key === key)?.label, this.language()) || String(key);
    switch (item.type) {
      case 'single-choice': return choice(value);
      case 'multiple-choice': return Array.isArray(value) ? value.map(choice).join(', ') : String(value);
      case 'photo': case 'file': return Array.isArray(value) ? `${value.length} attached` : '1 attached';
      case 'signature': return 'Signed';
      case 'point': case 'track': case 'polygon': case 'alert-value':
        if (typeof value === 'object') return 'A shape on the map';
        return String(value);
      default: {
        const unit = item['unit'] ? ' ' + String(item['unit']) : '';
        return String(value) + unit;
      }
    }
  }
}
