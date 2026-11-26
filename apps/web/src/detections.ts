import type { ExpressionSpecification, MapGeoJSONFeature, RequestParameters, SymbolLayerSpecification } from 'maplibre-gl';
import { ageColours, mapColours } from './token-values';
import { isApiRequest } from './config';
import type { Theme } from './theme';

export const classes = {
  inside: 'Inside forest', near_edge: 'Near the forest edge',
  outside: 'Outside forest', fixed_heat_source: 'Suspected fixed heat source'
};
export type ForestClass = keyof typeof classes;
export const shortClasses: Record<ForestClass, string> = {
  inside: 'Inside', near_edge: 'Edge', outside: 'Outside', fixed_heat_source: 'Fixed heat'
};
export { ageLabels } from './token-values';
export const triageReasons: Record<string, string> = {
  no_unit: 'No mapped unit covers this point.',
  overlapping_units: 'More than one mapped unit covers this point.',
  shared_boundary: 'This point lies on a shared unit boundary.'
};
export interface Detection {
  id: string | number;
  forest_class: ForestClass;
  observed_at: number;
  longitude?: number;
  latitude?: number;
  beat_code?: string;
  beat_name?: string;
  range_name?: string;
  division_name?: string;
  source?: string;
  product?: string;
  frp_mw?: number;
  confidence?: string | number;
  triage?: string;
  place_hierarchy?: string;
  /** Unit names by level slug. Map tiles send this as JSON text. */
  places?: Places | string;
}

export type Places = Record<string, { code?: string; name?: string } | undefined>;
export interface PlaceLevel { hierarchy: string; depth: number; slug: string; labels: { en: string } }
export interface Place { label: string; name: string; code: string }

const fallbackLevels = ['beat', 'range', 'division', 'circle'];

/**
 * The places a detection falls in, from the smallest unit to the largest below the workspace itself,
 * labelled with the workspace's own level names.
 */
export function placesOf(detection: Pick<Detection, 'places' | 'place_hierarchy'>, levels: readonly PlaceLevel[]): Place[] {
  let places: Places = {};
  if (typeof detection.places === 'string') {
    try { places = detection.places ? JSON.parse(detection.places) as Places : {}; } catch { places = {}; }
  } else if (detection.places) places = detection.places;
  const own = levels.filter(level => level.hierarchy === detection.place_hierarchy && level.depth > 0);
  const rows = own.length
    ? [...own].sort((a, b) => b.depth - a.depth).map(level => ({ slug: level.slug, label: level.labels.en || level.slug }))
    : fallbackLevels.filter(slug => places[slug]).map(slug => ({ slug, label: slug[0].toUpperCase() + slug.slice(1) }));
  return rows.map(({ slug, label }) => ({ label, name: places[slug]?.name ?? '', code: places[slug]?.code ?? '' }));
}

const sourceNames: Record<string, string> = { firms: 'NASA FIRMS', manual: 'Manual fire report' };

/** A readable name for a detection source code. */
export function sourceLabel(value: string | null | undefined): string {
  return value ? sourceNames[value] ?? value : 'Unknown';
}

const confidenceWords: Record<string, string> = { l: 'Low', low: 'Low', n: 'Nominal', nominal: 'Nominal', h: 'High', high: 'High' };

/** VIIRS sends l, n or h; MODIS sends a percentage. */
export function confidenceLabel(value: string | number | null | undefined): string {
  if (value === null || value === undefined || value === '') return 'Unknown';
  const text = String(value).trim();
  const word = confidenceWords[text.toLowerCase()];
  if (word) return word;
  return /^\d+(\.\d+)?$/.test(text) ? text + '%' : text;
}

export function readDetection(feature: Pick<MapGeoJSONFeature, 'id' | 'properties'> & Partial<Pick<MapGeoJSONFeature, 'geometry'>>): Detection {
  const coordinates = feature.geometry?.type === 'Point' ? feature.geometry.coordinates : null;
  return { ...feature.properties, ...(coordinates && { longitude: coordinates[0], latitude: coordinates[1] }),
    id: feature.properties['id'] ?? feature.id } as Detection;
}

