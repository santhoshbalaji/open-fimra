import { NgTemplateOutlet } from '@angular/common';
import { HlmSelectImports } from '@spartan-ng/helm/select';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmDialogImports } from '@spartan-ng/helm/dialog';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { mapColours } from './token-values';
import { RouterLink, UrlTree } from '@angular/router';
import { afterNextRender, AfterViewInit, Component, ViewEncapsulation, ElementRef, Injector, OnDestroy, TemplateRef, computed, effect, inject, signal, untracked, viewChild } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { catchError, EMPTY, exhaustMap, expand, forkJoin, reduce, timer } from 'rxjs';
import { StatusBadge } from './ui/status-badge';
import { SidePanel } from './ui/side-panel';
import { StateRegion, type DataState } from './ui/state-region';
import { GeoJSONSource, Map as LibreMap, setWorkerUrl, type VectorTileSource } from 'maplibre-gl';
import type { FeatureCollection, Point } from 'geojson';
import { DashboardFilters } from './dashboard-filters';
import { Workflow } from './workflow';
import { AlertQueue, AlertRow, alertRowLabel, queueLabels } from './alert-queue';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ageLabels, classes, confidenceLabel, sourceLabel, Detection, detectionAge, detectionLayer, ForestClass, iconName, incidentOffset, mapRequest, placesOf, readDetection, shortClasses, symbolImage, tileErrorStatus, tileUrl, triageReasons } from './detections';
import { ExportDialog } from './export-dialog';
import type { IncidentFocus, IncidentRow } from './incidents';
import { AlertReports } from './forms/alert-reports';
import { ShellNavigation, recordId } from './shell-navigation';
import { ShellListScroll } from './shell-list-scroll';
import { exclusionOpacity, referenceGroups, type MapLayerGroup, type WorkspaceMap } from './map-style';
import { ThemeService, type Theme } from './theme';
import { WorkspaceService } from './workspace';
import { Icon } from './ui/icon';
import { DataTable, type TableColumn } from './ui/data-table';
import { defaultWorkspaceFormat } from './workspace-format';

const detectionGroup: MapLayerGroup = { id: 'detections', label: 'Fire detections', minZoom: 0 };
const incidentGroup: MapLayerGroup = {
  id: 'reported-incidents', label: 'Reported incidents', minZoom: 0, swatch: { kind: 'incident' }
};
type TileSource = 'reference' | 'detections';

function pointCollection(points: [number, number][]): FeatureCollection<Point> {
  return {
    type: 'FeatureCollection',
    features: points.map(coordinates => ({ type: 'Feature', properties: {}, geometry: { type: 'Point', coordinates } }))
  };
}

