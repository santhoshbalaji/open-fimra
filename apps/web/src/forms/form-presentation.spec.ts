import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { AuthService } from '../auth';
import { FormPresentation } from './form-presentation';

const workspace = 'https://api.test/api/v1/workspaces/demo';
const field = (key: string, type: string, label: string, options: object = {}) => ({
  key, type, label: { en: label }, ...options
});
const choice = (key: string, en: string, hi: string, parent?: string) => ({ key, label: { en, hi }, parent });
const choiceFields = [
  field('status', 'single-choice', 'Status', { label: { en: 'Status', hi: 'स्थिति' }, choices: [choice('open', 'Open', 'खुला')] }),
  field('tags', 'multiple-choice', 'Tags', { list: 'tag_list', parent: 'status', minSelected: 1 })
];
const definition = {
  formatVersion: 1, id: 'demo-report', version: 1, title: { en: 'Demo report', hi: 'नमूना रिपोर्ट' },
  stages: [
    { key: 'first', workflowStep: 'first_report', title: { en: 'First report' } },
    { key: 'final', workflowStep: 'final_report', title: { en: 'Final report' } }
  ],
  lists: [{ key: 'tag_list', choices: [choice('review', 'Review', 'समीक्षा', 'open')] }],
  sections: [
    { key: 'details', title: { en: 'Details', hi: 'विवरण' }, stage: 'first', items: [
      ...choiceFields,
      field('notes', 'text', 'Notes', {
        minLength: 3, maxLength: 30, pattern: '[A-Z]+', patternHint: { en: 'Use capital letters.' },
        showWhen: { op: 'all', conditions: [
          { op: 'answered', field: 'status' },
          { op: 'any', conditions: [
            { op: 'includes', field: 'tags', value: 'review' },
            { op: 'not', condition: { op: 'not-equals', field: 'status', value: 'open' } }
          ] }
        ] },
        requireWhen: { op: 'less-than', field: 'weight', value: 10 }
      }),
      field('weight', 'number', 'Weight', { unit: 'kg', min: 0, max: 100, decimals: 2 }),
      field('day', 'date', 'Day', { notInFuture: true }),
      field('clock', 'time', 'Clock'),
      field('moment', 'date-time', 'Moment'),
      field('point', 'point', 'Point', { maxAccuracyMetres: 10 }),
      field('track', 'track', 'Walk'),
      field('polygon', 'polygon', 'Area'),
      field('photo', 'photo', 'Photo evidence', { maxCount: 2 }),
      field('file', 'file', 'Brief', { maxSizeMegabytes: 4, accept: ['.pdf'] }),
      field('signature', 'signature', 'Officer signature'),
      field('total', 'calculated', 'Total', { unit: 'kg', calculate: { op: 'number', value: 1 } }),
      field('power', 'alert-value', 'Power', { source: 'fire-power' }),
      field('location', 'alert-value', 'Detection position', { source: 'location' }),
      field('reported', 'alert-value', 'Observed at', { source: 'observed-at' }),
      { key: 'crew', type: 'repeat', title: { en: 'Crew' }, itemLabel: { en: 'Team' }, minRepeats: 1, maxRepeats: 4,
        showWhen: { op: 'more-than', field: 'total', value: 3 }, items: [
          { key: 'supervision', type: 'group', title: { en: 'Supervision' }, items: [
            field('lead', 'text', 'Lead'),
            { key: 'visits', type: 'repeat', title: { en: 'Visits' }, itemLabel: { en: 'Visit' },
              showWhen: { op: 'answered', field: 'lead' }, items: [
                field('visit_note', 'text', 'Visit note', { showWhen: { op: 'at-least', field: 'power', value: 0 } })
              ] }
          ] }
        ] }
    ] },
    { key: 'extra', stage: 'first', title: { en: 'Extra' }, showWhen: { op: 'at-most', field: 'power', value: -1 },
      items: [field('hidden', 'text', 'Hidden answer')] },
    { key: 'later', stage: 'final', title: { en: 'Later step' }, items: [field('reviewer', 'text', 'Reviewer')] }
  ],
  layout: { pages: [
    { key: 'main', title: { en: 'Site report' }, sections: ['details', 'extra'] },
    { key: 'close', sections: ['later'] }
  ], items: [{ item: 'notes', widget: 'paragraph' }, { item: 'status', widget: 'dropdown' }] }
};

