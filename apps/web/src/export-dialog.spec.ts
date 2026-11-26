import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ExportDialog } from './export-dialog';
import { ExportStatus } from './export-dialog-jobs';
import { ExportTray } from './export-dialog-tray';
import { stubLayoutApis } from './overlay-testing';
import { WorkspaceService } from './workspace';

describe('Export dialog and jobs', () => {
  const endpoint = 'https://api.test/api/v1/workspaces/demo/exports';
  const pending: ExportStatus = {
    id: 'e1', kind: 'alerts', format: 'gpkg', status: 'pending', rowCount: 7, featureCount: null, fileBytes: null,
    filters: { hours: 72, forestClasses: ['inside'], unitPath: 'ds.c1.d1', queue: 'edge_review',
      from: '2026-01-01T10:00:00Z', to: '2026-01-04T10:00:00Z' },
    createdAt: '2026-01-04T10:00:00Z', expiresAt: '2026-01-04T11:00:00Z'
  };
  const ready: ExportStatus = { ...pending, status: 'ready', featureCount: 7, fileBytes: 20_480,
    fileName: 'open-fimra-demo-alerts-20260104T1000Z.gpkg' };
  const sessionEnded = vi.fn();
  const user = signal({ profile: { sub: 'demo-user' } });
  let http: HttpTestingController;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-04T10:00:00Z'));
    stubLayoutApis();
    user.set({ profile: { sub: 'demo-user' } });
    TestBed.configureTestingModule({ providers: [
      provideHttpClient(), provideHttpClientTesting(), provideSpartanHlm(),
      { provide: AuthService, useValue: { user, sessionEnded } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
      { provide: WorkspaceService, useValue: { current: () => ({ slug: 'demo' }),
        jurisdictions: () => [['ds.c1.d1', 'Demo Division']] } }
    ] });
    http = TestBed.inject(HttpTestingController);
  });

  afterEach(() => {
    try { http.verify(); } finally {
      TestBed.resetTestingModule();
      vi.restoreAllMocks();
      vi.unstubAllGlobals();
      vi.useRealTimers();
    }
  });

  function render(inputs: Record<string, unknown> = {}) {
    const fixture = TestBed.createComponent(ExportDialog);
    for (const [name, value] of Object.entries({ endpoint: endpoint.replace('/exports', ''), hours: 72,
      forestClass: 'inside', unitPath: 'ds.c1.d1', queue: 'edge_review', queueLabel: 'Edge review', ...inputs })) {
      fixture.componentRef.setInput(name, value);
    }
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const submit = () => { el.querySelector<HTMLButtonElement>('button[type=submit]')!.click(); fixture.detectChanges(); };
    return { fixture, el, submit };
  }

  const body = (request: ReturnType<HttpTestingController['expectOne']>) => JSON.parse(JSON.stringify(request.request.body));
  const initialList = (items: ExportStatus[] = []) => http.expectOne({ method: 'GET', url: endpoint }).flush({ items });

  it('places the reviewed dataset, access and exact filters above the action', async () => {
    const { el, fixture, submit } = render();
    initialList();
    const summary = el.querySelector('#export-summary')!;
    const prepare = el.querySelector('button[type=submit]')!;
    for (const detail of ['Alerts', 'Past 3 days', 'Inside forest', 'Units under ds.c1.d1', 'Edge review', 'Demo Division']) {
      expect(summary.textContent).toContain(detail);
    }
    expect(summary.compareDocumentPosition(prepare) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(prepare.getAttribute('aria-describedby')).toBe('export-summary');
    expect(prepare.getAttribute('aria-label')).toBe('Prepare export as GeoPackage (recommended)');
    el.querySelector<HTMLButtonElement>('#export-format')!.click();
    fixture.detectChanges();
    [...document.querySelectorAll<HTMLElement>('[role=option]')].find(option => option.textContent.includes('Shapefile'))!.click();
    fixture.detectChanges();
    expect(prepare.getAttribute('aria-label')).toBe('Prepare export as Shapefile (zipped)');
    submit();
    const post = http.expectOne({ method: 'POST', url: endpoint });
    expect(body(post)).toEqual({ kind: 'alerts', format: 'shapefile', hours: 72, queue: 'edge_review',
      forestClasses: ['inside'], unitPath: 'ds.c1.d1' });
    post.flush({ ...pending, format: 'shapefile' });
    await vi.advanceTimersByTimeAsync(0);
    fixture.detectChanges();
    expect(el.querySelector('fimra-export-job [role=status]')?.textContent).toContain('Preparing 7 rows');
  });

  it('reopens an export closed before the create response and downloads it when ready', async () => {
    const { el, fixture, submit } = render();
    initialList();
    const closed = vi.fn(() => fixture.destroy());
    fixture.componentInstance.closed.subscribe(closed);
    submit();
    const post = http.expectOne({ method: 'POST', url: endpoint });
    el.querySelector<HTMLButtonElement>('button.secondary')!.click();
    expect(closed).toHaveBeenCalledOnce();
    post.flush(pending);
    await vi.advanceTimersByTimeAsync(0);

    const tray = TestBed.createComponent(ExportTray);
    tray.detectChanges();
    (tray.nativeElement as HTMLElement).querySelector<HTMLButtonElement>('[aria-label="Export jobs"]')!.click();
    initialList([pending]);
    tray.detectChanges();
    const modal = document.querySelector('[role=dialog]')!;
    expect(modal.textContent).toContain('Pending');
    TestBed.tick();
    await vi.advanceTimersByTimeAsync(2000);
    initialList([ready]);
    tray.detectChanges();
    modal.querySelector('summary')!.click();
    expect(modal.textContent).toContain('Ready');

    const createObjectURL = vi.fn(() => 'blob:export');
    vi.stubGlobal('URL', Object.assign(class extends URL {}, { createObjectURL, revokeObjectURL: vi.fn() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    modal.querySelector<HTMLButtonElement>('.download')!.click();
    const file = http.expectOne(endpoint + '/e1/file');
    expect(file.request.responseType).toBe('blob');
    file.flush(new Blob(['data']));
    await vi.advanceTimersByTimeAsync(0);
    expect(createObjectURL).toHaveBeenCalled();
    expect((click.mock.instances[0] as HTMLAnchorElement).download).toBe(ready.fileName);
  });

  it('lists failed and expired jobs and retries with the same filters', async () => {
    const tray = TestBed.createComponent(ExportTray);
    tray.detectChanges();
    const failed: ExportStatus = { ...pending, status: 'failed' };
    const expired: ExportStatus = { ...ready, id: 'e2', expiresAt: '2026-01-04T09:59:00Z' };
    initialList([failed, expired]);
    (tray.nativeElement as HTMLElement).querySelector<HTMLButtonElement>('[aria-label="Export jobs"]')!.click();
    initialList([failed, expired]);
    tray.detectChanges();
    const modal = document.querySelector('[role=dialog]')!;
    expect(modal.textContent).toContain('Failed');
    expect(modal.textContent).toContain('Expired');
    expect(modal.querySelector('.download')).toBeNull();
    modal.querySelector('summary')!.click();
    [...modal.querySelectorAll('button')].find(button => button.textContent === 'Retry export')!.click();
    const post = http.expectOne({ method: 'POST', url: endpoint });
    expect(body(post)).toEqual({ kind: 'alerts', format: 'gpkg', hours: 72, forestClasses: ['inside'],
      unitPath: 'ds.c1.d1', queue: 'edge_review' });
    post.flush({ ...pending, id: 'e3' });
    await vi.advanceTimersByTimeAsync(0);
    tray.detectChanges();
    expect(modal.querySelector('details[open]')?.textContent).toContain('Preparing 7 rows');
  });

  it('explains refused and oversized exports', async () => {
    const { el, fixture, submit } = render();
    initialList();
    submit();
    http.expectOne({ method: 'POST', url: endpoint }).flush({}, { status: 403, statusText: 'Forbidden' });
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('[role=alert]')?.textContent).toContain('Your role cannot export'); });
    submit();
    http.expectOne({ method: 'POST', url: endpoint }).flush({ error: 'Narrow the filters' },
      { status: 422, statusText: 'Unprocessable' });
    await vi.waitFor(() => { fixture.detectChanges(); expect(el.querySelector('[role=alert]')?.textContent).toContain('Narrow the filters'); });
    submit();
    http.expectOne({ method: 'POST', url: endpoint }).flush({}, { status: 401, statusText: 'Unauthorized' });
    await vi.waitFor(() => expect(sessionEnded).toHaveBeenCalled());
  });

  it('reviews the report form and decision and sends them only for that dataset', () => {
    const { el, submit } = render({ initialKind: 'submissions', form: 'first-report', formLabel: 'First report', decision: 'approved' });
    initialList();
    const summary = el.querySelector('#export-summary')!.textContent;
    for (const detail of ['Field reports', 'Received', 'First report (one column per answer)', 'Approved']) {
      expect(summary).toContain(detail);
    }
    submit();
    const post = http.expectOne({ method: 'POST', url: endpoint });
    expect(body(post)).toEqual({ kind: 'submissions', format: 'gpkg', hours: 72, unitPath: 'ds.c1.d1',
      forestClasses: ['inside'], form: 'first-report', decision: 'approved' });
    post.flush({ ...pending, kind: 'submissions' });
  });
});
