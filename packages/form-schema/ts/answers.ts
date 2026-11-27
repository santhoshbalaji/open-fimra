// Checks the answers to an Open FIMRA form before they are sent.
//
// The core API checks every submission with the same rules and the same
// messages (services/api/submission_rules.go), following SPEC.md sections 8
// to 13. Both run the shared cases in examples/answers. The browser checks
// first so that people see problems while they type; the server's answer is
// the one that counts.
//
// The form must already pass validateForm. Like form-schema.ts, this module
// has no dependencies and uses only type syntax that Node.js can strip.

type Obj = { [key: string]: unknown };

/** The values an alert-value field copies from the alert. */
export interface AlertFacts {
  id: number | string;
  /** Times in ISO 8601 form, such as 2026-03-15T06:00:00Z. */
  observedAt: string;
  raisedAt: string;
  longitude: number | null;
  latitude: number | null;
  unit?: string | null;
  forestClass?: string | null;
  source?: string | null;
  confidence?: string | null;
  firePower?: number | null;
}

/** What the checks need to know about an uploaded file. */
export interface StoredAttachment {
  contentType: string;
  fileName: string;
  sizeBytes: number;
}

export interface AnswerContext {
  /** The workflow step being filled in. A form with stages needs it. */
  step?: string;
  /** Evaluate visibility using the immutable answers already accepted by the server. */
  historical?: boolean;
  alert?: AlertFacts;
  /** The current time. Defaults to now. */
  now?: Date;
  /**
   * Attachments uploaded for the same alert, by ID. null means uploaded
   * earlier, with details unknown here: only the server checks its type.
   */
  attachments?: Record<string, StoredAttachment | null>;
  /**
   * The area in hectares of a GeoJSON Polygon, or the length in kilometres of
   * a GeoJSON LineString. Defaults to a calculation on a sphere; the server
   * measures on the ellipsoid and keeps its own result.
   */
  measure?: (geometry: Obj) => number | null;
}

/** One level of the answers: the top level, or one repeating group entry. */
export type EntryPath = readonly (readonly [string, number])[];

export interface Node {
  key: string;
  kind: string;
  node: Obj;
  parent: Node | null;
  repeat: Node | null;
  stage: number;
  /** Every item inside a section, group or repeating group, in order. */
  children: Node[];
  /** For a repeating group: its fields and inner repeating groups. */
  repeatChildren: Node[];
}

export class FormTree {
  readonly byKey = new Map<string, Node>();
  readonly top: Node[] = [];
  readonly sections: Node[] = [];
  readonly stageStep = new Map<string, number>();
  readonly stageKeys: string[] = [];
  readonly hasStages: boolean;
  readonly lists = new Map<string, Obj[]>();
  readonly form: Obj;

  constructor(form: Obj) {
    this.form = form;
    const stages = new Map<string, number>();
    const stageList = form['stages'] as Obj[] | undefined;
    this.hasStages = Array.isArray(stageList);
    (stageList ?? []).forEach((s, i) => {
      stages.set(s['key'] as string, i);
      this.stageKeys.push(s['key'] as string);
      this.stageStep.set(s['workflowStep'] as string, i);
    });
    for (const l of (form['lists'] as Obj[] | undefined) ?? []) this.lists.set(l['key'] as string, l['choices'] as Obj[]);
    const walk = (parent: Node, repeat: Node | null, items: Obj[], stage: number): void => {
      for (const m of items) {
        const n: Node = { key: m['key'] as string, kind: m['type'] as string, node: m, parent, repeat, stage, children: [], repeatChildren: [] };
        this.byKey.set(n.key, n);
        parent.children.push(n);
        if (isField(n) || n.kind === 'repeat') (repeat ? repeat.repeatChildren : this.top).push(n);
        if (n.kind === 'group') walk(n, repeat, m['items'] as Obj[], stage);
        if (n.kind === 'repeat') walk(n, n, m['items'] as Obj[], stage);
      }
    };
    for (const m of form['sections'] as Obj[]) {
      const name = m['stage'];
      const stage = typeof name === 'string' ? (stages.get(name) ?? -1) : -1;
      const sec: Node = { key: m['key'] as string, kind: 'section', node: m, parent: null, repeat: null, stage, children: [], repeatChildren: [] };
      this.byKey.set(sec.key, sec);
      this.sections.push(sec);
      walk(sec, null, m['items'] as Obj[], stage);
    }
  }

