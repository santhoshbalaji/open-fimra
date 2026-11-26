import { translate } from '../language';
import { CdkDrag, CdkDragDrop, CdkDragHandle, CdkDropList } from '@angular/cdk/drag-drop';
import { BreakpointObserver } from '@angular/cdk/layout';
import { DOCUMENT, NgTemplateOutlet } from '@angular/common';
import { Component, Injector, afterNextRender, computed, inject, input, model, signal, viewChild } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { validateForm } from '@open-fimra/form-schema';
import { HlmBadge } from '@spartan-ng/helm/badge';
import { HlmButton } from '@spartan-ng/helm/button';
import { HlmInput } from '@spartan-ng/helm/input';
import { HlmTextarea } from '@spartan-ng/helm/textarea';
import { map } from 'rxjs';
import { FieldErrorSummary } from '../ui/field-error-summary';
import { Icon } from '../ui/icon';
import { LiveStatus } from '../ui/live-status';
import { SidePanel } from '../ui/side-panel';
import { StatusBadge } from '../ui/status-badge';
import { workspacePhone } from '../ui/workspace-breakpoint';
import { FormPreview } from './form-preview';
import { TemplateRules, checkExtension, checkTemplateRules } from './form-locks';
import {
  Choice, Condition, FormDefinition, FormItem, FormSection, NodePath, alertSources, choicesOf, containers, fieldTypes,
  fieldsOf, findPath, insertItem, insertSection, isContainer, keysAlong, moveItem, moveSection, newItem, newSection,
  nodeAt, nodeName, removeNode, renameKey, shift, testWords, testsFor, typeName, updateNode
} from './form-model';

/** The template version a form extends, as the server sent it. */
export interface ParentTemplate { form: string; version: number; level: string; definition: FormDefinition; rules: TemplateRules }

type Tab = 'design' | 'preview' | 'file';
const keyPattern = /^[a-z][a-z0-9_]{0,39}$/;
const simpleTests = new Set(['answered', 'equals', 'not-equals', 'more-than', 'at-least', 'less-than', 'at-most', 'includes']);

/**
 * Edits a form in the form format: sections, groups, repeating groups,
 * fields, conditions and template locks. Drag and drop moves items; every
 * move also has buttons and Alt with the arrow keys. The form is checked
 * with the shared validator as it changes, and parts locked by the template
 * it extends cannot be changed here.
 */
@Component({
  selector: 'fimra-form-designer',
  imports: [CdkDrag, CdkDragHandle, CdkDropList, NgTemplateOutlet, FormPreview, HlmBadge, HlmButton, HlmInput, HlmTextarea,
    FieldErrorSummary, Icon, LiveStatus, SidePanel, StatusBadge],
  templateUrl: './form-designer.html',
  styleUrl: './form-designer.css',
  host: { class: 'block' }
})
export class FormDesigner {
  protected readonly msg = translate;
  readonly form = model.required<FormDefinition>();
  /** This version's own template rules, including locks it inherits. */
  readonly rules = model<TemplateRules>({ allowExtension: false, locked: [] });
  readonly parent = input<ParentTemplate | null>(null);
  readonly level = input<'national' | 'state' | 'division'>('state');
  readonly readonly = input(false);
  /** Problems the server reported on the last save. */
  readonly serverProblems = input<string[]>([]);

  private readonly injector = inject(Injector);
  private readonly document = inject(DOCUMENT);
  private readonly errorSummary = viewChild(FieldErrorSummary);
  protected readonly phone = toSignal(inject(BreakpointObserver).observe(workspacePhone).pipe(map(match => match.matches)),
    { requireSync: true });

  readonly fieldTypes = fieldTypes;
  readonly alertSources = alertSources;
  readonly testWords = testWords;
  readonly isContainer = isContainer;
  readonly typeName = typeName;
  readonly nodeName = nodeName;
  readonly tab = signal<Tab>('design');
  readonly selected = signal<number[] | null>(null);
  readonly announcement = signal('');
  readonly keyError = signal('');
  readonly fileText = signal('');
  readonly fileError = signal('');
  readonly jsonError = signal('');
  private readonly history = signal<FormDefinition[]>([]);
  readonly canUndo = computed(() => this.history().length > 0);

