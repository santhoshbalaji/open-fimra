import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, convertToParamMap, provideRouter } from '@angular/router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { WEB_CONFIG } from '../config';
import { ShellNavigation } from '../shell-navigation';
import { WorkspaceService } from '../workspace';
import { NationalPortal } from './national-portal';

const base = 'https://api.test/api/v1/workspaces/exchange/national';
const period = { from: '2026-03-08', to: '2026-03-08' };
const startsAt = '2026-03-07T12:30:00Z';
const endsAt = '2026-03-08T12:30:00Z';
const state = { ...period, state: 'XS-DEMO', geography: 'Demo State' };

describe('National portal', () => {
  const query = signal(convertToParamMap({}));

  beforeEach(() => {
    query.set(convertToParamMap({}));
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]), provideHttpClient(), provideHttpClientTesting(),
        { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
        { provide: WorkspaceService, useValue: { current: signal({ slug: 'exchange', kind: 'national' }) } },
        { provide: ShellNavigation, useValue: { query, href: () => TestBed.inject(Router).parseUrl('/national') } }
      ]
    });
  });

  afterEach(() => TestBed.inject(HttpTestingController).verify());

  async function open(role: string, view: string, payload: object, submittingStates: string[] = []) {
    const fixture = TestBed.createComponent(NationalPortal);
    fixture.detectChanges();
    TestBed.tick();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(base).flush({ roles: [role], submittingStates });
    await Promise.resolve();
    TestBed.tick();
    http.expectOne(base + '/' + view).flush(payload);
    await fixture.whenStable();
    fixture.detectChanges();
    return { page: fixture.nativeElement as HTMLElement, fixture, http };
  }

  it('keeps comparison figures, provenance and coverage separate and marks missing returns', async () => {
    const { page } = await open('national_survey', 'survey', [
      {
        ...state, stateReturnShared: true, feedbackShared: true,
        stateReturn: {
          source: 'approved_daily_returns_and_assessments',
          window: { ...period, startsAt, endsAt, timeZone: 'Asia/Kolkata', cutoff: '18:00' },
          generatedAt: endsAt, revision: 2, coverage: { expected: 3, reported: 2, status: 'incomplete' },
          detections: { records: 6, deduplicated: 4 }
        },
        satellite: { source: 'Fictional satellite', product: 'Hotspots', startsAt, endsAt, expected: 7, reported: 6, detections: 42, samePeriod: false },
        feedback: { prepared: 1, submitted: 2, accepted: 3, rejected: 1 }
      },
      { ...state, state: 'XS-NORTH', geography: 'North Demo State', stateReturnShared: true },
    ]);
    const [reported, missing] = page.querySelectorAll('article');
    const [returnData, satellite] = reported.querySelectorAll('section');
    expect(returnData.textContent).toContain('Approved daily returns and assessments');
    expect(returnData.textContent).toContain('Revision 2');
    expect(returnData.textContent).toContain('3 expected · 2 reported · 1 missing');
    expect(satellite.textContent).toContain('Fictional satellite');
    expect(satellite.textContent).toContain('7 expected · 6 reported · 1 missing');
    expect(satellite.textContent).toContain('The state and satellite reporting periods differ.');
    expect(reported.textContent).toContain('State-approved FSI feedback status');
    expect(missing.textContent).toContain('Expected state return · Not reported');
  });

  it('shows only the public release and renders suppressed figures without a numeric count', async () => {
    query.set(convertToParamMap({ view: 'incidents' }));
    const { page } = await open('national_public', 'public', [
      { ...state, coverage: 'reported', confirmedFires: null, source: 'approved_daily_returns_and_assessments' }
    ]);
    expect(page.textContent).toContain('Public aggregates');
    expect(page.textContent).toContain('Count withheld');
    expect(page.querySelector('article strong')).toBeNull();
    expect(page.querySelector('form')).toBeNull();
    expect(page.textContent).not.toContain('Major incidents');
  });

  it('groups both state updates under the same shared event', async () => {
    const update = {
      ...state, incidentRef: 'DEMO-01', sharedEventId: 'shared-1', status: 'active', severity: 'major',
      occurredAt: startsAt, nextUpdateAt: endsAt, precisionDecimals: 0, longitude: 82, latitude: 20,
      aidRequested: 'One crew', shared: true
    };
    const { page } = await open('national_disaster', 'incidents', { eventCount: 1, updates: [
      update, { ...update, incidentRef: 'NORTH-02', state: 'XS-NORTH', geography: 'North Demo State' }
    ] });
    expect(page.textContent).toContain('Distinct shared events: 1');
    expect(page.querySelectorAll('article')).toHaveLength(1);
    expect(page.textContent).toContain('DEMO-01');
    expect(page.textContent).toContain('NORTH-02');
    expect(page.textContent).toContain('One crew');
    expect(page.querySelector('form')).toBeNull();
  });

  it('publishes the exact current revision for a nodal officer and refreshes the release', async () => {
    const row = { ...state, currentIncidentId: 'synthetic-id', public: false, ministry: true, receipts: [] };
    const { page, fixture, http } = await open('national_nodal', 'submissions', [row], ['XS-DEMO']);
    expect(page.querySelector('input[type=file]')).toBeTruthy();
    const publish = [...page.querySelectorAll('button')].find(button => button.textContent?.includes('Publish state-level total'))!;
    publish.click();
    const request = http.expectOne({ method: 'PUT', url: base + '/states/XS-DEMO/publication' });
    expect(request.request.body).toEqual({ ...period, extractId: 'synthetic-id' });
    request.flush({});
    await Promise.resolve();
    TestBed.tick();
    http.expectOne(base + '/submissions').flush([{ ...row, public: true }]);
    await fixture.whenStable();
    fixture.detectChanges();
    expect(page.textContent).toContain('Withdraw public release');
  });
});
