import { describe, expect, it } from 'vitest';
import { validateForm } from '@open-fimra/form-schema';
import firstReport from '../../../../packages/form-schema/examples/first-report.json';
import {
  FormDefinition, allKeys, blankForm, containers, describeCondition, findPath, freshKey, insertItem, moveItem,
  newItem, newSection, insertSection, nodeAt, removeNode, renameKey, shift, testsFor, fieldTypes, updateNode
} from './form-model';

const sample = () => structuredClone(firstReport) as unknown as FormDefinition;
const keysIn = (form: FormDefinition, path: number[]) => (nodeAt(form, path)?.items ?? []).map(item => item.key);

describe('form model', () => {
  it('makes new items that pass the validator, for every type', () => {
    let form = blankForm('demo-report', 'Demo report');
    for (const type of fieldTypes) form = insertItem(form, [0], 99, newItem(form, type.type));
    form = insertSection(form, 1, newSection(form));
    expect(validateForm(form)).toEqual([]);
    expect(new Set(allKeys(form)).size).toBe(allKeys(form).length);
  });

  it('finds fresh keys', () => {
    const form = blankForm('demo-report', 'Demo report');
    expect(freshKey(form, 'notes')).toBe('notes_1');
    expect(freshKey(form, '9 Odd name')).toBe('odd_name_1');
  });

  it('moves items within a list, between lists and never into themselves', () => {
    let form = sample();
    expect(keysIn(form, [0])).toEqual(['observed_at', 'alert_location', 'reached_at', 'hours_to_reach']);
    form = shift(form, [0, 0], 1);
    expect(keysIn(form, [0])).toEqual(['alert_location', 'observed_at', 'reached_at', 'hours_to_reach']);
    form = shift(form, [0, 0], -1);
    expect(keysIn(form, [0])[0]).toBe('alert_location');
    // From the summary into the teams repeating group, as its second item.
    const teams = findPath(form, 'teams')!;
    form = moveItem(form, [0, 3], teams, 1);
    expect(keysIn(form, findPath(form, 'teams')!)).toEqual(['team_name', 'hours_to_reach', 'members', 'team_arrived']);
    // Moving a group into itself changes nothing.
    const again = moveItem(form, findPath(form, 'teams')!, findPath(form, 'teams')!, 0);
    expect(again).toBe(form);
    // Section paths after the moved item shift with it.
    const before = sample();
    const moved = moveItem(before, [2, 0], [3], 0);
    expect(keysIn(moved, [3])[0]).toBe('teams');
  });

  it('moves sections', () => {
    const form = shift(sample(), [0], 1);
    expect(form.sections.map(s => s.key)).toEqual(['fire', 'summary', 'response', 'sign_off']);
    expect(shift(form, [0], -1)).toBe(form);
  });

  it('removes items with their layout entries', () => {
    const form = removeNode(sample(), findPath(sample(), 'teams')!);
    expect(allKeys(form)).not.toContain('team_name');
    expect(form.layout?.items?.some(entry => entry.item === 'team_name')).toBe(false);
    // total_staff and team_count used the teams, so the validator now explains the problem.
    expect(validateForm(form).some(problem => problem.includes('"teams"'))).toBe(true);
  });

  it('renames a key and its references', () => {
    const form = renameKey(sample(), findPath(sample(), 'cause_group')!, 'cause_kind');
    expect(validateForm(form)).toEqual([]);
    expect(JSON.stringify(form)).not.toContain('"cause_group"');
  });

  it('changes and removes properties', () => {
    const path = findPath(sample(), 'cause_notes')!;
    const form = updateNode(sample(), path, { hint: { en: 'Say what you saw' }, showWhen: undefined, required: undefined });
    const item = nodeAt(form, path)!;
    expect(item).toMatchObject({ hint: { en: 'Say what you saw' } });
    expect('showWhen' in item).toBe(false);
  });

  it('lists the places an item can go', () => {
    const names = containers(sample()).map(c => c.name);
    expect(names).toContain('Section: The fire');
    expect(names).toContain('Repeating group: Teams on site');
  });

  it('describes conditions in plain English', () => {
    const form = sample();
    const notes = nodeAt(form, findPath(form, 'cause_notes')!)!;
    expect(describeCondition(form, notes.showWhen)).toMatch(/^Likely cause is .+ or Likely cause is .+$/);
    expect(testsFor(nodeAt(form, findPath(form, 'vegetation')!) as never)).toEqual(['answered', 'includes']);
  });
});
