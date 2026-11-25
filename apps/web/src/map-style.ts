import type { LayerSpecification, StyleSpecification } from 'maplibre-gl';
import tokens from '../../../docs/design-system/tokens.json';
import type { Theme } from './theme';

export const boundaries = [
  { id: 'beats', label: 'Beat', width: 1, color: 'textSecondary' },
  { id: 'ranges', label: 'Range', width: 1.6, color: 'textSecondary' },
  { id: 'divisions', label: 'Division', width: 2.2, color: 'brand' },
  { id: 'circles', label: 'Circle', width: 3, color: 'brand' },
  { id: 'state', label: 'State', width: 4, color: 'text' },
] as const;

export function demoStyle(theme: Theme): StyleSpecification {
  const color = tokens.color[theme];
  const ids = ['forest', 'exclusions', ...boundaries.map(row => row.id)];
  const layers: LayerSpecification[] = boundaries.map(row => ({
    id: row.id, type: 'line', source: row.id,
    paint: {
      'line-color': color[row.color], 'line-width': row.width,
      ...(row.id === 'beats' ? { 'line-dasharray': [3, 3] } : {})
    }
  }));
  return {
    version: 8,
    sources: Object.fromEntries(ids.map(id => [id, {
      type: 'geojson' as const, data: '/demo-state/' + id + '.geojson'
    }])),
    layers: [
      { id: 'background', type: 'background', paint: { 'background-color': color.mapBackground } },
      { id: 'land', type: 'fill', source: 'state', paint: { 'fill-color': color.card } },
      { id: 'forest', type: 'fill', source: 'forest', paint: {
        'fill-color': tokens.color.chart[theme][2], 'fill-opacity': 0.34
      } },
      { id: 'forest-edge', type: 'line', source: 'forest', paint: {
        'line-color': color.textSecondary, 'line-width': 1
      } },
      ...layers,
      { id: 'kiln', type: 'fill', source: 'exclusions', paint: { 'fill-color': color.neutralMark } },
      { id: 'kiln-outline', type: 'line', source: 'exclusions', paint: { 'line-color': color.text, 'line-width': 2 } }
    ]
  };
}
