import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { convertToParamMap, provideRouter } from '@angular/router';
import { signal } from '@angular/core';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { DashboardFilters } from './dashboard-filters';
import { DemoMap } from './demo-map';
import { stubLayoutApis } from './overlay-testing';
import { ShellNavigation } from './shell-navigation';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';

const mocks = vi.hoisted(() => ({
  listeners: new Map<string, (event?: unknown) => void>(),
  layers: new Map<string, { id: string; layout?: Record<string, string> }>(),
  sources: new Map<string, { setData: ReturnType<typeof vi.fn>; setTiles: ReturnType<typeof vi.fn> }>(),
  addSource: vi.fn((id: string) => mocks.sources.set(id, { setData: vi.fn(), setTiles: vi.fn() })),
  removeSource: vi.fn((id: string) => mocks.sources.delete(id)),
  features: vi.fn(),
  zoom: 5
}));

vi.mock('maplibre-gl', () => ({
  Map: class {
    readonly canvas = document.createElement('canvas');
    getCanvas() { return this.canvas; }
    getSource(id: string) { return mocks.sources.get(id); }
    getLayer(id: string) { return mocks.layers.get(id); }
    addLayer(layer: { id: string }) { mocks.layers.set(layer.id, layer); }
    removeLayer(id: string) { mocks.layers.delete(id); }
    getLayoutProperty(id: string, key: string) { return mocks.layers.get(id)?.layout?.[key]; }
    setLayoutProperty(id: string, key: string, value: string) {
      const layer = mocks.layers.get(id)!;
      layer.layout = { ...layer.layout, [key]: value };
    }
    on(name: string, handler: () => void) { mocks.listeners.set(name, handler); }
    once(name: string, handler: () => void) { this.on(name, handler); }
    getCenter() { return { toArray: () => [81, 20] }; }
    getZoom() { return mocks.zoom; }
    fitBounds() {}
    jumpTo() {}
    resize() {}
    remove() {}
    setPaintProperty() {}
    hasImage() { return true; }
    addSource = mocks.addSource;
    removeSource = mocks.removeSource;
    queryRenderedFeatures = mocks.features;
  },
  setWorkerUrl: vi.fn()
}));

