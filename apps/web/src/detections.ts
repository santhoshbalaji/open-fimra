import type { ExpressionSpecification, MapGeoJSONFeature, RequestParameters, SymbolLayerSpecification } from 'maplibre-gl';
import tokens from '../../../docs/design-system/tokens.json';
import { isApiRequest } from './config';
import type { Theme } from './theme';

export const classes = {
  inside: 'Inside forest', near_edge: 'Near the forest edge',
  outside: 'Outside forest', fixed_heat_source: 'Suspected fixed heat source'
};
export type ForestClass = keyof typeof classes;
export const ageLabels = tokens.color.fireAge.steps;
export const triageReasons: Record<string, string> = {
  no_unit: 'No mapped unit covers this point.',
  overlapping_units: 'More than one mapped unit covers this point.',
  shared_boundary: 'This point lies on a shared unit boundary.'
};
export interface Detection {
  id: string | number;
  forest_class: ForestClass;
  observed_at: number;
  beat_code?: string;
  beat_name?: string;
  range_name?: string;
  division_name?: string;
  source?: string;
  product?: string;
  frp_mw?: number;
  confidence?: string | number;
  triage?: string;
}

export function readDetection(feature: Pick<MapGeoJSONFeature, 'id' | 'properties'>): Detection {
  return { ...feature.properties, id: feature.properties['id'] ?? feature.id } as Detection;
}

export function tileErrorStatus(error: { message: string; status?: number }): number | undefined {
  if (error.status !== undefined) return error.status;
  // Worker messages can preserve MapLibre's AJAXError message but drop its custom fields.
  const match = /^AJAXError: .*? \((\d{3})\): /.exec(error.message);
  return match ? Number(match[1]) : undefined;
}

export function tileUrl(base: string, slug: string, hours: number, outside: boolean): string {
  const url = base.replace(/\/$/, '') + '/api/v1/workspaces/' + encodeURIComponent(slug) +
    '/tiles/detections/{z}/{x}/{y}.mvt?hours=' + hours;
  return outside ? url + '&classes=inside,near_edge,fixed_heat_source,outside' : url;
}

export function mapRequest(url: string, base: string, page: string, token: () => string | undefined): RequestParameters {
  if (!isApiRequest(url, base, page)) return { url };
  const bearer = token();
  return bearer ? { url, headers: { Authorization: 'Bearer ' + bearer }, cache: 'no-store' } : { url };
}

export function symbolStyle(cls: ForestClass, age: number, theme: Theme) {
  const palette = tokens.color[theme];
  const color = tokens.color.fireAge[theme][age];
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

export function symbolImage(cls: ForestClass, age: number, theme: Theme): ImageData {
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = 32;
  const ctx = canvas.getContext('2d')!;
  ctx.scale(2, 2);
  const { shape, radius, color } = symbolStyle(cls, age, theme);
  ctx.beginPath();
  if (shape === 'square') ctx.rect(8 - radius, 8 - radius, radius * 2, radius * 2);
  else ctx.arc(8, 8, radius, 0, Math.PI * 2);
  ctx.strokeStyle = tokens.color[theme].card;
  ctx.lineWidth = shape === 'ring' ? 4 : 2;
  ctx.stroke();
  if (shape === 'ring') {
    ctx.strokeStyle = color;
    ctx.lineWidth = 2.5;
    ctx.stroke();
  } else {
    ctx.fillStyle = color;
    ctx.fill();
  }
  return ctx.getImageData(0, 0, 32, 32);
}

export function detectionLayer(theme: Theme): SymbolLayerSpecification {
  const image: ExpressionSpecification = ['concat', 'fire-' + theme + '-', ['get', 'forest_class'], '-', ['to-string', ['get', 'age']]];
  return {
    id: 'detections', source: 'detections', 'source-layer': 'detections', type: 'symbol',
    layout: { 'icon-image': image, 'icon-overlap': 'always', 'symbol-z-order': 'source',
      'symbol-sort-key': ['get', 'observed_at'] }
  };
}

export function relativeAge(seconds: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now / 1000 - seconds) / 60));
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const [value, unit] = days ? [days, 'day'] : hours ? [hours, 'hour'] : [minutes, 'minute'];
  return minutes ? value + ' ' + unit + (value === 1 ? '' : 's') + ' ago' : 'Just now';
}
