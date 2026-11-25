// Checks Open FIMRA form definitions.
//
// The format is described in SPEC.md and form.schema.json in the folder
// above. The Go package in that folder applies the same checks with the
// same messages; both are tested against the shared files in examples/.
// This module has no dependencies and uses only type syntax that Node.js
// can strip, so it runs in the browser, in Node.js and in the web app's
// build without a separate compile step.

import schemaJSON from '../form.schema.json' with { type: 'json' };

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
type JsonObject = { [key: string]: Json };
type Schema = JsonObject;

/** The JSON Schema for a form definition, exactly as published. */
export const formSchema: Readonly<JsonObject> = schemaJSON as unknown as JsonObject;

/** One value that a form version produces. See SPEC.md, "The data a form produces". */
export interface Column {
  name: string;
  field: string;
  valueType: string;
  unit?: string;
  repeat?: string;
}

const msgNotJSON = 'The form is not valid JSON.';

/**
 * Checks a form definition given as JSON text. Returns every problem found,
 * in a fixed order, as plain-English messages. An empty list means the form
 * is valid.
 */
export function validateFormText(text: string): string[] {
  let form: unknown;
  try {
    form = JSON.parse(text);
  } catch {
    return [msgNotJSON];
  }
  return validateForm(form);
}

/**
 * Checks a form definition that is already parsed. The rules that need a
 * well-formed form run only after the schema passes.
 */
export function validateForm(form: unknown): string[] {
  const problems = checkSchema(form as Json);
  if (problems.length > 0) {
    return problems;
  }
  return checkRules(form as JsonObject);
}

/** Lists the values that a valid form produces, in form order. */
export function formColumns(form: unknown): { columns: Column[] | null; problems: string[] } {
  const problems = validateForm(form);
  if (problems.length > 0) {
    return { columns: null, problems };
  }
  const r = new RulesChecker(form as JsonObject);
  r.collectItems();
  return { columns: columnsOf(r.items), problems: [] };
}

// ---------------------------------------------------------------------------
// The schema. Only the keywords that form.schema.json uses are understood, so
// that this module and the Go package give the same messages in the same
// order. Loading fails on any other keyword.

const schemaKeywords = new Set([
  '$schema', '$id', '$defs', '$ref', 'title', 'description',
  'type', 'enum', 'const', 'pattern', 'x-patternMessage',
  'minLength', 'maxLength', 'minimum', 'maximum',
  'required', 'properties', 'additionalProperties', 'propertyNames',
  'items', 'minItems', 'maxItems', 'allOf', 'if', 'then', 'else',
]);

const typeWords: Record<string, string> = {
  string: 'text', number: 'a number', integer: 'a whole number', boolean: 'true or false',
  object: 'an object', array: 'a list', null: 'empty',
};

function isObject(v: Json | undefined): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Orders text by Unicode code point, as Go does. */
function compareText(a: string, b: string): number {
  const x = Array.from(a, (c) => c.codePointAt(0) as number);
  const y = Array.from(b, (c) => c.codePointAt(0) as number);
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    if (x[i] !== y[i]) return x[i] - y[i];
  }
  return x.length - y.length;
}

function sortedKeys(o: JsonObject): string[] {
  return Object.keys(o).sort(compareText);
}

class SchemaChecker {
  readonly patterns = new Map<string, RegExp>();
  readonly root: Schema;

  constructor(root: Schema) {
    this.root = root;
    this.prepare(root, '#');
    for (const [name, def] of Object.entries(root['$defs'] as JsonObject)) {
      this.prepare(def as Schema, `#/$defs/${name}`);
    }
  }

  private prepare(s: Schema, at: string): void {
    for (const k of Object.keys(s)) {
      if (!schemaKeywords.has(k)) {
        throw new Error(`form-schema: ${at} uses the keyword "${k}", which the validators do not support`);
      }
    }
    if (typeof s['$ref'] === 'string') this.resolve(s['$ref']);
    if (typeof s['pattern'] === 'string') this.patterns.set(s['pattern'], new RegExp(s['pattern'], 'u'));
    if (Array.isArray(s['enum']) && s['enum'].some((v) => typeof v !== 'string')) {
      throw new Error(`form-schema: ${at} has an enum value that is not text`);
    }
    const sub = (name: string, v: Json | undefined) => {
      if (isObject(v)) this.prepare(v, `${at}/${name}`);
    };
    if (isObject(s['properties'])) {
      for (const [name, p] of Object.entries(s['properties'])) sub(`properties/${name}`, p);
    }
    for (const name of ['additionalProperties', 'propertyNames', 'items', 'if', 'then', 'else']) sub(name, s[name]);
    if (Array.isArray(s['allOf'])) s['allOf'].forEach((p, i) => sub(`allOf/${i}`, p));
  }

