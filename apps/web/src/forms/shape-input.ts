import { translate } from '../language';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { HlmTextareaImports } from '@spartan-ng/helm/textarea';
import { Component, computed, effect, input, output, signal } from '@angular/core';

type Kind = 'point' | 'track' | 'polygon';
type Position = [number, number];
export type Shape = { type: 'Point'; coordinates: number[] } | { type: 'LineString'; coordinates: number[][] }
  | { type: 'Polygon'; coordinates: number[][][] };

/** The positions of a shape, without the closing position of a polygon. */
export function positionsOf(shape: unknown): Position[] {
  const s = shape as Shape | null | undefined;
  if (!s || typeof s !== 'object') return [];
  if (s.type === 'Point') return [[s.coordinates[0], s.coordinates[1]]];
  if (s.type === 'LineString') return s.coordinates.map(p => [p[0], p[1]]);
  if (s.type === 'Polygon') return (s.coordinates[0] ?? []).slice(0, -1).map(p => [p[0], p[1]]);
  return [];
}

/** A GeoJSON shape from positions, or null when there are none. Polygons are closed. */
export function shapeOf(kind: Kind, positions: Position[]): Shape | null {
  if (!positions.length) return null;
  if (kind === 'point') return { type: 'Point', coordinates: positions[0] };
  if (kind === 'track') return { type: 'LineString', coordinates: positions };
  return { type: 'Polygon', coordinates: [[...positions, positions[0]]] };
}

/** Reads one position per line, as longitude and latitude. */
export function parsePositions(text: string): Position[] | null {
  const lines = text.split('\n').map(line => line.trim()).filter(Boolean);
  const positions: Position[] = [];
  for (const line of lines) {
    const parts = line.split(/[\s,;]+/).filter(Boolean).map(Number);
    if (parts.length !== 2 || parts.some(n => !Number.isFinite(n))) return null;
    positions.push([parts[0], parts[1]]);
  }
  return positions;
}

const round = (n: number) => Math.round(n * 1e6) / 1e6;

/**
 * A position, a walked track or a drawn area, typed in as longitude and latitude
 * or added from the device's position. A small drawing shows the shape.
 */
@Component({
  selector: 'fimra-shape-input',
  imports: [HlmButtonImports, HlmTextareaImports],
  host: { class: 'shape-input' },
  template: `
    <textarea hlmTextarea [id]="inputId()" [disabled]="disabled()" [value]="text()" rows="3"
      [attr.aria-describedby]="description()" [attr.aria-invalid]="invalid() || error() ? 'true' : null"
      (change)="read($any($event.target).value)"></textarea>
    <small [id]="inputId() + '-how'">{{ how() }}</small>
    @if (error()) { <small class="field-error" [id]="inputId() + '-shape-error'" role="alert">{{ error() }}</small> }
    @if (!disabled()) {
      <div class="shape-actions">
        <button hlmBtn type="button" variant="outline" size="sm" [disabled]="locating()" (click)="addMine()">
          {{ kind() === 'point' ? msg('Use my position') : msg('Add my position') }}</button>
        @if (kind() === 'point' && alertPoint(); as point) {
          <button hlmBtn type="button" variant="outline" size="sm" (click)="emit([point])">{{ msg('Use the alert position') }}</button>
        }
        @if (positions().length) { <button hlmBtn type="button" variant="ghost" size="sm" (click)="emit([])">{{ msg('Clear') }}</button> }
      </div>
    }
    @if (drawing(); as d) {
      <svg class="shape-drawing" viewBox="0 0 100 60" role="img" [attr.aria-label]="summary()">
        @if (kind() === 'polygon') { <polygon [attr.points]="d" /> }
        @else if (kind() === 'track') { <polyline [attr.points]="d" /> }
        @for (p of dots(); track $index) { <circle [attr.cx]="p[0]" [attr.cy]="p[1]" r="1.6" /> }
      </svg>
    }
  `
})
export class ShapeInput {
  protected readonly msg = translate;
  readonly kind = input.required<Kind>();
  readonly value = input<unknown>(undefined);
  readonly disabled = input(false);
  readonly inputId = input.required<string>();
  readonly describedBy = input<string | null>(null);
  readonly invalid = input(false);
  readonly alertPoint = input<Position | null>(null);
  readonly changed = output<Shape | null>();
  readonly error = signal('');
  readonly description = computed(() => [this.describedBy(), this.inputId() + '-how',
    this.error() && this.inputId() + '-shape-error'].filter(Boolean).join(' '));
  readonly locating = signal(false);
  readonly positions = computed(() => positionsOf(this.value()));
  readonly text = signal('');
  readonly how = computed(() => ({
    point: 'Longitude and latitude in degrees, such as 86.30, 16.40.',
    track: 'One position per line, in the order walked: longitude, latitude.',
    polygon: 'One corner per line, going round the area: longitude, latitude. The area closes by itself.'
  })[this.kind()]);
  readonly summary = computed(() => {
    const n = this.positions().length;
    const kind = this.kind();
    if (kind === 'point') return n ? 'The chosen position' : 'No position yet';
    return `${kind === 'track' ? 'A track' : 'An area'} with ${n} ${n === 1 ? 'position' : 'positions'}`;
  });
  private readonly projected = computed(() => {
    const points = this.positions();
    if (!points.length) return [];
    const lons = points.map(p => p[0]);
    const lats = points.map(p => p[1]);
    const [w, e, s, n] = [Math.min(...lons), Math.max(...lons), Math.min(...lats), Math.max(...lats)];
    const scale = Math.min(90 / Math.max(e - w, 1e-9), 50 / Math.max(n - s, 1e-9));
    return points.map(([lon, lat]) => [5 + (lon - w) * scale + (90 - (e - w) * scale) / 2,
      55 - (lat - s) * scale - (50 - (n - s) * scale) / 2] as Position);
  });
  readonly dots = computed(() => this.projected());
  readonly drawing = computed(() => {
    const points = this.projected();
    return points.length ? points.map(p => p.join(',')).join(' ') : '';
  });

  constructor() {
    effect(() => this.text.set(this.positions().map(p => p.join(', ')).join('\n')));
  }

  read(text: string): void {
    const positions = parsePositions(text);
    if (!positions) {
      this.error.set('Each line needs a longitude and a latitude, such as 86.30, 16.40.');
      return;
    }
    if (this.kind() === 'point' && positions.length > 1) {
      this.error.set('Give one position only.');
      return;
    }
    this.emit(positions);
  }

  emit(positions: Position[]): void {
    this.error.set('');
    this.changed.emit(shapeOf(this.kind(), positions));
  }

  addMine(): void {
    if (!navigator.geolocation) {
      this.error.set('This browser cannot share its position.');
      return;
    }
    this.locating.set(true);
    navigator.geolocation.getCurrentPosition(position => {
      this.locating.set(false);
      const here: Position = [round(position.coords.longitude), round(position.coords.latitude)];
      this.emit(this.kind() === 'point' ? [here] : [...this.positions(), here]);
    }, () => {
      this.locating.set(false);
      this.error.set('Could not get your position. Allow location access and try again.');
    }, { enableHighAccuracy: true, timeout: 15000 });
  }
}