  /** The stage filled in at a workflow step: -1 for a form without stages, null for a step with no stage. */
  stageAt(step: string | undefined): number | null {
    if (!this.hasStages) return -1;
    return step !== undefined && this.stageStep.has(step) ? (this.stageStep.get(step) as number) : null;
  }

  /** The choices a choice field offers, before any cascading. */
  choicesOf(n: Node): Obj[] {
    const own = n.node['choices'];
    return Array.isArray(own) ? (own as Obj[]) : (this.lists.get(n.node['list'] as string) ?? []);
  }
}

export function isField(n: Node): boolean {
  return n.kind !== 'section' && n.kind !== 'group' && n.kind !== 'repeat';
}

const datePattern = /^[0-9]{4}-[0-9]{2}-[0-9]{2}$/;
const timePattern = /^([01][0-9]|2[0-3]):[0-5][0-9](:[0-5][0-9])?$/;
const dateTimePattern = /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2})(?::([0-9]{2})(\.[0-9]{1,9})?)?(Z|([+-])([0-9]{2}):([0-9]{2}))$/;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const clockSkewMs = 5 * 60 * 1000;

function validDay(y: number, m: number, d: number): boolean {
  const t = new Date(Date.UTC(y, m - 1, d));
  return t.getUTCFullYear() === y && t.getUTCMonth() === m - 1 && t.getUTCDate() === d;
}

/** Milliseconds since 1970 for a date such as 2026-03-15, or null. */
export function parseDate(s: unknown): number | null {
  if (typeof s !== 'string' || !datePattern.test(s)) return null;
  const [y, m, d] = s.split('-').map(Number);
  return validDay(y, m, d) ? Date.UTC(y, m - 1, d) : null;
}

/** Milliseconds since 1970 for a date and time with its offset, or null. */
export function parseDateTime(s: unknown): number | null {
  const g = typeof s === 'string' ? dateTimePattern.exec(s) : null;
  if (!g) return null;
  const [y, mo, d, h, mi] = [g[1], g[2], g[3], g[4], g[5]].map(Number);
  const sec = g[6] ? Number(g[6]) : 0;
  if (!validDay(y, mo, d) || h > 23 || mi > 59 || sec > 59) return null;
  let offset = 0;
  if (g[8] !== 'Z') {
    const oh = Number(g[10]);
    const om = Number(g[11]);
    if (oh > 23 || om > 59) return null;
    offset = (g[9] === '-' ? -1 : 1) * (oh * 60 + om) * 60000;
  }
  const fraction = g[7] ? Number(g[7]) * 1000 : 0;
  return Date.UTC(y, mo - 1, d, h, mi, sec) + fraction - offset;
}

/** Seconds after midnight for a time such as 14:30, or null. */
export function timeSeconds(s: unknown): number | null {
  if (typeof s !== 'string' || !timePattern.test(s)) return null;
  return Number(s.slice(0, 2)) * 3600 + Number(s.slice(3, 5)) * 60 + (s.length === 8 ? Number(s.slice(6, 8)) : 0);
}

/** Go's way of rounding: halves away from zero. */
function round(f: number): number {
  return Math.sign(f) * Math.round(Math.abs(f));
}

function hasDecimals(f: number, decimals: number): boolean {
  const scaled = f * 10 ** decimals;
  return Math.abs(scaled - round(scaled)) <= 1e-9 * Math.max(1, Math.abs(scaled));
}

function num(m: Obj, key: string): number | undefined {
  const v = m[key];
  return typeof v === 'number' ? v : undefined;
}

/** Formats a number the way Go's %v does for the values forms use. */
function fmtNumber(n: number): string {
  return String(n);
}

function quoteValue(v: unknown): string {
  return JSON.stringify(v) ?? 'null';
}

const earthRadius = 6371008.8;

