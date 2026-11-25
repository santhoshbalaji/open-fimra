import { AfterViewInit, Component, ElementRef, OnDestroy, effect, inject, signal, viewChild } from '@angular/core';
import { BrnButton } from '@spartan-ng/brain/button';
import { LucideLayers, LucideMaximize, LucideMinus, LucidePlus } from '@lucide/angular';
import { GeoJSONSource, LngLatBounds, Map as LibreMap, setWorkerUrl } from 'maplibre-gl';
import { boundaries, demoStyle } from './map-style';
import { ThemeService, type Theme } from './theme';

@Component({
  selector: 'fimra-demo-map',
  imports: [BrnButton, LucideLayers, LucideMaximize, LucideMinus, LucidePlus],
  templateUrl: './demo-map.html'
})
export class DemoMap implements AfterViewInit, OnDestroy {
  readonly boundaries = boundaries;
  readonly error = signal('');
  readonly ready = signal(false);
  readonly legend = signal(true);
  private readonly container = viewChild.required<ElementRef<HTMLDivElement>>('canvas');
  private readonly appearance = inject(ThemeService);
  private map?: LibreMap;
  private bounds?: LngLatBounds;
  private appliedTheme?: Theme;

  constructor() {
    effect(() => {
      const theme = this.appearance.theme();
      if (this.map && this.ready() && theme !== this.appliedTheme) {
        this.map.setStyle(demoStyle(theme));
        this.appliedTheme = theme;
      }
    });
  }

  ngAfterViewInit(): void {
    this.appliedTheme = this.appearance.theme();
    try {
      setWorkerUrl(new URL('map/maplibre-gl-worker.mjs', document.baseURI).href);
      const map = new LibreMap({
        container: this.container().nativeElement,
        style: demoStyle(this.appliedTheme),
        attributionControl: false,
        renderWorldCopies: false,
        dragRotate: false,
        pitchWithRotate: false,
        canvasContextAttributes: { antialias: true }
      });
      this.map = map;
      map.getCanvas().setAttribute('aria-label', 'Demo State map. Use arrow keys to pan and plus or minus to zoom.');
      map.on('error', () => this.error.set('Some map data could not load. Reload to try again.'));
      map.once('load', async () => {
        try {
          const state = map.getSource<GeoJSONSource>('state');
          this.bounds = await state?.getBounds();
          if (!this.map || !this.bounds || this.bounds.isEmpty()) return;
          this.fit();
          this.ready.set(true);
        } catch {
          this.error.set('Could not find the state boundary. Reload to try again.');
        }
      });
    } catch {
      this.error.set('The map needs WebGL. Turn on graphics acceleration in your browser and reload.');
    }
  }

  fit(): void {
    if (this.bounds) this.map?.fitBounds(this.bounds, { padding: Math.min(80, this.container().nativeElement.clientWidth * 0.1), duration: 0 });
  }

  zoom(inward: boolean): void {
    if (inward) this.map?.zoomIn();
    else this.map?.zoomOut();
  }

  ngOnDestroy(): void {
    this.map?.remove();
    this.map = undefined;
  }
}
