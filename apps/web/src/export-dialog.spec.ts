import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { ExportDialog, ExportStatus } from './export-dialog';

describe('Export dialog', () => {
  const endpoint = 'https://api.test/api/v1/workspaces/demo';
  const pending: ExportStatus = { id: 'e1', status: 'pending', rowCount: 7, featureCount: null, fileBytes: null,
    expiresAt: '2026-01-02T10:00:00Z' };
  const ready: ExportStatus = { ...pending, status: 'ready', featureCount: 7, fileBytes: 20_480,
    fileName: 'open-fimra-demo-alerts-20260101T1000Z.gpkg' };
  const sessionEnded = vi.fn();
  let http: HttpTestingController;
  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: vi.fn() });
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded } }] });
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView'); http.verify(); });

  function render(forestClass = '', unitPath = '') {
    const fixture = TestBed.createComponent(ExportDialog);
    fixture.componentRef.setInput('endpoint', endpoint);
    fixture.componentRef.setInput('hours', 72);
    fixture.componentRef.setInput('forestClass', forestClass);
    fixture.componentRef.setInput('unitPath', unitPath);
    fixture.componentRef.setInput('queue', 'edge_review');
    fixture.componentRef.setInput('queueLabel', 'Edge review');
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const submit = () => { el.querySelector<HTMLButtonElement>('button[type=submit]')!.click(); fixture.detectChanges(); };
    return { fixture, el, submit };
  }

  it('sends the current queue and time window', async () => {
    const { el, fixture, submit } = render();
    expect(el.querySelector('#export-summary')!.textContent).toContain('observed in the past 3 days · all forest classes · Edge review queue');
    el.querySelector<HTMLButtonElement>('#export-format')!.click();
    fixture.detectChanges();
    const options = Array.from(document.querySelectorAll<HTMLElement>('[role=option]'));
    expect(options).toHaveLength(8);
    options.find(option => option.textContent.includes('Shapefile'))!.click();
    fixture.detectChanges();
    submit();
    const post = http.expectOne(endpoint + '/exports');
    expect(post.request.method).toBe('POST');
    expect(post.request.body).toEqual({ kind: 'alerts', format: 'shapefile', hours: 72, queue: 'edge_review',
      forestClasses: ['inside', 'near_edge', 'outside', 'fixed_heat_source'] });
    post.flush(pending);
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('[role=status]')!.textContent).toContain('Preparing 7 rows'); });
    expect(el.querySelector<HTMLButtonElement>('button[type=submit]')!.disabled).toBe(true);
  });

  it('shows progress, then downloads the finished file with the bearer request', async () => {
    const { el, fixture, submit } = render('inside', 'ds.c1.d1');
    el.querySelector<HTMLInputElement>('hlm-radio[value=detections] input')!.click();
    fixture.detectChanges();
    expect(el.querySelector('#export-summary')!.textContent)
      .toContain('observed in the past 3 days · Inside forest · units under ds.c1.d1.');
    submit();
    const post = http.expectOne(endpoint + '/exports');
    expect(post.request.body).toEqual({ kind: 'detections', format: 'gpkg', hours: 72,
      forestClasses: ['inside'], unitPath: 'ds.c1.d1' });
    post.flush(pending);
    await vi.advanceTimersByTimeAsync(1000);
    http.expectOne(endpoint + '/exports/e1').flush(ready);
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('.download')).not.toBeNull(); });
    expect(el.querySelector('[role=status]')!.textContent).toContain('Ready: 7 rows, 20.0 KB.');

    const created = vi.fn(() => 'blob:export');
    Object.assign(URL, { createObjectURL: created, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    el.querySelector<HTMLButtonElement>('.download')!.click();
    const file = http.expectOne(endpoint + '/exports/e1/file');
    expect(file.request.responseType).toBe('blob');
    file.flush(new Blob(['data']));
    await vi.waitFor(() => expect(click).toHaveBeenCalled());
    const link = click.mock.instances[0] as unknown as HTMLAnchorElement;
    expect(link.download).toBe(ready.fileName);
    expect(created).toHaveBeenCalled();
  });

  it('explains refused and oversized exports', async () => {
    const { el, fixture, submit } = render();
    submit();
    http.expectOne(endpoint + '/exports').flush({ error: 'x' }, { status: 403, statusText: 'Forbidden' });
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('[role=alert]')!.textContent).toContain('Your role cannot export'); });
    submit();
    http.expectOne(endpoint + '/exports').flush({ error: 'This export has 200001 rows. Narrow the filters to at most 100000 rows.' },
      { status: 422, statusText: 'Unprocessable' });
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('[role=alert]')!.textContent).toContain('Narrow the filters'); });
    submit();
    http.expectOne(endpoint + '/exports').flush({}, { status: 401, statusText: 'Unauthorized' });
    await vi.waitFor(() => expect(sessionEnded).toHaveBeenCalled());
  });

  it('exports the field reports list with its form and decision', () => {
    const fixture = TestBed.createComponent(ExportDialog);
    const set = (name: string, value: unknown) => fixture.componentRef.setInput(name, value);
    set('endpoint', endpoint);
    set('hours', 168);
    set('forestClass', '');
    set('unitPath', 'ds.c1.d1');
    set('queue', 'unit');
    set('queueLabel', 'Unit');
    set('initialKind', 'submissions');
    set('form', 'first-report');
    set('formLabel', 'First report');
    set('decision', 'approved');
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('#export-summary')!.textContent).toContain(
      'received in the past 7 days · all forest classes · units under ds.c1.d1 · First report reports, with a column for each answer · approved');
    el.querySelector<HTMLButtonElement>('button[type=submit]')!.click();
    fixture.detectChanges();
    const post = http.expectOne(endpoint + '/exports');
    expect(post.request.body).toEqual({ kind: 'submissions', format: 'gpkg', hours: 168, unitPath: 'ds.c1.d1',
      forestClasses: ['inside', 'near_edge', 'outside', 'fixed_heat_source'], form: 'first-report', decision: 'approved' });
    post.flush(pending);
  });

  it('requests dismissal with the Close button', () => {
    const { el, fixture } = render();
    const closed = vi.fn();
    fixture.componentInstance.closed.subscribe(closed);
    el.querySelector<HTMLButtonElement>('button.secondary')!.click();
    expect(closed).toHaveBeenCalledTimes(1);
  });
});