/** The area of a GeoJSON Polygon in hectares, or the length of a LineString in kilometres, on a sphere. */
export function sphereMeasure(geometry: Obj): number | null {
  const rad = (d: number) => (d * Math.PI) / 180;
  const coordinates = geometry['coordinates'] as number[][] | number[][][];
  if (geometry['type'] === 'LineString') {
    let total = 0;
    const line = coordinates as number[][];
    for (let i = 1; i < line.length; i++) {
      const [lon1, lat1] = line[i - 1];
      const [lon2, lat2] = line[i];
      const a = Math.sin(rad(lat2 - lat1) / 2) ** 2 + Math.cos(rad(lat1)) * Math.cos(rad(lat2)) * Math.sin(rad(lon2 - lon1) / 2) ** 2;
      total += 2 * earthRadius * Math.asin(Math.min(1, Math.sqrt(a)));
    }
    return total / 1000;
  }
  if (geometry['type'] === 'Polygon') {
    const ringArea = (ring: number[][]) => {
      let sum = 0;
      for (let i = 0; i < ring.length - 1; i++) {
        const [lon1, lat1] = ring[i];
        const [lon2, lat2] = ring[i + 1];
        sum += rad(lon2 - lon1) * (2 + Math.sin(rad(lat1)) + Math.sin(rad(lat2)));
      }
      return Math.abs((sum * earthRadius * earthRadius) / 2);
    };
    const rings = coordinates as number[][][];
    let area = rings.length ? ringArea(rings[0]) : 0;
    for (const hole of rings.slice(1)) area -= ringArea(hole);
    return area / 10000;
  }
  return null;
}

const attachmentKinds: Record<string, string[]> = {
  photo: ['image/jpeg', 'image/png', 'image/webp'],
  signature: ['image/png', 'image/jpeg', 'image/webp'],
};

interface Scope {
  repeat: Node | null;
  parent: Scope | null;
  label: string;
  answers: Obj;
  clean: Map<string, unknown>;
  entries: Map<string, Scope[]>;
  memo: Map<string, { value: unknown; ok: boolean }>;
  busy: Set<string>;
}

/**
 * Answers worked out against a form: what is shown, what is required, the
 * calculated values and the problems. The renderer reads it after every
 * change; checkAnswers uses it before sending.
 */
export class AnswerSession {
  readonly problems: string[] = [];
  /** The answers as the server keeps them, or null when there are problems. */
  readonly answers: Obj | null;
  /** The attachments each field uses. */
  readonly used = new Map<string, string[]>();
  private readonly stage: number;
  private readonly now: number;
  private readonly root: Scope | null = null;
  readonly tree: FormTree;
  private readonly context: AnswerContext;

  constructor(tree: FormTree, values: unknown, context: AnswerContext = {}) {
    this.tree = tree;
    this.context = context;
    this.now = (context.now ?? new Date()).getTime();
    this.stage = tree.stageAt(context.step) ?? -1;
    if (typeof values !== 'object' || values === null || Array.isArray(values)) {
      this.problems.push('values must be a JSON object of answers by field key.');
      this.answers = null;
      return;
    }
    this.root = this.newScope(null, null, '', values as Obj);
    if (context.historical) {
      this.answers = this.problems.length ? null : values as Obj;
      return;
    }
    if (this.problems.length > 0) {
      this.answers = null;
      return;
    }
    const out = this.output(this.root, tree.top);
    this.answers = this.problems.length > 0 ? null : out;
  }

  /** Whether an item is shown, in the entry given by path. */
  visible(key: string, path: EntryPath = []): boolean {
    const n = this.tree.byKey.get(key);
    const at = this.scopeAt(path);
    if (!n || !at) return false;
    return this.visibleNode(n, find(at, n));
  }

  /** A field's answer as the checks see it, including calculations and alert values. */
  value(key: string, path: EntryPath = []): unknown {
    const at = this.scopeAt(path);
    if (!at) return undefined;
    const [v, ok] = this.valueOf(key, at);
    return ok ? v : undefined;
  }

  /** Whether a shown field must be answered. */
  isRequired(key: string, path: EntryPath = []): boolean {
    const n = this.tree.byKey.get(key);
    const at = this.scopeAt(path);
    return !!n && !!at && isField(n) && n.kind !== 'calculated' && n.kind !== 'alert-value' && this.required(n, at);
  }

  /** The choices a choice field offers now: for a cascading list, those under the parent's answer. */
  choices(key: string, path: EntryPath = []): Obj[] {
    const n = this.tree.byKey.get(key);
    if (!n) return [];
    const all = this.tree.choicesOf(n);
    const parent = n.node['parent'];
    if (typeof parent !== 'string') return all;
    const answer = this.value(parent, path);
    return answer === undefined ? [] : all.filter((c) => c['parent'] === answer);
  }

