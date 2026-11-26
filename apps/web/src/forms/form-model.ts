// The form designer's model: the form format from packages/form-schema, and
// pure functions that change a form. Every change returns a new form, so
// the designer can validate and undo by comparing values.

export type Text = Record<string, string>;

export interface Condition {
  op: string;
  field?: string;
  value?: unknown;
  conditions?: Condition[];
  condition?: Condition;
}

export interface Choice { key: string; label: Text; parent?: string }

export interface FormItem {
  key: string;
  type: string;
  label?: Text;
  title?: Text;
  hint?: Text;
  description?: Text;
  required?: boolean;
  showWhen?: Condition;
  items?: FormItem[];
  choices?: Choice[];
  [property: string]: unknown;
}

export interface FormSection {
  key: string;
  title: Text;
  description?: Text;
  stage?: string;
  showWhen?: Condition;
  items: FormItem[];
}

export interface LayoutItem { item: string; widget?: string; width?: string; collapsed?: boolean }

export interface FormDefinition {
  $schema?: string;
  formatVersion: 1;
  id: string;
  version: number;
  title: Text;
  description?: Text;
  languages?: string[];
  stages?: { key: string; title: Text; workflowStep: string }[];
  lists?: { key: string; choices: Choice[] }[];
  sections: FormSection[];
  layout?: { pages?: { key: string; title?: Text; sections: string[] }[]; items?: LayoutItem[] };
}

/** A node is a section or an item, found by its position: [section, item, item…]. */
export type NodePath = readonly number[];

export interface FieldType { type: string; name: string; description: string; container?: boolean }

export const fieldTypes: readonly FieldType[] = [
  { type: 'text', name: 'Text', description: 'Words typed in' },
  { type: 'number', name: 'Number', description: 'A number with an optional unit' },
  { type: 'date', name: 'Date', description: 'A day' },
  { type: 'time', name: 'Time', description: 'A time of day' },
  { type: 'date-time', name: 'Date and time', description: 'A day and time' },
  { type: 'single-choice', name: 'Single choice', description: 'Pick one choice' },
  { type: 'multiple-choice', name: 'Multiple choice', description: 'Pick any number of choices' },
  { type: 'point', name: 'Point', description: 'A position from GPS or the map' },
  { type: 'track', name: 'Walked track', description: 'A line recorded while walking' },
  { type: 'polygon', name: 'Area', description: 'An area drawn on the map. Its size is worked out' },
  { type: 'photo', name: 'Photos', description: 'One or more photos' },
  { type: 'signature', name: 'Signature', description: 'A signature drawn on the screen' },
  { type: 'file', name: 'Files', description: 'One or more files' },
  { type: 'calculated', name: 'Calculated number', description: 'Worked out from other answers' },
  { type: 'alert-value', name: 'Value from the alert', description: 'Copied from the alert' },
  { type: 'group', name: 'Group', description: 'A sub-section', container: true },
  { type: 'repeat', name: 'Repeating group', description: 'Filled in once for each team, vehicle or place', container: true }
];

export const alertSources: readonly { value: string; label: string }[] = [
  { value: 'alert-id', label: 'Alert number' }, { value: 'observed-at', label: 'Seen by the satellite at' },
  { value: 'raised-at', label: 'Alert raised at' }, { value: 'location', label: 'Detection position' },
  { value: 'unit', label: 'Responsible unit' }, { value: 'forest-class', label: 'Forest class' },
  { value: 'source', label: 'Satellite product' }, { value: 'confidence', label: 'Confidence' },
  { value: 'fire-power', label: 'Fire power (MW)' }
];

export const typeName = (type: string): string =>
  type === 'section' ? 'Section' : fieldTypes.find(t => t.type === type)?.name ?? type;

export const isContainer = (item: { type?: string }): boolean => item.type === 'group' || item.type === 'repeat';

/** The words people see for a node, in English or the given language. */
export function textOf(text: Text | undefined, language = 'en'): string {
  return text?.[language] || text?.['en'] || '';
}

export function nodeName(node: FormSection | FormItem): string {
  const item = node as FormItem;
  return textOf(item.label ?? item.title) || node.key;
}

/** Every section and item key in the form, in order. */
export function allKeys(form: FormDefinition): string[] {
  const keys: string[] = [];
  const walk = (items: FormItem[]) => items.forEach(item => { keys.push(item.key); if (item.items) walk(item.items); });
  form.sections.forEach(section => { keys.push(section.key); walk(section.items); });
  return keys;
}

