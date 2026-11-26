import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Component, signal, viewChild, type TemplateRef } from '@angular/core';
import { DeferBlockBehavior, DeferBlockState, TestBed } from '@angular/core/testing';
import { Router, provideRouter, withDisabledInitialNavigation } from '@angular/router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from './app';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ThemeService } from './theme';
import { WallDisplay } from './wall-display';
import { WorkspaceService } from './workspace';

@Component({
  template: `
    <ng-template #freshness><p>Updated just now</p></ng-template>
    <ng-template #primary><button type="button">Page action</button></ng-template>
  `
})
class Screen {
  readonly pageFreshness = viewChild<TemplateRef<unknown>>('freshness');
  readonly pagePrimary = viewChild<TemplateRef<unknown>>('primary');
}

describe('App shell', () => {
  function open(hasWorkspace = true) {
    vi.stubGlobal('matchMedia', () => ({ matches: false, addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {} }));
    const workspace = {
      current: signal(hasWorkspace ? {slug: 'demo', name: 'Demo'} : null),
      loaded: signal(true), failed: signal(false), canManageLayers: signal(false), canDesignForms: signal(false),
      workspaces: signal([]), jurisdictions: signal([['ds.c1.d1', 'Demo division']]),
      allowsScope: (path: string) => path === 'ds.c1.d1' || path.startsWith('ds.c1.d1.'),
      bootstrapAvailable: signal(false)
    };
    const auth = {
      user: signal({profile: {name: 'Demo Officer', email: 'officer@example.invalid'}}),
      error: signal(''), open: async () => undefined, signOut: vi.fn()
    };
    TestBed.configureTestingModule({
      deferBlockBehavior: DeferBlockBehavior.Manual,
      providers: [provideHttpClient(), provideHttpClientTesting(), provideRouter([{ path: 'monitor/map', component: Screen }], withDisabledInitialNavigation()),
        {provide: WEB_CONFIG, useValue: {apiBaseUrl: 'https://api.test'}},
        {provide: AuthService, useValue: auth},
        {provide: ThemeService, useValue: {theme: signal('light')}},
        {provide: WorkspaceService, useValue: workspace}]
    });
    // This suite checks shell states and controls. The route tests use the actual guards.
    vi.spyOn(TestBed.inject(Router), 'initialNavigation').mockImplementation(() => undefined);
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    return {fixture, auth, workspace, page: fixture.nativeElement as HTMLElement};
  }

  afterEach(() => {
    vi.unstubAllGlobals();
    localStorage.removeItem('fimra.lowBandwidth');
    localStorage.removeItem('fimra.wallDisplay');
  });

  it('renders a screen’s freshness and actions in the shared page header', async () => {
    const {fixture, page} = open();
    await TestBed.inject(Router).navigateByUrl('/monitor/map?workspace=demo');
    fixture.detectChanges();
    const header = page.querySelector('main > fimra-page-header');
    expect(header?.querySelector('h1')?.textContent).toBe('Map');
    expect(header?.textContent).toContain('Updated just now');
    expect(header?.textContent).toContain('Page action');
  });

  it('starts in normal display mode and applies a saved wall display choice', () => {
    const { fixture, page } = open();
    const wallDisplay = TestBed.inject(WallDisplay);
    expect(page.hasAttribute('data-wall-display')).toBe(false);

    wallDisplay.toggle();
    fixture.detectChanges();
    expect(page.hasAttribute('data-wall-display')).toBe(true);
    expect(localStorage.getItem('fimra.wallDisplay')).toBe('true');

    // A new browser load reads the saved choice.
    expect(new WallDisplay().enabled()).toBe(true);
    wallDisplay.toggle();
    fixture.detectChanges();
    expect(page.hasAttribute('data-wall-display')).toBe(false);
    expect(new WallDisplay().enabled()).toBe(false);
  });

  it('uses the wall display choice for this load when storage is blocked', () => {
    const blocked = () => { throw new DOMException('Storage is blocked', 'SecurityError'); };
    vi.stubGlobal('localStorage', { getItem: blocked, setItem: blocked });
    const wallDisplay = new WallDisplay();
    expect(wallDisplay.enabled()).toBe(false);
    wallDisplay.toggle();
    expect(wallDisplay.enabled()).toBe(true);
  });

  it('reads and saves the low-bandwidth preference through the labelled switch', async () => {
    localStorage.setItem('fimra.lowBandwidth', 'true');
    const {fixture, page} = open();
    await TestBed.inject(Router).navigateByUrl('/monitor/map?workspace=demo');
    fixture.detectChanges();
    expect(page.querySelector('button[role=switch]')).toBeNull();
    for (const block of await fixture.getDeferBlocks()) {
      if (!page.querySelector('button[role=switch]')) await block.render(DeferBlockState.Complete);
    }
    const toggle = page.querySelector<HTMLButtonElement>('button[role=switch]')!;
    expect(page.querySelector('label[for="' + toggle.id + '"]')?.textContent).toContain('Low bandwidth');
    expect(toggle.getAttribute('aria-checked')).toBe('true');
    toggle.click();
    fixture.detectChanges();
    expect(toggle.getAttribute('aria-checked')).toBe('false');
    expect(localStorage.getItem('fimra.lowBandwidth')).toBe('false');
  });

  it('resolves a legacy scope and offers only units that can be selected', async () => {
    const {fixture, page} = open();
    const router = TestBed.inject(Router);
    await router.navigateByUrl('/monitor/map?workspace=demo&hours=53&unitPath=ds.c1.d1.r1');
    fixture.detectChanges();
    for (const block of await fixture.getDeferBlocks()) await block.render(DeferBlockState.Complete);
    TestBed.inject(HttpTestingController).expectOne(request => request.url.endsWith('/situation-report/units')).flush({
      units: [
        { path: 'ds.c1.d1.r1', name: 'Demo Range', levelLabel: 'Range' },
        { path: 'ds.c2.d1', name: 'Other Division', levelLabel: 'Division' }
      ]
    });
    fixture.detectChanges();

    expect(page.querySelector<HTMLSelectElement>('#filter-period')?.value).toBe('custom');
    expect(page.querySelector<HTMLInputElement>('#filter-hours')?.value).toBe('53');
    const unit = page.querySelector<HTMLButtonElement>('.unit-filter-field button')!;
    expect(unit.textContent.trim()).toBe('Demo Range (Range)');
    unit.click();
    fixture.detectChanges();
    expect(page.querySelector('.unit-filter-results')?.textContent).toContain('Demo Range (Range)');
    expect(page.querySelector('.unit-filter-results')?.textContent).not.toContain('Other Division');
    expect(router.url).toContain('hours=53&unitPath=ds.c1.d1.r1');
  });

  it('explains restricted access and offers an account switch', () => {
    const {auth, page} = open(false);
    expect(page.querySelector('main h1')?.textContent).toBe('Restricted access');
    expect(page.querySelector('main')?.textContent).toContain('Signed in as Demo Officer (officer@example.invalid).');
    expect(page.querySelector('main')?.textContent).toContain('Ask your workspace administrator');
    expect(page.querySelector('router-outlet')).toBeNull();
    Array.from(page.querySelectorAll('button')).find(button => button.textContent.trim() === 'Switch account')!.click();
    expect(auth.signOut).toHaveBeenCalledWith(true);
  });

  it('includes the whole visible name in the account label', async () => {
    const {fixture, page} = open();
    for (const block of await fixture.getDeferBlocks()) await block.render(DeferBlockState.Complete);
    const account = page.querySelector('button.account-trigger')!;
    expect(account.getAttribute('aria-label')).toContain(account.querySelector('.user-name')?.textContent);
    expect(account.querySelector('.user-name')?.textContent).toBe('Demo Officer');
    expect(account.querySelector('.account-initials')?.textContent).toBe('DO');
  });
});
