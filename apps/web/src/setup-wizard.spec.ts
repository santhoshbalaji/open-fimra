import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { stubLayoutApis } from './overlay-testing';
import { WEB_CONFIG } from './config';
import { SetupLayer, SetupState, SetupWizard } from './setup-wizard';
import { WorkspaceService } from './workspace';

const base = 'https://api.test/api/v1/workspaces/demo-state';
const labels = (en: string, hi: string) => ({ en, hi });
const presets = [
  { slug: 'circle-division-range-beat', name: 'State → Circle → Division → Range → Beat', levels: [
    { slug: 'state', labels: labels('State', 'राज्य') }, { slug: 'circle', labels: labels('Circle', 'वृत्त') },
    { slug: 'division', labels: labels('Division', 'प्रभाग') }, { slug: 'range', labels: labels('Range', 'रेंज') },
    { slug: 'beat', labels: labels('Beat', 'बीट') }] },
  { slug: 'circle-division-subdivision-range-section-beat', name: 'State → Circle → Division → Sub-division → Range → Section → Beat',
    levels: [{ slug: 'state', labels: labels('State', 'राज्य') }, { slug: 'sub_division', labels: labels('Sub-division', 'उप-प्रभाग') }] }
];
const layer = (key: string, name: string, role: SetupLayer['role'], published: boolean, depth?: number): SetupLayer =>
  ({ key, slug: key === 'forest' ? 'forest' : 'territorial-' + name.toLowerCase(), name, role, depth, required: role !== 'exclusion', published, latest: null });

function setupState(overrides: Partial<SetupState> = {}): SetupState {
  return {
    workspace: { slug: 'demo-state', name: 'Demo State' }, mode: 'state', preset: null, hierarchy: 'territorial',
    root: { code: 'DS', path: 'ds', published: false }, boundarySet: false,
    levels: [{ depth: 0, slug: 'state', labels: labels('State', 'राज्य'), units: 0 }],
    layers: [layer('level:0', 'State', 'hierarchy', false, 0), layer('forest', 'Forest', 'forest', false)],
    steps: { mode: { status: 'done', updatedAt: '' }, workspace: { status: 'done', updatedAt: '' } },
    firms: { set: false, source: 'none', encryption: true }, emailEnabled: true, finishedAt: null, presets,
    ...overrides
  };
}

