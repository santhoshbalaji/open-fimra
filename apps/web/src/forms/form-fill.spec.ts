import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AlertRow } from '../alert-queue';
import { AuthService } from '../auth';
import { stubLayoutApis } from '../overlay-testing';
import { FormFill, withOffset } from './form-fill';
import { explain } from './form-text';
import { readForm } from '@open-fimra/form-schema/answers';

import firstReport from '../../../../packages/form-schema/examples/first-report.json';
import dispatch from '../../../../packages/form-schema/examples/control-room-dispatch.json';

const workspace = 'https://api.test/api/v1/workspaces/demo';
const alert = {
  id: '7', detection_id: '70', status: 'first_report', step_label: 'First report', step_time: '2026-03-15T10:00:00Z', overdue: false,
  queue: 'unit', forest_class: 'inside', observed_at: Date.parse('2026-03-15T06:00:00Z') / 1000, longitude: 86.3, latitude: 16.4,
  beat_name: 'Beat 05', source: 'firms', confidence: 'n', frp_mw: 12.5
} as AlertRow;
const photo = '00000000-0000-4000-8000-000000000001';
const signature = '00000000-0000-4000-8000-000000000002';
const complete = {
  reached_at: '2026-03-15T14:00:00+05:30', fire_status: 'controlled', cause_group: 'natural', cause: 'lightning',
  estimated_area: 1.25, site_photos: [photo], teams: [{ team_name: 'Demo Team A', members: 6 }], reporter_signature: signature
};

