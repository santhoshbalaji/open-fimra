import { AfterViewInit, Component, ElementRef, OnDestroy, computed, effect, inject, signal, untracked, viewChild } from '@angular/core';
import { BrnButton } from '@spartan-ng/brain/button';
import { LucideLayers, LucideMaximize, LucideMinus, LucidePlus } from '@lucide/angular';
import { GeoJSONSource, LngLatBounds, Map as LibreMap, setWorkerUrl } from 'maplibre-gl';
import { Workflow } from './workflow';
import { AlertQueue, AlertRow, queueLabels } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ageLabels, classes, Detection, detectionLayer, ForestClass, iconName, mapRequest, readDetection, relativeAge, symbolImage, tileErrorStatus, tileUrl, triageReasons } from './detections';
import { boundaries, demoStyle } from './map-style';
import { ThemeService, type Theme } from './theme';
import { unavailable, WorkspaceService } from './workspace';

@Component({
  selector: 'fimra-demo-map',
  imports: [Workflow, BrnButton, LucideLayers, LucideMaximize, LucideMinus, LucidePlus],
  providers: [AlertQueue],
  templateUrl: './demo-map.html'
})
export class DemoMap implements AfterViewInit, OnDestroy {
  readonly boundaries = boundaries;
  readonly workspace = inject(WorkspaceService);
  readonly classes = classes;
  readonly forestClasses = Object.keys(classes) as ForestClass[];
  readonly ageLabels = ageLabels;
  readonly reasons = triageReasons;
  readonly error = signal('');
  readonly tileError = signal('');
  readonly ready = signal(false);
  readonly legend = signal(true);
  readonly queue = inject(AlertQueue);
  readonly queueLabels = queueLabels;
  readonly hours = this.queue.hours;
  readonly mobileQueue = signal(false);
  private rowButton?: HTMLButtonElement;
  readonly outside = signal(false);
  readonly visible = signal<Detection[]>([]);
  readonly selected = this.queue.selected;
  readonly now = this.queue.now;
  readonly relativeAge = relativeAge;
  readonly stepDate = Date.parse;
  readonly localTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'long' });
  private readonly container = viewChild.required<ElementRef<HTMLDivElement>>('canvas');
  private readonly picker = viewChild<ElementRef<HTMLSelectElement>>('picker');
  private readonly detailTitle = viewChild<ElementRef<HTMLHeadingElement>>('detailTitle');
  private readonly appearance = inject(ThemeService);
  private readonly auth = inject(AuthService);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl;
  private readonly url = computed(() => {
    const workspace = this.workspace.current();
    return workspace && tileUrl(this.base, workspace.slug, this.hours(), this.outside());
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
      if (this.ready() && this.map) untracked(() => this.updateLayers(this.map!, theme, url));
    });
    effect(() => {
      const id = String(this.selected()?.id ?? '');
      if (this.ready() && this.map?.getLayer('selected-detection')) {
        this.map.setFilter('selected-detection', ['==', ['to-string', ['get', 'id']], id]);
      }
    });
    effect(() => this.detailTitle()?.nativeElement.focus());
  }

  private updateLayers(map: LibreMap, theme: Theme, url: string | null): void {
    const changed = theme !== this.appliedTheme;
    if (changed) {
      for (const layer of demoStyle(theme).layers) {
        if (!('paint' in layer)) continue;
        for (const [name, value] of Object.entries(layer.paint ?? {})) {
          const property = name as Parameters<LibreMap['setPaintProperty']>[1];
          map.setPaintProperty(layer.id, property, value);
        }
      }
      this.appliedTheme = theme;
    }
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
        paint: { 'circle-radius': 10, 'circle-opacity': 0, 'circle-stroke-width': 3, 'circle-stroke-color': '#277ea3' }
      });
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
        style: demoStyle(this.appliedTheme),
        transformRequest: url => mapRequest(url, this.base, document.baseURI, () => {
          const user = this.auth.user();
          if (user?.expired) { this.auth.sessionEnded(); return; }
          return user?.access_token;
        }),
        attributionControl: false, renderWorldCopies: false, dragRotate: false, pitchWithRotate: false,
        canvasContextAttributes: { antialias: true }
      });
      this.map = map;
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
        if (feature) { this.rowButton = undefined; this.queue.select(readDetection(feature)); }
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
          this.fit();
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

  choose(id: string): void {
    this.rowButton = undefined;
    this.queue.select(this.visible().find(point => String(point.id) === id) ?? null);
    if (id) this.detailTitle()?.nativeElement.focus();
  }

  openRow(row: AlertRow, button: HTMLButtonElement): void {
    this.rowButton = button;
    this.mobileQueue.set(false);
    this.queue.selectRow(row);
    this.map?.jumpTo({ center: [row.longitude, row.latitude] });
    this.detailTitle()?.nativeElement.focus();
  }

  closeDetails(): void {
    this.queue.select(null);
    if (this.rowButton?.isConnected) {
      this.mobileQueue.set(true);
      const button = this.rowButton;
      requestAnimationFrame(() => button.focus());
    } else this.picker()?.nativeElement.focus();
  }

  fit(): void {
    if (this.bounds && !this.bounds.isEmpty()) this.map?.fitBounds(this.bounds, { padding: Math.min(80, this.container().nativeElement.clientWidth * 0.1), duration: 0 });
  }

  zoom(inward: boolean): void {
    if (inward) this.map?.zoomIn();
    else this.map?.zoomOut();
  }

  ngOnDestroy(): void {
    clearInterval(this.timer);
    this.map?.remove();
    this.map = undefined;
  }
}