  private scopeAt(path: EntryPath): Scope | null {
    let at = this.root;
    for (const [key, index] of path) at = at?.entries.get(key)?.[index] ?? null;
    return at;
  }

  private add(sc: Scope | null, msg: string): void {
    this.problems.push(sc && sc.label !== '' ? 'In ' + sc.label + ': ' + msg : msg);
  }

  private newScope(repeat: Node | null, parent: Scope | null, label: string, answers: Obj): Scope {
    const sc: Scope = { repeat, parent, label, answers, clean: new Map(), entries: new Map(), memo: new Map(), busy: new Set() };
    const members = repeat ? repeat.repeatChildren : this.tree.top;
    for (const key of Object.keys(answers).sort()) {
      const value = answers[key];
      const n = this.tree.byKey.get(key);
      if (!n || (!isField(n) && n.kind !== 'repeat')) {
        this.add(sc, `values has "${key}", which is not a field of this form.`);
      } else if (!members.includes(n)) {
        if (n.repeat) this.add(sc, `Field "${key}" belongs inside the repeating group "${n.repeat.key}".`);
        else this.add(sc, `Field "${key}" is not inside a repeating group.`);
      } else if (value === null || value === undefined) {
        // A null answer is the same as no answer.
      } else if (n.kind === 'repeat') {
        if (!Array.isArray(value)) {
          this.add(sc, `Repeating group "${key}" must be a list of entries.`);
          continue;
        }
        value.forEach((entry, i) => {
          let where = `entry ${i + 1} of "${key}"`;
          if (label !== '') where = label + ', ' + where;
          if (typeof entry !== 'object' || entry === null || Array.isArray(entry)) {
            this.add(sc, `Entry ${i + 1} of "${key}" must be an object of answers.`);
            return;
          }
          const list = sc.entries.get(key) ?? [];
          list.push(this.newScope(n, sc, where, entry as Obj));
          sc.entries.set(key, list);
        });
      } else if (this.context.historical) {
        sc.clean.set(key, value);
      } else if (n.kind !== 'calculated' && n.kind !== 'alert-value') {
        const [clean, ok] = this.checkValue(sc, n, value);
        if (ok) sc.clean.set(key, clean);
      }
    }
    return sc;
  }

