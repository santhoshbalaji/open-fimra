import { HlmTableImports } from '@spartan-ng/helm/table';
import { HlmSkeletonImports } from '@spartan-ng/helm/skeleton';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import tokens from '../../../docs/design-system/tokens.json';
import { AfterViewInit, Component, ElementRef, OnDestroy, computed, effect, inject, input, signal, untracked, viewChild } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { catchError, EMPTY, exhaustMap, timer } from 'rxjs';
import { EmptyState } from './ui/empty-state';
import { StatusBadge } from './ui/status-badge';
import { LucideDownload, LucideLayers, LucideMaximize, LucideMinus, LucidePlus } from '@lucide/angular';
import { GeoJSONSource, Map as LibreMap, setWorkerUrl } from 'maplibre-gl';
import { DashboardFilters } from './dashboard-filters';
import { Workflow } from './workflow';
import { AlertQueue, AlertRow, queueLabels } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ageLabels, classes, confidenceLabel, Detection, detectionLayer, ForestClass, iconName, mapRequest, placesOf, readDetection, relativeAge, symbolImage, tileErrorStatus, tileUrl, triageReasons } from './detections';
import { ExportDialog } from './export-dialog';
import type { NotificationLink } from './notifications';
import { boundaryKey, referenceLayers, type WorkspaceMap } from './map-style';
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
  private readonly geography = signal<{ endpoint: string; info: WorkspaceMap } | null>(null);
  readonly boundaries = computed(() => boundaryKey(this.geography()?.info.levels ?? [], this.appearance.theme())
    .filter(row => !this.filters.lowBandwidth() || row.depth === 0));
  readonly workspace = inject(WorkspaceService);
  readonly classes = classes;
  readonly forestClasses = Object.keys(classes) as ForestClass[];
  readonly ageLabels = ageLabels;
  readonly reasons = triageReasons;
  readonly confidenceLabel = confidenceLabel;
  private readonly placeLevels = computed(() => this.geography()?.info.levels ?? []);
  /** The smallest place first, then the rest from smaller to larger. */
  places(detection: Detection) {
    const [first, ...rest] = placesOf(detection, this.placeLevels());
    const named = (place: { label: string; name: string }) => place.name || 'Unknown ' + place.label.toLowerCase();
    return { first, firstName: first ? named(first) : 'Unknown place', rest: rest.map(named).join(' · '), all: [first, ...rest].filter(Boolean) };
  }
  readonly error = signal('');
  readonly geographyError = signal('');
  readonly boundary = computed(() => this.geography()?.info.boundary);
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
  private fittedWorkspace?: string;
  private referenceIds: string[] = [];
  private referenceUrl?: string;
  private appliedTheme?: Theme;
  private appliedUrl: string | null = null;
  private timer?: ReturnType<typeof setInterval>;

  constructor() {
    effect(onCleanup => {
      const endpoint = this.exportEndpoint();
      this.geography.set(null);
      this.geographyError.set('');
      if (!endpoint) return;
      const request = timer(0, 60_000).pipe(exhaustMap(() =>
        this.http.get<WorkspaceMap>(endpoint).pipe(catchError(err => {
          if (err.status === 401) this.auth.sessionEnded();
          else if (err.status === 403) this.workspace.deny();
          else this.geographyError.set('Could not load workspace geography. Trying again in a minute.');
          return EMPTY;
        }))
      )).subscribe(info => {
        this.geographyError.set('');
        const current = this.geography();
        if (!current || current.info.version !== info.version ||
            JSON.stringify(current.info.levels) !== JSON.stringify(info.levels) ||
            JSON.stringify(current.info.boundary) !== JSON.stringify(info.boundary)) {
          this.geography.set({ endpoint, info });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const geography = this.geography();
      if (!geography) this.fittedWorkspace = undefined;
      if (this.ready() && geography?.info.boundary && this.fittedWorkspace !== geography.endpoint) {
        untracked(() => this.fitBoundary());
        this.fittedWorkspace = geography.endpoint;
      }
    });
    effect(() => {
      const geography = this.geography();
      const low = this.filters.lowBandwidth();
      const theme = this.appearance.theme();
      if (!this.ready() || !this.map) return;
      const map = this.map;
      for (const id of this.referenceIds) map.removeLayer(id);
      this.referenceIds = [];
      const url = geography ? geography.endpoint + '/tiles/reference/{z}/{x}/{y}.mvt?v=' +
        encodeURIComponent(geography.info.version) + (low ? '&outline=1' : '') : undefined;
      if (this.referenceUrl !== url) {
        if (map.getSource('reference')) map.removeSource('reference');
        if (url) map.addSource('reference', { type: 'vector', tiles: [url] });
        this.referenceUrl = url;
      }
      map.setPaintProperty('background', 'background-color', tokens.color[theme].mapBackground);
      if (geography) {
        const before = map.getLayer('detections') ? 'detections' : 'linked-alert';
        for (const layer of referenceLayers(geography.info, theme, low)) {
          map.addLayer(layer, before);
          this.referenceIds.push(layer.id);
        }
      }
    });
    effect(() => {
      const theme = this.appearance.theme();
      const url = this.url();
      if (this.ready() && this.map) untracked(() => this.updateLayers(this.map!, theme, url));
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

  private updateLayers(map: LibreMap, theme: Theme, url: string | null): void {
    const changed = theme !== this.appliedTheme;
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
      const map = new LibreMap({
        container: this.container().nativeElement,
        style: { version: 8, sources: {}, layers: [{ id: 'background', type: 'background',
          paint: { 'background-color': tokens.color[this.appliedTheme].mapBackground } }] },
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
        if ('sourceId' in event && (event.sourceId === 'detections' || event.sourceId === 'reference')) {
          const status = tileErrorStatus(event.error);
          if (status === 401) this.auth.sessionEnded();
          else if (status === 403) this.workspace.deny();
          else if (event.sourceId === 'reference') this.error.set('Could not load reference tiles. Reload to try again.');
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
      map.once('load', () => {
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
    this.fitBoundary();
  }

  private fitBoundary(): void {
    const bounds = this.boundary();
    if (bounds) this.map?.fitBounds(bounds, { padding: Math.min(80, this.container().nativeElement.clientWidth * 0.1), duration: 0 });
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
