import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';
import { Component, signal } from '@angular/core';
import { DeferBlockBehavior, DeferBlockState, TestBed } from '@angular/core/testing';
import { Router, provideRouter, withDisabledInitialNavigation } from '@angular/router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { App } from './app';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';

@Component({ template: '' })
class Screen {}

describe('App shell', () => {
  function open(hasWorkspace = true) {
    const workspace = {
      current: signal(hasWorkspace ? {slug: 'demo', name: 'Demo'} : null),
      loaded: signal(true), failed: signal(false), canManageLayers: signal(false), canDesignForms: signal(false),
      workspaces: signal([]), jurisdictions: signal([['ds.c1.d1', 'Demo division']]), bootstrapAvailable: signal(false)
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

  afterEach(() => localStorage.removeItem('fimra.lowBandwidth'));

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

  it('explains restricted access and offers an account switch', () => {
    const {auth, page} = open(false);
    expect(page.querySelector('main h1')?.textContent).toBe('Restricted access');
    expect(page.querySelector('main')?.textContent).toContain('Signed in as Demo Officer');
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
    expect(account.textContent?.trim()).toBe('Demo Officer');
  });
});
