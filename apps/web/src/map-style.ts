import type { FilterSpecification, LayerSpecification } from 'maplibre-gl';
import tokens from '../../../docs/design-system/tokens.json';
import type { Theme } from './theme';

interface Level {
  hierarchy: string;
  depth: number;
  slug: string;
  labels: { en: string; hi?: string };
}
export interface WorkspaceMap {
  version: string;
  boundary: [number, number, number, number] | null;
  levels: Level[];
}

export function boundaryKey(levels: Level[], theme: Theme) {
  const color = tokens.color[theme];
  return levels.map(level => ({
    ...level, id: level.hierarchy + '-' + level.depth,
    width: 4 / (1 + level.depth * 0.65),
    color: level.depth === 0 ? color.text : level.depth === 1 ? color.brand : color.textSecondary,
    opacity: 1 / (1 + level.depth * 0.13)
  }));
}

export function referenceLayers(info: WorkspaceMap, theme: Theme, outline: boolean): LayerSpecification[] {
  const color = tokens.color[theme];
  const units = { source: 'reference', 'source-layer': 'units' };
  const forest = { source: 'reference', 'source-layer': 'forest' };
  const exclusions = { source: 'reference', 'source-layer': 'exclusions' };
  const layers: LayerSpecification[] = outline ? [] : [
    { id: 'land', type: 'fill', ...units, filter: ['==', ['get', 'depth'], 0], paint: { 'fill-color': color.card } },
    { id: 'forest', type: 'fill', ...forest, paint: { 'fill-color': tokens.color.chart[theme][2], 'fill-opacity': 0.34 } },
    { id: 'forest-edge', type: 'line', ...forest, paint: { 'line-color': color.textSecondary, 'line-width': 1 } },
    { id: 'exclusions', type: 'fill', ...exclusions, paint: { 'fill-color': color.neutralMark } },
    { id: 'exclusion-edge', type: 'line', ...exclusions, paint: { 'line-color': color.text, 'line-width': 2 } }
  ];
  for (const row of boundaryKey(info.levels, theme).sort((a, b) => b.depth - a.depth)) {
    if (outline && row.depth !== 0) continue;
    const filter: FilterSpecification = ['all', ['==', ['get', 'hierarchy'], row.hierarchy], ['==', ['get', 'depth'], row.depth]];
    layers.push({
      id: 'unit-' + row.id, type: 'line', ...units, filter,
      paint: { 'line-color': row.color, 'line-width': row.width, 'line-opacity': row.opacity,
        ...(row.depth > 0 ? { 'line-dasharray': [Math.max(2, 9 - row.depth * 2), 2] } : {}) }
    });
    if (!outline && !info.levels.some(other => other.hierarchy === row.hierarchy && other.depth > row.depth)) {
      layers.push({
        id: 'label-' + row.id, type: 'symbol', ...units, filter, minzoom: 11,
        layout: { 'text-field': ['get', 'name'], 'text-font': ['sans-serif'], 'text-size': 12 },
        paint: { 'text-color': color.text, 'text-halo-color': color.card, 'text-halo-width': 1 }
      });
    }
  }
  return layers;
}