  private checkValue(sc: Scope, n: Node, value: unknown): [unknown, boolean] {
    const who = `Field "${n.key}"`;
    const m = n.node;
    switch (n.kind) {
      case 'text': {
        if (typeof value !== 'string') {
          this.add(sc, `${who} must be text.`);
          return [null, false];
        }
        if (value === '') return [null, false];
        const length = [...value].length;
        const lo = num(m, 'minLength');
        const hi = num(m, 'maxLength');
        if (lo !== undefined && length < lo) this.add(sc, `${who} needs at least ${fmtNumber(lo)} characters.`);
        if (hi !== undefined && length > hi) this.add(sc, `${who} can have at most ${fmtNumber(hi)} characters.`);
        const p = m['pattern'];
        if (typeof p === 'string' && !new RegExp(`^(?:${p})$`, 'u').test(value)) {
          const hint = m['patternHint'] as Obj | undefined;
          if (hint) this.add(sc, `${who} does not match the expected pattern: ${String(hint['en']).replace(/\.$/, '')}.`);
          else this.add(sc, `${who} does not match the expected pattern.`);
        }
        return [value, true];
      }
      case 'number': {
        if (typeof value !== 'number' || !Number.isFinite(value)) {
          this.add(sc, `${who} must be a number.`);
          return [null, false];
        }
        const lo = num(m, 'min');
        const hi = num(m, 'max');
        const d = num(m, 'decimals');
        if (lo !== undefined && value < lo) this.add(sc, `${who} must be at least ${fmtNumber(lo)}.`);
        if (hi !== undefined && value > hi) this.add(sc, `${who} must be at most ${fmtNumber(hi)}.`);
        if (d !== undefined && !hasDecimals(value, d)) {
          if (d === 0) this.add(sc, `${who} must be a whole number.`);
          else this.add(sc, `${who} can have at most ${fmtNumber(d)} decimal places.`);
        }
        return [value, true];
      }
      case 'date': {
        const day = parseDate(value);
        if (day === null) {
          this.add(sc, `${who} must be a date such as 2026-03-15.`);
          return [null, false];
        }
        // The latest date anywhere on Earth is 14 hours ahead of universal time.
        if (m['notInFuture'] === true && day > this.now + 14 * 3600 * 1000) this.add(sc, `${who} cannot be in the future.`);
        return [value, true];
      }
      case 'time':
        if (timeSeconds(value) === null) {
          this.add(sc, `${who} must be a time such as 14:30.`);
          return [null, false];
        }
        return [value, true];
      case 'date-time': {
        const t = parseDateTime(value);
        if (t === null) {
          this.add(sc, `${who} must be a date and time with its offset from universal time, such as 2026-03-15T14:30:00+05:30.`);
          return [null, false];
        }
        if (m['notInFuture'] === true && t > this.now + clockSkewMs) this.add(sc, `${who} cannot be in the future.`);
        return [value, true];
      }
      case 'single-choice':
        if (typeof value !== 'string' || !this.choice(n, value)) {
          this.add(sc, `${who} has no choice ${quoteValue(value)}.`);
          return [null, false];
        }
        return [value, true];
      case 'multiple-choice': {
        if (!Array.isArray(value)) {
          this.add(sc, `${who} must be a list of choices.`);
          return [null, false];
        }
        const keys: string[] = [];
        for (const v of value) {
          if (typeof v !== 'string' || !this.choice(n, v)) {
            this.add(sc, `${who} has no choice ${quoteValue(v)}.`);
            return [null, false];
          }
          if (keys.includes(v)) {
            this.add(sc, `${who} lists "${v}" more than once.`);
            return [null, false];
          }
          keys.push(v);
        }
        if (keys.length === 0) return [null, false];
        const lo = num(m, 'minSelected');
        const hi = num(m, 'maxSelected');
        if (lo !== undefined && keys.length < lo) this.add(sc, `${who} needs at least ${fmtNumber(lo)} choices.`);
        if (hi !== undefined && keys.length > hi) this.add(sc, `${who} can have at most ${fmtNumber(hi)} choices.`);
        return [keys, true];
      }
      case 'point':
      case 'track':
      case 'polygon': {
        const problem = checkGeoJSON(n.kind, value);
        if (problem !== '') {
          this.add(sc, `${who} ${problem}`);
          return [null, false];
        }
        return [value, true];
      }
      case 'photo':
      case 'file': {
        if (!Array.isArray(value)) {
          this.add(sc, `${who} must be a list of attachment IDs.`);
          return [null, false];
        }
        const ids: string[] = [];
        for (const v of value) {
          const id = typeof v === 'string' ? v : '';
          if (!this.checkAttachment(sc, n, id)) return [null, false];
          if (ids.includes(id)) {
            this.add(sc, `${who} lists the attachment "${id}" more than once.`);
            return [null, false];
          }
          ids.push(id);
        }
        if (ids.length === 0) return [null, false];
        const hi = num(m, 'maxCount') ?? 20;
        const lo = num(m, 'minCount');
        if (lo !== undefined && ids.length < lo) this.add(sc, `${who} needs at least ${fmtNumber(lo)} files.`);
        if (ids.length > hi) this.add(sc, `${who} can have at most ${fmtNumber(hi)} files.`);
        return [ids, true];
      }
      case 'signature': {
        const id = typeof value === 'string' ? value : '';
        return this.checkAttachment(sc, n, id) ? [id, true] : [null, false];
      }
    }
    return [null, false];
  }

  private choice(n: Node, key: string): Obj | undefined {
    return this.tree.choicesOf(n).find((c) => c['key'] === key);
  }

  private checkAttachment(sc: Scope, n: Node, id: string): boolean {
    const who = `Field "${n.key}"`;
    if (!uuidPattern.test(id)) {
      this.add(sc, `${who} must give attachment IDs, as returned by the upload.`);
      return false;
    }
    const known = this.context.attachments ?? {};
    if (!(id in known)) {
      this.add(sc, `${who} uses the attachment "${id}", which was not uploaded for this alert.`);
      return false;
    }
    const a = known[id];
    if (!a) return true;
    const kinds = attachmentKinds[n.kind];
    if (kinds && !kinds.includes(a.contentType)) {
      this.add(sc, `${who} needs an image, but the attachment "${id}" is ${a.contentType}.`);
      return false;
    }
    const accept = n.node['accept'];
    if (Array.isArray(accept)) {
      const name = a.fileName.toLowerCase();
      if (!accept.some((ending) => name.endsWith(String(ending).toLowerCase()))) {
        this.add(sc, `${who} does not accept the file "${a.fileName}".`);
        return false;
      }
    }
    const mb = num(n.node, 'maxSizeMegabytes');
    if (mb !== undefined && a.sizeBytes > mb * 1024 * 1024) {
      this.add(sc, `${who} accepts files up to ${fmtNumber(mb)} MB, but "${a.fileName}" is larger.`);
      return false;
    }
    return true;
  }