  resolve(ref: string): Schema {
    if (!ref.startsWith('#/$defs/')) throw new Error(`form-schema: unsupported reference ${ref}`);
    const def = (this.root['$defs'] as JsonObject)[ref.slice('#/$defs/'.length)];
    if (!isObject(def)) throw new Error(`form-schema: unknown reference ${ref}`);
    return def;
  }

  check(s: Schema, v: Json, at: string, out: string[]): void {
    const add = (rest: string) => out.push(`${subject(at)} ${rest}.`);
    if (typeof s['$ref'] === 'string') this.check(this.resolve(s['$ref']), v, at, out);
    const t = s['type'];
    if (typeof t === 'string' && !hasType(v, t)) {
      add(`must be ${typeWords[t]}`);
      return;
    }
    const e = s['enum'];
    if (Array.isArray(e) && (typeof v !== 'string' || !e.includes(v))) {
      add(`must be one of: ${e.join(', ')}`);
    }
    if ('const' in s && !sameValue(v, s['const'])) {
      add(`must be ${describeValue(s['const'])}`);
    }
    if (typeof v === 'string') {
      const p = s['pattern'];
      if (typeof p === 'string' && !(this.patterns.get(p) as RegExp).test(v)) {
        const m = s['x-patternMessage'];
        add(typeof m === 'string' ? m : `does not match the pattern ${p}`);
      }
      const n = Array.from(v).length;
      const min = s['minLength'];
      if (typeof min === 'number' && n < min) {
        add(min === 1 ? 'must not be empty' : `must have at least ${plural(min, 'character')}`);
      }
      const max = s['maxLength'];
      if (typeof max === 'number' && n > max) add(`must have at most ${plural(max, 'character')}`);
    } else if (typeof v === 'number') {
      const min = s['minimum'];
      if (typeof min === 'number' && v < min) add(`must be at least ${formatNumber(min)}`);
      const max = s['maximum'];
      if (typeof max === 'number' && v > max) add(`must be at most ${formatNumber(max)}`);
    } else if (isObject(v)) {
      const req = s['required'];
      if (Array.isArray(req)) {
        for (const name of req as string[]) {
          if (!(name in v)) add(`is missing "${name}"`);
        }
      }
      const props = isObject(s['properties']) ? s['properties'] : {};
      for (const name of sortedKeys(v)) {
        const pn = s['propertyNames'];
        if (isObject(pn)) {
          this.check(pn, name, `The name "${name}" in ${at === '' ? 'the form' : at}`, out);
        }
        const p = Object.hasOwn(props, name) ? props[name] : undefined;
        if (isObject(p)) {
          this.check(p, v[name], childPath(at, name), out);
          continue;
        }
        const extra = s['additionalProperties'];
        if (extra === false) {
          add(`has "${name}", which is not allowed here`);
        } else if (isObject(extra)) {
          this.check(extra, v[name], childPath(at, name), out);
        }
      }
    } else if (Array.isArray(v)) {
      const min = s['minItems'];
      if (typeof min === 'number' && v.length < min) {
        add(min === 1 ? 'must not be empty' : `must have at least ${plural(min, 'item')}`);
      }
      const max = s['maxItems'];
      if (typeof max === 'number' && v.length > max) add(`must have at most ${plural(max, 'item')}`);
      const items = s['items'];
      if (isObject(items)) v.forEach((item, i) => this.check(items, item, `${at}[${i}]`, out));
    }
    const all = s['allOf'];
    if (Array.isArray(all)) {
      for (const sub of all) this.check(sub as Schema, v, at, out);
    }
    const cond = s['if'];
    if (isObject(cond)) {
      const trial: string[] = [];
      this.check(cond, v, at, trial);
      const next = s[trial.length === 0 ? 'then' : 'else'];
      if (isObject(next)) this.check(next, v, at, out);
    }
  }
}

function subject(at: string): string {
  return at === '' ? 'The form' : at;
}

function childPath(at: string, name: string): string {
  return at === '' ? name : `${at}.${name}`;
}