describe('Form renderer', () => {
  let http: HttpTestingController;
  beforeEach(() => {
    stubLayoutApis();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:preview'), revokeObjectURL: vi.fn() }));
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } }] });
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => { http.verify(); vi.unstubAllGlobals(); });

  function render(options: { definition?: object; step?: string; initial?: object } = {}) {
    const fixture = TestBed.createComponent(FormFill);
    const set = (name: string, value: unknown) => fixture.componentRef.setInput(name, value);
    set('workspaceUrl', workspace);
    set('form', 'first-report');
    set('version', 1);
    set('step', options.step ?? 'first_report');
    set('definition', options.definition ?? firstReport);
    set('alert', alert);
    set('initial', options.initial ?? {});
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const buttons = () => Array.from(el.querySelectorAll<HTMLButtonElement>('button'));
    const press = (text: string) => {
      const button = buttons().find(b => b.textContent.trim() === text);
      if (!button) throw new Error(`No button "${text}" in ${buttons().map(b => b.textContent.trim()).join(' | ')}`);
      button.click();
      fixture.detectChanges();
    };
    return { fixture, el, component: fixture.componentInstance, press, buttons };
  }

  it('shows values from the alert and works out calculations as people type', () => {
    const { el, fixture, component } = render();
    expect(el.querySelector('h3')!.textContent).toContain('First report');
    expect(el.textContent).toContain('Page 1 of 4');
    expect(el.textContent).toContain('Seen by the satellite at');
    expect(el.textContent).toContain('86.3, 16.4');
    const reached = el.querySelector<HTMLInputElement>('input[type=datetime-local]')!;
    expect(el.querySelector('label[for=field-reached_at]')!.textContent).toContain('(required)');
    reached.value = '2026-03-15T08:30';
    reached.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(component.values()['reached_at']).toBe(withOffset('2026-03-15T08:30'));
    expect(el.querySelector('#field-hours_to_reach')!.textContent).not.toBe('No answer');
  });

  it('shows a question only when its condition holds, and narrows a cascading list', () => {
    const { el, press, component } = render();
    press('Next');
    expect(el.textContent).toContain('Fire now');
    expect(el.textContent).not.toContain('Describe the cause');
    expect(el.textContent).toContain('Answer “Kind of cause” first.');
    press('Natural');
    const cause = component.tree()!.byKey.get('cause')!;
    expect(component.choices(cause, []).map(c => c.key)).toEqual(['lightning', 'natural-other']);
    component.set(cause, [], 'natural-other');
    TestBed.tick();
    expect(el.textContent).toContain('Describe the cause');
  });

  it('adds and removes repeating group entries, starting with the fewest needed', () => {
    const { el, press } = render();
    press('Next');
    press('Next');
    expect(Array.from(el.querySelectorAll('legend'), l => l.textContent.trim())).toEqual(['Team 1']);
    press('Add Team');
    expect(Array.from(el.querySelectorAll('legend'), l => l.textContent.trim())).toEqual(['Team 1', 'Team 2']);
    press('Remove Team 1');
    expect(Array.from(el.querySelectorAll('legend'), l => l.textContent.trim())).toEqual(['Team 1']);
  });

  it('explains missing answers in plain words and sends nothing', () => {
    const { el, press } = render();
    press('Next');
    press('Next');
    press('Next');
    press('Send report');
    const problems = el.querySelector('#form-problems')!;
    expect(problems.textContent).toContain('“Team reached the site at” is required.');
    expect(problems.textContent).toContain('Team 1: “Team name” is required.');
    expect(problems.textContent).not.toContain('reached_at');
    http.expectNone(workspace + '/alerts/7/submissions');
  });

  it('sends checked answers to the alert and shows the server’s problems beside the questions', async () => {
    const { el, fixture, press, component } = render({ initial: complete });
    for (let i = 0; i < 3; i++) press('Next');
    press('Send report');
    const post = http.expectOne(workspace + '/alerts/7/submissions');
    expect(post.request.body).toMatchObject({ form: 'first-report', version: 1, step: 'first_report',
      values: { fire_status: 'controlled', site_photos: [photo], teams: [{ team_name: 'Demo Team A', members: 6 }] } });
    expect(post.request.body.id).toMatch(/^[0-9a-f-]{36}$/);
    // Calculated values go too; the server works them out again.
    expect(post.request.body.values.total_staff).toBe(6);
    post.flush({ error: 'The answers do not fit the form.', problems: ['Field "estimated_area" must be at least 0.'] },
      { status: 422, statusText: 'Unprocessable' });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('#form-problems')!.textContent).toContain('The answers do not fit the form.');
    expect(el.querySelector('#form-problems')!.textContent).toContain('“Area burnt so far” must be at least 0.');
    component.show(explain('Field "estimated_area" must be at least 0.', component.tree(), 'en'));
    fixture.detectChanges();
    expect(el.textContent).toContain('Area burnt so far');
    expect(el.querySelector('.form-field.invalid')?.textContent).toContain('must be at least 0.');
    const field = el.querySelector('#field-estimated_area')!;
    expect(field.getAttribute('aria-describedby')).toBe('field-estimated_area-hint field-estimated_area-errors');
    expect(el.querySelector('#field-estimated_area-hint')?.textContent).toContain('A rough estimate.');
    expect(el.querySelector('#field-estimated_area-errors')?.textContent).toContain('must be at least 0.');
  });

  it('describes a missing dropdown answer on the focusable select button', () => {
    const { el, press } = render();
    press('Next');
    press('Natural');
    press('Next');
    press('Next');
    press('Send report');
    const problem = Array.from(el.querySelectorAll<HTMLButtonElement>('#form-problems button'))
      .find(button => button.textContent.includes('“Likely cause”'));
    expect(problem).toBeDefined();
    problem!.click();
    TestBed.tick();
    const trigger = el.querySelector('button#field-cause')!;
    expect(trigger.getAttribute('aria-describedby')?.split(' ')).toContain('field-cause-errors');
    expect(el.querySelector('#field-cause-errors')?.textContent).toContain('“Likely cause” is required.');
  });

  it('uploads photos to the alert before the report', async () => {
    const { component, fixture, el } = render();
    const field = component.tree()!.byKey.get('site_photos')!;
    const file = new File([new Uint8Array([0xff, 0xd8, 0xff])], 'site.jpg', { type: 'image/jpeg' });
    const done = component.addFiles(field, [], [file] as unknown as FileList);
    const put = http.expectOne(request => request.url.startsWith(workspace + '/alerts/7/attachments/'));
    expect(put.request.method).toBe('PUT');
    expect(put.request.params.get('name')).toBe('site.jpg');
    expect(put.request.headers.get('Content-Type')).toBe('image/jpeg');
    put.flush({}, { status: 201, statusText: 'Created' });
    await done;
    const id = put.request.url.split('/').pop()!;
    expect(component.values()['site_photos']).toEqual([id]);
    component.page.set(1);
    fixture.detectChanges();
    expect(el.textContent).toContain('site.jpg (3 bytes)');
  });

  it('shows labels in the chosen language, with English where a translation is missing', () => {
    const { el, fixture, component } = render();
    expect(component.languages()).toEqual(['en', 'hi']);
    expect(el.querySelector('#form-language')).toBeTruthy();
    component.language.set('hi');
    fixture.detectChanges();
    expect(el.querySelector('h3')!.textContent).toContain('प्रारंभिक रिपोर्ट');
    expect(el.textContent).toContain('Seen by the satellite at');
  });

  it('keeps earlier stages read-only at a later step', () => {
    const earlier = { team_name: 'Demo Team A', team_leader: 'Demo Leader', team_size: 5, vehicle: 'jeep', dispatched_at: '2026-03-15T07:00:00Z' };
    const { el, fixture, component, press } = render({ definition: dispatch, step: 'reached_site', initial: earlier });
    expect(component.stageTitle()).toBe('Reached site');
    // The dispatch answers show as text, not as inputs.
    expect(el.querySelector('#field-team_name')!.tagName).toBe('P');
    expect(el.querySelector('#field-team_name')!.textContent).toBe('Demo Team A');
    while (component.pages().indexOf(component.currentPage()) < component.pages().length - 1) press('Next');
    fixture.detectChanges();
    expect(el.querySelector('#field-reached_at')!.tagName).toBe('INPUT');
  });
});

describe('Plain-English problems', () => {
  const tree = readForm(firstReport);
  it('names fields and entries as people see them', () => {
    expect(explain('In entry 2 of "teams": Field "members" must be at least 1.', tree, 'en')).toEqual(
      { message: 'Team 2: “People” must be at least 1.', field: 'members', path: [['teams', 1]] });
    expect(explain('Field "cause": the choice "campfire" does not go with the answer to "cause_group".', tree, 'en').message)
      .toBe('“Likely cause”: the choice "campfire" does not go with the answer to “Kind of cause”.');
    expect(explain('values has "fire_tpye", which is not a field of this form.', tree, 'en').message).toBe('The form has no question “fire_tpye”.');
    expect(explain('Repeating group "teams" needs at least 1 entries.', tree, 'en')).toMatchObject({ field: 'teams', path: [] });
  });
});
