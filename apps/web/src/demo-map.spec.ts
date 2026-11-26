import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { DemoMap } from './demo-map';
import { stubLayoutApis } from './overlay-testing';
import { ShellNavigation } from './shell-navigation';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';

const { handlers, addSource } = vi.hoisted(() => ({
  handlers: new Map<string, (event: unknown) => void>(),
  addSource: vi.fn()
}));
vi.mock('maplibre-gl', () => ({
  Map: class {
    readonly canvas = document.createElement('canvas');
    getCanvas() { return this.canvas; }
    getSource(id: string) { return id === 'reference' ? {} : undefined; }
    on(name: string, handler: (event: unknown) => void) { handlers.set(name, handler); }
    once() { /* The map need not draw in jsdom. */ }
    remove() { handlers.clear(); }
    getLayer() { return undefined; }
    setPaintProperty() { /* No canvas to paint in jsdom. */ }
    removeSource() { /* Keep the test focused on the replacement source. */ }
    addLayer() { /* No canvas to paint in jsdom. */ }
    addSource = addSource;
  },
  setWorkerUrl: vi.fn()
}));

describe('Detection map', () => {
  const endpoint = 'https://api.test/api/v1/workspaces/demo';
  beforeEach(() => {
    stubLayoutApis();
    vi.useFakeTimers();
    addSource.mockClear();
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(),
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: ShellNavigation, useValue: { path: signal('/monitor/map'), url: signal(''), query: signal(new URLSearchParams()) } },
      { provide: ThemeService, useValue: { theme: signal('light') } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }), message: signal('') } }
    ] });
  });
  afterEach(() => vi.useRealTimers());

  function render() {
    const fixture = TestBed.createComponent(DemoMap);
    fixture.detectChanges();
    const http = TestBed.inject(HttpTestingController);
    http.expectOne(req => req.url === endpoint + '/alerts').flush({
      counts: { unit: 1 }, canReview: false, items: [{
        id: '14', detection_id: '99', forest_class: 'inside', queue: 'unit', step_label: 'New',
        step_time: '2026-03-15T10:00:00Z', observed_at: 1773568800, places: { beat: { name: 'Beat 05' } }
      }]
    });
    vi.advanceTimersByTime(0);
    return { fixture, page: fixture.nativeElement as HTMLElement, http };
  }

  it('names the failed reference layers and retries their tiles', () => {
    const { fixture, page, http } = render();
    http.expectOne(endpoint).flush({ version: '1', levels: [] });
    handlers.get('error')!({ sourceId: 'reference', error: { status: 503, message: 'Unavailable' } });
    fixture.detectChanges();
    expect(page.querySelector('fimra-state-region')?.textContent).toContain(
      'Could not load reference layers (boundaries, forest and exclusions).');
    const button = page.querySelector<HTMLButtonElement>('fimra-state-region button')!;
    expect(button.textContent?.trim()).toBe('Retry');
    button.click();
    fixture.detectChanges();
    expect(addSource).toHaveBeenCalledWith('reference', {
      type: 'vector', tiles: [endpoint + '/tiles/reference/{z}/{x}/{y}.mvt?v=1']
    });
    expect(page.querySelector('fimra-state-region button')).toBeNull();
    http.verify();
  });

  it('retries workspace geography and distinguishes it from fire detections', () => {
    const { fixture, page, http } = render();
    http.expectOne(endpoint).flush({}, { status: 503, statusText: 'Unavailable' });
    handlers.get('error')!({ sourceId: 'detections', error: { status: 503, message: 'Unavailable' } });
    fixture.detectChanges();
    const message = page.querySelector('fimra-state-region')?.textContent;
    expect(message).toContain('workspace boundary and map hierarchy');
    expect(message).toContain('fire detections layer');
    page.querySelector<HTMLButtonElement>('fimra-state-region button')!.click();
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    http.expectOne(endpoint).flush({ version: '1', levels: [] });
    http.verify();
  });

  it('names the row button for the place and alert ID, with forest class below', () => {
    const { fixture, page, http } = render();
    http.expectOne(endpoint).flush({ version: '1', levels: [] });
    fixture.detectChanges();
    const button = page.querySelector('.row-open')!;
    expect(button.textContent?.trim()).toBe('Beat 05 · Alert #14');
    expect(button.getAttribute('aria-label')).toBeNull();
    expect(button.nextElementSibling?.textContent).toBe('Inside forest');
    http.verify();
  });
});