describe('Detection map', () => {
  const endpoint = 'https://api.test/api/v1/workspaces/demo';
  const query = signal(convertToParamMap({}));
  const update = vi.fn((params: Record<string, string>) => query.set(convertToParamMap(params)));
  const feature = (id: number) => ({
    properties: { id, forest_class: 'inside', observed_at: Math.floor(Date.now() / 1000) - 60 },
    geometry: { type: 'Point', coordinates: [81, 20] }
  });
  let fixture: ComponentFixture<DemoMap>;
  let html: HTMLElement;
  let http: HttpTestingController;

  beforeEach(() => {
    stubLayoutApis();
    vi.stubGlobal('matchMedia', vi.fn().mockImplementation(() => ({
      matches: false, addListener: vi.fn(), removeListener: vi.fn(),
      addEventListener: vi.fn(), removeEventListener: vi.fn()
    })));
    vi.useFakeTimers();
    TestBed.configureTestingModule({ providers: [
      provideHttpClient(), provideHttpClientTesting(), provideRouter([]),
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
      { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo', name: 'Demo State' }), message: signal(''), deny: vi.fn() } },
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: ThemeService, useValue: { theme: signal('light') } },
      { provide: ShellNavigation, useValue: { url: signal('/monitor/map'), path: signal('/monitor/map'), query, update } }
    ] });
    http = TestBed.inject(HttpTestingController);
    query.set(convertToParamMap({}));
    mocks.features.mockReturnValue([]);
    mocks.zoom = 5;
  });

  function render(geographyAvailable = true) {
    fixture = TestBed.createComponent(DemoMap);
    html = fixture.nativeElement;
    fixture.detectChanges();
    mocks.listeners.get('load')!();
    vi.advanceTimersByTime(0);
    const geography = http.expectOne(endpoint);
    if (geographyAvailable) geography.flush({
      version: 'test', boundary: [80, 19, 82, 21],
      levels: [
        { hierarchy: 'territorial', depth: 0, slug: 'state', labels: { en: 'Region' } },
        { hierarchy: 'territorial', depth: 2, slug: 'beat', labels: { en: 'Beat' } }
      ]
    });
    else geography.flush({}, { status: 503, statusText: 'Unavailable' });
    http.expectOne(req => req.params.has('queue')).flush({
      counts: { unit: 1 }, canReview: true, items: [{
        id: '14', detection_id: '99', forest_class: 'inside', queue: 'unit', step_label: 'New', overdue: true, next_action: 'Next step: Acknowledged',
        step_time: new Date().toISOString(), observed_at: Math.floor(Date.now() / 1000) - 60,
        places: { beat: { name: 'Beat 05' } }
      }]
    });
    fixture.detectChanges();
  }

  afterEach(() => {
    fixture?.destroy();
    mocks.layers.clear();
    mocks.sources.clear();
    mocks.listeners.clear();
    vi.clearAllMocks();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function toggle(label: string) {
    const item = Array.from(html.querySelectorAll<HTMLLabelElement>('.layer-options label'))
      .find(element => element.textContent?.includes(label))!;
    item.querySelector('input')!.click();
    fixture.detectChanges();
  }

  it('keeps the legend in step with map visibility, zoom and low bandwidth', () => {
    render();
    html.querySelector<HTMLButtonElement>('.map-legend button')!.click();
    fixture.detectChanges();
    const legend = html.querySelector('.layer-options section')!;
    expect(legend.textContent).toContain('Forest areas');
    expect(legend.textContent).not.toContain('Beat names');
    mocks.zoom = 11;
    mocks.listeners.get('zoom')!();
    fixture.detectChanges();
    expect(legend.textContent).toContain('Beat names');

    toggle('Forest areas');
    expect(legend.textContent).not.toContain('Forest areas');
    expect(mocks.layers.get('forest-edge')?.layout?.visibility).toBe('none');
    toggle('Reported incidents');
    expect(legend.textContent).toContain('Reported incidents');
    expect(html.querySelector('.map-key')?.textContent).toContain('Reported incident');
    expect(mocks.layers.get('reported-incidents')?.layout?.visibility).toBe('visible');
    toggle('Fire detections');
    expect(html.querySelector('.map-key [aria-label="Detection age"]')).toBeNull();
    expect(mocks.layers.get('detections')?.layout?.visibility).toBe('none');

    TestBed.inject(DashboardFilters).lowBandwidth.set(true);
    fixture.detectChanges();
    expect(legend.textContent).not.toContain('Beat names');
    expect(legend.textContent).toContain('Region boundaries');
    expect(mocks.layers.get('label-territorial-2')?.layout?.visibility).toBe('none');
    expect(mocks.addSource.mock.calls.filter(([id]) => id === 'reference')).toHaveLength(1);
    expect(mocks.sources.get('reference')?.setTiles).toHaveBeenCalledWith([expect.stringContaining('&outline=1')]);
  });

  it('selects the same detection from the picker and the map, updating the halo', () => {
    render();
    mocks.features.mockReturnValue([feature(42), feature(43)]);
    mocks.listeners.get('idle')!();
    fixture.detectChanges();
    fixture.componentInstance.choose('42');
    fixture.detectChanges();
    expect(update).toHaveBeenLastCalledWith({ alert: null, detection: '42' }, expect.anything());
    expect(html.querySelector('#detail-title')?.textContent).toBe('Detection #42');
    expect(html.querySelector('#detection-picker')?.textContent).toContain('Detection #42');
    expect(mocks.sources.get('selection')?.setData).toHaveBeenLastCalledWith({
      type: 'FeatureCollection', features: [{ type: 'Feature', properties: {}, geometry: { type: 'Point', coordinates: [81, 20] } }]
    });

    fixture.componentInstance.closeDetails();
    fixture.detectChanges();
    mocks.listeners.get('click')!({ point: {} });
    fixture.detectChanges();
    expect(update).toHaveBeenLastCalledWith({ alert: null, detection: '42' }, expect.anything());
    expect(html.querySelector('#detection-picker')?.textContent).toContain('Detection #42');
  });

  it('separates empty results from missing tiles and retries each failed source with the alert list still mounted', () => {
    render();
    const list = html.querySelector('.alert-panel');
    expect(html.textContent).toContain('Loading fire detections');
    mocks.listeners.get('idle')!();
    fixture.detectChanges();
    expect(html.textContent).toContain('No detections in view');

    for (const sourceId of ['detections', 'reference']) {
      mocks.listeners.get('error')!({ sourceId, error: { message: 'Tile unavailable', status: 503 } });
    }
    fixture.detectChanges();
    expect(html.textContent).not.toContain('No detections in view');
    expect(html.textContent).toContain('Fire detection tiles');
    expect(html.textContent).toContain('Reference tiles');
    html.querySelector<HTMLButtonElement>('fimra-state-region button')!.click();
    fixture.detectChanges();
    expect(mocks.removeSource.mock.calls).toEqual([['reference'], ['detections']]);
    expect(mocks.addSource.mock.calls.filter(([id]) => id === 'detections')).toHaveLength(2);
    expect(mocks.addSource.mock.calls.filter(([id]) => id === 'reference')).toHaveLength(2);
    expect(html.querySelector('.alert-panel')).toBe(list);
  });

  it('retries workspace geography separately from fire detections', () => {
    render(false);
    mocks.listeners.get('error')!({ sourceId: 'detections', error: { status: 503, message: 'Unavailable' } });
    fixture.detectChanges();
    const status = html.querySelector('fimra-state-region')!;
    expect(status.textContent).toContain('Workspace boundary and layer information');
    expect(status.textContent).toContain('Fire detection tiles');
    status.querySelector('button')!.click();
    fixture.detectChanges();
    vi.advanceTimersByTime(0);
    http.expectOne(endpoint).flush({ version: 'test', levels: [], boundary: null });
    fixture.detectChanges();
    expect(status.textContent).not.toContain('Some map data is missing');
    http.verify();
  });

  it('leads with place and alert ID, then shows overdue, age and the next action', () => {
    render();
    const button = html.querySelector('.row-open')!;
    expect(button.textContent?.trim()).toBe('Beat 05 · Alert #14');
    expect(button.getAttribute('aria-label')).toBeNull();
    expect(button.nextElementSibling?.textContent).toMatch(/Overdue.*ago.*Next step: Acknowledged/);
    expect(html.querySelector('.compact-alerts li .step-meta')?.textContent).toContain('New');
    http.verify();
  });
});
