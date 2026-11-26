import { InteractivityChecker } from '@angular/cdk/a11y';
import { NgTemplateOutlet } from '@angular/common';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Component, signal, viewChild } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import firstReport from '../../../../packages/form-schema/examples/first-report.json';
import { AuthService } from '../auth';
import { WEB_CONFIG } from '../config';
import { stubLayoutApis } from '../overlay-testing';
import { Role, WorkspaceService } from '../workspace';
import { FormSummary, FormsScreen, VersionDetail, VersionSummary } from './forms-screen';

const base = 'https://api.test/api/v1/workspaces/demo/forms';
const national = structuredClone(firstReport) as unknown as VersionDetail['definition'];
const template = { allowExtension: true, locked: ['summary', 'fire'] };
const nationalVersion: VersionSummary = {
  form: 'first-report', version: 1, title: national.title, level: 'national', unit: null, status: 'published',
  createdAt: '2026-11-01T10:00:00Z', updatedAt: '2026-11-01T10:00:00Z', publishedAt: '2026-11-01T10:00:00Z',
  extends: null, template, canManage: true, submissionCount: 1
};
const nationalForm: FormSummary = { form: 'first-report', level: 'national', unit: null, canManage: true, versions: [nationalVersion], assignments: [] };

@Component({
  imports: [FormsScreen, NgTemplateOutlet],
  template: `
    <header>
      <ng-container [ngTemplateOutlet]="screen()?.pageSecondary() ?? null" />
      <ng-container [ngTemplateOutlet]="screen()?.pagePrimary() ?? null" />
    </header>
    <fimra-forms-screen />
  `
})
class FormsHost {
  readonly screen = viewChild(FormsScreen);
}