function hasType(v: Json, t: string): boolean {
  switch (t) {
    case 'string': return typeof v === 'string';
    case 'number': return typeof v === 'number';
    case 'integer': return typeof v === 'number' && Number.isInteger(v);
    case 'boolean': return typeof v === 'boolean';
    case 'object': return isObject(v);
    case 'array': return Array.isArray(v);
    case 'null': return v === null;
  }
  throw new Error(`form-schema: unknown type ${t}`);
}

function sameValue(v: Json, want: Json): boolean {
  if (typeof want !== 'string' && typeof want !== 'number') {
    throw new Error('form-schema: const must be text or a number');
  }
  return v === want;
}

function describeValue(v: Json): string {
  return typeof v === 'string' ? `"${v}"` : formatNumber(v as number);
}

function formatNumber(n: number): string {
  return String(n);
}

function plural(n: number, word: string): string {
  return n === 1 ? `1 ${word}` : `${formatNumber(n)} ${word}s`;
}

let schemaRules: SchemaChecker | undefined;

function checkSchema(form: Json): string[] {
  schemaRules ??= new SchemaChecker(formSchema as Schema);
  const out: string[] = [];
  schemaRules.check(schemaRules.root, form, '', out);
  return out;
}

// ---------------------------------------------------------------------------
// The rules that a JSON Schema cannot express. They run only on forms that
// pass the schema, so they trust the shape of every value. The Go copy in
// rules.go follows the same steps in the same order.

interface Item {
  key: string;
  kind: string; // "section", "group", "repeat" or a field type
  node: JsonObject;
  path: string;
  parent: Item | null; // the section or group that holds the item
  repeats: string[]; // keys of the repeating groups around the item, outermost first
  stage: number; // index of the item's stage, or -1
  deps: Item[]; // fields this item's visibility, value or choices depend on
}

function objects(v: Json | undefined): JsonObject[] {
  return Array.isArray(v) ? (v as JsonObject[]) : [];
}

function stringList(v: Json | undefined): string[] {
  return Array.isArray(v) ? (v as string[]) : [];
}

function str(o: JsonObject, name: string): string | undefined {
  const v = o[name];
  return typeof v === 'string' ? v : undefined;
}

function num(o: JsonObject, name: string): number | undefined {
  const v = o[name];
  return typeof v === 'number' ? v : undefined;
}

function has(o: JsonObject, name: string): boolean {
  return Object.hasOwn(o, name);
}

function checkRules(form: JsonObject): string[] {
  const r = new RulesChecker(form);
  r.checkLanguages();
  r.checkStages();
  r.checkLists();
  r.collectItems();
  for (const it of r.items) r.checkItem(it);
  r.checkColumnNames();
  r.checkCycles();
  r.checkStagesUsed();
  r.checkLayout();
  r.checkTranslations();
  return r.out;
}

function duplicateChoices(choices: JsonObject[]): string[] {
  const seen = new Set<string>();
  const dups: string[] = [];
  for (const c of choices) {
    const k = c['key'] as string;
    if (seen.has(k)) dups.push(k);
    seen.add(k);
  }
  return dups;
}

function isField(it: Item): boolean {
  return it.kind !== 'section' && it.kind !== 'group' && it.kind !== 'repeat';
}

function owner(it: Item): string {
  switch (it.kind) {
    case 'section': return `Section "${it.key}"`;
    case 'group': return `Group "${it.key}"`;
    case 'repeat': return `Repeating group "${it.key}"`;
  }
  return `Field "${it.key}"`;
}

const kindWords: Record<string, string> = {
  section: 'a section', group: 'a group', repeat: 'a repeating group',
  text: 'a text field', number: 'a number field', date: 'a date field', time: 'a time field',
  'date-time': 'a date and time field', 'single-choice': 'a single choice field',
  'multiple-choice': 'a multiple choice field', point: 'a GPS point', track: 'a walked track',
  polygon: 'a drawn area', photo: 'a photo field', signature: 'a signature', file: 'a file field',
  calculated: 'a calculated field', 'alert-value': 'a value from the alert',
};

const alertValueTypes: Record<string, string> = {
  'alert-id': 'text', 'observed-at': 'date-time', 'raised-at': 'date-time', location: 'point',
  unit: 'text', 'forest-class': 'text', source: 'text', confidence: 'text', 'fire-power': 'number',
};