  private visibleNode(n: Node | null | undefined, holder: Scope | null): boolean {
    if (!n || !holder) return !n;
    if (this.tree.hasStages && n.stage > this.stage) return false;
    const p = n.parent;
    if (p) {
      // A repeating group is shown or hidden in the scope around its entries.
      const around = p.kind === 'repeat' && p === n.repeat ? holder.parent : holder;
      if (!this.visibleNode(p, around)) return false;
    }
    const cond = n.node['showWhen'];
    return cond ? this.condition(cond as Obj, holder) : true;
  }

  private valueOf(key: string, sc: Scope): [unknown, boolean] {
    const n = this.tree.byKey.get(key);
    if (!n) return [undefined, false];
    const holder = find(sc, n);
    if (!holder) return [undefined, false];
    const memo = holder.memo.get(key);
    if (memo) return [memo.value, memo.ok];
    if (holder.busy.has(key)) return [undefined, false];
    holder.busy.add(key);
    let result: { value: unknown; ok: boolean } = { value: undefined, ok: false };
    if (this.visibleNode(n, holder)) {
      if (this.context.historical) {
        if (holder.clean.has(key)) result = { value: holder.clean.get(key), ok: true };
      } else if (n.kind === 'calculated') {
        const [f, ok] = this.calculate(n.node['calculate'] as Obj, holder);
        if (ok && Number.isFinite(f)) {
          const d = num(n.node, 'decimals');
          result = { value: d === undefined ? f : round(f * 10 ** d) / 10 ** d, ok: true };
        }
      } else if (n.kind === 'alert-value') {
        result = this.alertValue(n.node['source'] as string);
      } else if (holder.clean.has(key)) {
        result = { value: holder.clean.get(key), ok: true };
      }
    }
    holder.busy.delete(key);
    holder.memo.set(key, result);
    return [result.value, result.ok];
  }

  private alertValue(source: string): { value: unknown; ok: boolean } {
    const a = this.context.alert;
    if (!a) return { value: undefined, ok: false };
    const text = (v: string | null | undefined) => ({ value: v ?? undefined, ok: v !== null && v !== undefined && v !== '' });
    const utc = (s: string) => new Date(s).toISOString().replace(/\.\d{3}Z$/, 'Z');
    switch (source) {
      case 'alert-id':
        return { value: String(a.id), ok: true };
      case 'observed-at':
        return { value: utc(a.observedAt), ok: true };
      case 'raised-at':
        return { value: utc(a.raisedAt), ok: true };
      case 'location':
        return a.longitude === null || a.latitude === null
          ? { value: undefined, ok: false }
          : { value: { type: 'Point', coordinates: [a.longitude, a.latitude] }, ok: true };
      case 'unit':
        return text(a.unit);
      case 'forest-class':
        return text(a.forestClass);
      case 'source':
        return text(a.source);
      case 'confidence':
        return a.confidence === null || a.confidence === undefined ? { value: undefined, ok: false } : { value: a.confidence, ok: true };
      case 'fire-power':
        return typeof a.firePower === 'number' ? { value: a.firePower, ok: true } : { value: undefined, ok: false };
    }
    return { value: undefined, ok: false };
  }

  private condition(cond: Obj, sc: Scope): boolean {
    const op = cond['op'] as string;
    switch (op) {
      case 'all':
        return (cond['conditions'] as Obj[]).every((c) => this.condition(c, sc));
      case 'any':
        return (cond['conditions'] as Obj[]).some((c) => this.condition(c, sc));
      case 'not':
        return !this.condition(cond['condition'] as Obj, sc);
    }
    const key = cond['field'] as string;
    const [answer, answered] = this.valueOf(key, sc);
    if (op === 'answered') return answered;
    if (!answered) return false;
    const want = cond['value'];
    const n = this.tree.byKey.get(key) as Node;
    if (op === 'includes') return Array.isArray(answer) && answer.includes(String(want));
    const order = compareAnswer(n, answer, want);
    if (op === 'equals' || op === 'not-equals') {
      const same = order !== null ? order === 0 : String(answer) === String(want);
      return same === (op === 'equals');
    }
    if (order === null) return false;
    switch (op) {
      case 'more-than':
        return order > 0;
      case 'at-least':
        return order >= 0;
      case 'less-than':
        return order < 0;
      case 'at-most':
        return order <= 0;
    }
    return false;
  }

