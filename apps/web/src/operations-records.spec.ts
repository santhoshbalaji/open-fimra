import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { DeferBlockBehavior, TestBed } from '@angular/core/testing';
import { convertToParamMap, provideRouter, Router } from '@angular/router';
import { afterEach, describe, expect, it } from 'vitest';
import { WEB_CONFIG } from './config';
import { OperationsRecords } from './operations-records';
import { ShellNavigation } from './shell-navigation';
import { WorkspaceService } from './workspace';

describe('Referrals and preparedness', () => {
  const base = 'https://api.test/api/v1/workspaces/demo/operations-records';
  const id = 'de330000-0000-4000-8000-000000000001';
  const caseID = '42';
  function open() {
    const query = signal(convertToParamMap({ operation: id, operationCase: caseID }));
    TestBed.configureTestingModule({
      deferBlockBehavior: DeferBlockBehavior.Manual,
      providers: [
        provideRouter([]), provideHttpClient(), provideHttpClientTesting(),
        { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
        { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }) } },
        { provide: ShellNavigation, useValue: {
          query, href: (path: string, queryParams: object) => TestBed.inject(Router).createUrlTree([path], { queryParams })
        } }
      ]
    });
    const fixture = TestBed.createComponent(OperationsRecords);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(req => req.url === base && req.params.get('incident') === caseID).flush({ items: [] });
    return { fixture, http, query };
  }

  afterEach(() => TestBed.inject(HttpTestingController).verify());

  it('shows only the approved summary and case link to a summary-only viewer', () => {
    const { fixture, http } = open();
    http.expectOne(req => req.url === base + '/' + id).flush({
      id, kind: 'offence', incidentId: caseID, summaryOnly: true, summary: 'The enquiry has started.'
    });
    fixture.detectChanges();
    const element: HTMLElement = fixture.nativeElement;
    expect(element.textContent).toContain('Approved summary');
    expect(element.textContent).toContain('The enquiry has started.');
    expect(element.querySelector('a[href*="incident=42"]')).toBeTruthy();
    expect(element.textContent).not.toContain('Owner');
    expect(element.textContent).not.toContain('Add entry');
    expect(element.querySelector('fimra-submission-view')).toBeNull();
    http.expectNone(req => req.url.includes('/submissions/') || req.url.endsWith('/options'));
  });

  it('shows permitted follow-up and reports a pending completion', () => {
    const { fixture, http } = open();
    http.expectOne(req => req.url === base + '/' + id).flush({
      id, kind: 'after-action', summaryOnly: false, canWrite: true, owner: 'Demo Officer', unitPath: 'ds.c1.d1',
      entries: [], completed: false, completionPending: true
    });
    fixture.detectChanges();
    http.expectOne(req => req.url === base + '/options' && req.params.get('record') === id).flush({
      forms: [{ form: 'after-action-finding', kind: 'after-action', version: 1, completionOnly: false }], owners: []
    });
    fixture.detectChanges();
    const element: HTMLElement = fixture.nativeElement;
    expect(element.textContent).toContain('Demo Officer');
    expect(element.textContent).toContain('Completion awaiting review');
    expect(element.textContent).not.toContain('Record completion');
  });
});