  readonly languages = computed(() => this.form().languages ?? ['en']);
  readonly inherited = computed(() => new Set(this.parent()?.rules.locked ?? []));
  readonly ownLocks = computed(() => new Set(this.rules().locked));
  readonly problems = computed(() => {
    const form = this.form();
    const parent = this.parent();
    return [
      ...validateForm(form),
      ...checkTemplateRules(this.rules(), form),
      ...(parent ? checkExtension(parent.definition, parent.rules, form) : [])
    ];
  });
  protected readonly errors = computed(() => [...new Set([...this.problems(), ...this.serverProblems()])].map(message => {
    const key = /^(?:Field|Section|Group|Repeating group|The key|The template locks) "([a-z][a-z0-9_]*)"/.exec(message)?.[1];
    // Schema errors give a position such as sections[0].items[1].label.
    const position = /^sections\[\d+\](?:\.items\[\d+\])*/.exec(message)?.[0];
    const path = key ? findPath(this.form(), key) : position?.match(/\d+/g)?.map(Number);
    return { message, fieldId: path && nodeAt(this.form(), path) ? this.rowId(path) : undefined };
  }));
  readonly listIds = computed(() => {
    const ids: string[] = [];
    const walk = (items: FormItem[], path: number[]) => items.forEach((item, i) => {
      if (isContainer(item)) { ids.push(this.listId([...path, i])); walk(item.items ?? [], [...path, i]); }
    });
    this.form().sections.forEach((section, i) => { ids.push(this.listId([i])); walk(section.items, [i]); });
    return ids;
  });
  readonly node = computed(() => {
    const path = this.selected();
    return path ? nodeAt(this.form(), path) : undefined;
  });
  readonly item = computed(() => this.node() as FormItem | undefined);
  readonly places = computed(() => containers(this.form()));
  readonly fields = computed(() => fieldsOf(this.form()));
  readonly canLock = computed(() => this.level() !== 'division');
  /** Sections, groups and repeating groups have a title; fields have a label. */
  readonly textName = computed(() => {
    const path = this.selected();
    return !path || path.length === 1 || isContainer(this.item() ?? {}) ? 'title' : 'label';
  });

  /** The id of the drop list for a section or group. */
  listId(path: NodePath): string { return 'form-list-' + path.join('-'); }
  rowId(path: NodePath): string { return 'form-row-' + path.join('-'); }
  pathOf(container: NodePath, index: number): number[] { return [...container, index]; }
  isSelected(path: NodePath): boolean { const s = this.selected(); return !!s && s.join() === path.join(); }
  asItems(items: unknown): FormItem[] { return items as FormItem[]; }
  asPath(path: unknown): number[] { return path as number[]; }
  prop(node: unknown, name: string): unknown { return (node as Record<string, unknown>)[name]; }
  indent(depth: number): string { return '\u00a0\u00a0'.repeat(depth); }
  text(value: unknown, language = 'en'): string { return (value as Record<string, string> | undefined)?.[language] ?? ''; }
  numberOf(name: string): string { const v = this.item()?.[name]; return typeof v === 'number' ? String(v) : ''; }
  stringOf(name: string): string { const v = this.item()?.[name]; return typeof v === 'string' ? v : ''; }

  /** A part locked by the template this form extends cannot be changed here. */
  frozen(path: NodePath): boolean {
    const locked = this.inherited();
    return locked.size > 0 && keysAlong(this.form(), path).some(key => locked.has(key));
  }

  /** Locked by this form for the forms that extend it, or inherited. */
  lockedHere(path: NodePath): boolean {
    const node = nodeAt(this.form(), path);
    return !!node && this.ownLocks().has(node.key) && !this.inherited().has(node.key);
  }

  /** Selects by key, so an event never acts on a position from before a change. */
  selectKey(key: string): void { this.select(findPath(this.form(), key)); }

  select(path: NodePath | null): void {
    this.selected.set(path ? [...path] : null);
    this.keyError.set('');
    this.jsonError.set('');
  }

  setTab(tab: Tab): void {
    if (tab === 'file') { this.fileText.set(JSON.stringify(this.form(), null, 2)); this.fileError.set(''); }
    this.tab.set(tab);
  }