const fieldValueTypes: Record<string, string> = {
  text: 'text', number: 'number', date: 'date', time: 'time', 'date-time': 'date-time',
  'single-choice': 'choice', 'multiple-choice': 'choices', point: 'point', track: 'line',
  polygon: 'polygon', photo: 'attachments', signature: 'attachment', file: 'attachments',
  calculated: 'number',
};

function valueType(it: Item): string {
  if (it.kind === 'alert-value') return alertValueTypes[it.node['source'] as string];
  return fieldValueTypes[it.kind];
}

const compareOps: Record<string, string[]> = {
  equals: ['text', 'number', 'date', 'time', 'date-time', 'choice'],
  'not-equals': ['text', 'number', 'date', 'time', 'date-time', 'choice'],
  'more-than': ['number', 'date', 'time', 'date-time'],
  'at-least': ['number', 'date', 'time', 'date-time'],
  'less-than': ['number', 'date', 'time', 'date-time'],
  'at-most': ['number', 'date', 'time', 'date-time'],
  includes: ['choices'],
};

const datePattern = /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/;
const timePattern = /^[0-9]{2}:[0-9]{2}$/;
const dateTimePattern = /^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}(:[0-9]{2})?(Z|[+-][0-9]{2}:[0-9]{2})$/;
const countPattern = /^\{[0-9]+(,[0-9]*)?\}/;

/**
 * Returns the first part of a pattern that the Go and browser regular
 * expression engines read differently, or "" if none.
 */
function unsupportedPattern(p: string): string {
  let inClass = false;
  for (let i = 0; i < p.length; i++) {
    const c = p[i];
    const next = i + 1 < p.length ? p[i + 1] : '';
    if (c === '\\') {
      if (next !== '' && '123456789kAzQECpPGZ'.includes(next)) return `\\${next}`;
      i++;
      continue;
    }
    if (inClass) {
      if (c === ']') inClass = false;
      else if (c === '[' && next === ':') return '[:';
      continue;
    }
    switch (c) {
      case '[':
        if (next === ']' || (next === '^' && p[i + 2] === ']')) return '[]';
        inClass = true;
        break;
      case '(':
        if (next === '?') return '(?';
        break;
      case '{': {
        const m = countPattern.exec(p.slice(i));
        if (m === null) return '{';
        i += m[0].length - 1;
        break;
      }
      case '}':
        return '}';
      case ']':
        return ']';
    }
  }
  return '';
}

function choiceKeys(choices: JsonObject[]): Set<string> {
  return new Set(choices.map((c) => c['key'] as string));
}

function isInside(it: Item, container: Item): boolean {
  for (let p = it.parent; p !== null; p = p.parent) {
    if (p === container) return true;
  }
  return false;
}

function repeatOutside(target: string[], from: string[]): string {
  for (let i = 0; i < target.length; i++) {
    if (i >= from.length || from[i] !== target[i]) return target[i];
  }
  return '';
}

