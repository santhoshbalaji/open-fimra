import { provideHttpClient } from '@angular/common/http';
import { provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { DeferBlockBehavior, DeferBlockState, TestBed } from '@angular/core/testing';
import { afterEach, describe, expect, it } from 'vitest';
import { App } from './app';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';

describe('App shell', () => {
  function open() {
    const workspace = {
      current: signal<{slug: string; name: string} | null>({slug: 'demo', name: 'Demo'}),
      loaded: signal(true), canManageLayers: signal(true), canDesignForms: signal(true), setupPending: signal(false),
      bootstrapAvailable: signal(false)
    };
    TestBed.configureTestingModule({
      deferBlockBehavior: DeferBlockBehavior.Manual,
      providers: [provideHttpClient(), provideHttpClientTesting(),
        {provide: WEB_CONFIG, useValue: {apiBaseUrl: 'https://api.test'}},
        {provide: AuthService, useValue: {user: signal({profile: {name: 'Demo'}}), open: async () => undefined}},
        {provide: ThemeService, useValue: {theme: signal('light')}},
        {provide: WorkspaceService, useValue: workspace}]
    });
    const fixture = TestBed.createComponent(App);
    fixture.detectChanges();
    return {fixture, workspace, page: fixture.nativeElement as HTMLElement};
  }

  afterEach(() => localStorage.removeItem('fimra.lowBandwidth'));

  it('reads and saves the low-bandwidth preference through the labelled switch', async () => {
    localStorage.setItem('fimra.lowBandwidth', 'true');
    const {fixture, page} = open();
    expect(page.querySelector('button[role=switch]')).toBeNull();
    // The filters are the first deferred block, ahead of the main screen.
    await (await fixture.getDeferBlocks())[0].render(DeferBlockState.Complete);
    const toggle = page.querySelector<HTMLButtonElement>('button[role=switch]')!;
    expect(page.querySelector('label[for="' + toggle.id + '"]')?.textContent).toContain('Low bandwidth');
    expect(toggle.getAttribute('aria-checked')).toBe('true');
    toggle.click();
    fixture.detectChanges();
    expect(toggle.getAttribute('aria-checked')).toBe('false');
    expect(localStorage.getItem('fimra.lowBandwidth')).toBe('false');
  });

  it.each(['Layers', 'Forms', 'First-run setup', 'Set up Open FIMRA'])('loads %s on entry', async heading => {
    const {fixture, workspace, page} = open();
    if (heading === 'Layers' || heading === 'Forms') {
      Array.from(page.querySelectorAll('button')).find(button => button.textContent.trim() === heading)!.click();
    } else if (heading === 'First-run setup') {
      workspace.setupPending.set(true);
    } else {
      workspace.current.set(null);
      workspace.bootstrapAvailable.set(true);
    }
    fixture.detectChanges();
    expect(page.querySelector('main h2')).toBeNull();
    for (const block of await fixture.getDeferBlocks()) await block.render(DeferBlockState.Complete);
    expect(page.querySelector('main h2')?.textContent).toBe(heading);
  });
});
