// Runs the shared answer cases in examples/answers through the TypeScript
// checker. The core API's Go tests run the same files and expect the same
// messages in the same order (services/api/submission_rules_test.go).
// Run with: node --test packages/form-schema/ts/answers.test.ts

import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';

import { AnswerSession, checkAnswers, parseDateTime, readForm, sphereMeasure, type AnswerContext } from './answers.ts';

const examples = join(import.meta.dirname, '..', 'examples');

// The context the Go tests use: a fixed time, alert and attachments, and a
// fixed measure, because the server measures on the ellipsoid.
const context: AnswerContext = {
  now: new Date('2026-03-16T12:00:00Z'),
  alert: {
    id: 42, observedAt: '2026-03-15T06:00:00Z', raisedAt: '2026-03-15T06:20:00Z', longitude: 86.3, latitude: 16.4,
    unit: 'Beat 05', forestClass: 'inside', source: 'demo', confidence: 'nominal', firePower: 12.5,
  },
  attachments: {
    '00000000-0000-4000-8000-000000000001': { contentType: 'image/jpeg', fileName: 'site.jpg', sizeBytes: 1000 },
    '00000000-0000-4000-8000-000000000002': { contentType: 'image/png', fileName: 'signature.png', sizeBytes: 500 },
    '00000000-0000-4000-8000-000000000003': { contentType: 'application/pdf', fileName: 'map.pdf', sizeBytes: 3 << 20 },
  },
  measure: (g) => (g['type'] === 'Polygon' ? 1.23456 : 0.5),
};

interface Case { name: string; step?: string; values: unknown; expected: string[]; answers?: Record<string, unknown>; absent?: string[] }

const files = readdirSync(join(examples, 'answers')).filter((f) => f.endsWith('.json')).sort();

test('there are shared answer cases', () => {
  assert.ok(files.length >= 3);
});

for (const file of files) {
  const doc = JSON.parse(readFileSync(join(examples, 'answers', file), 'utf8')) as { form: string; cases: Case[] };
  const form = JSON.parse(readFileSync(join(examples, doc.form + '.json'), 'utf8'));
  for (const c of doc.cases) {
    test(`${doc.form}: ${c.name}`, () => {
      const { answers, problems } = checkAnswers(form, c.values, { ...context, step: c.step });
      assert.deepEqual(problems, c.expected);
      for (const [key, want] of Object.entries(c.answers ?? {})) assert.deepEqual(answers?.[key], want, key);
      for (const key of c.absent ?? []) assert.ok(answers && !(key in answers), `${key} was kept`);
    });
  }
}

test('a session says what is shown and what is offered', () => {
  const form = JSON.parse(readFileSync(join(examples, 'first-report.json'), 'utf8'));
  const tree = readForm(form);
  const session = new AnswerSession(tree, { cause_group: 'natural', cause: 'natural-other', teams: [{ members: 3 }] }, context);
  assert.equal(session.visible('cause_notes'), true);
  assert.equal(session.isRequired('cause_notes'), true);
  assert.ok(session.choices('cause').every((c) => c['parent'] === 'natural'));
  assert.equal(session.value('members', [['teams', 0]]), 3);
  assert.equal(session.value('team_count'), 1);
  assert.deepEqual(new AnswerSession(tree, {}, context).choices('cause'), []);
  assert.equal(new AnswerSession(tree, { cause: 'lightning' }, context).visible('cause_notes'), false);
});

test('a staged form needs a step it has a stage for', () => {
  const form = JSON.parse(readFileSync(join(examples, 'control-room-dispatch.json'), 'utf8'));
  assert.deepEqual(checkAnswers(form, {}, { ...context, step: 'first_report' }).problems, ['The form has no stage at the step first_report.']);
});

test('dates, times and shapes', () => {
  assert.equal(parseDateTime('2026-03-15T14:30+05:30'), Date.UTC(2026, 2, 15, 9, 0));
  assert.equal(parseDateTime('2026-02-30T14:30Z'), null);
  assert.equal(parseDateTime('2026-03-15T24:30Z'), null);
  const area = sphereMeasure({ type: 'Polygon', coordinates: [[[86.3, 16.4], [86.31, 16.4], [86.31, 16.41], [86.3, 16.4]]] }) as number;
  assert.ok(area > 58 && area < 60, String(area));
  const length = sphereMeasure({ type: 'LineString', coordinates: [[86.3, 16.4], [86.31, 16.4]] }) as number;
  assert.ok(length > 1.06 && length < 1.07, String(length));
});
