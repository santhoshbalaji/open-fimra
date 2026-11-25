import { describe, expect, it } from 'vitest';
import { validateForm } from '@open-fimra/form-schema';
import cases from '../../../../services/api/testdata/form-extension-cases.json';
import { checkExtension, checkTemplateRules, mergeLocks, sameJSON } from './form-locks';

// The same cases run in services/api/form_templates_test.go, so the designer
// and the server give the same messages.
describe('template locks', () => {
  it('starts from a valid shared parent form', () => {
    expect(validateForm(cases.parent)).toEqual([]);
  });

  for (const c of cases.extension) {
    it('extension: ' + c.name, () => {
      expect(validateForm(c.child)).toEqual([]);
      expect(checkExtension(cases.parent, { allowExtension: true, locked: c.locked }, c.child)).toEqual(c.expected);
    });
  }

  for (const c of cases.rules) {
    it('rules: ' + c.name, () => {
      expect(checkTemplateRules(c.rules, cases.parent)).toEqual(c.expected);
    });
  }

  it('keeps inherited locks first and adds its own', () => {
    expect(mergeLocks({ allowExtension: true, locked: ['extra', 'basics'] }, { allowExtension: false, locked: ['basics', 'crew_size'] }))
      .toEqual({ allowExtension: true, locked: ['basics', 'crew_size', 'extra'] });
  });

  it('compares JSON values, not the order of their properties', () => {
    expect(sameJSON({ a: 1, b: [1, { c: 2 }] }, { b: [1, { c: 2 }], a: 1 })).toBe(true);
    expect(sameJSON({ a: [1, 2] }, { a: [2, 1] })).toBe(false);
    expect(sameJSON({ a: 1 }, { a: 1, b: undefined })).toBe(false);
  });
});
