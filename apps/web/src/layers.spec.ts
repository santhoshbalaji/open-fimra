import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Layers } from './layers';
import { stubLayoutApis } from './overlay-testing';
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

  // Dialogs and select lists open in an overlay outside the component, so search the whole page.
  const button = (label: string): HTMLButtonElement | undefined =>
    [...document.querySelectorAll('button')].find(element => element.textContent.trim() === label);
  const text = () => document.body.textContent as string;
  function choose(trigger: string, option: string): void {
    (document.getElementById(trigger) as HTMLButtonElement).click();
    fixture.detectChanges();
    const item = [...document.querySelectorAll<HTMLElement>('hlm-select-item')].find(element => element.textContent.trim() === option);
    if (!item) throw new Error('No option ' + option);
    item.click();
    fixture.detectChanges();
  }

  beforeEach(() => {
    stubLayoutApis();
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
    choose('upload-choice', 'Beats · Version 2 · Ready for review');
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
    choose('layer-choice', 'Beats (beats)');
    expect(document.getElementById('layer-choice')?.textContent).toContain('Beats (beats)');
    expect(text()).toContain('Hierarchy boundaries');
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
    const row = fixture.nativeElement.querySelector('.problems tbody tr') as HTMLElement;
    expect(row.dataset['blocking']).toBe('true');
    expect(row.textContent).toContain('Blocks publishing');
    expect(row.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true');
    expect(text()).toContain('1 problem blocks publishing.');
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
    const counts = [...fixture.nativeElement.querySelectorAll('.preview-counts > div') as NodeListOf<HTMLElement>]
      .map(card => [card.querySelector('dt')?.textContent, card.querySelector('dd')?.textContent?.trim()]);
    expect(counts).toContainEqual(['Beat or class changed', '1']);
    button('Publish layer')?.click();
    fixture.detectChanges();
    const dialog = document.querySelector('[role=alertdialog]') as HTMLElement;
    expect(dialog.textContent).toContain('Confirm publication');
    expect(button('Confirm and publish')?.disabled).toBe(true);
    (dialog.querySelector('[role=checkbox]') as HTMLElement).click();
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

  it('closes the confirmation without publishing', () => {
    openDraft(ready);
    runPreview(defaultPreview);
    button('Publish layer')?.click();
    fixture.detectChanges();
    expect(document.querySelector('[role=alertdialog]')).not.toBeNull();
    button('Cancel')?.click();
    fixture.detectChanges();
    expect(fixture.componentInstance.confirming()).toBe(false);
    expect(button('Publish layer')).toBeDefined();
  });

  it('asks for a hierarchy and level before uploading a new hierarchy layer', () => {
    choose('layer-role', 'Hierarchy boundaries');
    const uploadForm = fixture.nativeElement.querySelector('form') as HTMLFormElement;
    uploadForm.dispatchEvent(new Event('submit', { cancelable: true }));
    fixture.detectChanges();
    expect(text()).toContain('Choose a hierarchy.');
    expect(text()).toContain('Choose a level.');
    expect(document.getElementById('layer-hierarchy')?.getAttribute('aria-invalid')).toBe('true');
    choose('layer-hierarchy', 'Territorial');
    choose('layer-depth', 'Beat');
    expect(text()).not.toContain('Choose a level.');
    (uploadForm.querySelector('[name=layerName]') as HTMLInputElement).value = 'Beats';
    (uploadForm.querySelector('[name=layerSlug]') as HTMLInputElement).value = 'beats-new';
    uploadForm.dispatchEvent(new Event('submit', { cancelable: true }));
    const post = http.expectOne({ url: root, method: 'POST' });
    expect(post.request.body.get('hierarchy')).toBe('territorial');
    expect(JSON.parse(post.request.body.get('settings'))).toEqual({ depth: 4 });
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