describe('Shared form presentation', () => {
  let http: HttpTestingController;
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: () => undefined } }] });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => http.verify());

  function render(mode: 'preview' | 'historical', values: object = {}) {
    const fixture = TestBed.createComponent(FormPresentation);
    fixture.componentRef.setInput('mode', mode);
    fixture.componentRef.setInput('definition', definition);
    fixture.componentRef.setInput('workspaceUrl', workspace);
    fixture.componentRef.setInput('step', 'first_report');
    fixture.componentRef.setInput('values', values);
    fixture.detectChanges();
    return { fixture, el: fixture.nativeElement as HTMLElement };
  }

  it('previews the layout, translates labels, and explains all condition operators and limits', () => {
    const { el, fixture } = render('preview');
    const content = el.textContent.replace(/\s+/g, ' ');
    [
      'Shown when Status is answered and (Tags includes Review or not (Status is not Open)).',
      'Required when Weight is less than 10.', 'Shown when Total is more than 3.',
      'Shown when Power is at most -1.', 'Shown when Power is at least 0.', 'Shown when Lead is answered.',
      'Choices depend on “Status”.', 'When the answer is “Open”', 'From 3 to 30 characters.', 'Use capital letters.',
      'From 0 to 100 kg.', 'Up to 2 decimal places.', 'Cannot be in the future.', 'accurate to within 10 metres.',
      'At most 2 photos.', 'Up to 4 MB per file.', 'Accepted file endings: .pdf.', 'From 1 to 4 entries.',
      'Team 1', 'Visit 1', 'Pick a position', 'Record a walked track', 'Draw an area', 'Add photos', 'Add files', 'Sign here',
      'Worked out from other answers (kg)', 'From the alert: Fire power (MW)', 'Filled in at “Final report”'
    ].forEach(text => expect(content).toContain(text));
    expect(el.querySelector('textarea')?.disabled).toBe(true);
    expect(el.querySelector('input[type=datetime-local]')).toBeTruthy();
    const language = el.querySelector('select:not([disabled])') as HTMLSelectElement;
    language.value = 'hi';
    language.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(el.querySelector('h3')?.textContent).toBe('नमूना रिपोर्ट');
    expect(el.textContent).toContain('Shown when स्थिति is answered and (Tags includes समीक्षा or not (स्थिति is not खुला)).');
  });

  it('shows accepted answers, repeat scopes and stage visibility using stored alert values and calculations', async () => {
    const { el, fixture } = render('historical', {
      status: 'open', tags: ['review'], notes: 'DEMO', weight: 7.500125, day: '2026-03-15', clock: '14:30:01',
      moment: '2026-03-15T14:31+05:30', power: 0, reported: '2026-03-15T09:02:00Z', total: 8,
      point: { type: 'Point', coordinates: [81.25, 20.5] },
      location: { type: 'Point', coordinates: [82, 21] },
      track: { type: 'LineString', coordinates: [[81.25, 20.5], [81.3, 20.6]] },
      polygon: { type: 'Polygon', coordinates: [[[81, 20], [82, 21], [82, 20], [81, 20]], [[81.5, 20.1], [81.9, 20.2], [81.8, 20.1], [81.5, 20.1]]] },
      photo: ['demo-photo'], file: ['demo-file'], signature: 'demo-signature',
      crew: [{ lead: 'Demo A', visits: [{ visit_note: 'First visit' }, { visit_note: 'Second visit' }] }, { lead: 'Demo B' }]
    });
    http.expectOne(workspace + '/attachments/demo-photo').flush(new Blob(['photo'], { type: 'image/png' }));
    http.expectOne(workspace + '/attachments/demo-signature').flush(new Blob(['signature'], { type: 'image/png' }));
    http.expectNone(workspace + '/attachments/demo-file');
    fixture.detectChanges();

    const term = (label: string) => [...el.querySelectorAll('dt')].find(dt => dt.textContent === label);
    const answer = (label: string) => term(label)?.nextElementSibling?.textContent.replace(/\s+/g, ' ').trim();
    expect(answer('Status')).toBe('Open');
    expect(answer('Tags')).toBe('Review');
    expect(answer('Notes')).toBe('DEMO');
    expect(answer('Weight')).toBe('7.500125 kg');
    expect(answer('Day')).toContain('Mar');
    expect(answer('Clock')).toMatch(/2:30:01|14:30:01/);
    expect(answer('Moment')).toMatch(/2:31|14:31/);
    expect(answer('Observed at')).toMatch(/2:32|14:32/);
    expect(answer('Total')).toBe('8 kg');
    expect(answer('Power')).toBe('0 MW');
    expect(answer('Point')).toBe('20.5, 81.25 (latitude, longitude)');
    expect(answer('Detection position')).toBe('21, 82 (latitude, longitude)');
    expect(answer('Walk')).toContain('20.6, 81.3');
    expect(answer('Area')).toContain('20, 82');
    expect(answer('Area')).toContain('Inner boundary 1');
    expect(answer('Area')).toContain('20.1, 81.5');
    expect(el.querySelectorAll('img')).toHaveLength(2);
    expect(el.querySelector('img[alt="Officer signature, signature"]')).toBeTruthy();
    expect(el.textContent).toContain('Download file 1');
    expect(el.textContent).toContain('Second visit');
    expect(el.textContent).toContain('Team 2');
    expect(term('Hidden answer')).toBeUndefined();
    expect(term('Reviewer')).toBeUndefined();
    expect(el.querySelector('input, textarea')).toBeNull();

    const fileButton = [...el.querySelectorAll('button')].find(button => button.textContent.includes('Download file 1'));
    fileButton?.click();
    http.expectOne(workspace + '/attachments/demo-file').flush(null, { status: 403, statusText: 'Forbidden' });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.textContent).toContain('The file could not download. Try again.');

    const language = el.querySelector('select')!;
    language.value = 'hi';
    language.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    expect(answer('स्थिति')).toBe('खुला');
    expect(answer('Tags')).toBe('समीक्षा');
    expect(answer('Notes')).toBe('DEMO');
  });
});
