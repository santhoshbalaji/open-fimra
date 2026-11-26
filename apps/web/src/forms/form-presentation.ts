import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, computed, input, linkedSignal } from '@angular/core';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmInputImports } from '@spartan-ng/helm/input';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { AnswerSession, readForm, parseDate, timeSeconds, type EntryPath, type Node } from '@open-fimra/form-schema/answers';
import { StateRegion } from '../ui/state-region';
import { reportingTimeZone, WorkspaceFormat } from '../workspace-format';
import { alertSources, describeCondition, type Condition, type FormDefinition } from './form-model';
import { StoredFormAttachment } from './stored-form-attachment';
import { entryName, formLanguages, itemName, languageName, startLanguage, textIn } from './form-text';
import type { Shape } from './shape-input';

type Answers = Record<string, unknown>;

@Component({
  selector: 'fimra-form-presentation',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [NgTemplateOutlet, HlmButtonImports, HlmInputImports, HlmTextareaImports, StateRegion, StoredFormAttachment],
  templateUrl: './form-presentation.html',
  host: { class: 'block min-w-0' }
})
export class FormPresentation {
  readonly definition = input.required<Answers>();
  readonly mode = input.required<'preview' | 'historical'>();
  readonly values = input<Answers>({});
  readonly step = input<string>();
  readonly workspaceUrl = input('');

  protected readonly languageName = languageName;
  protected readonly preview = computed(() => this.mode() === 'preview');
  protected readonly tree = computed(() => readForm(this.definition()));
  protected readonly languages = computed(() => formLanguages(this.definition()));
  protected readonly language = linkedSignal(() => startLanguage(this.languages()));
  private readonly session = computed(() => this.preview() ? null :
    new AnswerSession(this.tree(), this.values(), { step: this.step(), historical: true }));
  private readonly locale = computed(() => {
    try {
      return Intl.getCanonicalLocales(this.language() === 'en' ? 'en-IN' : this.language())[0];
    } catch {
      // The form format also accepts codes that Intl cannot format.
      return 'en-IN';
    }
  });
  private readonly format = computed(() => new WorkspaceFormat(this.locale(), reportingTimeZone, { maximumSignificantDigits: 21 }));
  private readonly coordinate = computed(() => new Intl.NumberFormat(this.locale(), {
    maximumSignificantDigits: 21, useGrouping: false
  }).format);
  private readonly layout = computed(() => {
    const layout = this.definition()['layout'] as FormDefinition['layout'];
    return new Map(layout?.items?.map(item => [item.item, item]));
  });
  protected readonly pages = computed(() => {
    const tree = this.tree();
    const layout = this.definition()['layout'] as FormDefinition['layout'];
    const pages = layout?.pages?.map(page => ({
      key: page.key, title: this.text(page.title),
      sections: page.sections.map(key => tree.byKey.get(key)).filter((node): node is Node => !!node)
    })) ?? tree.sections.map(section => ({ key: section.key, title: '', sections: [section] }));
    return pages.map(page => ({ ...page, sections: page.sections.filter(section => this.visible(section, [])) }))
      .filter(page => page.sections.length);
  });

  protected text(value: unknown): string {
    return textIn(value, this.language());
  }

  protected name(node: Node): string {
    return itemName(node, this.language());
  }

  protected stageName(node: Node): string {
    const stages = this.definition()['stages'] as FormDefinition['stages'];
    return this.text(stages?.[node.stage]?.title);
  }

  protected entryTitle(node: Node, index: number): string {
    return entryName(node, index, this.language());
  }

  protected entryPath(path: EntryPath, node: Node, index: number): EntryPath {
    return [...path, [node.key, index]];
  }

  protected visible(node: Node, path: EntryPath): boolean {
    return this.preview() || !!this.session()?.visible(node.key, path);
  }

  protected entries(node: Node, answers: Answers): Answers[] {
    if (this.preview()) return [{}];
    const list = answers[node.key];
    return Array.isArray(list) ? list as Answers[] : [];
  }

  protected choiceLabel(node: Node, key: unknown): string {
    return this.text(this.tree().choicesOf(node).find(c => c['key'] === key)?.['label']) || String(key);
  }

  protected choiceGroups(node: Node): { label: string; labels: string[] }[] {
    const choices = this.tree().choicesOf(node);
    const parent = this.parent(node);
    if (!parent) return [{ label: '', labels: choices.map(choice => this.text(choice['label'])) }];
    return this.tree().choicesOf(parent).map(choice => ({
      label: this.text(choice['label']),
      labels: choices.filter(c => c['parent'] === choice['key']).map(c => this.text(c['label']))
    })).filter(group => group.labels.length);
  }

  protected parent(node: Node): Node | undefined {
    return this.tree().byKey.get(String(node.node['parent']));
  }

  protected half(node: Node): boolean {
    return this.layout().get(node.key)?.width === 'half';
  }