@Component({
  selector: 'fimra-demo-map',
  styleUrl: './demo-map.css',
  // The map library's class names are global.
  encapsulation: ViewEncapsulation.None,
  imports: [HlmButtonImports, HlmDialogImports, HlmFieldImports, HlmSelectImports, NgTemplateOutlet, DataTable, Workflow, StatusBadge, ExportDialog, AlertReports, ShellListScroll, Icon, SidePanel, StateRegion, RouterLink],
  providers: [AlertQueue],
  templateUrl: './demo-map.html'
})
export class DemoMap implements AfterViewInit, OnDestroy {
  readonly navigation = inject(ShellNavigation);
  readonly linkedMessage = signal('');
  private readonly alertId = computed(() => recordId(this.navigation.query().get('alert')));
  private readonly detectionId = computed(() => recordId(this.navigation.query().get('detection')));
  private readonly incidentId = computed(() => this.navigation.path() === '/monitor/map' ? recordId(this.navigation.query().get('incident')) : '');
  private readonly geography = signal<{ endpoint: string; info: WorkspaceMap } | null>(null);
  readonly workspace = inject(WorkspaceService);
  readonly classes = classes;
  readonly forestClasses = Object.keys(classes) as ForestClass[];
  readonly shortClasses = shortClasses;
  readonly keyClasses = computed(() => {
    const forestClass = this.filters.forestClass();
    return forestClass ? [forestClass] : this.forestClasses;
  });
  readonly keyAges = computed(() => {
    if (!this.keyClasses().some(cls => cls === 'inside' || cls === 'near_edge')) return [];
    return ageLabels.slice(0, this.hours() <= 6 ? 1 : this.hours() <= 24 ? 2 : this.hours() <= 72 ? 3 : 4)
      .map((label, i) => ({
        colour: 'var(--map-age-' + (i + 1) + ')',
        label: i === 3 && this.hours() > 168 ? '3 days or more' : label,
        short: ['<6h', '6–24h', '1–3d', this.hours() > 168 ? '3d+' : '3–7d'][i]
      }));
  });
  readonly reasons = triageReasons;
  readonly confidenceLabel = confidenceLabel;
  readonly sourceLabel = sourceLabel;
  private readonly placeLevels = computed(() => this.geography()?.info.levels ?? []);
  /** The smallest place first, then the rest from smaller to larger. */
  places(detection: Detection) {
    const [first, ...rest] = placesOf(detection, this.placeLevels());
    const named = (place: { label: string; name: string }) => place.name || 'Unknown ' + place.label.toLowerCase();
    return { first, firstName: first ? named(first) : 'Unknown place', rest: rest.map(named).join(' · '), all: [first, ...rest].filter(Boolean) };
  }
  readonly error = signal('');
  private readonly geographyError = signal(false);
  private readonly referenceError = signal(false);
  private readonly tileError = signal(false);
  private readonly incidentError = signal(false);
  private readonly geographyRetries = signal(0);
  private readonly incidentRetries = signal(0);
  private readonly detectionsDrawn = signal(false);
  private readonly incidentPoints = signal<[number, number][]>([]);
  readonly boundary = computed(() => this.geography()?.info.boundary);
  readonly ready = signal(false);
  readonly zoomLevel = signal(0);
  private readonly layerChoices = signal<Record<string, boolean>>({});
  readonly reference = computed(() => {
    const geography = this.geography();
    return geography ? referenceGroups(geography.info, this.appearance.theme()) : [];
  });
  readonly exclusionOpacity = exclusionOpacity;
  readonly operational = [detectionGroup, incidentGroup];
  readonly showDetections = computed(() => !!this.workspace.current() && this.layerEnabled(detectionGroup));
  readonly showIncidents = computed(() => !!this.workspace.current() && this.layerEnabled(incidentGroup));
  readonly visibleLegend = computed(() =>
    [...this.reference(), incidentGroup].filter(group => this.layerEnabled(group) && this.zoomLevel() >= group.minZoom)
  );
  readonly mapDataState = computed<DataState>(() => {
    const missing = [
      this.tileError() && this.showDetections() ? 'Fire detection tiles' : '',
      this.referenceError() && this.reference().some(group => this.layerEnabled(group) && this.zoomLevel() >= group.minZoom)
        ? (this.filters.lowBandwidth() ? 'Workspace outline tiles' : 'Reference tiles (boundaries, forest and known heat source areas)') : '',
      this.geographyError() ? 'Workspace boundary and layer information' : '',
      this.incidentError() && this.showIncidents() ? 'Reported incidents' : ''
    ].filter(Boolean);
    if (missing.length) {
      const detail = missing.join('. ') + '. You can still use the alert queue.';
      return { kind: 'error', heading: 'Some map data is missing', detail };
    }
    if (!this.showDetections() || this.error()) return { kind: 'ready' };
    if (!this.detectionsDrawn()) return { kind: 'loading', heading: 'Loading fire detections' };
    return this.visible().length ? { kind: 'ready' } :
      { kind: 'empty', heading: 'No detections in view', detail: 'Pan the map or change the filters.' };
  });
  readonly queue = inject(AlertQueue);
  readonly queueLabels = queueLabels;
  readonly hours = this.queue.hours;
  readonly alertsView = computed(() => this.navigation.path() === '/monitor/alerts');
  readonly incidentFocus = signal<IncidentFocus | null>(null);
  readonly filters = inject(DashboardFilters);
  readonly visible = signal<Detection[]>([]);
  readonly detectionChoices = computed(() => {
    const point = this.selected();
    const rows = this.visible();
    return point && !rows.some(row => String(row.id) === String(point.id)) ? [point, ...rows] : rows;
  });
  readonly selected = this.queue.selected;
  protected readonly mapOnly = computed(() => !this.alertsView() && this.navigation.url().endsWith('#map'));
  protected readonly detailHeading = computed(() => {
    const alert = this.queue.pinned();
    const id = alert?.id || this.alertId();
    return id ? 'Alert #' + id : 'Detection #' + (this.selected()?.id ?? this.detectionId());
  });
  private readonly detailRequested = computed(() => !this.mapOnly() && !!(this.alertId() || this.detectionId()));
  private readonly detailReady = computed(() => !!this.selected());
  private readonly selectionCoordinates = computed(() => {
    const selected = this.selected();
    if (!selected) return null;
    // Use the current tile geometry when available. Its precision depends on zoom.
    const point = this.visible().find(row => String(row.id) === String(selected.id)) ?? selected;
    return point.longitude != null && point.latitude != null
      ? [point.longitude, point.latitude] as [number, number] : null;
  }, { equal: (a, b) => a?.[0] === b?.[0] && a?.[1] === b?.[1] });
  readonly now = this.queue.now;
  readonly detectionAge = detectionAge;
  readonly dateTime = (value: string | number) => defaultWorkspaceFormat.dateTime(new Date(value));
  readonly rowAge = (row: AlertRow) => {
    const observed = row.observed_at * 1000;
    const now = this.now();
    return now - observed < 60_000 ? 'Just now' : defaultWorkspaceFormat.age(new Date(observed), new Date(now));
  };
  readonly rowKey = (row: AlertRow) => row.id;
  readonly rowLabel = (row: AlertRow) => alertRowLabel(row, this.places(row).firstName);
  private readonly rowHeading = viewChild<TemplateRef<{ $implicit: AlertRow }>>('rowHeading');
  private readonly rowStatus = viewChild<TemplateRef<{ $implicit: AlertRow }>>('rowStatus');
  readonly queueCaption = computed(() =>
    (this.queue.listScope() || 'Alerts') + ' · ' + (this.queue.allQueues() ? 'All queues' : queueLabels[this.queue.active()]));
  readonly queueColumns = computed<TableColumn<AlertRow>[]>(() => [
    { id: 'alert', label: 'Place and alert', rowHeading: true, value: this.rowLabel, cell: this.rowHeading() },
    { id: 'workflow', label: 'Workflow', value: row => row.step_label, cell: this.rowStatus() },
    { id: 'observed', label: 'Observed', sortable: true, value: row => this.dateTime(row.observed_at * 1000) },
    { id: 'forest', label: 'Forest class', value: row => this.classes[row.forest_class] }
  ]);
  readonly dockState = computed<DataState>(() => {
    const state = this.queue.state();
    return state.kind === 'empty' && this.queue.rows().length ? { kind: 'ready' } : state;
  });
  private readonly workflow = viewChild(Workflow);
  private readonly reports = viewChild(AlertReports);
  private readonly reportsHeading = viewChild<ElementRef<HTMLElement>>('reportsHeading');
  private readonly queueTitle = viewChild<ElementRef<HTMLElement>>('queueTitle');
  private readonly container = viewChild.required<ElementRef<HTMLDivElement>>('canvas');
  private selectionTrigger?: HTMLElement;
  private readonly injector = inject(Injector);
  private readonly queuePanel = viewChild<ElementRef<HTMLElement>>('queuePanel');
  private resize?: ResizeObserver;
  private selectedId = '';
  private previousView?: { center: [number, number]; zoom: number };
  private restoredDetection = '';
  private movedInDetails = false;
  private readonly detailPanel = viewChild<SidePanel>('detailPanel');
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
  private readonly referenceTileUrl = computed(() => {
    const geography = this.geography();
    return geography ? geography.endpoint + '/tiles/reference/{z}/{x}/{y}.mvt?v=' +
      encodeURIComponent(geography.info.version) + (this.filters.lowBandwidth() ? '&outline=1' : '') : undefined;
  });
  private appliedTheme?: Theme;
  private appliedUrl: string | null = null;
  private timer?: ReturnType<typeof setInterval>;

