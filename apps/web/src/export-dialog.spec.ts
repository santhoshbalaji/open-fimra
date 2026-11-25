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
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded } }] });
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => { vi.useRealTimers(); http.verify(); });

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

  it('is a labelled dialog that sends the current queue and time window', async () => {
    const { el, fixture, submit } = render();
    const dialog = el.querySelector('[role=dialog]')!;
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(el.querySelector('#' + dialog.getAttribute('aria-labelledby'))!.textContent).toBe('Export');
    expect(el.querySelector('#export-summary')!.textContent).toContain('observed in the past 3 days · all forest classes · Edge review queue');
    const select = el.querySelector('select')!;
    expect(Array.from(select.options, o => o.value)).toEqual(
      ['gpkg', 'shapefile', 'kml', 'geojson', 'flatgeobuf', 'csv_wkt', 'csv_lonlat', 'xlsx']);
    select.value = 'shapefile';
    select.dispatchEvent(new Event('change'));
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
    el.querySelector<HTMLInputElement>('input[value=detections]')!.click();
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

  it('closes with Escape or the Close button', () => {
    const { el, fixture } = render();
    const closed = vi.fn();
    fixture.componentInstance.closed.subscribe(closed);
    el.querySelector('[role=dialog]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    el.querySelector<HTMLButtonElement>('button.secondary')!.click();
    expect(closed).toHaveBeenCalledTimes(2);
  });
});
