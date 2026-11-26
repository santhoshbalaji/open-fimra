import { DOCUMENT } from '@angular/common';
import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, OnInit, ViewEncapsulation,
  afterRenderEffect, inject, input, signal, viewChild } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { HlmButton } from '@spartan-ng/helm/button';
import type { FeatureCollection, Geometry } from 'geojson';
import { LngLatBounds, Map as LibreMap, setWorkerUrl } from 'maplibre-gl';
import type { LayerSpecification } from 'maplibre-gl';
import { ThemeService } from './theme';
import { interfaceColours, mapColours } from './token-values';
import { DataState, StateRegion } from './ui/state-region';

interface MapPreview {
  boundary: Geometry | null;
  data: FeatureCollection;
  truncated: boolean;
}

@Component({
  selector: 'fimra-layer-preview-map',
  imports: [HlmButton, StateRegion],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styleUrl: './layers-map.css',
  template: `
    <figure class="grid min-w-0 gap-2" [class.min-h-72]="state().kind === 'loading'">
      <figcaption class="font-semibold">Draft map</figcaption>
      <fimra-state-region [state]="state()" (retry)="load()" />
      @if (preview(); as mapPreview) {
        <div class="flex flex-wrap gap-x-4 gap-y-1 text-sm">
          <span class="inline-flex items-center gap-2"><span class="w-5 border-t-2 border-primary" aria-hidden="true"></span>Draft features</span>
          @if (mapPreview.boundary) {
            <span class="inline-flex items-center gap-2"><span class="w-5 border-t-2 border-dashed border-text-secondary"
              aria-hidden="true"></span>Workspace boundary</span>
          }
        </div>
        <div #container class="h-64 overflow-hidden rounded-control border border-border bg-background sm:h-80"></div>
        <div class="flex flex-wrap gap-2">
          <button hlmBtn variant="outline" type="button" aria-label="Zoom in on draft map" (click)="zoom(1)">Zoom in</button>
          <button hlmBtn variant="outline" type="button" aria-label="Zoom out on draft map" (click)="zoom(-1)">Zoom out</button>
          <button hlmBtn variant="outline" type="button" (click)="fit()">Fit draft</button>
          @if (mapPreview.boundary) {
            <button hlmBtn variant="outline" type="button" (click)="fitWorkspace()">Fit workspace</button>
          }
        </div>
        @if (mapPreview.truncated) {
          <p class="text-sm text-muted-foreground">The map shows a sample of the checked features.</p>
        }
      }
    </figure>
  `
})
export class LayerPreviewMap implements OnInit {
  readonly url = input.required<string>();
  protected readonly preview = signal<MapPreview | null>(null);
  protected readonly state = signal<DataState>({ kind: 'loading', heading: 'Loading the draft map' });

  private readonly container = viewChild<ElementRef<HTMLElement>>('container');
  private readonly http = inject(HttpClient);
  private readonly appearance = inject(ThemeService);
  private readonly destroy = inject(DestroyRef);
  private readonly document = inject(DOCUMENT);
  private map: LibreMap | null = null;
  private draftBounds = new LngLatBounds();

