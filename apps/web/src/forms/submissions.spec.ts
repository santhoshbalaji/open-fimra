import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { convertToParamMap, type Params } from '@angular/router';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from '../auth';
import { DashboardFilters } from '../dashboard-filters';
import { stubLayoutApis } from '../overlay-testing';
import { ShellNavigation } from '../shell-navigation';
import { WorkspaceService } from '../workspace';
import { Submissions } from './submissions';

const endpoint = 'https://api.test/api/v1/workspaces/demo';
const timestamp = '2026-03-15T09:00:00Z';
const id = '5ca1ab1e-0000-4000-8000-0000000000a1';

const row = {
  id, title: { en: 'First report' }, step_label: 'First report', beat_name: 'Demo Beat',
  submitted_by: 'Demo Beat Guard', received_time: timestamp, alert_id: '7', alert_step_label: 'First report',
  decision: 'pending'
};

describe('Field reports queue', () => {
  let http: HttpTestingController;
  let params: Params;
  const query = signal(convertToParamMap({}));
  const setParams = (values: Params) => {
    params = { ...params, ...values };
    for (const key of Object.keys(params)) if (params[key] === null) delete params[key];
    query.set(convertToParamMap(params));
  };

  beforeEach(() => {
    stubLayoutApis();
    params = {};
    setParams({});
    TestBed.configureTestingModule({ providers: [
      provideSpartanHlm(), provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: ShellNavigation, useValue: { query, update: setParams } },
      { provide: WorkspaceService, useValue: { jurisdictions: () => [['ds.c1.d1', 'Demo Division']] } }
    ] });
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => http.verify());

  function render() {
    const fixture = TestBed.createComponent(Submissions);
    fixture.componentRef.setInput('endpoint', endpoint);
    fixture.componentRef.setInput('now', 1);
    fixture.detectChanges();
    http.expectOne(endpoint + '/forms').flush([]);
    const element = fixture.nativeElement as HTMLElement;
    const button = (label: string) => Array.from(element.querySelectorAll('button')).find(btn => btn.textContent.trim().startsWith(label))!;
    return { fixture, element, button };
  }
  const request = () => http.expectOne(req => req.url === endpoint + '/submissions');

  it('uses alert filters and keeps sort, pages and columns in the URL', () => {
    const filters = TestBed.inject(DashboardFilters);
    filters.unitPath.set('ds.c1.d1');
    filters.forestClass.set('inside');
    filters.hours.set(24);
    setParams({ decision: 'all', form: 'first-report' });
    const { fixture, element, button } = render();
    const filtered = request();
    expect(filtered.request.params.toString()).toBe('hours=24&class=inside&unitPath=ds.c1.d1&limit=20&form=first-report');
    filtered.flush({ items: [row] });
    fixture.detectChanges();
    expect(element.textContent).toContain('Demo Division');
    expect(element.querySelector('tbody')!.textContent).toContain('Submitted');
    expect(element.querySelector('tbody svg')).toBeTruthy();

    button('Received').click();
    fixture.detectChanges();
    expect(params['reportSort']).toBe('oldest');
    const sorted = request();
    expect(sorted.request.params.get('sort')).toBe('oldest');
    const cursor = timestamp + ',' + id;
    sorted.flush({ items: [row], nextCursor: cursor });
    fixture.detectChanges();
    button('Next').click();
    fixture.detectChanges();
    expect(params['cursor']).toBe(cursor);
    request().flush({ items: [{ ...row, decision: 'approved' }] });
    fixture.detectChanges();
    expect(element.textContent).toContain('Page 2');
    button('Previous').click();
    fixture.detectChanges();
    expect(params['cursor']).toBeUndefined();
    request().flush({ items: [row], nextCursor: cursor });
    fixture.detectChanges();

    const sender = Array.from(element.querySelectorAll('label')).find(label => label.textContent.trim() === 'Sender')!;
    sender.querySelector('input')!.click();
    fixture.detectChanges();
    expect(params['reportColumns']).toBe('sender');
    expect(element.querySelector('tbody')!.textContent).not.toContain(row.submitted_by);
    setParams({ reportColumns: null });
    fixture.detectChanges();
    expect(element.querySelector('tbody')!.textContent).toContain(row.submitted_by);
  });

  it('offers Retry after a failed request and describes an empty result', () => {
    const { fixture, element, button } = render();
    request().flush({}, { status: 500, statusText: 'Error' });
    fixture.detectChanges();
    expect(element.textContent).toContain('Field reports could not load');
    button('Retry').click();
    fixture.detectChanges();
    request().flush({ items: [] });
    fixture.detectChanges();
    expect(element.textContent).toContain('No field reports match');
  });
});