  private calculate(e: Obj, sc: Scope): [number, boolean] {
    const args = (): number[] | null => {
      const out: number[] = [];
      for (const raw of e['args'] as Obj[]) {
        const [v, ok] = this.calculate(raw, sc);
        if (!ok) return null;
        out.push(v);
      }
      return out;
    };
    switch (e['op']) {
      case 'number':
        return [e['value'] as number, true];
      case 'field': {
        const [v, ok] = this.valueOf(e['field'] as string, sc);
        return typeof v === 'number' && ok ? [v, true] : [0, false];
      }
      case 'add':
      case 'multiply': {
        const values = args();
        if (!values) return [0, false];
        return [values.slice(1).reduce((t, v) => (e['op'] === 'add' ? t + v : t * v), values[0]), true];
      }
      case 'subtract':
      case 'divide': {
        const values = args();
        if (!values) return [0, false];
        if (e['op'] === 'subtract') return [values[0] - values[1], true];
        return values[1] === 0 ? [0, false] : [values[0] / values[1], true];
      }
      case 'sum':
      case 'count': {
        const group = this.tree.byKey.get(e['group'] as string) as Node;
        const holder = find(sc, { repeat: group.repeat } as Node);
        if (!holder || !this.visibleNode(group, holder)) return [0, false];
        const entries = holder.entries.get(group.key) ?? [];
        if (e['op'] === 'count') return [entries.length, true];
        let total = 0;
        for (const entry of entries) {
          const [v, ok] = this.valueOf(e['field'] as string, entry);
          if (ok) total += v as number;
        }
        return [total, true];
      }
      case 'area':
      case 'length': {
        const [v, ok] = this.valueOf(e['field'] as string, sc);
        if (!ok) return [0, false];
        const size = (this.context.measure ?? sphereMeasure)(v as Obj);
        return size === null ? [0, false] : [size, true];
      }
      case 'hours-between': {
        const [start, okStart] = this.valueOf(e['start'] as string, sc);
        const [end, okEnd] = this.valueOf(e['end'] as string, sc);
        const a = parseDateTime(start);
        const b = parseDateTime(end);
        if (!okStart || !okEnd || a === null || b === null) return [0, false];
        return [(b - a) / 3600000, true];
      }
    }
    return [0, false];
  }

  private required(n: Node, sc: Scope): boolean {
    if (n.node['required'] === true) return true;
    const cond = n.node['requireWhen'];
    return cond ? this.condition(cond as Obj, find(sc, n) ?? sc) : false;
  }

  private output(sc: Scope, members: Node[]): Obj {
    const out: Obj = {};
    for (const n of members) {
      if (n.kind === 'repeat') {
        const entries = sc.entries.get(n.key) ?? [];
        if (!this.visibleNode(n, sc)) continue;
        const lo = num(n.node, 'minRepeats');
        const hi = num(n.node, 'maxRepeats');
        if (lo !== undefined && entries.length < lo) this.add(sc, `Repeating group "${n.key}" needs at least ${fmtNumber(lo)} entries.`);
        if (hi !== undefined && entries.length > hi) this.add(sc, `Repeating group "${n.key}" can have at most ${fmtNumber(hi)} entries.`);
        if (entries.length === 0) continue;
        out[n.key] = entries.map((entry) => this.output(entry, n.repeatChildren));
        continue;
      }
      const [v, ok] = this.valueOf(n.key, sc);
      if (!ok) {
        if (this.visibleNode(n, sc) && n.kind !== 'calculated' && n.kind !== 'alert-value' && this.required(n, sc)) {
          this.add(sc, `Field "${n.key}" is required.`);
        }
        continue;
      }
      const parent = n.node['parent'];
      if (typeof parent === 'string') this.checkCascade(sc, n, parent, v);
      if (n.kind === 'photo' || n.kind === 'file') this.used.set(n.key, [...(this.used.get(n.key) ?? []), ...(v as string[])]);
      if (n.kind === 'signature') this.used.set(n.key, [...(this.used.get(n.key) ?? []), v as string]);
      out[n.key] = v;
    }
    return out;
  }

