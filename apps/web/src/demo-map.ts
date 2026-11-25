import { HlmTableImports } from '@spartan-ng/helm/table';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import tokens from '../../../docs/design-system/tokens.json';
import { AfterViewInit, Component, ElementRef, OnDestroy, computed, effect, inject, input, signal, untracked, viewChild } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { EmptyState } from './ui/empty-state';
import { StatusBadge } from './ui/status-badge';
import { LucideDownload, LucideLayers, LucideMaximize, LucideMinus, LucidePlus } from '@lucide/angular';
import { GeoJSONSource, LngLatBounds, Map as LibreMap, setWorkerUrl } from 'maplibre-gl';
import { DashboardFilters } from './dashboard-filters';
import { Workflow } from './workflow';
import { AlertQueue, AlertRow, queueLabels } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ageLabels, classes, Detection, detectionLayer, ForestClass, iconName, mapRequest, readDetection, relativeAge, symbolImage, tileErrorStatus, tileUrl, triageReasons } from './detections';
import { ExportDialog } from './export-dialog';
import type { NotificationLink } from './notifications';
import { boundaries, demoStyle } from './map-style';
import { ThemeService, type Theme } from './theme';
import { unavailable, WorkspaceService } from './workspace';

@Component({
  selector: 'fimra-demo-map',
  imports: [HlmButtonImports, HlmDialogImports, HlmFieldImports, HlmSelectImports, HlmSkeletonImports, HlmTableImports, Workflow, EmptyState, StatusBadge, LucideLayers, LucideMaximize, LucideMinus, LucidePlus, ExportDialog, LucideDownload],
  providers: [AlertQueue],
  templateUrl: './demo-map.html'
})
export class DemoMap implements AfterViewInit, OnDestroy {
  readonly linkedAlert = input<NotificationLink | null>(null);
  readonly linkedMessage = signal('');
  readonly boundaries = boundaries;
  readonly workspace = inject(WorkspaceService);
  readonly classes = classes;
  readonly forestClasses = Object.keys(classes) as ForestClass[];
  readonly ageLabels = ageLabels;
  readonly reasons = triageReasons;
  readonly error = signal('');
  readonly tileError = signal('');
  readonly ready = signal(false);
  readonly legend = signal(false);
  readonly queue = inject(AlertQueue);
  readonly queueLabels = queueLabels;
  readonly hours = this.queue.hours;
  readonly mobileQueue = signal(false);
  private fromQueue = false;
  readonly filters = inject(DashboardFilters);
  readonly visible = signal<Detection[]>([]);
  readonly selected = this.queue.selected;
  readonly now = this.queue.now;
  readonly relativeAge = relativeAge;
  readonly stepDate = Date.parse;
  readonly localTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' });
  private readonly container = viewChild.required<ElementRef<HTMLDivElement>>('canvas');
  private selectionTrigger?: HTMLElement;
  private resize?: ResizeObserver;
  private selectedId = '';
  private previousView?: { center: [number, number]; zoom: number };
  private movedInDetails = false;
  private readonly detailTitle = viewChild<ElementRef<HTMLHeadingElement>>('detailTitle');
  private readonly http = inject(HttpClient);
  private readonly appearance = inject(ThemeService);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl;
  private readonly url = computed(() => {
    const workspace = this.workspace.current();
    return workspace && tileUrl(this.base, workspace.slug, this.hours(), this.filters.tileClasses(), this.filters.unitPath());
  });
  readonly exportEndpoint = computed(() => {
    const workspace = this.workspace.current();
    return workspace && this.base.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug);
  });
  private map?: LibreMap;
  private bounds?: LngLatBounds;
  private appliedTheme?: Theme;
  private appliedUrl: string | null = null;
  private timer?: ReturnType<typeof setInterval>;

  constructor() {
    effect(() => {
      const theme = this.appearance.theme();
      const url = this.url();
      const low = this.filters.lowBandwidth();
      if (this.ready() && this.map) untracked(() => this.updateLayers(this.map!, theme, url, low));
    });
    effect(() => {
      const id = String(this.selected()?.id ?? '');
      const map = this.map;
      if (!this.ready() || !map) return;
      if (map.getLayer('selected-detection')) {
        map.setFilter('selected-detection', ['==', ['to-string', ['get', 'id']], id]);
      }
      if (id !== this.selectedId) {
        if (id && (!this.selectedId || this.movedInDetails)) {
          this.previousView = { center: map.getCenter().toArray(), zoom: map.getZoom() };
        } else if (!id && this.previousView && !this.movedInDetails) {
          map.resize();
          map.jumpTo(this.previousView);
        }
        this.selectedId = id;
        this.movedInDetails = false;
      }
      this.centerSelection();
    });
    effect(() => this.detailTitle()?.nativeElement.focus({ preventScroll: true }));
    effect(onCleanup => {
      const link = this.linkedAlert();
      const endpoint = this.queue.endpoint();
      this.linkedMessage.set('');
      if (!link?.alert || !endpoint || this.workspace.current()?.slug !== link.workspace) {
        if (untracked(this.queue.linked)) this.queue.select(null);
        return;
      }
      const request = this.http.get<AlertRow>(endpoint + '/' + link.alert).subscribe({
        next: row => {
          this.fromQueue = false;
          this.selectionTrigger = this.map?.getCanvas();
          this.mobileQueue.set(false);
          this.queue.selectLinked(row);
        },
        error: err => {
          if (err.status === 401) this.auth.sessionEnded();
          this.linkedMessage.set('This alert could not be opened.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const row = this.queue.linked();
      if (!this.ready() || !this.map) return;
      const source = this.map.getSource<GeoJSONSource>('linked-alert');
      if (!source) return;
      source.setData({
        type: 'FeatureCollection',
        features: row ? [{
          type: 'Feature', properties: {},
          geometry: { type: 'Point', coordinates: [row.longitude, row.latitude] }
        }] : []
      });
    });
  }

  private updateLayers(map: LibreMap, theme: Theme, url: string | null, low = this.filters.lowBandwidth()): void {
    const changed = theme !== this.appliedTheme;
    const style = demoStyle(theme);
    for (const [id, source] of Object.entries(style.sources)) {
      if (!low && !map.getSource(id)) map.addSource(id, source);
    }
    for (const layer of style.layers) {
      if (low && layer.id !== 'background') {
        if (map.getLayer(layer.id)) map.removeLayer(layer.id);
      } else if (!map.getLayer(layer.id)) {
        map.addLayer(layer, map.getLayer('detections') ? 'detections' : undefined);
      } else if (changed && 'paint' in layer) {
        for (const [name, value] of Object.entries(layer.paint ?? {})) {
          map.setPaintProperty(layer.id, name as Parameters<LibreMap['setPaintProperty']>[1], value);
        }
      }
    }
    if (low) {
      for (const id of Object.keys(style.sources)) {
        if (id !== 'state' && map.getSource(id)) map.removeSource(id);
      }
    }
    if (changed && map.getLayer('linked-alert')) {
      map.setPaintProperty('linked-alert', 'circle-color', tokens.color[theme].brand);
      map.setPaintProperty('linked-alert', 'circle-stroke-color', tokens.color[theme].brand);
    }
    if (changed && map.getLayer('selected-detection')) {
      map.setPaintProperty('selected-detection', 'circle-stroke-color', tokens.color[theme].brand);
    }
    this.appliedTheme = theme;
    if (url !== this.appliedUrl) {
      this.removeDetections(map);
      this.visible.set([]);
      this.tileError.set('');
      this.appliedUrl = url;
    }
    if (!url) return;
    const source = map.getSource('detections');
    if (!source || changed) {
      for (const cls of this.forestClasses) {
        for (const age of this.ageLabels.keys()) {
          const name = iconName(cls, age, theme);
          if (!map.hasImage(name)) map.addImage(name, symbolImage(cls, age, theme), { pixelRatio: 2 });
        }
      }
    }
    const layer = detectionLayer(theme);
    if (!source) {
      map.addSource('detections', { type: 'vector', tiles: [url] });
      map.addLayer(layer);
      map.addLayer({ id: 'selected-detection', type: 'circle', source: 'detections', 'source-layer': 'detections',
        filter: ['==', ['to-string', ['get', 'id']], String(this.selected()?.id ?? '')],
        paint: { 'circle-radius': 10, 'circle-opacity': 0, 'circle-stroke-width': 3, 'circle-stroke-color': tokens.color[theme].brand }
      });
      if (map.getLayer('linked-alert')) map.moveLayer('linked-alert');
    } else if (changed) {
      map.setLayoutProperty(layer.id, 'icon-image', layer.layout!['icon-image']);
    }
  }

  private removeDetections(map: LibreMap): void {
    if (map.getLayer('selected-detection')) map.removeLayer('selected-detection');
    if (map.getLayer('detections')) map.removeLayer('detections');
    if (map.getSource('detections')) map.removeSource('detections');
  }

  ngAfterViewInit(): void {
    this.appliedTheme = this.appearance.theme();
    try {
      setWorkerUrl(new URL('map/maplibre-gl-worker.mjs', document.baseURI).href);
      const style = demoStyle(this.appliedTheme);
      if (this.filters.lowBandwidth()) {
        // Keep only the state extent for the fit control.
        style.sources = { state: style.sources['state'] };
        style.layers = style.layers.filter(layer => layer.id === 'background');
      }
      const map = new LibreMap({
        container: this.container().nativeElement,
        style,
        transformRequest: url => mapRequest(url, this.base, document.baseURI, () => {
          const user = this.auth.user();
          if (user?.expired) { this.auth.sessionEnded(); return; }
          return user?.access_token;
        }),
        attributionControl: false, renderWorldCopies: false, dragRotate: false, pitchWithRotate: false,
        canvasContextAttributes: { antialias: true }
      });
      this.map = map;
      map.on('movestart', event => {
        if (event.originalEvent && this.selected()) this.movedInDetails = true;
      });
      this.resize = new ResizeObserver(() => {
        map.resize();
        this.centerSelection();
      });
      this.resize.observe(this.container().nativeElement);
      map.getCanvas().setAttribute('aria-label', 'Workspace map. Use arrow keys to pan and plus or minus to zoom.');
      map.on('error', event => {
        if ('sourceId' in event && event.sourceId === 'detections') {
          const status = tileErrorStatus(event.error);
          if (status === 401) this.auth.sessionEnded();
          else if (status === 403) this.workspace.deny();
          else this.tileError.set(unavailable);
        } else this.error.set('Some map data could not load. Reload to try again.');
      });
      map.on('click', event => {
        if (!map.getLayer('detections')) return;
        const [feature] = map.queryRenderedFeatures(event.point, { layers: ['detections'] });
        if (feature) {
          this.fromQueue = false;
          this.selectionTrigger = map.getCanvas();
          this.queue.select(readDetection(feature));
        }
      });
      map.on('idle', () => {
        if (!map.getLayer('detections')) return;
        const points = new Map<string, Detection>();
        for (const feature of map.queryRenderedFeatures({ layers: ['detections'] })) {
          const point = readDetection(feature);
          points.set(String(point.id), point);
        }
        this.visible.set([...points.values()].sort((a, b) => b.observed_at - a.observed_at));
      });
      map.once('load', async () => {
        try {
          this.bounds = await map.getSource<GeoJSONSource>('state')?.getBounds();
          if (!this.map) return;
          this.fitState();
          map.addSource('linked-alert', { type: 'geojson', data: { type: 'FeatureCollection', features: [] } });
          map.addLayer({ id: 'linked-alert', type: 'circle', source: 'linked-alert',
            paint: { 'circle-radius': 9, 'circle-color': tokens.color[this.appearance.theme()].brand, 'circle-opacity': 0.2,
              'circle-stroke-width': 3, 'circle-stroke-color': tokens.color[this.appearance.theme()].brand }
          });
          this.ready.set(true);
          this.timer = setInterval(() => {
            if (map.getSource('detections')) {
              if (this.tileError()) {
                this.removeDetections(map);
                this.tileError.set('');
                this.updateLayers(map, this.appearance.theme(), this.url());
              } else map.refreshTiles('detections');
            }
          }, 60_000);
        } catch {
          this.error.set('Could not find the state boundary. Reload to try again.');
        }
      });
    } catch {
      this.error.set('The map needs WebGL. Turn on graphics acceleration in your browser and reload.');
    }
  }

  private centerSelection(): void {
    const point = this.selected();
    if (this.selectedId && !this.movedInDetails && point?.longitude != null && point.latitude != null) {
      this.map?.jumpTo({ center: [point.longitude, point.latitude] });
    }
  }

  choose(id: string): void {
    this.fromQueue = false;
    this.selectionTrigger = document.getElementById('detection-picker') ?? undefined;
    this.queue.select(this.visible().find(point => String(point.id) === id) ?? null);
  }

  openRow(row: AlertRow, button: HTMLButtonElement): void {
    this.fromQueue = true;
    this.selectionTrigger = button;
    this.mobileQueue.set(false);
    this.queue.selectRow(row);

    this.detailTitle()?.nativeElement.focus();
  }

  closeDetails(): void {
    this.queue.select(null);
    const trigger = this.selectionTrigger;
    if (this.fromQueue && trigger?.isConnected) this.mobileQueue.set(true);
    requestAnimationFrame(() => {
      const target = trigger?.isConnected ? trigger : this.map?.getCanvas();
      target?.focus();
    });
  }

  fit(): void {
    if (this.selected()) this.movedInDetails = true;
    this.fitState();
  }

  private fitState(): void {
    if (this.bounds && !this.bounds.isEmpty()) this.map?.fitBounds(this.bounds, { padding: Math.min(80, this.container().nativeElement.clientWidth * 0.1), duration: 0 });
  }

  zoom(inward: boolean): void {
    if (this.selected()) this.movedInDetails = true;
    if (inward) this.map?.zoomIn();
    else this.map?.zoomOut();
  }

  ngOnDestroy(): void {
    this.resize?.disconnect();
    clearInterval(this.timer);
    this.map?.remove();
    this.map = undefined;
  }
}
