// Runs the shared examples through the TypeScript validator. The Go tests in
// the folder above run the same files and expect the same messages.
// Run with: node --test packages/form-schema/ts/form-schema.test.ts

import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';

import { formColumns, formSchema, validateForm, validateFormText } from './form-schema.ts';

const examples = join(import.meta.dirname, '..', 'examples');

function jsonFiles(dir: string): string[] {
  return readdirSync(dir).filter((f) => f.endsWith('.json')).sort();
}

const valid = jsonFiles(examples);
const invalid = jsonFiles(join(examples, 'invalid'));

test('there are enough shared examples', () => {
  assert.ok(valid.length >= 6, `found ${valid.length} valid examples`);
  assert.ok(invalid.length >= 30, `found ${invalid.length} invalid examples`);
});

for (const file of valid) {
  test(`valid: ${file}`, () => {
    const text = readFileSync(join(examples, file), 'utf8');
    assert.deepEqual(validateFormText(text), []);
    const { columns, problems } = formColumns(JSON.parse(text));
    assert.deepEqual(problems, []);
    const want = JSON.parse(readFileSync(join(examples, 'columns', file), 'utf8'));
    assert.deepEqual(columns, want);
  });
}

for (const file of invalid) {
  test(`invalid: ${file}`, () => {
    const c = JSON.parse(readFileSync(join(examples, 'invalid', file), 'utf8'));
    assert.ok(typeof c.expected === 'string' && c.expected !== '' && c.form !== undefined, 'needs "expected" and "form"');
    assert.deepEqual(validateForm(c.form), [c.expected]);
  });
}

test('text that is not JSON', () => {
  for (const text of ['', '{', '{"id": "a"} {}', '[1,]']) {
    assert.deepEqual(validateFormText(text), ['The form is not valid JSON.']);
  }
});

test('the exported schema is the published file', () => {
  const published = JSON.parse(readFileSync(join(import.meta.dirname, '..', 'form.schema.json'), 'utf8'));
  assert.deepEqual(formSchema, published);
});