describe('SetupWizard', () => {
  let fixture: ComponentFixture<SetupWizard>;
  let http: HttpTestingController;
  const text = () => fixture.nativeElement.textContent as string;
  const button = (label: string) => [...fixture.nativeElement.querySelectorAll('button') as NodeListOf<HTMLButtonElement>]
    .find(element => element.textContent.trim() === label);
  function choose(trigger: string, option: string): void {
    (document.getElementById(trigger) as HTMLButtonElement).click();
    fixture.detectChanges();
    const item = [...document.querySelectorAll<HTMLElement>('hlm-select-item')].find(element => element.textContent.trim() === option);
    if (!item) throw new Error('No option ' + option);
    item.click();
    fixture.detectChanges();
  }
  const heading = () => (fixture.nativeElement.querySelector('#step-heading') as HTMLElement).textContent.trim();

  function open(state: SetupState): void {
    fixture = TestBed.createComponent(SetupWizard);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    http.expectOne(base + '/setup').flush(state);
    fixture.detectChanges();
  }

  beforeEach(() => {
    stubLayoutApis();
    vi.useFakeTimers();
    TestBed.configureTestingModule({ imports: [SetupWizard], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo-state', name: 'Demo State' }) } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    vi.useRealTimers();
  });

  it('opens at the first unfinished step and lists every step with its status', () => {
    open(setupState());
    expect(heading()).toBe('Hierarchy');
    const items = [...fixture.nativeElement.querySelectorAll('.setup-steps button')].map(item => (item as HTMLElement).getAttribute('aria-label'));
    expect(items).toEqual(['1. Mode, Done', '2. Workspace, Done', '3. Hierarchy, To do', '4. Layers, To do',
      '5. NASA FIRMS, To do', '6. Starter forms, To do', '7. Invite officers, To do', '8. Finish, To do']);
    const current = fixture.nativeElement.querySelector('[aria-current=step]') as HTMLElement;
    expect(current.textContent).toContain('Hierarchy');
    expect(current.textContent).toContain('To do · Current');
    const done = fixture.nativeElement.querySelector('.setup-steps button') as HTMLElement;
    expect(done.querySelector('svg')).not.toBeNull();
    expect(done.textContent).toContain('Done');
    (fixture.nativeElement.querySelector('.setup-steps button') as HTMLButtonElement).click();
    fixture.detectChanges();
    expect(text()).toContain('Phase 1 fully supports state mode only');
  });

  it('saves a preset with edited Hindi labels', () => {
    open(setupState());
    const radios = fixture.nativeElement.querySelectorAll('input[name=preset]') as NodeListOf<HTMLInputElement>;
    expect(radios).toHaveLength(2);
    expect(radios[0].checked).toBe(true);
    const hindi = fixture.nativeElement.querySelector('[aria-label="Hindi label for level 5"]') as HTMLInputElement;
    hindi.value = ' बीट (वन) ';
    hindi.dispatchEvent(new Event('input'));
    radios[1].click();
    fixture.detectChanges();
    expect(fixture.componentInstance.presetSlug()).toBe('circle-division-subdivision-range-section-beat');
    radios[0].click();
    fixture.detectChanges();
    const edited = fixture.nativeElement.querySelector('[aria-label="Hindi label for level 5"]') as HTMLInputElement;
    edited.value = ' बीट (वन) ';
    edited.dispatchEvent(new Event('input'));
    button('Save hierarchy')?.click();
    const request = http.expectOne(base + '/setup/hierarchy');
    expect(request.request.body.preset).toBe('circle-division-range-beat');
    expect(request.request.body.levels).toHaveLength(5);
    expect(request.request.body.levels[4]).toEqual({ slug: 'beat', labels: { en: 'Beat', hi: 'बीट (वन)' } });
    request.flush(setupState({ steps: { ...setupState().steps, hierarchy: { status: 'done', updatedAt: '' } } }));
    fixture.detectChanges();
    expect(heading()).toBe('Layers');
  });

  it('uploads a level with the shared field names, then publishes it after a preview', () => {
    open(setupState({ steps: { ...setupState().steps, hierarchy: { status: 'done', updatedAt: '' } } }));
    expect(heading()).toBe('Layers');
    expect(text()).toContain('Publish the levels above first.');
    expect(button('Continue')?.disabled).toBe(true);
    const input = fixture.nativeElement.querySelector('#file-level\\:0') as HTMLInputElement;
    const file = new File(['{}'], 'state.geojson', { type: 'application/geo+json' });
    Object.defineProperty(input, 'files', { value: [file] });
    button('Upload')?.click();
    const upload = http.expectOne(base + '/layer-imports');
    const body = upload.request.body as FormData;
    expect(body.get('layerSlug')).toBe('territorial-state');
    expect(body.get('role')).toBe('hierarchy');
    expect(body.get('hierarchy')).toBe('territorial');
    expect(JSON.parse(String(body.get('settings')))).toEqual({ nameField: 'name', codeField: 'code', depth: 0 });
    upload.flush({ versionId: 'v1', status: 'pending', unitCount: 0, featureCount: 0, problems: [] });
    vi.advanceTimersByTime(1000);
    http.expectOne(base + '/layer-imports/v1').flush({ versionId: 'v1', status: 'ready', unitCount: 1, featureCount: 0, problems: [] });
    fixture.detectChanges();
    expect(text()).toContain('Ready to publish');
    button('Publish')?.click();
    http.expectOne(base + '/layer-imports/v1/preview').flush({ days: 30, blocked: '', grantConfirmation: '', brokenGrants: [] });
    const publish = http.expectOne(base + '/layer-imports/v1/publish');
    expect(publish.request.body).toEqual({ days: 30, grantConfirmation: '' });
    publish.flush({ status: 'published' });
    const published = setupState({ boundarySet: true,
      layers: [layer('level:0', 'State', 'hierarchy', true, 0), layer('forest', 'Forest', 'forest', true)] });
    http.expectOne(base + '/setup').flush(published);
    fixture.detectChanges();
    expect(text()).toContain('Published');
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('lists blocking problems and does not offer Publish', () => {
    open(setupState({ steps: { ...setupState().steps, hierarchy: { status: 'done', updatedAt: '' } },
      layers: [{ ...layer('level:0', 'State', 'hierarchy', false, 0), latest: { versionId: 'v2', version: 2, status: 'ready' } }] }));
    vi.advanceTimersByTime(0);
    http.expectOne(base + '/layer-imports/v2').flush({ versionId: 'v2', status: 'ready', unitCount: 1, featureCount: 0,
      problems: [{ kind: 'missing_code', featureRef: 'feature:1', message: 'This unit has no code.', blocksPublish: true }] });
    fixture.detectChanges();
    expect(text()).toContain('This unit has no code.');
    expect(button('Publish')).toBeUndefined();
  });

  it('saves the FIRMS key, clears the field and tests the connection', () => {
    open(setupState({ steps: { ...setupState().steps, hierarchy: { status: 'done', updatedAt: '' }, layers: { status: 'done', updatedAt: '' } } }));
    expect(heading()).toBe('NASA FIRMS');
    expect(button('Continue')?.disabled).toBe(true);
    const key = fixture.nativeElement.querySelector('input[type=password]') as HTMLInputElement;
    key.value = '0123456789abcdef0123456789abcdef';
    (key.form as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }));
    const save = http.expectOne(base + '/settings/firms');
    expect(save.request.method).toBe('PUT');
    expect(save.request.body).toEqual({ mapKey: '0123456789abcdef0123456789abcdef' });
    save.flush({ set: true, setAt: '2026-11-25T05:00:00Z', hint: 'cdef', source: 'settings', encryption: true });
    expect(key.value).toBe('');
    http.expectOne(base + '/settings/firms/test').flush({ ok: true, message: 'FIRMS answered.' });
    fixture.detectChanges();
    expect(text()).toContain('ending in cdef');
    expect(text()).not.toContain('0123456789abcdef0123456789abcdef');
    expect(text()).toContain('FIRMS answered.');
    expect(button('Continue')?.disabled).toBe(false);
  });

  it('warns before skipping FIRMS', () => {
    open(setupState({ steps: { ...setupState().steps, hierarchy: { status: 'done', updatedAt: '' }, layers: { status: 'done', updatedAt: '' } } }));
    button('Skip for now')?.click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[role=alert]').textContent).toContain('no new fire detections arrive');
    button('Skip anyway')?.click();
    const skip = http.expectOne(base + '/setup/steps/firms');
    expect(skip.request.body).toEqual({ status: 'skipped' });
    skip.flush(setupState());
  });

  it('shows starter forms as coming later and skips them', () => {
    const done = { status: 'done' as const, updatedAt: '' };
    open(setupState({ steps: { ...setupState().steps, hierarchy: done, layers: done, firms: done } }));
    expect(heading()).toBe('Starter forms');
    expect(text()).toContain('Coming later');
    expect((fixture.nativeElement.querySelector('.setup-step [role=switch]') as HTMLButtonElement).disabled).toBe(true);
    button('Continue')?.click();
    expect(http.expectOne(base + '/setup/steps/forms').request.body).toEqual({ status: 'skipped' });
  });

  it('invites an officer to a unit and lists masked addresses', () => {
    const done = { status: 'done' as const, updatedAt: '' };
    open(setupState({ steps: { ...setupState().steps, hierarchy: done, layers: done, firms: done, forms: { status: 'skipped', updatedAt: '' } } }));
    expect(heading()).toBe('Invite officers');
    http.expectOne(base + '/invitations').flush({ invitations: [] });
    http.expectOne(base + '/setup/units').flush({ units: [
      { depth: 0, code: 'DS', name: 'Demo State', path: 'ds' }, { depth: 1, code: 'C1', name: 'North Circle', path: 'ds.c1' }] });
    fixture.detectChanges();
    const form = fixture.nativeElement.querySelector('.invite-form') as HTMLFormElement;
    (form.querySelector('[name=email]') as HTMLInputElement).value = 'division.officer@example.invalid';
    expect(document.getElementById('invite-role')?.textContent).toContain('Field staff');
    choose('invite-role', 'Division officer');
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    fixture.detectChanges();
    expect(text()).toContain('Choose a unit.');
    expect(document.getElementById('invite-unit')?.getAttribute('aria-invalid')).toBe('true');
    choose('invite-unit', 'North Circle ( C1)');
    expect(document.getElementById('invite-unit')?.textContent?.startsWith('North Circle')).toBe(true);
    form.dispatchEvent(new Event('submit', { cancelable: true }));
    const invite = http.expectOne(base + '/invitations');
    expect(invite.request.method).toBe('POST');
    expect(invite.request.body).toEqual({ email: 'division.officer@example.invalid', role: 'division_officer', unitPath: 'ds.c1' });
    invite.flush({ id: '1', emailStatus: 'pending' }, { status: 201, statusText: 'Created' });
    http.expectOne(base + '/invitations').flush({ invitations: [{ id: '1', email: 'd…@example.invalid',
      role: 'division_officer', unitPath: 'ds.c1', unitName: 'North Circle', expiresAt: '2026-12-09T00:00:00Z', emailStatus: 'sent' }] });
    fixture.detectChanges();
    expect(text()).toContain('d…@example.invalid');
    expect(text()).toContain('Email sent');
    const row = fixture.nativeElement.querySelector('.invite-table tbody tr') as HTMLElement;
    expect(row.textContent).toContain('Division officer');
    expect(row.querySelector('button')?.getAttribute('aria-label')).toBe('Revoke invitation for d…@example.invalid');
    expect(text()).not.toContain('division.officer@example.invalid');
  });

  it('finishes and hands over to the alert map', () => {
    const done = { status: 'done' as const, updatedAt: '' };
    open(setupState({ steps: { ...setupState().steps, hierarchy: done, layers: done, firms: { status: 'skipped', updatedAt: '' },
      forms: { status: 'skipped', updatedAt: '' }, invitations: done } }));
    expect(heading()).toBe('Finish');
    expect(text()).toContain('No FIRMS key yet');
    const finished = vi.fn();
    fixture.componentInstance.finished.subscribe(finished);
    button('Finish and open the alert map')?.click();
    http.expectOne(base + '/setup/finish').flush(setupState({ finishedAt: '2026-11-25T05:00:00Z' }));
    expect(finished).toHaveBeenCalledOnce();
  });
});