  protected open(node: Node): boolean {
    return !this.preview() || this.layout().get(node.key)?.collapsed !== true;
  }

  protected widget(node: Node): string {
    return this.layout().get(node.key)?.widget ??
      (node.kind === 'single-choice' && this.tree().choicesOf(node).length > 6 ? 'dropdown' : '');
  }

  protected condition(value: unknown): string {
    return describeCondition(this.definition() as unknown as FormDefinition, value as Condition | undefined, this.language());
  }

  protected constraints(node: Node): string[] {
    const spec = node.node;
    const number = this.format().number;
    const messages: string[] = [];
    const limits = (min: string, max: string, label: string) => {
      const low = spec[min];
      const high = spec[max];
      const suffix = label ? ' ' + label : '';
      if (typeof low === 'number' && typeof high === 'number') messages.push(`From ${number(low)} to ${number(high)}${suffix}.`);
      else if (typeof low === 'number') messages.push(`At least ${number(low)}${suffix}.`);
      else if (typeof high === 'number') messages.push(`At most ${number(high)}${suffix}.`);
    };
    limits('minLength', 'maxLength', 'characters');
    limits('min', 'max', this.unit(node));
    limits('minSelected', 'maxSelected', 'choices');
    limits('minCount', 'maxCount', node.kind === 'photo' ? 'photos' : 'files');
    limits('minRepeats', 'maxRepeats', 'entries');
    if (typeof spec['decimals'] === 'number') messages.push(spec['decimals'] === 0 ? 'Whole numbers only.' :
      `Up to ${number(spec['decimals'])} decimal places.`);
    if (spec['notInFuture']) messages.push('Cannot be in the future.');
    if (typeof spec['maxAccuracyMetres'] === 'number') messages.push(`Position must be accurate to within ${number(spec['maxAccuracyMetres'])} metres.`);
    if (typeof spec['maxSizeMegabytes'] === 'number') messages.push(`Up to ${number(spec['maxSizeMegabytes'])} MB per file.`);
    if (Array.isArray(spec['accept'])) messages.push('Accepted file endings: ' + spec['accept'].join(', ') + '.');
    if (spec['pattern']) messages.push(this.text(spec['patternHint']) || `Must match the pattern ${spec['pattern']}.`);
    return messages;
  }

  protected unit(node: Node): string {
    return node.node['source'] === 'fire-power' ? 'MW' : String(node.node['unit'] ?? '');
  }

  protected alertSource(node: Node): string {
    return alertSources.find(source => source.value === node.node['source'])?.label ?? '';
  }

  protected value(node: Node, path: EntryPath): unknown {
    return this.session()?.value(node.key, path);
  }

  protected files(value: unknown): string[] {
    return typeof value === 'string' ? [value] : Array.isArray(value) ? Array.from(new Set(value as string[])) : [];
  }

  protected attachmentKind(node: Node): 'photo' | 'file' | 'signature' | null {
    const kind = node.kind;
    return kind === 'photo' || kind === 'file' || kind === 'signature' ? kind : null;
  }

  protected geometry(value: unknown): string[][] {
    if (!value || typeof value !== 'object') return [];
    const shape = value as Shape;
    const format = this.coordinate();
    const position = ([longitude, latitude, height]: number[]) =>
      `${format(longitude)}, ${format(latitude)}` + (height === undefined ? '' : ` (${format(height)} m altitude)`);
    switch (shape.type) {
      case 'Point': return [[position(shape.coordinates)]];
      case 'LineString': return [shape.coordinates.map(position)];
      case 'Polygon': return shape.coordinates.map(ring => ring.slice(0, -1).map(position));
      default: return [];
    }
  }

  protected answer(node: Node, value: unknown): string {
    if (value === undefined || value === null || value === '' || (Array.isArray(value) && !value.length)) return 'No answer';
    switch (node.kind) {
      case 'single-choice':
        return this.choiceLabel(node, value);
      case 'multiple-choice':
        return (value as unknown[]).map(key => this.choiceLabel(node, key)).join(', ');
      case 'date': {
        const date = parseDate(value);
        return date === null ? String(value) : this.format().date(new Date(date));
      }
      case 'time': {
        const seconds = timeSeconds(value);
        return seconds === null ? String(value) : this.format().time(seconds, String(value).length > 5);
      }
      case 'date-time':
        return this.dateTime(value);
      case 'alert-value':
        if (node.node['source'] === 'observed-at' || node.node['source'] === 'raised-at') return this.dateTime(value);
    }
    if (typeof value === 'number') {
      const unit = this.unit(node);
      return this.format().number(value) + (unit ? ' ' + unit : '');
    }
    return String(value);
  }

  private dateTime(value: unknown): string {
    const instant = new Date(String(value));
    return Number.isNaN(instant.getTime()) ? String(value) : this.format().dateTime(instant);
  }
}
