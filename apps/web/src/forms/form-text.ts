import { language as interfaceLanguage, languageName, matchLanguage, translate } from '../language';
import { workspaceFormat } from '../workspace-format';
import type { FormTree, Node } from '@open-fimra/form-schema/answers';

/** Words people see, in one or more languages (form format, section 4). */
export type Text = Record<string, string>;

export { languageName };

/** A text in the chosen language, or in English when it has no translation. */
export function textIn(text: unknown, language: string): string {
  if (!text || typeof text !== 'object') return '';
  const t = text as Text;
  return t[language] ?? t[language.split('-')[0]] ?? t['en'] ?? '';
}

/** The languages a form offers: its list, or every language its title has. English comes first. */
export function formLanguages(form: Record<string, unknown>): string[] {
  const listed = form['languages'];
  const found = Array.isArray(listed) ? (listed as string[]) : Object.keys((form['title'] as Text) ?? {});
  return ['en', ...found.filter(code => code !== 'en')];
}

/** Use the app preference if this form offers it. */
export function startLanguage(languages: string[]): string {
  return matchLanguage(interfaceLanguage(), languages) ?? 'en';
}

/** The name of an item as people see it: a field's label, or a group's title. */
export function itemName(node: Node | undefined, language: string): string {
  if (!node) return '';
  return textIn(node.node['label'] ?? node.node['title'], language) || node.key;
}

/** The name of one repeating group entry, such as "Team 2". */
export function entryName(node: Node | undefined, index: number, language: string): string {
  if (!node) return translate('Entry {0}', [workspaceFormat.number(index + 1)]);
  const label = textIn(node.node['itemLabel'], language) || itemName(node, language);
  return translate('{0} {1}', [label, workspaceFormat.number(index + 1)]);
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
  const item = field ? tree?.byKey.get(field) : undefined;
  if (interfaceLanguage() !== 'en' && item) {
    const label = '“' + itemName(item, language) + '”';
    const clause = (prefix?.[2] ?? message).replace(/^(?:Field|Repeating group) "[^"]+"\s*/, '');
    const localized = answerProblem(label, clause, textIn(item.node['patternHint'], language));
    const where = path.map(([key, index]) => entryName(tree?.byKey.get(key), index, language)).join(', ');
    return { message: where ? where + ': ' + localized : localized, field, path };
  }
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

function answerProblem(field: string, clause: string, hint: string): string {
  if (clause === 'is required.') return translate('{0} is required.', [field]);
  if (clause === 'cannot be in the future.') return translate('{0} cannot be in the future.', [field]);
  if (clause === 'must be a whole number.') return translate('{0} must be a whole number.', [field]);
  if (clause === 'must be a number.') return translate('{0} must be a number.', [field]);
  if (clause === 'must be text.') return translate('{0} must be text.', [field]);
  if (clause.startsWith('does not match the expected pattern')) {
    return hint ? translate('{0} does not match the expected pattern: {1}', [field, hint])
      : translate('{0} does not match the expected pattern.', [field]);
  }
  const limit = /^(needs at least|can have at most|must be at least|must be at most) ([0-9.]+)(?: (.+))?\.$/.exec(clause);
  if (limit) {
    const quantity = [workspaceFormat.number(Number(limit[2])), limit[3] && translate(limit[3])].filter(Boolean).join(' ');
    const messages: Record<string, string> = {
      'needs at least': '{0} needs at least {1}.',
      'can have at most': '{0} can have at most {1}.',
      'must be at least': '{0} must be at least {1}.',
      'must be at most': '{0} must be at most {1}.'
    };
    return translate(messages[limit[1]], [field, quantity]);
  }
  return translate('The answer for {0} is invalid.', [field]);
}

/** The same entry path as a short string, for element IDs and lookups. */
export function pathId(path: readonly (readonly [string, number])[]): string {
  return path.map(([key, index]) => key + '-' + index).join('-');
}