  private checkCascade(sc: Scope, n: Node, parentKey: string, v: unknown): void {
    const [parentAnswer, answered] = this.valueOf(parentKey, sc);
    const keys = typeof v === 'string' ? [v] : (v as string[]);
    for (const key of keys) {
      if (!answered || this.choice(n, key)?.['parent'] !== parentAnswer) {
        this.add(sc, `Field "${n.key}": the choice "${key}" does not go with the answer to "${parentKey}".`);
        return;
      }
    }
  }
}

/** The scope that holds a node's answer, seen from sc. */
function find(sc: Scope | null, n: Node): Scope | null {
  for (let s = sc; s; s = s.parent) if (s.repeat === n.repeat) return s;
  return null;
}

function compareAnswer(n: Node, answer: unknown, want: unknown): number | null {
  let kind = n.kind;
  if (kind === 'alert-value') {
    kind = ({ 'observed-at': 'date-time', 'raised-at': 'date-time', 'fire-power': 'number' } as Record<string, string>)[n.node['source'] as string] ?? '';
  }
  const cmp = (a: number, b: number) => (a < b ? -1 : a > b ? 1 : 0);
  switch (kind) {
    case 'number':
    case 'calculated':
      return typeof answer === 'number' && typeof want === 'number' ? cmp(answer, want) : null;
    case 'date': {
      const a = String(answer);
      const b = String(want);
      return a < b ? -1 : a > b ? 1 : 0;
    }
    case 'time': {
      const a = timeSeconds(String(answer));
      const b = timeSeconds(String(want));
      return a === null || b === null ? null : cmp(a, b);
    }
    case 'date-time': {
      const a = parseDateTime(String(answer));
      const b = parseDateTime(String(want));
      return a === null || b === null ? null : cmp(a, b);
    }
  }
  return null;
}

function checkGeoJSON(kind: string, value: unknown): string {
  const want = ({ point: 'Point', track: 'LineString', polygon: 'Polygon' } as Record<string, string>)[kind];
  const problem = `must be a GeoJSON ${want} in longitude and latitude.`;
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return problem;
  const m = value as Obj;
  if (m['type'] !== want || Object.keys(m).length !== 2) return problem;
  const position = (v: unknown): number[] | null => {
    if (!Array.isArray(v) || v.length < 2 || v.length > 3 || !v.every((x) => typeof x === 'number' && Number.isFinite(x))) return null;
    return v[0] >= -180 && v[0] <= 180 && v[1] >= -90 && v[1] <= 90 ? v : null;
  };
  const line = (v: unknown, least: number, closed: boolean): boolean => {
    if (!Array.isArray(v) || v.length < least) return false;
    const points = v.map(position);
    if (points.some((p) => p === null)) return false;
    const first = points[0] as number[];
    const last = points[points.length - 1] as number[];
    return !closed || (first[0] === last[0] && first[1] === last[1]);
  };
  const coordinates = m['coordinates'];
  if (kind === 'point') return position(coordinates) ? '' : problem;
  if (kind === 'track') return line(coordinates, 2, false) ? '' : problem;
  const rings = coordinates;
  const ok = Array.isArray(rings) && rings.length > 0 && rings.every((ring) => line(ring, 4, true));
  return ok ? '' : 'must be a GeoJSON Polygon with closed rings of at least four positions.';
}

/** Reads a form once, for answering it many times. The form must pass validateForm. */
export function readForm(form: unknown): FormTree {
  return new FormTree(form as Obj);
}

/**
 * Checks answers to a form, with the server's rules and messages. Returns the
 * answers as the server would keep them, or the problems.
 */
export function checkAnswers(form: unknown, values: unknown, context: AnswerContext = {}): { answers: Obj | null; problems: string[] } {
  const tree = form instanceof FormTree ? form : readForm(form);
  if (tree.stageAt(context.step) === null) {
    return { answers: null, problems: [`The form has no stage at the step ${context.step ?? '(none)'}.`] };
  }
  const session = new AnswerSession(tree, values, context);
  return { answers: session.answers, problems: session.problems };
}
