import { Component, signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import firstReport from '../../../../packages/form-schema/examples/first-report.json';
import { stubLayoutApis } from '../overlay-testing';
import { FormDesigner, ParentTemplate } from './form-designer';
import { TemplateRules } from './form-locks';
import { FormDefinition, findPath, nodeAt } from './form-model';

@Component({
  imports: [FormDesigner],
  template: `<fimra-form-designer [(form)]="form" [(rules)]="rules" [parent]="parent()" [level]="level()" [readonly]="readonly()" />`
})
class Host {
  readonly form = signal(structuredClone(firstReport) as unknown as FormDefinition);
  readonly rules = signal<TemplateRules>({ allowExtension: false, locked: [] });
  readonly parent = signal<ParentTemplate | null>(null);
  readonly level = signal<'national' | 'state' | 'division'>('state');
  readonly readonly = signal(false);
}

describe('FormDesigner', () => {
  let fixture: ComponentFixture<Host>;
  let host: Host;
  const root = () => fixture.nativeElement as HTMLElement;
  const designer = () => fixture.debugElement.children[0].componentInstance as FormDesigner;
  const button = (label: string) => [...root().querySelectorAll('button')].find(b => b.textContent?.trim() === label || b.getAttribute('aria-label') === label);
  const click = (label: string) => { const b = button(label); expect(b, label).toBeTruthy(); b!.click(); fixture.detectChanges(); };
  const keys = (container: string) => nodeAt(host.form(), findPath(host.form(), container)!)!.items!.map(i => i.key);
  const row = (key: string) => root().querySelector<HTMLButtonElement>('#form-row-' + findPath(host.form(), key)!.join('-'))!;
  const status = () => root().querySelector('[role=status]')!.textContent!.trim();

  beforeEach(() => {
    stubLayoutApis();
    TestBed.configureTestingModule({ imports: [Host] });
    fixture = TestBed.createComponent(Host);
    host = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('shows the sections and items, and finds no problems in a valid form', () => {
    expect(root().querySelectorAll('.designer-section')).toHaveLength(4);
    expect(row('cause').textContent).toContain('Likely cause');
    expect(root().querySelector('.designer-problems')!.textContent).toContain('No problems found');
  });

  it('adds a field after the selected item from the palette', () => {
    row('cause').click();
    fixture.detectChanges();
    click('Number');
    expect(keys('fire')).toEqual(['fire_status', 'cause_group', 'cause', 'number_1', 'cause_notes', 'estimated_area', 'vegetation', 'site_photos']);
    expect(status()).toBe('Added number "New question" to section "The fire".');
    expect(root().querySelector('#properties-heading')!.textContent).toContain('Number: New question');
  });

  it('moves items with the keyboard and the move buttons', () => {
    const cause = row('cause');
    cause.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', altKey: true, bubbles: true }));
    fixture.detectChanges();
    expect(keys('fire').slice(0, 4)).toEqual(['fire_status', 'cause_group', 'cause_notes', 'cause']);
    expect(status()).toBe('Moved "Likely cause" down to place 4.');
    click('Move Likely cause up');
    expect(keys('fire').slice(0, 4)).toEqual(['fire_status', 'cause_group', 'cause', 'cause_notes']);
    // Moving the cause before its parent list breaks nothing the validator checks, so a move to
    // another section is allowed too, from the "Move to" list.
    row('remarks').click();
    fixture.detectChanges();
    const select = root().querySelector<HTMLSelectElement>('#prop-move')!;
    select.value = findPath(host.form(), 'teams')!.join('-');
    select.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(keys('teams')).toEqual(['team_name', 'members', 'team_arrived', 'remarks']);
  });

  it('drops a new item from the palette and moves an item between lists', () => {
    const d = designer();
    const list = (path: number[]) => ({ id: d.listId(path), data: path });
    const palette = { id: 'form-palette', data: null };
    d.dropped({ container: list([0]), previousContainer: palette, item: { data: 'photo' }, currentIndex: 0, previousIndex: 3 } as never);
    expect(keys('summary')[0]).toBe('photo_1');
    const teams = findPath(host.form(), 'teams')!;
    d.dropped({ container: list(teams), previousContainer: list([0]), item: { data: [0, 0] }, currentIndex: 0, previousIndex: 0 } as never);
    expect(keys('teams')[0]).toBe('photo_1');
    // A group cannot go inside itself.
    expect(d.canDrop({ data: teams } as never, { data: [...teams] } as never)).toBe(false);
    expect(d.canDrop({ data: 'text' } as never, { data: teams } as never)).toBe(true);
  });

  it('changes properties and shows the validator problems in plain English', () => {
    row('cause_notes').click();
    fixture.detectChanges();
    const label = root().querySelector<HTMLInputElement>('#prop-text-en')!;
    label.value = 'What else happened';
    label.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(row('cause_notes').textContent).toContain('What else happened');
    const key = root().querySelector<HTMLInputElement>('#prop-key')!;
    key.value = 'cause';
    key.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(root().textContent).toContain('The key "cause" is already used in this form.');
    // An edit through the form file that breaks a reference shows the validator's message.
    click('Form file');
    const file = root().querySelector<HTMLTextAreaElement>('#form-file')!;
    file.value = file.value.replace('"list": "cause_groups"', '"list": "cause_kinds"');
    file.dispatchEvent(new Event('input'));
    click('Apply the file');
    expect(root().querySelector('.designer-problems')!.textContent).toContain('Field "cause_group": uses the list "cause_kinds", which does not exist.');
    click('Field "cause_group": uses the list "cause_kinds", which does not exist.');
    expect(root().querySelector('#properties-heading')!.textContent).toContain('Single choice: Kind of cause');
  });

  it('names a new question with its label after a section was selected', () => {
    click('Section');
    click('Text');
    const label = root().querySelector<HTMLInputElement>('#prop-text-en')!;
    label.value = 'Range office';
    label.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    const added = host.form().sections[4].items[1];
    expect(added).toMatchObject({ type: 'text', label: { en: 'Range office' } });
    expect('title' in added).toBe(false);
  });

  it('builds a condition from simple rules', () => {
    row('remarks').click();
    fixture.detectChanges();
    click('Add a rule');
    const [field, test] = [...root().querySelectorAll<HTMLSelectElement>('.condition-rule select')];
    field.value = 'fire_status';
    field.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    test.value = 'equals';
    test.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    const remarks = nodeAt(host.form(), findPath(host.form(), 'remarks')!)!;
    expect(remarks.showWhen).toMatchObject({ op: 'equals', field: 'fire_status' });
    expect(root().querySelector('.designer-problems')!.textContent).toContain('No problems found');
    click('Preview');
    expect(root().textContent).toContain('Shown when Fire now is Still burning');
  });

  it('keeps parts locked by the template it extends', () => {
    host.parent.set({ form: 'first-report', version: 1, level: 'national', definition: structuredClone(host.form()),
      rules: { allowExtension: true, locked: ['summary', 'cause'] } });
    host.rules.set({ allowExtension: false, locked: ['summary', 'cause'] });
    fixture.detectChanges();
    expect(row('reached_at').textContent).toContain('Locked by template');
    expect(button('Move Likely cause up')!.disabled).toBe(false);
    expect(button('Move Team reached the site at down')!.disabled).toBe(true);
    row('reached_at').click();
    fixture.detectChanges();
    expect(root().querySelector('.designer-properties fieldset')!.hasAttribute('disabled')).toBe(true);
    expect(root().textContent).toContain('Locked by first-report version 1.');
    // Adding with the summary selected puts the item in the next unlocked place.
    click('Text');
    expect(keys('summary')).toHaveLength(4);
    // A change that reaches a locked part anyway, through the form file, is a problem.
    const changed = structuredClone(host.form());
    changed.sections[0].title = { en: 'Overview' };
    host.form.set(changed);
    fixture.detectChanges();
    expect(root().querySelector('.designer-problems')!.textContent).toContain('Section "summary" is locked by the template it extends and cannot change.');
  });

  it('locks parts for the forms that extend this one', () => {
    host.level.set('national');
    fixture.detectChanges();
    const allow = [...root().querySelectorAll<HTMLInputElement>('.designer-properties input[type=checkbox]')].at(-1)!;
    allow.click();
    fixture.detectChanges();
    expect(host.rules().allowExtension).toBe(true);
    row('cause').click();
    fixture.detectChanges();
    const lock = [...root().querySelectorAll<HTMLInputElement>('.designer-properties input[type=checkbox]')].at(-1)!;
    lock.click();
    fixture.detectChanges();
    expect(host.rules().locked).toEqual(['cause']);
    expect(row('cause').textContent).toContain('Locked for extensions');
  });

  it('shows a published version read-only', () => {
    host.readonly.set(true);
    fixture.detectChanges();
    expect(button('Number')).toBeUndefined();
    expect(button('Move Likely cause up')).toBeUndefined();
    row('cause').click();
    fixture.detectChanges();
    expect(root().querySelector('.designer-properties fieldset')!.hasAttribute('disabled')).toBe(true);
  });
});