describe('FormsScreen', () => {
  let fixture: ComponentFixture<FormsHost>;
  let http: HttpTestingController;
  const roles = signal<Role[]>([{ role: 'state_admin', hierarchy: 'territorial', path: 'ds' }]);
  const admin = signal(true);
  const root = () => fixture.nativeElement as HTMLElement;
  const button = (label: string) => [...root().querySelectorAll('button')].find(b => b.textContent?.trim() === label || b.getAttribute('aria-label') === label);
  const click = (label: string) => { const b = button(label); expect(b, label).toBeTruthy(); b!.click(); fixture.detectChanges(); };
  const type = (selector: string, value: string) => { root().querySelector<HTMLInputElement>(selector)!.value = value; };

  beforeEach(() => {
    stubLayoutApis();
    admin.set(true);
    roles.set([{ role: 'state_admin', hierarchy: 'territorial', path: 'ds' }]);
    TestBed.configureTestingModule({ imports: [FormsHost], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      provideSpartanHlm(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo', name: 'Demo State' }), canManageLayers: admin, roles } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test/' } }
    ] });
    vi.spyOn(TestBed.inject(InteractivityChecker), 'isVisible').mockReturnValue(true);
    fixture = TestBed.createComponent(FormsHost);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    vi.restoreAllMocks();
  });

  it('lists forms by level with how many reports each version has', () => {
    http.expectOne(base).flush([nationalForm]);
    fixture.detectChanges();
    const headings = [...root().querySelectorAll('h2')].map(h => h.textContent?.trim());
    expect(headings).toEqual(['National templates', 'State forms', 'Division forms']);
    expect(root().querySelector('.version-row')!.textContent).toContain('1 report');
    expect(root().querySelector('.version-row time')!.textContent).toContain('3:30 pm IST');
    expect(root().querySelector('.version-row fimra-status-badge svg[data-icon=circle-check]')).toBeTruthy();
    expect(button('Extend version 1 of First report')).toBeTruthy();
    expect(root().textContent).toContain('No state forms yet.');
  });

  it('extends a national template, saves the draft and publishes through the portal dialog', async () => {
    http.expectOne(base).flush([nationalForm]);
    fixture.detectChanges();
    click('Extend version 1 of First report');
    http.expectOne(base + '/first-report/versions/1').flush({ ...nationalVersion, definition: national });
    fixture.detectChanges();
    type('#start-id', 'first-report');
    type('#start-title', 'Demo State first report');
    click('Open in the designer');
    expect(root().textContent).toContain('The id "first-report" is already used by a form in this workspace.');
    type('#start-id', 'demo-first-report');
    click('Open in the designer');
    expect(root().querySelector('h2')!.textContent).toContain('Demo State first report');
    expect(root().textContent).toContain('Extends first-report version 1');
    expect(root().querySelector('fimra-status-badge svg[data-icon=file-pen-line]')).toBeTruthy();
    // The locked summary cannot be changed; a new section can be added.
    expect(root().querySelector('#form-row-0')!.textContent).toContain('Locked by template');
    click('Section');
    click('Save as draft');
    const saved = http.expectOne(base);
    expect(saved.request.method).toBe('POST');
    expect(saved.request.body).toMatchObject({ level: 'state', extends: { form: 'first-report', version: 1 },
      template: { allowExtension: false, locked: ['summary', 'fire'] } });
    expect(saved.request.body.definition.sections.map((s: { key: string }) => s.key)).toEqual(['summary', 'fire', 'response', 'sign_off', 'section_1']);
    saved.flush({ ...nationalVersion, form: 'demo-first-report', level: 'state', status: 'draft', template: { allowExtension: false, locked: ['summary', 'fire'] },
      extends: { form: 'first-report', version: 1 } });
    http.expectOne(base).flush([nationalForm]);
    fixture.detectChanges();
    const publish = button('Publish…')!;
    const confirmPublish = vi.spyOn(fixture.componentInstance.screen()!, 'confirmPublish');
    publish.focus();
    click('Publish…');
    TestBed.tick();
    const dialog = document.querySelector('[role=alertdialog]')!;
    expect(root().contains(dialog)).toBe(false);
    expect(dialog.textContent).toContain('Publish version 1?');
    expect(dialog.textContent).toContain('Reports already filled in keep the version they used.');
    const cancel = dialog.querySelector('button')!;
    expect(document.activeElement).toBe(cancel);
    cancel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    TestBed.tick();
    await confirmPublish.mock.results[0].value;
    expect(document.querySelector('[role=alertdialog]')).toBeNull();
    expect(document.activeElement).toBe(publish);
    http.expectNone(base + '/demo-first-report/versions/1/publish');

    click('Publish…');
    TestBed.tick();
    document.querySelectorAll<HTMLButtonElement>('[role=alertdialog] button')[1].click();
    TestBed.tick();
    await confirmPublish.mock.results[1].value;
    http.expectOne(base + '/demo-first-report/versions/1/publish').flush({ ...nationalVersion, form: 'demo-first-report', level: 'state' });
    http.expectOne(base).flush([nationalForm]);
    fixture.detectChanges();
    expect(root().textContent).toContain('Published versions never change.');
    expect(root().querySelector('fimra-status-badge svg[data-icon=circle-check]')).toBeTruthy();
    expect(button('Publish…')).toBeUndefined();
  });

  it('shows the server problems when it refuses a draft', () => {
    http.expectOne(base).flush([]);
    fixture.detectChanges();
    click('New state form');
    type('#start-id', 'demo-patrol');
    type('#start-title', 'Demo patrol');
    click('Open in the designer');
    click('Save as draft');
    http.expectOne(base).flush({ error: 'The form changes locked parts of first-report version 1.',
      problems: ['Section "summary" is locked by the template it extends and cannot change.'] }, { status: 422, statusText: 'Unprocessable Entity' });
    fixture.detectChanges();
    expect(root().textContent).toContain('The form changes locked parts of first-report version 1.');
    expect(root().querySelector('.designer-problems')!.textContent).toContain('Section "summary" is locked by the template it extends and cannot change.');
  });

  it('opens an earlier version read-only, with its reports', () => {
    http.expectOne(base).flush([nationalForm]);
    fixture.detectChanges();
    click('View version 1 of First report');
    http.expectOne(base + '/first-report/versions/1').flush({ ...nationalVersion, definition: national });
    fixture.detectChanges();
    expect(root().textContent).toContain('1 report filled in on this version');
    expect(button('Save draft')).toBeUndefined();
    click('Back to forms');
    expect(root().querySelector('h2')!.textContent).toContain('National templates');
  });

  it('keeps unsaved changes on cancel and returns to the list on discard', async () => {
    http.expectOne(base).flush([]);
    fixture.detectChanges();
    click('New state form');
    type('#start-id', 'demo-patrol');
    type('#start-title', 'Demo patrol');
    click('Open in the designer');
    const screen = fixture.componentInstance.screen()!;
    const close = vi.spyOn(screen, 'closeDesigner');
    const back = button('Back to forms')!;
    back.focus();
    click('Back to forms');
    TestBed.tick();
    const dialog = () => document.querySelector('[role=alertdialog]')!;
    expect(dialog().textContent).toContain('Leave without saving?');
    const keep = dialog().querySelector('button')!;
    expect(keep.textContent).toContain('Keep editing');
    expect(document.activeElement).toBe(keep);
    keep.click();
    TestBed.tick();
    await close.mock.results[0].value;
    expect(screen.dirty()).toBe(true);
    expect(document.activeElement).toBe(back);

    click('Back to forms');
    TestBed.tick();
    const discard = dialog().querySelectorAll('button')[1];
    expect(discard.textContent).toContain('Discard changes');
    discard.click();
    TestBed.tick();
    await close.mock.results[1].value;
    fixture.detectChanges();
    expect(screen.editing()).toBeNull();
    expect(root().textContent).toContain('National templates');
  });

  it('adds a national template from a file', () => {
    http.expectOne(base).flush([]);
    fixture.detectChanges();
    click('Add a national template');
    const text = root().querySelector<HTMLTextAreaElement>('#import-text')!;
    text.value = JSON.stringify({ level: 'national', template, definition: national });
    text.dispatchEvent(new Event('input'));
    fixture.detectChanges();
    click('Add template');
    const added = http.expectOne(base);
    expect(added.request.body).toEqual({ level: 'national', template, definition: national });
    added.flush(nationalVersion);
    http.expectOne(base).flush([nationalForm]);
  });

  it('lets a division officer extend a state form for their division', () => {
    admin.set(false);
    roles.set([{ role: 'division_officer', hierarchy: 'territorial', path: 'ds.c1.d1' }]);
    const title = { en: 'Demo State first report' };
    const state: VersionSummary = { ...nationalVersion, form: 'demo-first-report', title, level: 'state', submissionCount: undefined,
      extends: { form: 'first-report', version: 1 } };
    http.expectOne(base).flush([nationalForm, { ...nationalForm, form: 'demo-first-report', level: 'state', canManage: false, versions: [state] }]);
    fixture.detectChanges();
    expect(button('Extend version 1 of First report')).toBeUndefined();
    expect(button('New state form')).toBeUndefined();
    click('Extend version 1 of Demo State first report');
    http.expectOne(base + '/demo-first-report/versions/1').flush({ ...state, definition: { ...national, id: 'demo-first-report', title } });
    fixture.detectChanges();
    expect(root().querySelector<HTMLSelectElement>('#start-unit')!.value).toBe('ds.c1.d1');
    type('#start-id', 'hill-first-report');
    click('Open in the designer');
    expect(root().textContent).toContain('For ds.c1.d1');
    click('Section');
    click('Save as draft');
    const saved = http.expectOne(base);
    expect(saved.request.body).toMatchObject({ level: 'division', hierarchy: 'territorial', unitPath: 'ds.c1.d1',
      extends: { form: 'demo-first-report', version: 1 } });
    saved.flush({ ...state, form: 'hill-first-report', level: 'division', status: 'draft' });
    http.expectOne(base).flush([]);
  });
});
