// Template locks, as the core API checks them in services/api/form_templates.go.
// Both give the same plain-English messages and run the same shared cases in
// services/api/testdata/form-extension-cases.json, so the designer shows a
// problem before the server would refuse it.

export interface TemplateRules { allowExtension: boolean; locked: string[] }

interface Node { key: string; kind: string; node: Record<string, unknown>; parent: Node | null }

interface Tree { byKey: Map<string, Node>; lists: Map<string, unknown>; stages: Map<string, unknown> }

function readTree(form: unknown): Tree {
  const byKey = new Map<string, Node>();
  const lists = new Map<string, unknown>();
  const stages = new Map<string, unknown>();
  const f = (form ?? {}) as Record<string, unknown>;
  for (const list of (f['lists'] as Record<string, unknown>[] | undefined) ?? []) lists.set(list['key'] as string, list['choices']);
  for (const stage of (f['stages'] as Record<string, unknown>[] | undefined) ?? []) stages.set(stage['key'] as string, stage);
  const walk = (items: unknown, parent: Node) => {
    for (const raw of (items as Record<string, unknown>[] | undefined) ?? []) {
      const n: Node = { key: raw['key'] as string, kind: raw['type'] as string, node: raw, parent };
      if (!byKey.has(n.key)) byKey.set(n.key, n);
      if (n.kind === 'group' || n.kind === 'repeat') walk(raw['items'], n);
    }
  };
  for (const raw of (f['sections'] as Record<string, unknown>[] | undefined) ?? []) {
    const section: Node = { key: raw['key'] as string, kind: 'section', node: raw, parent: null };
    if (!byKey.has(section.key)) byKey.set(section.key, section);
    walk(raw['items'], section);
  }
  return { byKey, lists, stages };
}

/** Deep equality of parsed JSON: object keys in any order, arrays in order. */
export function sameJSON(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b || a === null || b === null || typeof a !== 'object') return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) {
    const other = b as unknown[];
    return a.length === other.length && a.every((value, i) => sameJSON(value, other[i]));
  }
  const x = a as Record<string, unknown>, y = b as Record<string, unknown>;
  const keys = Object.keys(x);
  return keys.length === Object.keys(y).length && keys.every(key => key in y && sameJSON(x[key], y[key]));
}

function describe(n: Node): string {
  switch (n.kind) {
    case 'section': return `Section "${n.key}"`;
    case 'group': return `Group "${n.key}"`;
    case 'repeat': return `Repeating group "${n.key}"`;
    default: return `Field "${n.key}"`;
  }
}

function containerWords(n: Node | null): string {
  if (!n) return 'the top level of the form';
  switch (n.kind) {
    case 'section': return `section "${n.key}"`;
    case 'group': return `group "${n.key}"`;
    default: return `repeating group "${n.key}"`;
  }
}

/** Checks template rules for a form: every lock names a section or item, once. */
export function checkTemplateRules(rules: TemplateRules | null | undefined, form: unknown): string[] {
  if (!rules) return [];
  if (rules.locked.length > 500) return ['A template can lock at most 500 keys.'];
  const tree = readTree(form);
  const seen = new Set<string>();
  const problems: string[] = [];
  for (const key of rules.locked) {
    if (seen.has(key)) problems.push(`The template locks "${key}" more than once.`);
    else if (!tree.byKey.has(key)) problems.push(`The template locks "${key}", which is not a section or item of this form.`);
    seen.add(key);
  }
  return problems;
}

/**
 * Compares a form with the template version it extends. Every locked section
 * or item must be in the form, unchanged and in the same place, and the lists
 * and stages that locked parts use must be unchanged too.
 */
export function checkExtension(parentForm: unknown, rules: TemplateRules, childForm: unknown): string[] {
  const parent = readTree(parentForm);
  const child = readTree(childForm);
  const problems: string[] = [];
  const lists: string[] = [];
  const stages: string[] = [];
  for (const key of rules.locked) {
    const p = parent.byKey.get(key);
    if (!p) continue;
    const c = child.byKey.get(key);
    const who = describe(p);
    if (!c) {
      problems.push(`${who} is locked by the template it extends and cannot be removed.`);
      continue;
    }
    if (c.kind !== p.kind || !sameJSON(c.node, p.node)) {
      problems.push(`${who} is locked by the template it extends and cannot change.`);
    } else if ((c.parent?.key ?? '') !== (p.parent?.key ?? '')) {
      problems.push(`${who} is locked by the template it extends and must stay in ${containerWords(p.parent)}.`);
    }
    let section = p;
    while (section.parent) section = section.parent;
    const stage = section.node['stage'];
    if (typeof stage === 'string' && !stages.includes(stage)) stages.push(stage);
    const walk = (n: Record<string, unknown>) => {
      const list = n['list'];
      if (typeof list === 'string' && !lists.includes(list)) lists.push(list);
      for (const item of (n['items'] as Record<string, unknown>[] | undefined) ?? []) walk(item);
    };
    walk(p.node);
  }
  for (const name of lists) {
    if (!sameJSON(parent.lists.get(name) ?? null, child.lists.get(name) ?? null)) {
      problems.push(`The list "${name}" is used by a locked part of the template and cannot change.`);
    }
  }
  for (const name of stages) {
    if (!sameJSON(parent.stages.get(name) ?? null, child.stages.get(name) ?? null)) {
      problems.push(`The stage "${name}" is used by a locked part of the template and cannot change.`);
    }
  }
  return problems;
}

/** Adds the locks a form inherits from the template it extends. */
export function mergeLocks(own: TemplateRules, inherited: TemplateRules): TemplateRules {
  const locked = [...inherited.locked];
  for (const key of own.locked) if (!locked.includes(key)) locked.push(key);
  return { allowExtension: own.allowExtension, locked };
}