/** A new key from a base, unused in the form: text_1, text_2… */
export function freshKey(form: FormDefinition, base: string): string {
  const taken = new Set(allKeys(form));
  const stem = base.toLowerCase().replace(/[^a-z0-9_]/g, '_').replace(/^[^a-z]+/, '') || 'item';
  for (let n = 1; ; n++) {
    const key = (stem.slice(0, 35) + '_' + n);
    if (!taken.has(key)) return key;
  }
}

/** A new item of a type, with what the format needs to be valid. */
export function newItem(form: FormDefinition, type: string): FormItem {
  const key = freshKey(form, type.replace('-', '_'));
  if (type === 'group' || type === 'repeat') {
    const inner = freshKey({ ...form, sections: [...form.sections, { key, title: { en: '' }, items: [] }] }, 'text');
    const item: FormItem = { key, type, title: { en: type === 'group' ? 'New group' : 'New repeating group' },
      items: [{ key: inner, type: 'text', label: { en: 'New question' } }] };
    if (type === 'repeat') item['itemLabel'] = { en: 'Entry' };
    return item;
  }
  const item: FormItem = { key, type, label: { en: 'New question' } };
  if (type === 'single-choice' || type === 'multiple-choice') {
    item.choices = [{ key: 'yes', label: { en: 'Yes' } }, { key: 'no', label: { en: 'No' } }];
  }
  if (type === 'calculated') item['calculate'] = { op: 'number', value: 0 };
  if (type === 'alert-value') item['source'] = 'alert-id';
  return item;
}

export function newSection(form: FormDefinition): FormSection {
  const key = freshKey(form, 'section');
  const withSection = { ...form, sections: [...form.sections, { key, title: { en: '' }, items: [] }] };
  return { key, title: { en: 'New section' }, items: [{ key: freshKey(withSection, 'text'), type: 'text', label: { en: 'New question' } }] };
}

export function blankForm(id: string, title: string): FormDefinition {
  return {
    formatVersion: 1, id, version: 1, title: { en: title },
    sections: [{ key: 'details', title: { en: 'Details' }, items: [{ key: 'notes', type: 'text', label: { en: 'Notes' } }] }]
  };
}

export function nodeAt(form: FormDefinition, path: NodePath): FormSection | FormItem | undefined {
  let node: FormSection | FormItem | undefined = form.sections[path[0]];
  for (const index of path.slice(1)) node = node?.items?.[index];
  return node;
}

/** The path of the node with this key, if any. */
export function findPath(form: FormDefinition, key: string): number[] | null {
  let found: number[] | null = null;
  const walk = (items: FormItem[], path: number[]) => items.forEach((item, i) => {
    if (found) return;
    if (item.key === key) found = [...path, i];
    else if (item.items) walk(item.items, [...path, i]);
  });
  form.sections.forEach((section, i) => {
    if (!found && section.key === key) found = [i];
    else if (!found) walk(section.items, [i]);
  });
  return found;
}

/** The keys of the node and its ancestors, from the section down. */
export function keysAlong(form: FormDefinition, path: NodePath): string[] {
  const keys: string[] = [];
  for (let n = 1; n <= path.length; n++) {
    const node = nodeAt(form, path.slice(0, n));
    if (node) keys.push(node.key);
  }
  return keys;
}

function edit(form: FormDefinition, change: (copy: FormDefinition) => void): FormDefinition {
  const copy = structuredClone(form);
  change(copy);
  return copy;
}

function itemsAt(form: FormDefinition, container: NodePath): FormItem[] | undefined {
  return nodeAt(form, container)?.items;
}

/** Puts an item into a section or group at an index. */
export function insertItem(form: FormDefinition, container: NodePath, index: number, item: FormItem): FormDefinition {
  return edit(form, copy => {
    const items = itemsAt(copy, container);
    if (items) items.splice(Math.max(0, Math.min(index, items.length)), 0, structuredClone(item));
  });
}

export function insertSection(form: FormDefinition, index: number, section: FormSection): FormDefinition {
  return edit(form, copy => { copy.sections.splice(Math.max(0, Math.min(index, copy.sections.length)), 0, section); });
}

