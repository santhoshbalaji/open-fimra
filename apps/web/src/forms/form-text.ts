import type { FormTree, Node } from '@open-fimra/form-schema/answers';

/** Words people see, in one or more languages (form format, section 4). */
export type Text = Record<string, string>;

const languageNames: Record<string, string> = { en: 'English', hi: 'हिन्दी (Hindi)', ur: 'اردو (Urdu)', bn: 'বাংলা (Bengali)' };

/** A text in the chosen language, or in English when it has no translation. */
export function textIn(text: unknown, language: string): string {
  if (!text || typeof text !== 'object') return '';
  const t = text as Text;
  return t[language] ?? t['en'] ?? '';
}

/** The languages a form offers: its list, or every language its title has. English comes first. */
export function formLanguages(form: Record<string, unknown>): string[] {
  const listed = form['languages'];
  const found = Array.isArray(listed) ? (listed as string[]) : Object.keys((form['title'] as Text) ?? {});
  return ['en', ...found.filter(code => code !== 'en')];
}

export function languageName(code: string): string {
  return languageNames[code] ?? code;
}

/** The language to start with: the browser's, when the form has it. */
export function startLanguage(languages: string[]): string {
  const wanted = (typeof navigator !== 'undefined' ? navigator.languages ?? [] : []).map(l => l.toLowerCase());
  for (const code of wanted) {
    const match = languages.find(l => l.toLowerCase() === code || l.toLowerCase() === code.split('-')[0]);
    if (match) return match;
  }
  return 'en';
}

/** The name of an item as people see it: a field's label, or a group's title. */
export function itemName(node: Node | undefined, language: string): string {
  if (!node) return '';
  return textIn(node.node['label'] ?? node.node['title'], language) || node.key;
}

/** The name of one repeating group entry, such as "Team 2". */
export function entryName(node: Node | undefined, index: number, language: string): string {
  if (!node) return 'Entry ' + (index + 1);
  const label = textIn(node.node['itemLabel'], language) || itemName(node, language);
  return label + ' ' + (index + 1);
}

export interface Problem {
  /** The message, with keys replaced by the names people see. */
  message: string;
  /** The field the problem is about, if any. */
  field?: string;
  /** The repeating group entries the field is in, as in EntryPath. */
  path: [string, number][];
}

const entryPattern = /entry (\d+) of "([a-z][a-z0-9_]*)"/g;

/**
 * Turns a checker or server message into words people know: field keys become
 * their labels and repeating group entries become names such as "Team 2".
 * The field and entry are kept so the form can show the message beside it.
 */
export function explain(message: string, tree: FormTree | null, language: string): Problem {
  const path: [string, number][] = [];
  let text = message;
  const prefix = /^In (.+?): (.*)$/.exec(message);
  if (prefix) {
    for (const match of prefix[1].matchAll(entryPattern)) path.push([match[2], Number(match[1]) - 1]);
    const where = path.map(([key, index]) => entryName(tree?.byKey.get(key), index, language)).join(', ');
    text = where + ': ' + prefix[2];
  }
  const field = /(?:Field|Repeating group) "([a-z][a-z0-9_]*)"/.exec(text)?.[1];
  if (tree) {
    text = text.replace(/(Field|Repeating group|Entry (\d+) of) "([a-z][a-z0-9_]*)"/g, (all, kind: string, n: string | undefined, key: string) => {
      const node = tree.byKey.get(key);
      if (!node) return all;
      if (n) return entryName(node, Number(n) - 1, language);
      return '“' + itemName(node, language) + '”';
    });
    text = text.replace(/the answer to "([a-z][a-z0-9_]*)"/g, (all, key: string) =>
      tree.byKey.has(key) ? 'the answer to “' + itemName(tree.byKey.get(key), language) + '”' : all);
    text = text.replace(/^values has "([^"]+)", which is not a field of this form\.$/, 'The form has no question “$1”.');
  }
  return { message: text.charAt(0).toUpperCase() + text.slice(1), field, path };
}

/** The same entry path as a short string, for element IDs and lookups. */
export function pathId(path: readonly (readonly [string, number])[]): string {
  return path.map(([key, index]) => key + '-' + index).join('-');
}
