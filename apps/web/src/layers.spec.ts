import { InteractivityChecker } from '@angular/cdk/a11y';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { ComponentFixture, DeferBlockBehavior, TestBed } from '@angular/core/testing';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
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
const previous = {
  versionId: 'prior-version', layerSlug: 'beats', layerName: 'Beats', version: 1, depth: 4,
  sourceFile: 'older-beats.geojson', status: 'published', hasImport: false, blocksPublish: false
};
const latest = { ...previous, versionId: version, version: 2, sourceFile: 'beats.geojson', status: 'ready', hasImport: true };
const row = {
  slug: 'beats', name: 'Beats', role: 'hierarchy', hierarchy: 'territorial', depth: 4,
  published: previous, draft: latest, previous: [], lastChangedAt: '2026-11-25T10:30:00Z'
};
const catalog = {
  layers: [row], hierarchies: [{ slug: 'territorial', name: 'Territorial', levels: [{ depth: 4, name: 'Beat' }] }],
  uploads: [latest]
};
const defaultPreview = {
  days: 30, scanned: 11, blocked: '', brokenGrants: [], grantConfirmation: '', changes: {},
  previous: [{ layerName: 'Beats', version: 1 }]
};

describe('Layers', () => {
  let fixture: ComponentFixture<Layers>;
  let http: HttpTestingController;

  const button = (label: string): HTMLButtonElement | undefined =>
    [...document.querySelectorAll('button')].find(element => element.textContent.trim() === label);
  const text = () => document.body.textContent as string;

  beforeEach(() => {
    stubLayoutApis();
    vi.useFakeTimers();
    TestBed.configureTestingModule({ imports: [Layers], deferBlockBehavior: DeferBlockBehavior.Manual, providers: [
      provideHttpClient(), provideHttpClientTesting(), provideSpartanHlm(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo', name: 'Demo State' }) } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
    vi.spyOn(TestBed.inject(InteractivityChecker), 'isVisible').mockReturnValue(true);
    fixture = TestBed.createComponent(Layers);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    http.expectOne(root).flush(catalog);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  function openDraft(draft: object = ready): void {
    button('Review draft')!.click();
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    http.expectOne(detail).flush(draft);
    fixture.detectChanges();
  }

  function runPreview(data: object = defaultPreview): void {
    const preview = fixture.nativeElement.querySelector('.window-form') as HTMLFormElement;
    preview.dispatchEvent(new Event('submit', { cancelable: true }));
    http.expectOne(detail + '/preview?days=30').flush(data);
    fixture.detectChanges();
  }

  it('shows published and draft versions, status, last change and next action in the catalog', () => {
    const row = fixture.nativeElement.querySelector('table tbody tr') as HTMLElement;
    expect([...row.querySelectorAll('th, td')].map(cell => cell.textContent.trim())).toEqual([
      'Beats', 'Hierarchy boundaries', 'Version 1', 'Version 2', 'Checks passed', expect.stringContaining('25 Nov 2026'), 'Review draft'
    ]);
    expect(row.querySelector('fimra-layer-status svg')?.getAttribute('aria-hidden')).toBe('true');
    expect(row.querySelector('time')?.dateTime).toBe('2026-11-25T10:30:00Z');
  });

  it('uploads a replacement with the selected layer and resumes its check after closing', () => {
    openDraft();
    button('Upload new version')!.click();
    fixture.detectChanges();
    const uploadForm = fixture.nativeElement.querySelector('.upload-form') as HTMLFormElement;
    (uploadForm.querySelector('[name=nameField]') as HTMLInputElement).value = 'label';
    (uploadForm.querySelector('[name=parentCodeField]') as HTMLInputElement).value = 'parent';
    uploadForm.dispatchEvent(new Event('submit', { cancelable: true }));
    const post = http.expectOne({ url: root, method: 'POST' });
    expect(post.request.body.get('layerSlug')).toBe('beats');
    expect(post.request.body.get('hierarchy')).toBe('territorial');
    expect(JSON.parse(post.request.body.get('settings'))).toEqual({ depth: 4, nameField: 'label', parentCodeField: 'parent' });
    post.flush({ ...ready, status: 'pending' });
    const pending = { ...latest, status: 'pending' };
    http.expectOne(root).flush({ ...catalog, layers: [{ ...row, draft: pending }], uploads: [pending] });
    fixture.detectChanges();
    expect(text()).toContain('The file is being checked.');
    button('Close')!.click();
    fixture.detectChanges();
    button('View progress')!.click();
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    http.expectOne(detail).flush(ready);
    http.expectOne(root).flush(catalog);
    fixture.detectChanges();
    expect(button('Run preview')).toBeDefined();
  });

  it('links each blocker to its reason and repair guidance and offers a replacement', () => {
    openDraft({ ...ready, problems: [
      { kind: 'missing_code', featureRef: 'feature:3', message: 'This unit has no code.', blocksPublish: true },
      { kind: 'missing_name', featureRef: 'feature:7', message: 'This unit has no name.', blocksPublish: true }
    ] });
    const links = [...document.querySelectorAll<HTMLAnchorElement>('fimra-field-error-summary a')];
    expect(links).toHaveLength(2);
    for (const [index, label] of ['code', 'name'].entries()) {
      links[index].click();
      const detail = document.getElementById(links[index].hash.slice(1));
      expect(document.activeElement).toBe(detail);
      expect(detail?.textContent).toContain('This unit has no ' + label);
      expect(detail?.textContent).toContain('Fill in the ' + label);
    }
    expect(button('Run preview')).toBeUndefined();
    button('Replace file')!.click();
    fixture.detectChanges();
    expect(document.activeElement?.id).toBe('layer-file');
  });

  it('confirms the scoped replacement and returns focus to Publish when canceled', async () => {
    openDraft();
    runPreview();
    const publish = button('Publish layer')!;
    publish.focus();
    publish.click();
    TestBed.tick();
    const dialog = document.querySelector('[role=alertdialog]')!;
    expect(dialog.textContent).toContain('Publish version 2?');
    expect(dialog.textContent).toContain('Demo State · Territorial · Beat');
    expect(dialog.textContent).toContain('replace Beats version 1 in this scope');
    expect(document.activeElement).toBe(button('Cancel'));
    button('Cancel')!.click();
    await vi.advanceTimersByTimeAsync(300);
    TestBed.tick();
    expect(document.querySelector('[role=alertdialog]')).toBeNull();
    expect(document.activeElement).toBe(publish);
    http.expectNone(detail + '/publish');
  });

  it('requires approval for lost grants and shows publication, previous version and replay result', async () => {
    openDraft();
    runPreview({
      ...defaultPreview,
      brokenGrants: [{ userId: '1', username: 'beat.guard', unitPath: 'ds.b01', role: 'field_staff' }],
      grantConfirmation: 'preview-confirmation'
    });
    expect(text()).toContain('beat.guard');
    button('Publish layer')!.click();
    fixture.detectChanges();
    expect(document.querySelector('[role=alertdialog]')).toBeNull();
    document.querySelector<HTMLElement>('[role=checkbox]')!.click();
    fixture.detectChanges();
    button('Publish layer')!.click();
    TestBed.tick();
    expect(text()).toContain('The grant you reviewed will be removed.');
    button('Confirm and publish')!.click();
    await vi.advanceTimersByTimeAsync(300);
    TestBed.tick();
    const publish = http.expectOne({ url: detail + '/publish', method: 'POST' });
    expect(publish.request.body).toEqual({ days: 30, grantConfirmation: 'preview-confirmation' });
    publish.flush({ status: 'published' }, { status: 202, statusText: 'Accepted' });
    const published = { ...latest, status: 'published' };
    http.expectOne(root).flush({
      ...catalog, layers: [{ ...row, draft: null, published, previous: [{ ...previous, status: 'retired' }] }],
      uploads: [published]
    });
    fixture.detectChanges();
    expect(text()).toContain('Checking past detections is queued.');
    vi.advanceTimersByTime(1000);
    http.expectOne(detail).flush({ ...ready, status: 'published', rematch: { status: 'completed', processed: 11, alertsCreated: 1 } });
    fixture.detectChanges();
    expect(text()).toContain('11 detections checked.');
    expect(text()).toContain('Previous in this scope');
    expect(text()).toContain('Beats version 1');
  });

  it('requires a fresh preview after a publish conflict', async () => {
    openDraft();
    runPreview();
    button('Publish layer')!.click();
    TestBed.tick();
    button('Confirm and publish')!.click();
    await vi.advanceTimersByTimeAsync(300);
    TestBed.tick();
    http.expectOne(detail + '/publish').flush({ error: 'Grants changed. Run the preview again.' },
      { status: 409, statusText: 'Conflict' });
    fixture.detectChanges();
    expect(text()).toContain('Grants changed.');
    expect(button('Publish layer')).toBeUndefined();
  });
});