/** Changes properties of a node. A property set to undefined is removed. */
export function updateNode(form: FormDefinition, path: NodePath, patch: Record<string, unknown>): FormDefinition {
  return edit(form, copy => {
    const node = nodeAt(copy, path) as unknown as Record<string, unknown> | undefined;
    if (!node) return;
    for (const [name, value] of Object.entries(patch)) {
      if (value === undefined) delete node[name];
      else node[name] = structuredClone(value);
    }
  });
}

/**
 * Removes a section or item. Layout entries for the keys that go are removed
 * too, so the layout keeps naming only what exists.
 */
export function removeNode(form: FormDefinition, path: NodePath): FormDefinition {
  const node = nodeAt(form, path);
  if (!node) return form;
  const gone = new Set<string>();
  const collect = (n: FormSection | FormItem) => { gone.add(n.key); n.items?.forEach(collect); };
  collect(node);
  return edit(form, copy => {
    if (path.length === 1) copy.sections.splice(path[0], 1);
    else itemsAt(copy, path.slice(0, -1))?.splice(path[path.length - 1], 1);
    dropLayout(copy, gone);
  });
}

function dropLayout(form: FormDefinition, gone: Set<string>): void {
  if (!form.layout) return;
  if (form.layout.items) {
    form.layout.items = form.layout.items.filter(entry => !gone.has(entry.item));
    if (!form.layout.items.length) delete form.layout.items;
  }
  if (form.layout.pages) {
    form.layout.pages = form.layout.pages
      .map(page => ({ ...page, sections: page.sections.filter(key => !gone.has(key)) }))
      .filter(page => page.sections.length);
    if (!form.layout.pages.length) delete form.layout.pages;
  }
  if (!Object.keys(form.layout).length) delete form.layout;
}

const isInside = (path: NodePath, container: NodePath) =>
  container.length >= path.length && path.every((n, i) => container[i] === n);

/**
 * Moves an item to a container. The index is its place in the target list
 * once it has left its old place. Moving a group into itself does nothing.
 */
export function moveItem(form: FormDefinition, from: NodePath, to: NodePath, index: number): FormDefinition {
  if (from.length < 2 || isInside(from, to)) return form;
  const item = nodeAt(form, from) as FormItem | undefined;
  if (!item || !itemsAt(form, to)) return form;
  return edit(form, copy => {
    const source = itemsAt(copy, from.slice(0, -1))!;
    const target = itemsAt(copy, adjust(to, from))!;
    source.splice(from[from.length - 1], 1);
    target.splice(Math.max(0, Math.min(index, target.length)), 0, item);
  });
}

/** A container path after the item at `removed` leaves its list. */
function adjust(container: NodePath, removed: NodePath): number[] {
  const out = [...container];
  const depth = removed.length - 1;
  if (container.length > depth && removed.slice(0, depth).every((n, i) => container[i] === n) &&
      container[depth] > removed[depth]) out[depth]--;
  return out;
}

export function moveSection(form: FormDefinition, from: number, to: number): FormDefinition {
  if (from === to || to < 0 || to >= form.sections.length) return form;
  return edit(form, copy => { const [section] = copy.sections.splice(from, 1); copy.sections.splice(to, 0, section); });
}

/** Moves a node one place up or down in its own list. */
export function shift(form: FormDefinition, path: NodePath, by: -1 | 1): FormDefinition {
  const at = path[path.length - 1];
  if (path.length === 1) return moveSection(form, at, at + by);
  const container = path.slice(0, -1);
  const length = itemsAt(form, container)?.length ?? 0;
  if (at + by < 0 || at + by >= length) return form;
  return moveItem(form, path, container, at + by);
}

/** Every section, group and repeating group an item can go into, with a name. */
export function containers(form: FormDefinition): { path: number[]; name: string; depth: number }[] {
  const out: { path: number[]; name: string; depth: number }[] = [];
  const walk = (items: FormItem[], path: number[], depth: number) => items.forEach((item, i) => {
    if (!isContainer(item)) return;
    out.push({ path: [...path, i], name: typeName(item.type) + ': ' + nodeName(item), depth });
    walk(item.items ?? [], [...path, i], depth + 1);
  });
  form.sections.forEach((section, i) => {
    out.push({ path: [i], name: 'Section: ' + nodeName(section), depth: 0 });
    walk(section.items, [i], 1);
  });
  return out;
}

/**
 * Gives a node a new key, and changes the references to the old key in
 * conditions, calculations, cascading lists and the layout.
 */
