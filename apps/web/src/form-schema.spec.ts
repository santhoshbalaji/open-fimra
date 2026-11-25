import { formColumns, validateForm } from '@open-fimra/form-schema';

import firstReport from '../../../packages/form-schema/examples/first-report.json';

// The shared form validator, built the way the web app builds it. The full
// set of shared examples runs in `make form-schema-test`.
describe('form schema', () => {
  it('accepts a starter-pack draft', () => {
    expect(validateForm(firstReport)).toEqual([]);
    expect(formColumns(firstReport).columns?.find((c) => c.name === 'members')).toEqual({
      name: 'members',
      field: 'members',
      valueType: 'number',
      unit: 'people',
      repeat: 'teams',
    });
  });

  it('explains what is wrong in plain English', () => {
    const broken = structuredClone(firstReport) as { sections: { items: Record<string, unknown>[] }[] };
    broken.sections[1].items[2].list = 'cause_kinds';
    expect(validateForm(broken)).toEqual(['Field "cause": uses the list "cause_kinds", which does not exist.']);
  });
});