  constructor() {
    afterRenderEffect(onCleanup => {
      const preview = this.preview();
      const container = this.container()?.nativeElement;
      const theme = this.appearance.theme();
      if (!preview || !container) return;

      const colors = interfaceColours[theme];
      const mapColors = mapColours[theme];
      const layers: LayerSpecification[] = [
        { id: 'background', type: 'background', paint: { 'background-color': mapColors.canvas } },
        { id: 'workspace', source: 'workspace', type: 'fill', paint: { 'fill-color': mapColors.land } },
        { id: 'workspace-edge', source: 'workspace', type: 'line',
          paint: { 'line-color': colors.textSecondary, 'line-width': 2, 'line-dasharray': [3, 2] } },
        { id: 'draft-fill', source: 'draft', type: 'fill',
          paint: { 'fill-color': colors.action, 'fill-opacity': 0.24 } },
        { id: 'draft-edge', source: 'draft', type: 'line',
          paint: { 'line-color': colors.action, 'line-width': 2 } },
        { id: 'draft-points', source: 'draft', type: 'circle', filter: ['==', ['geometry-type'], 'Point'],
          paint: { 'circle-color': colors.action, 'circle-radius': 5 } }
      ];
      try {
        setWorkerUrl(new URL('map/maplibre-gl-worker.mjs', this.document.baseURI).href);
        const map = new LibreMap({
          container, attributionControl: false, renderWorldCopies: false, scrollZoom: false,
          dragRotate: false, pitchWithRotate: false, maxZoom: 17,
          style: { version: 8, layers, sources: {
            draft: { type: 'geojson', data: preview.data },
            workspace: { type: 'geojson', data: { type: 'FeatureCollection', features: preview.boundary ?
              [{ type: 'Feature', properties: {}, geometry: preview.boundary }] : [] } }
          } }
        });
        this.map = map;
        onCleanup(() => { map.remove(); this.map = null; });
        this.draftBounds = new LngLatBounds();
        for (const feature of preview.data.features) this.extendBounds(this.draftBounds, feature.geometry);
        this.fit();
        map.getCanvas().setAttribute('aria-label', 'Draft map. Use arrow keys to pan and plus or minus to zoom.');
        map.on('error', () => this.mapFailed('The checked features could not be drawn.'));
      } catch {
        this.mapFailed('The browser could not start the map.');
      }
    });
  }

  ngOnInit(): void {
    this.load();
  }

  protected load(): void {
    this.state.set({ kind: 'loading', heading: 'Loading the draft map' });
    this.preview.set(null);
    this.http.get<MapPreview>(this.url()).pipe(takeUntilDestroyed(this.destroy)).subscribe({
      next: preview => {
        if (preview.data.features.length) {
          this.preview.set(preview);
          this.state.set({ kind: 'ready', announcement: 'Draft map ready.' });
        } else {
          this.state.set({ kind: 'empty', heading: 'No features could be shown on the map',
            detail: preview.truncated ? 'The shapes are too detailed for this map preview.' : undefined });
        }
      },
      error: (error: HttpErrorResponse) => this.mapFailed(typeof error.error?.error === 'string' ? error.error.error : '')
    });
  }

  protected zoom(step: number): void {
    if (this.map) this.map.zoomTo(this.map.getZoom() + step, { duration: 0 });
  }

  protected fit(): void {
    this.fitBounds(this.draftBounds);
  }

  protected fitWorkspace(): void {
    const boundary = this.preview()?.boundary;
    if (!boundary) return;
    const bounds = new LngLatBounds().extend(this.draftBounds);
    this.extendBounds(bounds, boundary);
    this.fitBounds(bounds);
  }

  private fitBounds(bounds: LngLatBounds): void {
    if (!bounds.isEmpty()) this.map?.fitBounds(bounds, { padding: 20, maxZoom: 13, duration: 0 });
  }

  private mapFailed(detail: string): void {
    this.preview.set(null);
    this.state.set({ kind: 'error', heading: 'Could not show the draft map', detail });
  }

  private extendBounds(bounds: LngLatBounds, geometry: Geometry): void {
    if (geometry.type === 'GeometryCollection') {
      for (const part of geometry.geometries) this.extendBounds(bounds, part);
    } else {
      this.extendCoordinates(bounds, geometry.coordinates);
    }
  }

  private extendCoordinates(bounds: LngLatBounds, coordinates: number[] | number[][] | number[][][] | number[][][][]): void {
    if (typeof coordinates[0] === 'number') {
      const [lng, lat] = coordinates as number[];
      bounds.extend([lng, lat]);
    } else {
      for (const part of coordinates as number[][] | number[][][] | number[][][][]) this.extendCoordinates(bounds, part);
    }
  }
}