export function tileErrorStatus(error: { message: string; status?: number }): number | undefined {
  if (error.status !== undefined) return error.status;
  // Worker messages can preserve MapLibre's AJAXError message but drop its custom fields.
  const match = /^AJAXError: .*? \((\d{3})\): /.exec(error.message);
  return match ? Number(match[1]) : undefined;
}

export function tileUrl(base: string, slug: string, hours: number, forestClasses: string, unitPath: string): string {
  const url = base.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(slug) +
    '/tiles/detections/{z}/{x}/{y}.mvt?hours=' + hours;
  return url + '&classes=' + forestClasses + (unitPath ? '&unitPath=' + encodeURIComponent(unitPath) : '');
}

export function mapRequest(url: string, base: string, page: string, token: () => string | undefined): RequestParameters {
  if (!isApiRequest(url, base, page)) return { url };
  const bearer = token();
  return bearer ? { url, headers: { Authorization: 'Bearer ' + bearer } } : { url };
}

export function symbolStyle(cls: ForestClass, age: number, theme: Theme) {
  const palette = mapColours[theme];
  const color = ageColours[theme][age];
  switch (cls) {
    case 'inside': return { shape: 'circle', radius: 6, color };
    case 'near_edge': return { shape: 'ring', radius: 6, color };
    case 'outside': return { shape: 'circle', radius: 3, color: palette.neutralMark };
    case 'fixed_heat_source': return { shape: 'square', radius: 5, color: palette.neutralMark };
  }
}

export function iconName(cls: ForestClass, age: number, theme: Theme): string {
  return ['fire', theme, cls, age].join('-');
}

export function symbolImage(cls: ForestClass | 'incident', age: number, theme: Theme): ImageData {
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = 40;
  const ctx = canvas.getContext('2d')!;
  ctx.scale(2, 2);
  const { shape, radius, color } = cls === 'incident'
    ? { shape: 'diamond', radius: 6, color: mapColours[theme].selection }
    : symbolStyle(cls, age, theme);
  const centre = canvas.width / 4;
  ctx.beginPath();
  if (shape === 'square') ctx.rect(centre - radius, centre - radius, radius * 2, radius * 2);
  else if (shape === 'diamond') {
    ctx.moveTo(centre, centre - radius);
    ctx.lineTo(centre + radius, centre);
    ctx.lineTo(centre, centre + radius);
    ctx.lineTo(centre - radius, centre);
    ctx.closePath();
  } else ctx.arc(centre, centre, radius, 0, Math.PI * 2);
  ctx.lineJoin = 'round';
  ctx.strokeStyle = mapColours[theme].ageOutline;
  if (shape !== 'ring') {
    ctx.fillStyle = color;
    ctx.fill();
    ctx.lineWidth = 2;
    ctx.stroke();
  } else {
    ctx.lineWidth = 5;
    ctx.stroke();
    ctx.strokeStyle = color;
    ctx.lineWidth = 2.5;
    ctx.stroke();
  }
  return ctx.getImageData(0, 0, canvas.width, canvas.height);
}

export function detectionLayer(theme: Theme): SymbolLayerSpecification {
  const image: ExpressionSpecification = ['concat', 'fire-' + theme + '-', ['get', 'forest_class'], '-', ['to-string', ['get', 'age']]];
  return {
    id: 'detections', source: 'detections', 'source-layer': 'detections', type: 'symbol',
    layout: { 'icon-image': image, 'icon-overlap': 'always', 'symbol-z-order': 'source',
      'symbol-sort-key': ['get', 'observed_at'] }
  };
}

/** Observation age to the minute, short enough to fit in the map picker. */
export function detectionAge(seconds: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now / 1000 - seconds) / 60));
  if (!minutes) return 'Just now';
  const parts = [
    [Math.floor(minutes / 1440), 'd'],
    [Math.floor(minutes / 60) % 24, 'h'],
    [minutes % 60, 'm']
  ];
  return parts.filter(([value]) => value).map(([value, unit]) => `${value}${unit}`).join(' ') + ' ago';
}

export function relativeAge(seconds: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now / 1000 - seconds) / 60));
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const [value, unit] = days ? [days, 'day'] : hours ? [hours, 'hour'] : [minutes, 'minute'];
  return minutes ? value + ' ' + unit + (value === 1 ? '' : 's') + ' ago' : 'Just now';
}