  constructor() {
    effect(onCleanup => {
      const endpoint = this.exportEndpoint();
      this.geographyRetries();
      if (untracked(this.geography)?.endpoint !== endpoint) this.geography.set(null);
      this.geographyError.set(false);
      if (!endpoint) return;
      const request = timer(0, 60_000).pipe(exhaustMap(() =>
        this.http.get<WorkspaceMap>(endpoint).pipe(catchError(err => {
          if (err.status === 401) this.auth.sessionEnded();
          else if (err.status === 403) this.workspace.deny();
          else this.geographyError.set(true);
          return EMPTY;
        }))
      )).subscribe(info => {
        this.geographyError.set(false);
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
      const detection = this.detectionId();
      if (!geography) this.fittedWorkspace = undefined;
      if (!this.ready() || !geography?.info.boundary) return;
      const mapView = history.state?.mapView as { detection: string; workspace: string; center: [number, number]; zoom: number } | undefined;
      const restore = detection !== this.restoredDetection && detection && mapView?.detection === detection &&
        mapView.workspace === this.workspace.current()?.slug;
      if (restore) this.map?.jumpTo({ center: mapView.center, zoom: mapView.zoom });
      else if (this.fittedWorkspace !== geography.endpoint) untracked(() => this.fitBoundary());
      this.restoredDetection = detection;
      this.fittedWorkspace = geography.endpoint;
    });
    effect(() => {
      const theme = this.appearance.theme();
      const groups = this.reference();
      if (this.ready() && this.map) untracked(() => this.updateReference(this.map!, groups, theme));
    });
    effect(() => {
      const theme = this.appearance.theme();
      const url = this.url();
      if (this.ready() && this.map) untracked(() => this.updateLayers(this.map!, theme, url));
    });
    effect(() => {
      const url = this.referenceTileUrl();
      const groups = this.reference();
      const referenceVisible = groups.map(group => [group, this.layerEnabled(group)] as const);
      const detections = this.showDetections();
      const incidents = this.showIncidents();
      if (!this.ready() || !this.map) return;
      for (const [group, visible] of referenceVisible) {
        for (const layer of group.layers) this.setVisibility(layer.id, visible);
      }
      this.setVisibility('detections', detections);
      this.setVisibility('selected-detection', detections);
      this.setVisibility('selected-detection-edge', detections);
      this.setVisibility('reported-incidents', incidents);
      const source = this.map.getSource<VectorTileSource>('reference');
      if (source && url && url !== this.referenceUrl) {
        // Keep the layers and their choices while switching to the smaller outline tiles.
        this.referenceUrl = url;
        this.referenceError.set(false);
        source.setTiles([url]);
      }
    });
    effect(() => {
      const coordinates = this.selectionCoordinates();
      if (this.ready()) this.map?.getSource<GeoJSONSource>('selection')?.setData(pointCollection(coordinates ? [coordinates] : []));
    });
    effect(() => {
      const id = String(this.selected()?.id ?? '');
      const map = this.map;
      if (!this.ready() || !map) return;
      if (id !== this.selectedId) {
        if (id && (!this.selectedId || this.movedInDetails)) {
          this.previousView = { center: map.getCenter().toArray(), zoom: map.getZoom() };
        } else if (!id && this.previousView && !this.movedInDetails) {
          map.resize();
          map.jumpTo(this.previousView);
        }
        this.selectedId = id;
        this.movedInDetails = false;
        this.centerSelection();
      }
    });
    effect(() => {
      const panel = this.detailPanel();
      const requested = this.detailRequested();
      const alert = this.alertId();
      this.detectionId();
      const ready = this.detailReady();
      if (!panel) return;
      untracked(() => {
        if (!requested) panel.close();
        else if (ready) this.showDetails(panel, alert);
      });
    });
    effect(() => {
      if (this.mapOnly() && this.ready() && !this.detailPanel()?.open()) {
        afterNextRender({ read: () => {
          // Let the shell set route focus and the sheet release its focus trap first.
          if (this.mapOnly()) this.map?.getCanvas().focus({ preventScroll: true });
        } }, { injector: this.injector });
      }
    });
    effect(onCleanup => {
      const id = this.alertId();
      const endpoint = this.queue.endpoint();
      this.linkedMessage.set('');
      if (!id || !endpoint) {
        if (untracked(this.queue.linked)) this.queue.select(null);
        return;
      }
      if (untracked(this.queue.linked)?.id !== id) this.queue.select(null);
      this.queue.revision();
      const request = this.http.get<AlertRow>(endpoint + '/' + id).subscribe({
        next: row => this.queue.selectLinked(row),
        error: err => {
          if (err.status === 401) this.auth.sessionEnded();
          this.linkedMessage.set('This alert could not be opened.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const detection = this.detectionId();
      if (!detection) {
        if (!this.alertId() && untracked(this.selected)) this.queue.select(null);
        return;
      }
      const point = this.visible().find(item => String(item.id) === detection);
      const previous = untracked(this.selected);
      if (String(previous?.id ?? '') !== detection) this.queue.select(point ?? null);
    });
    effect(onCleanup => {
      const id = this.incidentId();
      const endpoint = this.exportEndpoint();
      this.incidentFocus.set(null);
      if (!id || !endpoint) return;
      const request = forkJoin({
        incident: this.http.get<IncidentRow>(endpoint + '/incidents/' + id),
        alerts: this.http.get<{ items: AlertRow[] }>(endpoint + '/alerts', { params: { incident: id, limit: 200 } })
      }).subscribe({
        next: ({ incident, alerts }) => this.incidentFocus.set({
          id, bounds: incident.bounds, points: alerts.items.map(alert => [alert.longitude, alert.latitude])
        }),
        error: err => {
          if (err.status === 401) this.auth.sessionEnded();
          this.linkedMessage.set('This incident could not be shown on the map.');
        }
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const focus = this.incidentFocus();
      if (!this.ready() || !this.map) return;
      if (focus?.points.length) {
        const [west, south, east, north] = focus.bounds;
        const padding = Math.min(80, this.container().nativeElement.clientWidth * 0.1);
        this.map.fitBounds([[west, south], [east, north]], { padding, maxZoom: 13, duration: 0 });
      }
      this.map.getSource<GeoJSONSource>('incident-members')?.setData(pointCollection(focus?.points ?? []));
    });
    effect(onCleanup => {
      const endpoint = this.exportEndpoint();
      const params = { ...this.filters.params(), limit: 200 };
      const enabled = this.showIncidents();
      this.incidentRetries();
      this.incidentPoints.set([]);
      this.incidentError.set(false);
      if (!enabled || !endpoint) return;
      type Page = { items: IncidentRow[]; nextCursor?: string };
      const page = (cursor?: string) => this.http.get<Page>(endpoint + '/incidents', {
        params: cursor ? { ...params, cursor } : params
      });
      const request = timer(0, 60_000).pipe(exhaustMap(() => page().pipe(
        expand(result => result.nextCursor ? page(result.nextCursor) : EMPTY),
        reduce((points, result) => {
          for (const item of result.items) {
            const [west, south, east, north] = item.bounds;
            points.push([(west + east) / 2, (south + north) / 2]);
          }
          return points;
        }, [] as [number, number][]),
        catchError(err => {
          if (err.status === 401) this.auth.sessionEnded();
          else this.incidentError.set(true);
          return EMPTY;
        })
      ))).subscribe(points => {
        this.incidentPoints.set(points);
        this.incidentError.set(false);
      });
      onCleanup(() => request.unsubscribe());
    });
    effect(() => {
      const points = this.incidentPoints();
      if (this.ready()) this.map?.getSource<GeoJSONSource>('reported-incidents')?.setData(pointCollection(points));
    });
  }

  layerEnabled(group: MapLayerGroup): boolean {
    if (group.optional && this.filters.lowBandwidth()) return false;
    return this.layerChoices()[group.id] ?? (group.id !== incidentGroup.id);
  }

  toggleLayer(group: MapLayerGroup, enabled: boolean): void {
    this.layerChoices.update(choices => ({ ...choices, [group.id]: enabled }));
    if (group.id === detectionGroup.id && !enabled) {
      this.detectionsDrawn.set(false);
      this.visible.set([]);
    }
  }

  private setVisibility(id: string, visible: boolean): void {
    const map = this.map;
    if (!map?.getLayer(id)) return;
    const visibility = visible ? 'visible' : 'none';
    if (map.getLayoutProperty(id, 'visibility') !== visibility) map.setLayoutProperty(id, 'visibility', visibility);
  }

  private updateReference(map: LibreMap, groups: ReturnType<typeof referenceGroups>, theme: Theme, retry = false): void {
    for (const id of this.referenceIds) map.removeLayer(id);
    this.referenceIds = [];
    const url = this.referenceTileUrl();
    if (retry || this.referenceUrl !== url) {
      this.referenceError.set(false);
      if (map.getSource('reference')) map.removeSource('reference');
      if (url) map.addSource('reference', { type: 'vector', tiles: [url] });
      this.referenceUrl = url;
    }
    map.setPaintProperty('background', 'background-color', mapColours[theme].canvas);
    for (const group of groups) {
      for (const layer of group.layers) {
        map.addLayer({
          ...layer, layout: { ...layer.layout, visibility: this.layerEnabled(group) ? 'visible' : 'none' }
        } as typeof layer, 'selected-detection-edge');
        this.referenceIds.push(layer.id);
      }
    }
  }

  private updateLayers(map: LibreMap, theme: Theme, url: string | null): void {
    const changed = theme !== this.appliedTheme;
    if (changed) {
      map.setPaintProperty('selected-detection-edge', 'circle-stroke-color', mapColours[theme].ageOutline);
      map.setPaintProperty('selected-detection', 'circle-stroke-color', mapColours[theme].selection);
      map.setPaintProperty('incident-members', 'circle-stroke-color', mapColours[theme].selection);
      map.setLayoutProperty('reported-incidents', 'icon-image', this.incidentIcon(map, theme));
    }
    this.appliedTheme = theme;
    if (url !== this.appliedUrl) {
      this.removeDetections(map);
      this.visible.set([]);
      this.tileError.set(false);
      this.detectionsDrawn.set(false);
      this.appliedUrl = url;
    }
    if (!url) return;
    const source = map.getSource('detections');
    if (!source || changed) {
      for (const cls of this.forestClasses) {
        for (const age of ageLabels.keys()) {
          const name = iconName(cls, age, theme);
          if (!map.hasImage(name)) map.addImage(name, symbolImage(cls, age, theme), { pixelRatio: 2 });
        }
      }
    }
    const layer = detectionLayer(theme);
    if (!source) {
      map.addSource('detections', { type: 'vector', tiles: [url] });
      layer.layout!.visibility = this.showDetections() ? 'visible' : 'none';
      map.addLayer(layer, 'reported-incidents');
    } else if (changed) {
      map.setLayoutProperty(layer.id, 'icon-image', layer.layout!['icon-image']);
    }
  }

  private removeDetections(map: LibreMap): void {
    if (map.getLayer('detections')) map.removeLayer('detections');
    if (map.getSource('detections')) map.removeSource('detections');
  }

  private incidentIcon(map: LibreMap, theme: Theme): string {
    const name = 'reported-incident-' + theme;
    if (!map.hasImage(name)) map.addImage(name, symbolImage('incident', 0, theme), { pixelRatio: 2 });
    return name;
  }

  ngAfterViewInit(): void {
    this.appliedTheme = this.appearance.theme();
    try {
      setWorkerUrl(new URL('map/maplibre-gl-worker.mjs', document.baseURI).href);
      const map = new LibreMap({
        container: this.container().nativeElement,
        style: { version: 8, sources: {}, layers: [{ id: 'background', type: 'background',
          paint: { 'background-color': mapColours[this.appliedTheme].canvas } }] },
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
      map.on('zoom', () => this.zoomLevel.set(map.getZoom()));
      map.on('error', event => {
        const source = 'sourceId' in event ? event.sourceId : undefined;
        if (source === 'detections' || source === 'reference') this.tileFailed(source, event.error);
        else this.error.set('The map display encountered an error. Reload to try again.');
      });
      map.on('click', event => {
        if (!this.showDetections() || !map.getLayer('detections')) return;
        const [feature] = map.queryRenderedFeatures(event.point, { layers: ['detections'] });
        if (!feature) return;
        const point = readDetection(feature);
        const id = String(point.id);
        if (!this.visible().some(row => String(row.id) === id)) this.visible.update(rows => [point, ...rows]);
        this.selectionTrigger = map.getCanvas();
        this.openDetection(id);
      });
      map.on('idle', () => {
        if (!this.showDetections() || !map.getLayer('detections')) return;
        const points = new Map<string, Detection>();
        for (const feature of map.queryRenderedFeatures({ layers: ['detections'] })) {
          const point = readDetection(feature);
          points.set(String(point.id), point);
        }
        this.visible.set([...points.values()].sort((a, b) => b.observed_at - a.observed_at));
        this.detectionsDrawn.set(true);
      });
      map.once('load', () => {
        const theme = this.appearance.theme();
        map.addSource('selection', { type: 'geojson', data: pointCollection([]) });
        map.addSource('incident-members', { type: 'geojson', data: pointCollection([]) });
        map.addSource('reported-incidents', { type: 'geojson', data: pointCollection([]) });
        map.addLayer({ id: 'selected-detection-edge', type: 'circle', source: 'selection',
          paint: { 'circle-radius': 11, 'circle-opacity': 0, 'circle-stroke-width': 5, 'circle-stroke-color': mapColours[theme].ageOutline }
        });
        map.addLayer({ id: 'selected-detection', type: 'circle', source: 'selection',
          paint: { 'circle-radius': 12, 'circle-opacity': 0, 'circle-stroke-width': 3, 'circle-stroke-color': mapColours[theme].selection }
        });
        map.addLayer({ id: 'reported-incidents', type: 'symbol', source: 'reported-incidents',
          layout: { 'icon-image': this.incidentIcon(map, theme), 'icon-offset': incidentOffset, 'icon-overlap': 'always', visibility: 'none' }
        });
        map.addLayer({ id: 'incident-members', type: 'circle', source: 'incident-members',
          paint: { 'circle-radius': 19, 'circle-opacity': 0, 'circle-stroke-width': 2, 'circle-stroke-color': mapColours[theme].selection }
        });
        this.ready.set(true);
        this.timer = setInterval(() => this.refreshDetections(), 60_000);
      });
    } catch {
      this.error.set('The map needs WebGL. Turn on graphics acceleration in your browser and reload.');
    }
  }

  private tileFailed(source: TileSource, error: { message: string; status?: number }): void {
    const status = tileErrorStatus(error);
    if (status === 401) this.auth.sessionEnded();
    else if (status === 403) this.workspace.deny();
    else if (source === 'reference') this.referenceError.set(true);
    else this.tileError.set(true);
  }

  retryMapData(): void {
    if (this.geographyError()) this.geographyRetries.update(count => count + 1);
    if (this.referenceError() && this.map) {
      // Recreate the source so failed tiles are requested from scratch.
      this.updateReference(this.map, this.reference(), this.appearance.theme(), true);
    }
    if (this.tileError()) this.refreshDetections();
    if (this.incidentError()) this.incidentRetries.update(count => count + 1);
  }

  private refreshDetections(): void {
    const map = this.map;
    if (!this.showDetections() || !map?.getSource('detections')) return;
    if (this.tileError()) {
      this.removeDetections(map);
      this.tileError.set(false);
      this.detectionsDrawn.set(false);
      this.updateLayers(map, this.appearance.theme(), this.url());
    } else map.refreshTiles('detections');
  }

  private centerSelection(): void {
    const point = this.selected();
    if (this.selectedId && !this.movedInDetails && point?.longitude != null && point.latitude != null) {
      this.map?.jumpTo({ center: [point.longitude, point.latitude] });
    }
  }

  choose(id: string): void {
    this.selectionTrigger = document.getElementById('detection-picker') ?? undefined;
    this.openDetection(id);
  }

  private openDetection(id: string): void {
    const map = this.map;
    // Keep the viewport with the history entry so the same tiles can load again.
    const mapView = map && id ? {
      detection: id, workspace: this.workspace.current()?.slug, center: map.getCenter().toArray(), zoom: map.getZoom()
    } : undefined;
    this.navigation.update({ alert: null, detection: id || null }, mapView && { state: { mapView } });
  }

  openRow(row: AlertRow, button: HTMLButtonElement): void {
    this.selectionTrigger = button;
    this.navigation.update({ alert: row.id, detection: null, queue: this.queue.allQueues() ? 'all' : this.queue.active() });
  }

  openReport(key: string): void {
    const reports = this.reports();
    const form = reports?.forms()?.find(form => form.form === key && form.step === this.queue.pinned()?.status);
    if (reports && form && reports.canFill(form)) reports.fill(form);
    else this.reportsHeading()?.nativeElement.focus();
  }

  reportChanged(): void {
    this.workflow()?.reload();
    this.queue.refresh();
  }

  closeDetails(): void {
    if (this.detailRequested()) this.navigation.update({ alert: null, detection: null });
  }

  protected mapLink(mapOnly = true): UrlTree {
    const alert = this.queue.pinned();
    const tree = this.navigation.href('/monitor/map', {
      alert: alert?.id ?? null, detection: alert ? null : this.selected()?.id
    });
    return new UrlTree(tree.root, tree.queryParams, mapOnly ? 'map' : null);
  }

  private showDetails(panel: SidePanel, alert: string): void {
    const trigger = this.selectionTrigger;
    this.selectionTrigger = undefined;
    panel.show(() => {
      if (this.mapOnly()) return this.map?.getCanvas() ?? null;
      if (trigger?.isConnected) return trigger;
      const row = alert ? this.queuePanel()?.nativeElement.querySelector<HTMLElement>(`[data-alert-id="${alert}"]`) : null;
      return (this.alertsView() ? row : row ?? document.getElementById('detection-picker')) ?? null;
    }, this.alertsView() ? this.queueTitle()?.nativeElement : this.map?.getCanvas());
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