  private apply(next: FormDefinition, message = ''): void {
    if (next === this.form()) return;
    this.history.update(list => [...list.slice(-49), this.form()]);
    this.form.set(next);
    if (message) this.announcement.set(message);
  }

  undo(): void {
    const list = this.history();
    if (!list.length) return;
    this.form.set(list[list.length - 1]);
    this.history.set(list.slice(0, -1));
    this.select(null);
    this.announcement.set('Undid the last change.');
  }

  /** The place where a new item goes: after the selected item, or at the end of the selected container. */
  private target(): { container: number[]; index: number } {
    const form = this.form();
    const path = this.selected();
    const usable = (p: number[]) => !this.frozen(p);
    if (path) {
      const node = nodeAt(form, path);
      if (node && (path.length === 1 || isContainer(node as FormItem)) && usable(path)) {
        return { container: path, index: node.items?.length ?? 0 };
      }
      const container = path.slice(0, -1);
      if (container.length && usable(container)) return { container, index: path[path.length - 1] + 1 };
    }
    for (let i = form.sections.length - 1; i >= 0; i--) {
      if (usable([i])) return { container: [i], index: form.sections[i].items.length };
    }
    return { container: [], index: 0 };
  }

  add(type: string, container?: number[], index?: number): boolean {
    if (this.readonly()) return false;
    const place = container ? { container, index: index ?? 0 } : this.target();
    if (!place.container.length) {
      this.announcement.set('Every section is locked. Add a section first.');
      return false;
    }
    const item = newItem(this.form(), type);
    const next = insertItem(this.form(), place.container, place.index, item);
    this.apply(next, `Added ${typeName(type).toLowerCase()} "${nodeName(item)}" to ${this.containerName(place.container)}.`);
    this.select(findPath(next, item.key));
    return true;
  }

  addSection(): void {
    if (this.readonly()) return;
    const section = newSection(this.form());
    const next = insertSection(this.form(), this.form().sections.length, section);
    this.apply(next, `Added section "${nodeName(section)}".`);
    this.select([next.sections.length - 1]);
  }

  containerName(path: NodePath): string {
    const node = nodeAt(this.form(), path);
    return node ? (path.length === 1 ? 'section' : typeName((node as FormItem).type).toLowerCase()) + ' "' + nodeName(node) + '"' : 'the form';
  }

  /** Drop from the palette, or a move between sections and groups. */
  dropped(event: CdkDragDrop<number[] | null, number[] | null, string | number[]>): void {
    const to = event.container.data;
    if (!to || this.readonly()) return;
    if (event.previousContainer.id === 'form-palette') {
      this.add(event.item.data as string, to, event.currentIndex);
      return;
    }
    if (event.previousContainer === event.container && event.previousIndex === event.currentIndex) return;
    this.moveTo(event.item.data as number[], to, event.currentIndex);
  }

  canDrop = (drag: CdkDrag<string | number[]>, drop: CdkDropList<number[] | null>): boolean => {
    const to = drop.data;
    if (!to || this.frozen(to)) return false;
    const from = drag.data;
    return typeof from === 'string' || !(to.length >= from.length && from.every((n, i) => to[i] === n));
  };

  noDrop = (): boolean => false;

  moveTo(from: number[], to: number[], index: number): void {
    const form = this.form();
    const node = nodeAt(form, from);
    if (!node || this.frozen(from) || this.frozen(to)) return;
    const next = moveItem(form, from, to, index);
    if (next === form) return;
    const path = findPath(next, node.key)!;
    this.apply(next, `Moved "${nodeName(node)}" to place ${path[path.length - 1] + 1} in ${this.containerName(path.slice(0, -1))}.`);
    this.select(path);
  }

  sectionDropped(event: CdkDragDrop<unknown>): void {
    if (this.readonly() || event.previousIndex === event.currentIndex) return;
    const section = this.form().sections[event.previousIndex];
    this.apply(moveSection(this.form(), event.previousIndex, event.currentIndex),
      `Moved section "${nodeName(section)}" to place ${event.currentIndex + 1}.`);
    this.select([event.currentIndex]);
  }

