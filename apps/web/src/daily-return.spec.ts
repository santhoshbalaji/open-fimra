import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { convertToParamMap, provideRouter } from '@angular/router';
import { afterEach, describe, expect, it } from 'vitest';
import { WEB_CONFIG } from './config';
import { DailyReturnView, returnDateTime } from './daily-return';
import { notificationURL } from './notifications';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';

describe('Daily situation return', () => {
  const cutoff = '2026-03-08T12:30:00Z';
  const day = '2026-03-08';
  const path = 'ds.c1.d1';
  const base = 'https://api.test/api/v1/workspaces/demo/daily-returns';
  const policy = { hierarchy: 'territorial', level: 'division', timeZone: 'Asia/Kolkata', cutoff: '18:00',
    from: day, to: day, approver: 'state_admin' };

  function open() {
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]), provideHttpClient(), provideHttpClientTesting(),
        { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
        { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }) } },
        { provide: ShellNavigation, useValue: { query: signal(convertToParamMap({ day })) } }
      ]
    });
    const fixture = TestBed.createComponent(DailyReturnView);
    const http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    http.expectOne(req => req.url === base && !req.params.has('unit')).flush({
      day, today: day, policy, cutoff, canConfigure: false, coverage: {
        expected: 2, reported: 1, missing: 1, daily: {}, season: {},
        units: [
          { path, name: 'Hill Division', status: 'reported', nil: true },
          { path: 'ds.c1.d2', name: 'Valley Division', status: 'missing', nil: false }
        ]
      }
    });
    fixture.detectChanges();
    return { fixture, http, page: fixture.nativeElement as HTMLElement };
  }

  afterEach(() => TestBed.inject(HttpTestingController, null)?.verify());

  it('labels a nil return and a missing return distinctly and shows zero for the filed return', () => {
    const { fixture, http, page } = open();
    const rows = page.querySelectorAll('tbody tr');
    expect(rows[0].textContent).toContain('Nil return');
    expect(rows[1].textContent).toContain('Reporting gap');
    expect(page.textContent).toContain('2 expected, 1 reported, 1 missing');
    rows[0].querySelector('button')?.click();
    fixture.detectChanges();
    const reviewed = { id: 'prior', revision: 1, status: 'approved', submittedBy: 'Demo officer',
      submittedAt: cutoff, decidedBy: 'Demo reviewer', decidedAt: cutoff };
    http.expectOne(req => req.url === base && req.params.get('unit') === path).flush({
      unit: 'Hill Division', cutoff, history: [reviewed], canReview: false, canSubmit: false,
      snapshot: { review: reviewed, period: { end: cutoff }, dailyReturn: { nil: true, totals: {} } }
    });
    fixture.detectChanges();
    const source = page.querySelector('table[aria-label="Sources"] tbody tr');
    expect(source?.textContent).toContain('Satellite reports0');
    expect(page.textContent).toContain('Revision 1');
    expect(page.textContent).toContain('Download PDF');
  });

  it('displays the cutoff in the reporting time zone and links the inbox to its unit and day', () => {
    expect(returnDateTime(cutoff, 'Asia/Kolkata')).not.toEqual(returnDateTime(cutoff, 'America/New_York'));
    const url = notificationURL({ workspace: 'demo', alert: null, dailyDay: day, dailyUnit: path });
    expect(url.pathname).toMatch(/\/reports\/daily$/);
    expect(url.searchParams.get('day')).toBe(day);
    expect(url.searchParams.get('unit')).toBe(path);
  });
});