export function renameKey(form: FormDefinition, path: NodePath, key: string): FormDefinition {
  const node = nodeAt(form, path);
  if (!node || node.key === key) return form;
  const old = node.key;
  const references = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(references);
    if (value && typeof value === 'object') {
      const out: Record<string, unknown> = {};
      for (const [name, inner] of Object.entries(value)) {
        out[name] = ['field', 'group', 'start', 'end'].includes(name) && inner === old ? key : references(inner);
      }
      return out;
    }
    return value;
  };
  return edit(form, copy => {
    (nodeAt(copy, path) as { key: string }).key = key;
    const walk = (items: FormItem[]) => items.forEach(item => {
      if (item.showWhen) item.showWhen = references(item.showWhen) as Condition;
      if (item['requireWhen']) item['requireWhen'] = references(item['requireWhen']);
      if (item['calculate']) item['calculate'] = references(item['calculate']);
      if (item['parent'] === old) item['parent'] = key;
      if (item.items) walk(item.items);
    });
    copy.sections.forEach(section => {
      if (section.showWhen) section.showWhen = references(section.showWhen) as Condition;
      walk(section.items);
    });
    copy.layout?.items?.forEach(entry => { if (entry.item === old) entry.item = key; });
    copy.layout?.pages?.forEach(page => { page.sections = page.sections.map(s => s === old ? key : s); });
  });
}

/** Fields a condition can test, with their names, in form order. */
export function fieldsOf(form: FormDefinition): FormItem[] {
  const out: FormItem[] = [];
  const walk = (items: FormItem[]) => items.forEach(item => { if (isContainer(item)) walk(item.items ?? []); else out.push(item); });
  form.sections.forEach(section => walk(section.items));
  return out;
}

/** The choices a choice field offers, from the field or its shared list. */
export function choicesOf(form: FormDefinition, item: FormItem): Choice[] {
  if (item.choices) return item.choices;
  const list = item['list'];
  return form.lists?.find(l => l.key === list)?.choices ?? [];
}

const opWords: Record<string, string> = {
  'equals': 'is', 'not-equals': 'is not', 'more-than': 'is more than', 'at-least': 'is at least',
  'less-than': 'is less than', 'at-most': 'is at most', 'includes': 'includes'
};

/** A condition in plain English, such as "Likely cause is Lightning". */
export function describeCondition(form: FormDefinition, condition: Condition | undefined, language = 'en'): string {
  if (!condition) return '';
  const field = (key?: string) => fieldsOf(form).find(f => f.key === key);
  const name = (item: FormItem | undefined, key?: string) => textOf(item?.label, language) || key || 'the question';
  switch (condition.op) {
    case 'answered':
      return name(field(condition.field), condition.field) + ' is answered';
    case 'all':
    case 'any': {
      const parts = (condition.conditions ?? []).map(c => {
        const part = describeCondition(form, c, language);
        return c.op === 'all' || c.op === 'any' ? '(' + part + ')' : part;
      });
      return parts.join(condition.op === 'all' ? ' and ' : ' or ');
    }
    case 'not':
      return 'not (' + describeCondition(form, condition.condition, language) + ')';
    default: {
      const target = field(condition.field);
      const choice = target ? choicesOf(form, target).find(c => c.key === condition.value) : undefined;
      const value = choice ? textOf(choice.label, language) : String(condition.value);
      return name(target, condition.field) + ' ' + (opWords[condition.op] ?? condition.op) + ' ' + value;
    }
  }
}

/** The tests that suit a field (form format, section 10). */
export function testsFor(item: FormItem | undefined): string[] {
  switch (item?.type) {
    case undefined: return [];
    case 'single-choice': case 'text': return ['answered', 'equals', 'not-equals'];
    case 'multiple-choice': return ['answered', 'includes'];
    case 'number': case 'calculated': case 'date': case 'time': case 'date-time':
      return ['answered', 'equals', 'not-equals', 'more-than', 'at-least', 'less-than', 'at-most'];
    case 'alert-value': {
      const source = item['source'];
      if (source === 'fire-power' || source === 'observed-at' || source === 'raised-at') {
        return ['answered', 'equals', 'not-equals', 'more-than', 'at-least', 'less-than', 'at-most'];
      }
      return source === 'location' ? ['answered'] : ['answered', 'equals', 'not-equals'];
    }
    default: return ['answered'];
  }
}

export const testWords: Record<string, string> = {
  'answered': 'is answered', ...opWords
};