function sameList(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

function columnsOf(items: Item[]): Column[] {
  const cols: Column[] = [];
  const column = (name: string, field: string, valueType: string, unit: string, repeat: string): Column => {
    const c: Column = { name, field, valueType };
    if (unit !== '') c.unit = unit;
    if (repeat !== '') c.repeat = repeat;
    return c;
  };
  for (const it of items) {
    if (!isField(it)) continue;
    const repeat = it.repeats.length > 0 ? it.repeats[it.repeats.length - 1] : '';
    let unit = str(it.node, 'unit') ?? '';
    if (it.kind === 'alert-value' && it.node['source'] === 'fire-power') unit = 'MW';
    cols.push(column(it.key, it.key, valueType(it), unit, repeat));
    if (it.kind === 'polygon') cols.push(column(`${it.key}_area_ha`, it.key, 'number', 'ha', repeat));
    if (it.kind === 'track') cols.push(column(`${it.key}_length_km`, it.key, 'number', 'km', repeat));
  }
  return cols;
}

const widgetKinds: Record<string, string[]> = {
  line: ['text'], paragraph: ['text'], input: ['number'], stepper: ['number'],
  radio: ['single-choice'], dropdown: ['single-choice'], buttons: ['single-choice', 'multiple-choice'],
  checkboxes: ['multiple-choice'],
};

const textProperties = ['title', 'label', 'description', 'itemLabel', 'hint', 'patternHint'];

class RulesChecker {
  readonly out: string[] = [];
  readonly items: Item[] = [];
  readonly byKey = new Map<string, Item>();
  readonly lists = new Map<string, JsonObject[]>();
  readonly stages = new Map<string, number>();
  hasStages = false;
  readonly form: JsonObject;

  constructor(form: JsonObject) {
    this.form = form;
  }

  private add(msg: string): void {
    this.out.push(msg);
  }

  checkLanguages(): void {
    if (!has(this.form, 'languages')) return;
    const seen = new Set<string>();
    for (const l of stringList(this.form['languages'])) {
      if (seen.has(l)) this.add(`languages lists "${l}" more than once.`);
      seen.add(l);
    }
    if (!seen.has('en')) this.add('languages must include "en".');
  }

  checkStages(): void {
    this.hasStages = has(this.form, 'stages');
    const steps = new Map<string, string>();
    objects(this.form['stages']).forEach((s, i) => {
      const key = s['key'] as string;
      const step = s['workflowStep'] as string;
      if (this.stages.has(key)) this.add(`Stage "${key}" is defined more than once.`);
      else this.stages.set(key, i);
      const other = steps.get(step);
      if (other !== undefined) this.add(`Stages "${other}" and "${key}" both use the workflow step "${step}".`);
      else steps.set(step, key);
    });
  }

  checkLists(): void {
    for (const l of objects(this.form['lists'])) {
      const key = l['key'] as string;
      if (this.lists.has(key)) {
        this.add(`List "${key}" is defined more than once.`);
        continue;
      }
      const choices = objects(l['choices']);
      this.lists.set(key, choices);
      for (const c of duplicateChoices(choices)) this.add(`List "${key}" has the choice "${c}" more than once.`);
    }
  }

  collectItems(): void {
    objects(this.form['sections']).forEach((s, i) => {
      const name = str(s, 'stage');
      const stage = name !== undefined ? (this.stages.get(name) ?? -1) : -1;
      const sec = this.addItem(s, 'section', `sections[${i}]`, null, [], stage);
      this.collectChildren(sec, []);
    });
  }

  private collectChildren(parent: Item, repeats: string[]): void {
    objects(parent.node['items']).forEach((n, i) => {
      const it = this.addItem(n, n['type'] as string, `${parent.path}.items[${i}]`, parent, repeats, parent.stage);
      if (it.kind === 'group') this.collectChildren(it, repeats);
      else if (it.kind === 'repeat') this.collectChildren(it, [...repeats, it.key]);
    });
  }

  private addItem(node: JsonObject, kind: string, path: string, parent: Item | null, repeats: string[], stage: number): Item {
    const it: Item = { key: node['key'] as string, kind, node, path, parent, repeats, stage, deps: [] };
    if (this.byKey.has(it.key)) this.add(`The key "${it.key}" is used by more than one item.`);
    else this.byKey.set(it.key, it);
    this.items.push(it);
    return it;
  }

  checkItem(it: Item): void {
    const who = owner(it);
    const n = it.node;
    if (it.kind === 'section') {
      const name = str(n, 'stage');
      if (name !== undefined) {
        if (!this.stages.has(name)) this.add(`${who}: uses the stage "${name}", which does not exist.`);
      } else if (this.hasStages) {
        this.add(`${who}: needs a stage, because the form has stages.`);
      }
    }
    for (const [loName, hiName] of [['minLength', 'maxLength'], ['min', 'max'], ['minSelected', 'maxSelected'], ['minCount', 'maxCount'], ['minRepeats', 'maxRepeats']]) {
      const lo = num(n, loName);
      const hi = num(n, hiName);
      if (lo !== undefined && hi !== undefined && lo > hi) this.add(`${who}: ${loName} is more than ${hiName}.`);
    }
    const p = str(n, 'pattern');
    if (p !== undefined) {
      const bad = unsupportedPattern(p);
      if (bad !== '') {
        this.add(`${who}: pattern uses "${bad}", which not every device supports.`);
      } else {
        try {
          new RegExp(`^(?:${p})$`, 'u');
        } catch {
          this.add(`${who}: pattern is not a valid regular expression.`);
        }
      }
    }
    if (it.kind === 'single-choice' || it.kind === 'multiple-choice') this.checkChoices(it);
    if (has(n, 'required') && has(n, 'requireWhen')) {
      this.add(`${who}: has both "required" and "requireWhen"; use one.`);
    }
    if (isObject(n['showWhen'])) this.checkCondition(it, 'showWhen', n['showWhen']);
    if (isObject(n['requireWhen'])) this.checkCondition(it, 'requireWhen', n['requireWhen']);
    if (isObject(n['calculate'])) this.checkExpression(it, n['calculate']);
  }

  private choicesOf(it: Item): JsonObject[] | undefined {
    if (Array.isArray(it.node['choices'])) return objects(it.node['choices']);
    const name = str(it.node, 'list');
    return name !== undefined ? this.lists.get(name) : undefined;
  }

  private checkChoices(it: Item): void {
    const who = owner(it);
    const hasChoices = has(it.node, 'choices');
    const listName = str(it.node, 'list');
    if (!hasChoices && listName === undefined) {
      this.add(`${who}: needs "choices" or "list".`);
      return;
    }
    if (hasChoices && listName !== undefined) {
      this.add(`${who}: has both "choices" and "list"; use one.`);
      return;
    }
    if (listName !== undefined && !this.lists.has(listName)) {
      this.add(`${who}: uses the list "${listName}", which does not exist.`);
      return;
    }
    const choices = this.choicesOf(it) as JsonObject[];
    if (hasChoices) {
      for (const c of duplicateChoices(choices)) this.add(`${who}: has the choice "${c}" more than once.`);
    }
    const parentKey = str(it.node, 'parent');
    if (parentKey === undefined) {
      for (const c of choices) {
        if (has(c, 'parent')) this.add(`${who}: the choice "${c['key'] as string}" has a parent, but the field has no "parent".`);
      }
      return;
    }
    const parent = this.resolve(it, 'parent', parentKey);
    if (parent === null) return;
    if (parent.kind !== 'single-choice') {
      this.add(`${who}: parent "${parentKey}" is ${kindWords[parent.kind]}, not a single choice field.`);
      return;
    }
    it.deps.push(parent);
    const parentChoices = this.choicesOf(parent);
    if (parentChoices === undefined) return;
    const allowed = choiceKeys(parentChoices);
    for (const c of choices) {
      const key = c['key'] as string;
      const p = str(c, 'parent');
      if (p === undefined) this.add(`${who}: the choice "${key}" has no parent.`);
      else if (!allowed.has(p)) this.add(`${who}: the choice "${key}" has the parent "${p}", which is not a choice of "${parentKey}".`);
    }
  }

  private placeProblem(from: Item, prop: string, key: string, target: Item): boolean {
    const who = owner(from);
    if (isInside(target, from)) {
      this.add(`${who}: ${prop} refers to "${key}", which is inside it.`);
      return true;
    }
    const rep = repeatOutside(target.repeats, from.repeats);
    if (rep !== '') {
      this.add(`${who}: ${prop} refers to "${key}", which is inside the repeating group "${rep}".`);
      return true;
    }
    if (from.stage >= 0 && target.stage > from.stage) {
      this.add(`${who}: ${prop} refers to "${key}", which is filled in at a later stage.`);
      return true;
    }
    return false;
  }

  private resolve(from: Item, prop: string, key: string): Item | null {
    const who = owner(from);
    const target = this.byKey.get(key);
    if (target === undefined) {
      this.add(`${who}: ${prop} refers to "${key}", which does not exist.`);
      return null;
    }
    if (!isField(target)) {
      this.add(`${who}: ${prop} refers to "${key}", which is ${kindWords[target.kind]}, not a field.`);
      return null;
    }
    if (this.placeProblem(from, prop, key, target)) return null;
    return target;
  }

  private checkCondition(from: Item, prop: string, c: JsonObject): void {
    const op = c['op'] as string;
    if (op === 'all' || op === 'any') {
      for (const sub of objects(c['conditions'])) this.checkCondition(from, prop, sub);
      return;
    }
    if (op === 'not') {
      this.checkCondition(from, prop, c['condition'] as JsonObject);
      return;
    }
    const key = c['field'] as string;
    const target = this.resolve(from, prop, key);
    if (target === null) return;
    if (prop === 'showWhen') from.deps.push(target);
    if (op === 'answered') return;
    const who = `${owner(from)}: ${prop}`;
    const vt = valueType(target);
    if (!compareOps[op].includes(vt)) {
      this.add(`${who} cannot use "${op}" on "${key}", which is ${kindWords[target.kind]}.`);
      return;
    }
    const value = c['value'];
    const compares = `${who} compares "${key}" with `;
    const isStr = typeof value === 'string';
    switch (vt) {
      case 'number':
        if (typeof value !== 'number') this.add(`${compares}a value that is not a number.`);
        break;
      case 'text':
        if (!isStr) this.add(`${compares}a value that is not text.`);
        break;
      case 'date':
        if (!isStr || !datePattern.test(value)) this.add(`${compares}a value that is not a date such as 2026-03-15.`);
        break;
      case 'time':
        if (!isStr || !timePattern.test(value)) this.add(`${compares}a value that is not a time such as 14:30.`);
        break;
      case 'date-time':
        if (!isStr || !dateTimePattern.test(value)) {
          this.add(`${compares}a value that is not a date and time such as 2026-03-15T14:30+05:30.`);
        }
        break;
      case 'choice':
      case 'choices': {
        if (!isStr) {
          this.add(`${compares}a value that is not a choice key.`);
          break;
        }
        const choices = this.choicesOf(target);
        if (choices !== undefined && !choiceKeys(choices).has(value)) {
          this.add(`${compares}"${value}", which is not one of its choices.`);
        }
        break;
      }
    }
  }

  private checkExpression(from: Item, e: JsonObject): void {
    const who = `${owner(from)}: calculate`;
    const op = e['op'] as string;
    switch (op) {
      case 'add':
      case 'multiply':
      case 'subtract':
      case 'divide':
        for (const arg of objects(e['args'])) this.checkExpression(from, arg);
        break;
      case 'field':
      case 'area':
      case 'length': {
        const key = e['field'] as string;
        const target = this.resolve(from, 'calculate', key);
        if (target === null) return;
        from.deps.push(target);
        if (op === 'field' && valueType(target) !== 'number') {
          this.add(`${who} uses "${key}", which is not a number.`);
        } else if (op === 'area' && target.kind !== 'polygon') {
          this.add(`${who} takes the area of "${key}", which is not a drawn area.`);
        } else if (op === 'length' && target.kind !== 'track') {
          this.add(`${who} takes the length of "${key}", which is not a walked track.`);
        }
        break;
      }
      case 'hours-between':
        for (const end of ['start', 'end']) {
          const key = e[end] as string;
          const target = this.resolve(from, 'calculate', key);
          if (target === null) continue;
          from.deps.push(target);
          if (valueType(target) !== 'date-time') {
            this.add(`${who} uses "${key}" in hours-between, which is not a date and time.`);
          }
        }
        break;
      case 'sum':
      case 'count': {
        const group = this.resolveGroup(from, e['group'] as string);
        if (group === null || op === 'count') return;
        const key = e['field'] as string;
        const target = this.byKey.get(key);
        if (target === undefined) {
          this.add(`${who} refers to "${key}", which does not exist.`);
        } else if (!isField(target)) {
          this.add(`${who} refers to "${key}", which is ${kindWords[target.kind]}, not a field.`);
        } else if (!sameList(target.repeats, [...group.repeats, group.key])) {
          this.add(`${who} sums "${key}", which is not directly inside the repeating group "${group.key}".`);
        } else if (from.stage >= 0 && target.stage > from.stage) {
          this.add(`${who} refers to "${key}", which is filled in at a later stage.`);
        } else {
          from.deps.push(target);
          if (valueType(target) !== 'number') this.add(`${who} sums "${key}", which is not a number.`);
        }
        break;
      }
    }
  }

  private resolveGroup(from: Item, key: string): Item | null {
    const who = `${owner(from)}: calculate`;
    const group = this.byKey.get(key);
    if (group === undefined) {
      this.add(`${who} refers to "${key}", which does not exist.`);
    } else if (group.kind !== 'repeat') {
      this.add(`${who} needs a repeating group, but "${key}" is ${kindWords[group.kind]}.`);
    } else if (from.repeats.includes(key)) {
      this.add(`${who} refers to "${key}", which contains it.`);
    } else if (!this.placeProblem(from, 'calculate', key, group)) {
      return group;
    }
    return null;
  }

  checkColumnNames(): void {
    const taken = new Set(this.byKey.keys());
    for (const col of columnsOf(this.items)) {
      if (col.name === col.field) continue;
      if (taken.has(col.name)) this.add(`Field "${col.field}": the column "${col.name}" it adds clashes with another key.`);
      taken.add(col.name);
    }
  }

  checkCycles(): void {
    const state = new Map<Item, 'open' | 'done'>();
    const stack: Item[] = [];
    const visit = (it: Item) => {
      state.set(it, 'open');
      stack.push(it);
      for (const dep of it.deps) {
        const s = state.get(dep);
        if (s === 'open') {
          const names = stack.slice(stack.indexOf(dep)).map((x) => x.key);
          names.push(dep.key);
          this.add(`${owner(dep)} depends on itself: ${names.join(' → ')}.`);
        } else if (s === undefined) {
          visit(dep);
        }
      }
      stack.pop();
      state.set(it, 'done');
    };
    for (const it of this.items) {
      if (!state.has(it)) visit(it);
    }
  }

  checkStagesUsed(): void {
    const used = new Set<number>();
    for (const it of this.items) {
      if (it.kind === 'section' && it.stage >= 0) used.add(it.stage);
    }
    objects(this.form['stages']).forEach((s, i) => {
      const key = s['key'] as string;
      if (this.stages.get(key) === i && !used.has(i)) this.add(`Stage "${key}" is not used by any section.`);
    });
  }

  checkLayout(): void {
    const layout = this.form['layout'];
    if (!isObject(layout)) return;
    if (has(layout, 'pages')) {
      const pageKeys = new Set<string>();
      const onPage = new Set<string>();
      for (const p of objects(layout['pages'])) {
        const page = p['key'] as string;
        if (pageKeys.has(page)) this.add(`Layout: the page "${page}" is defined more than once.`);
        pageKeys.add(page);
        const stagesSeen = new Set<number>();
        for (const s of stringList(p['sections'])) {
          const sec = this.byKey.get(s);
          if (sec === undefined || sec.kind !== 'section') {
            this.add(`Layout: the page "${page}" lists "${s}", which is not a section.`);
            continue;
          }
          if (onPage.has(s)) {
            this.add(`Layout: the section "${s}" is on more than one page.`);
            continue;
          }
          onPage.add(s);
          stagesSeen.add(sec.stage);
        }
        if (this.hasStages && stagesSeen.size > 1) this.add(`Layout: the page "${page}" mixes sections from different stages.`);
      }
      for (const it of this.items) {
        if (it.kind === 'section' && this.byKey.get(it.key) === it && !onPage.has(it.key)) {
          this.add(`Layout: the section "${it.key}" is not on any page.`);
        }
      }
    }
    const listed = new Set<string>();
    for (const li of objects(layout['items'])) {
      const key = li['item'] as string;
      const it = this.byKey.get(key);
      if (it === undefined) {
        this.add(`Layout: "${key}" is not an item in the form.`);
        continue;
      }
      if (listed.has(key)) {
        this.add(`Layout: "${key}" is listed more than once.`);
        continue;
      }
      listed.add(key);
      const w = str(li, 'widget');
      if (w !== undefined && !widgetKinds[w].includes(it.kind)) {
        this.add(`Layout: the widget "${w}" does not suit "${key}", which is ${kindWords[it.kind]}.`);
      }
      if (has(li, 'collapsed') && isField(it)) {
        this.add(`Layout: "collapsed" only works on sections and groups, not on "${key}".`);
      }
    }
  }

  checkTranslations(): void {
    if (!has(this.form, 'languages')) return;
    const want = stringList(this.form['languages']);
    const check = (path: string, v: Json | undefined) => {
      if (!isObject(v)) return;
      for (const l of want) {
        if (!has(v, l)) this.add(`${path} is missing the "${l}" translation.`);
      }
      for (const l of sortedKeys(v)) {
        if (l !== 'en' && !want.includes(l)) this.add(`${path} has "${l}", which is not one of the form's languages.`);
      }
    };
    const checkNode = (path: string, n: JsonObject) => {
      for (const p of textProperties) check(childPath(path, p), n[p]);
      objects(n['choices']).forEach((c, i) => check(`${path}.choices[${i}].label`, c['label']));
    };
    checkNode('', this.form);
    objects(this.form['stages']).forEach((s, i) => check(`stages[${i}].title`, s['title']));
    objects(this.form['lists']).forEach((l, i) => checkNode(`lists[${i}]`, l));
    for (const it of this.items) checkNode(it.path, it.node);
    const layout = this.form['layout'];
    if (isObject(layout)) {
      objects(layout['pages']).forEach((p, i) => check(`layout.pages[${i}].title`, p['title']));
    }
  }
}
