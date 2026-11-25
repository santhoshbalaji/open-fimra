import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Layers } from './layers';
import { WorkspaceService } from './workspace';

const root = 'https://api.test/api/v1/workspaces/demo/layer-imports';
const version = 'demo-version';
const detail = root + '/' + version;
const ready = {
  versionId: version, status: 'ready', role: 'hierarchy', unitCount: 24,
  featureCount: 0, sourceCrs: 'EPSG:4326', problems: []
};
const defaultPreview = { days: 30, scanned: 11, blocked: '', brokenGrants: [], grantConfirmation: '', changes: {} };

describe('Layers', () => {
  let fixture: ComponentFixture<Layers>;
  let http: HttpTestingController;

  const button = (label: string): HTMLButtonElement | undefined =>
    [...fixture.nativeElement.querySelectorAll('button') as NodeListOf<HTMLButtonElement>]
      .find(element => element.textContent.trim() === label);
  const text = () => fixture.nativeElement.textContent as string;

  beforeEach(() => {
    vi.useFakeTimers();
    TestBed.configureTestingModule({ imports: [Layers], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }) } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
    fixture = TestBed.createComponent(Layers);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    http.expectOne(root).flush({
      layers: [{ slug: 'beats', name: 'Beats', role: 'hierarchy', hierarchy: 'territorial', depth: 4 }],
      hierarchies: [{ slug: 'territorial', name: 'Territorial', levels: [{ depth: 4, name: 'Beat' }] }],
      uploads: [{ versionId: version, layerName: 'Beats', version: 2, sourceFile: 'beats.geojson', status: 'ready' }]
    });
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    vi.useRealTimers();
  });

  function openDraft(draft: object): void {
    const upload = fixture.nativeElement.querySelector('.layer-review select') as HTMLSelectElement;
    upload.value = version;
    upload.dispatchEvent(new Event('change'));
    vi.advanceTimersByTime(0);
    http.expectOne(detail).flush(draft);
    if (draft === ready || (draft as typeof ready).status === 'ready') {
      http.expectOne(root).flush({ layers: [], hierarchies: [], uploads: [] });
    }
    fixture.detectChanges();
  }

  function runPreview(data: object): void {
    const preview = fixture.nativeElement.querySelector('.window-form') as HTMLFormElement;
    preview.dispatchEvent(new Event('submit', { cancelable: true }));
    http.expectOne(detail + '/preview?days=30').flush(data);
    fixture.detectChanges();
  }

  it('submits the selected hierarchy mapping and polls for review', () => {
    const uploadForm = fixture.nativeElement.querySelector('form') as HTMLFormElement;
    const layer = uploadForm.querySelector('select') as HTMLSelectElement;
    layer.value = 'beats';
    layer.dispatchEvent(new Event('change'));
    fixture.detectChanges();
    (uploadForm.querySelector('[name=nameField]') as HTMLInputElement).value = 'label';
    (uploadForm.querySelector('[name=parentCodeField]') as HTMLInputElement).value = 'parent';
    uploadForm.dispatchEvent(new Event('submit', { cancelable: true }));
    const post = http.expectOne({ url: root, method: 'POST' });
    expect(post.request.body.get('role')).toBe('hierarchy');
    expect(post.request.body.get('layerSlug')).toBe('beats');
    expect(post.request.body.get('hierarchy')).toBe('territorial');
    expect(JSON.parse(post.request.body.get('settings'))).toEqual({ depth: 4, nameField: 'label', parentCodeField: 'parent' });
    post.flush({ ...ready, status: 'pending' }, { status: 202, statusText: 'Accepted' });
    http.expectOne(root).flush({ layers: [], hierarchies: [], uploads: [] });
    fixture.detectChanges();
    expect(text()).toContain('Importing');
    expect(button('Publish layer')).toBeUndefined();
    vi.advanceTimersByTime(1000);
    http.expectOne(detail).flush(ready);
    http.expectOne(root).flush({ layers: [], hierarchies: [], uploads: [] });
    fixture.detectChanges();
    expect(text()).toContain('Ready for review');
  });

  it('shows blocking review problems and disallows publishing after preview', () => {
    openDraft({ ...ready, problems: [{ kind: 'overlap', featureRef: 'feature:3', message: 'These beats overlap.', blocksPublish: true }] });
    runPreview({ ...defaultPreview, blocked: 'Fix the blocking problems.' });
    expect(text()).toContain('These beats overlap.');
    expect(text()).toContain('Fix the blocking problems.');
    expect(button('Publish layer')).toBeUndefined();
  });

  it('requires preview and explicit grant removal, then shows replay progress', () => {
    openDraft(ready);
    expect(button('Publish layer')).toBeUndefined();
    runPreview({
      ...defaultPreview,
      changes: { reassigned: { count: 1, items: [{
        detectionId: '42', observedAt: '2026-11-20T00:00:00Z', oldClass: 'inside', newClass: 'inside',
        oldBeats: ['territorial/ds.b01'], newBeats: ['territorial/ds.b02'], oldTriage: null, newTriage: null
      }] } },
      brokenGrants: [{ userId: '1', username: 'beat.guard', unitPath: 'ds.b01', role: 'field_staff' }],
      grantConfirmation: 'preview-confirmation'
    });
    expect(text()).toContain('territorial/ds.b01 → territorial/ds.b02');
    expect(text()).toContain('beat.guard');
    button('Publish layer')?.click();
    fixture.detectChanges();
    expect(button('Confirm and publish')?.disabled).toBe(true);
    (fixture.nativeElement.querySelector('[type=checkbox]') as HTMLInputElement).click();
    fixture.detectChanges();
    button('Confirm and publish')?.click();
    const publish = http.expectOne({ url: detail + '/publish', method: 'POST' });
    expect(publish.request.body).toEqual({ days: 30, grantConfirmation: 'preview-confirmation' });
    publish.flush({}, { status: 202, statusText: 'Accepted' });
    vi.advanceTimersByTime(0);
    http.expectOne(detail).flush({ ...ready, status: 'published', rematch: { status: 'running', processed: 5 } });
    fixture.detectChanges();
    expect(text()).toContain('5 checked so far.');
    vi.advanceTimersByTime(1000);
    http.expectOne(detail).flush({ ...ready, status: 'published', rematch: { status: 'completed', processed: 11, alertsCreated: 1 } });
    http.expectOne(root).flush({ layers: [], hierarchies: [], uploads: [] });
    fixture.detectChanges();
    expect(text()).toContain('Re-match complete. 11 detections checked.');
  });

  it('requires a fresh preview after a publish conflict', () => {
    openDraft(ready);
    runPreview(defaultPreview);
    button('Publish layer')?.click();
    fixture.detectChanges();
    button('Confirm and publish')?.click();
    http.expectOne(detail + '/publish').flush({ error: 'Grants changed. Run the preview again.' },
      { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    expect(text()).toContain('Grants changed.');
    expect(button('Publish layer')).toBeUndefined();
  });
});