  move(path: NodePath, by: -1 | 1): void {
    if (this.readonly() || (path.length > 1 && this.frozen(path.slice(0, -1)))) return;
    const form = this.form();
    const node = nodeAt(form, path);
    const next = shift(form, path, by);
    if (!node || next === form) return;
    const moved = findPath(next, node.key)!;
    this.apply(next, `Moved "${nodeName(node)}" ${by < 0 ? 'up' : 'down'} to place ${moved[moved.length - 1] + 1}.`);
    this.select(moved);
  }

  moveKey(key: string, by: -1 | 1): void {
    const path = findPath(this.form(), key);
    if (path) this.move(path, by);
  }

  /** Alt with the arrow keys moves the focused row, as drag and drop would. */
  rowKey(event: KeyboardEvent, key: string): void {
    if (!event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown')) return;
    event.preventDefault();
    this.moveKey(key, event.key === 'ArrowUp' ? -1 : 1);
    const moved = findPath(this.form(), key);
    if (moved) this.focusAfterRender(this.rowId(moved));
  }

  first(path: NodePath): boolean { return path[path.length - 1] === 0; }
  last(path: NodePath): boolean {
    const length = path.length === 1 ? this.form().sections.length : nodeAt(this.form(), path.slice(0, -1))?.items?.length ?? 0;
    return path[path.length - 1] === length - 1;
  }

  removeSelected(): void {
    const path = this.selected();
    if (path) this.remove(path);
  }

  remove(path: NodePath): void {
    const node = nodeAt(this.form(), path);
    if (!node || this.readonly() || this.frozen(path)) return;
    const next = removeNode(this.form(), path);
    this.apply(next, `Removed "${nodeName(node)}".`);
    this.rules.update(rules => ({ ...rules, locked: rules.locked.filter(key => findPath(next, key)) }));
    this.select(null);
  }

  /** Moves the selected item to the end of another section or group, from the "Move to" list. */
  moveToPlace(event: Event): void {
    const path = this.selected();
    const value = (event.target as HTMLSelectElement).value;
    if (!path || !value) return;
    const to = value.split('-').map(Number);
    this.moveTo(path, to, nodeAt(this.form(), to)?.items?.length ?? 0);
  }

  placeValue(path: NodePath): string { return path.join('-'); }
  currentPlace(): string { return (this.selected() ?? []).slice(0, -1).join('-'); }

  // Properties of the selected node.

  private change(patch: Record<string, unknown>): void {
    const path = this.selected();
    if (!path || this.readonly() || this.frozen(path)) return;
    this.apply(updateNode(this.form(), path, patch));
  }

  private value(event: Event): string { return (event.target as HTMLInputElement).value; }

  setKey(event: Event): void {
    const path = this.selected();
    const key = this.value(event).trim();
    const node = this.node();
    if (!path || !node || key === node.key) return;
    if (!keyPattern.test(key)) {
      this.keyError.set('A key starts with a small letter and uses only small letters, digits and underscores, up to 40 characters.');
      return;
    }
    if (findPath(this.form(), key)) {
      this.keyError.set(`The key "${key}" is already used in this form.`);
      return;
    }
    if (this.readonly() || this.frozen(path)) return;
    this.keyError.set('');
    this.apply(renameKey(this.form(), path, key), `Changed the key to "${key}".`);
    this.rules.update(rules => ({ ...rules, locked: rules.locked.map(k => k === node.key ? key : k) }));
  }

  setText(name: string, language: string, event: Event): void {
    const value = this.value(event).trim();
    const current = { ...((this.node() as unknown as Record<string, Record<string, string> | undefined>)?.[name] ?? {}) };
    if (value) current[language] = value;
    else delete current[language];
    this.change({ [name]: Object.keys(current).length ? current : undefined });
  }

  setNumber(name: string, event: Event): void {
    const raw = this.value(event).trim();
    const number = Number(raw);
    this.change({ [name]: raw === '' || Number.isNaN(number) ? undefined : number });
  }

  setString(name: string, event: Event): void {
    const value = this.value(event).trim();
    this.change({ [name]: value || undefined });
  }

  setFlag(name: string, event: Event): void {
    this.change({ [name]: (event.target as HTMLInputElement).checked || undefined });
  }

  setAccept(event: Event): void {
    const list = this.value(event).split(',').map(s => s.trim()).filter(Boolean);
    this.change({ accept: list.length ? list : undefined });
  }

  acceptText(): string { const v = this.item()?.['accept']; return Array.isArray(v) ? v.join(', ') : ''; }

  setJSON(name: string, event: Event): void {
    const raw = this.value(event).trim();
    if (!raw) { this.change({ [name]: undefined }); this.jsonError.set(''); return; }
    try {
      this.change({ [name]: JSON.parse(raw) });
      this.jsonError.set('');
    } catch {
      this.jsonError.set('This is not valid JSON. Check the brackets, quotes and commas.');
    }
  }

  jsonOf(name: string): string {
    const v = this.item()?.[name];
    return v === undefined ? '' : JSON.stringify(v, null, 2);
  }

  // Choices.

  choices(): Choice[] { const item = this.item(); return item ? choicesOf(this.form(), item) : []; }
  ownChoices(): boolean { return !!this.item()?.choices; }

  setChoice(index: number, name: 'key' | 'label', event: Event): void {
    const choices = structuredClone(this.item()?.choices ?? []);
    const value = this.value(event).trim();
    if (!choices[index]) return;
    if (name === 'key') choices[index].key = value;
    else choices[index].label = { ...choices[index].label, en: value };
    this.change({ choices });
  }

  addChoice(): void {
    const choices = structuredClone(this.item()?.choices ?? []);
    let n = choices.length + 1;
    while (choices.some(c => c.key === 'choice_' + n)) n++;
    choices.push({ key: 'choice_' + n, label: { en: 'Choice ' + n } });
    this.change({ choices });
  }

  removeChoice(index: number): void {
    const choices = (this.item()?.choices ?? []).filter((_, i) => i !== index);
    this.change({ choices });
  }

  moveChoice(index: number, by: -1 | 1): void {
    const choices = structuredClone(this.item()?.choices ?? []);
    if (index + by < 0 || index + by >= choices.length) return;
    [choices[index], choices[index + by]] = [choices[index + by], choices[index]];
    this.change({ choices });
  }

  // Conditions. Simple rules, joined by "all" or "any", get a form; anything
  // else is edited as JSON.

  rulesOf(condition: Condition | undefined): Condition[] | null {
    if (!condition) return [];
    if (simpleTests.has(condition.op)) return [condition];
    if ((condition.op === 'all' || condition.op === 'any') && condition.conditions?.every(c => simpleTests.has(c.op))) return condition.conditions;
    return null;
  }

  joinOf(condition: Condition | undefined): string { return condition?.op === 'any' ? 'any' : 'all'; }

  private setRules(rules: Condition[], join: string): void {
    this.change({ showWhen: rules.length === 0 ? undefined : rules.length === 1 ? rules[0] : { op: join, conditions: rules } });
  }

  addRule(): void {
    const node = this.node();
    const rules = this.rulesOf(node?.showWhen) ?? [];
    const own = new Set(this.ownKeys());
    const field = this.fields().find(f => !own.has(f.key));
    if (!field) { this.announcement.set('Add a question to test first.'); return; }
    this.setRules([...rules, { op: 'answered', field: field.key }], this.joinOf(node?.showWhen));
  }

  removeRule(index: number): void {
    const node = this.node();
    this.setRules((this.rulesOf(node?.showWhen) ?? []).filter((_, i) => i !== index), this.joinOf(node?.showWhen));
  }

  setJoin(event: Event): void {
    const node = this.node();
    this.setRules(this.rulesOf(node?.showWhen) ?? [], this.value(event));
  }

  /** Changes the field, test or value of one rule. The value is kept as the field's kind of value. */
  setRule(index: number, part: 'field' | 'op' | 'value', event: Event): void {
    const node = this.node();
    const rules = structuredClone(this.rulesOf(node?.showWhen) ?? []);
    const rule = rules[index];
    if (!rule) return;
    const raw = this.value(event);
    if (part === 'field') {
      rule.field = raw;
      if (!testsFor(this.fieldNamed(raw)).includes(rule.op)) { rule.op = 'answered'; delete rule.value; }
    } else if (part === 'op') {
      rule.op = raw;
      if (raw === 'answered') delete rule.value;
      else if (rule.value === undefined) rule.value = this.defaultValue(this.fieldNamed(rule.field));
    } else {
      const field = this.fieldNamed(rule.field);
      const numeric = field?.type === 'number' || field?.type === 'calculated' || field?.['source'] === 'fire-power';
      rule.value = numeric && raw.trim() !== '' && !Number.isNaN(Number(raw)) ? Number(raw) : raw;
    }
    this.setRules(rules, this.joinOf(node?.showWhen));
  }

  fieldNamed(key: string | undefined): FormItem | undefined { return this.fields().find(f => f.key === key); }
  testsOf(rule: Condition): string[] { return testsFor(this.fieldNamed(rule.field)); }
  choicesFor(rule: Condition): Choice[] { const f = this.fieldNamed(rule.field); return f && f.type.endsWith('choice') ? choicesOf(this.form(), f) : []; }
  ruleValue(rule: Condition): string { return rule.value === undefined ? '' : String(rule.value); }

  private defaultValue(field: FormItem | undefined): unknown {
    if (!field) return '';
    const choices = field.type.endsWith('choice') ? choicesOf(this.form(), field) : [];
    if (choices.length) return choices[0].key;
    return field.type === 'number' || field.type === 'calculated' ? 0 : '';
  }

  /** Keys inside the selected node, which its own condition cannot use. */
  private ownKeys(): string[] {
    const node = this.node();
    const keys: string[] = [];
    const walk = (n: FormSection | FormItem) => { keys.push(n.key); n.items?.forEach(walk); };
    if (node) walk(node);
    return keys;
  }

  conditionFields(): FormItem[] {
    const own = new Set(this.ownKeys());
    return this.fields().filter(f => !own.has(f.key));
  }

  // Form settings and template rules.

  setFormText(name: 'title' | 'description', event: Event): void {
    if (this.readonly()) return;
    const value = this.value(event).trim();
    const form = structuredClone(this.form());
    if (value) form[name] = { ...(form[name] ?? {}), en: value };
    else if (name === 'description') delete form.description;
    this.apply(form);
  }

  setAllowExtension(event: Event): void {
    this.rules.update(rules => ({ ...rules, allowExtension: (event.target as HTMLInputElement).checked }));
  }

  toggleLock(event: Event): void {
    const path = this.selected();
    const node = path ? nodeAt(this.form(), path) : undefined;
    if (!node || this.inherited().has(node.key) || this.readonly()) return;
    const on = (event.target as HTMLInputElement).checked;
    this.rules.update(rules => ({ ...rules, locked: on ? [...rules.locked, node.key] : rules.locked.filter(k => k !== node.key) }));
    this.announcement.set(on ? `Locked "${nodeName(node)}" for the forms that extend this one.` : `Unlocked "${nodeName(node)}".`);
  }

  // The form file.

  setFileText(event: Event): void { this.fileText.set(this.value(event)); }

  applyFile(): void {
    if (this.readonly()) return;
    try {
      const parsed = JSON.parse(this.fileText()) as FormDefinition;
      if (!parsed || typeof parsed !== 'object' || !Array.isArray(parsed.sections)) {
        this.fileError.set('The file needs a list of sections.');
        return;
      }
      this.fileError.set('');
      this.apply(parsed, 'Applied the form file.');
      this.select(null);
    } catch {
      this.fileError.set('This is not valid JSON. Check the brackets, quotes and commas.');
    }
  }

  focusProblems(): void {
    afterNextRender(() => this.errorSummary()?.focus(), { injector: this.injector });
  }

  /** The summary can be used from Preview and Form file, where outline rows are not yet mounted. */
  protected showProblem(id: string): void {
    const needsRender = this.tab() !== 'design';
    this.setTab('design');
    this.select(id.slice('form-row-'.length).split('-').map(Number));
    if (needsRender) this.focusAfterRender(id);
  }

  private focusAfterRender(id: string): void {
    afterNextRender(() => this.document.getElementById(id)?.focus(), { injector: this.injector });
  }
}
