import type { FilterSpecification, LayerSpecification } from 'maplibre-gl';
import { forestOpacity, interfaceColours, mapColours } from './token-values';
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
  jurisdiction?: WorkspaceMap['boundary'];
  levels: Level[];
}

export const exclusionOpacity = 0.55;

type Swatch =
  | { kind: 'line'; color: string; width: number; opacity: number; dashed: boolean }
  | { kind: 'land' | 'forest' | 'exclusions' | 'label' | 'incident' };

export interface MapLayerGroup {
  id: string;
  label: string;
  minZoom: number;
  optional?: boolean;
  swatch?: Swatch;
}

interface ReferenceGroup extends MapLayerGroup {
  layers: LayerSpecification[];
}

export function referenceGroups(info: WorkspaceMap, theme: Theme): ReferenceGroup[] {
  const color = interfaceColours[theme];
  const map = mapColours[theme];
  const units = { source: 'reference', 'source-layer': 'units' };
  const forest = { source: 'reference', 'source-layer': 'forest' };
  const exclusions = { source: 'reference', 'source-layer': 'exclusions' };
  const groups: ReferenceGroup[] = [
    { id: 'land', label: 'Workspace land', optional: true, minZoom: 0, swatch: { kind: 'land' }, layers: [
      { id: 'land', type: 'fill', ...units, filter: ['==', ['get', 'depth'], 0], paint: { 'fill-color': map.land } }
    ] },
    { id: 'forest', label: 'Forest areas', optional: true, minZoom: 0, swatch: { kind: 'forest' }, layers: [
      { id: 'forest', type: 'fill', ...forest, paint: { 'fill-color': map.forest, 'fill-opacity': forestOpacity } },
      { id: 'forest-edge', type: 'line', ...forest, paint: { 'line-color': color.textSecondary, 'line-width': 1 } }
    ] },
    { id: 'exclusions', label: 'Known heat source areas', optional: true, minZoom: 0, swatch: { kind: 'exclusions' }, layers: [
      { id: 'exclusions', type: 'fill', ...exclusions, paint: { 'fill-color': map.neutralMark, 'fill-opacity': exclusionOpacity } },
      { id: 'exclusion-edge', type: 'line', ...exclusions, paint: { 'line-color': color.text, 'line-width': 2 } }
    ] }
  ];
  for (const level of [...info.levels].sort((a, b) => b.depth - a.depth)) {
    const { hierarchy, depth } = level;
    const id = 'unit-' + hierarchy + '-' + depth;
    const name = level.labels.en || level.slug;
    const minZoom = depth < 2 ? 0 : Math.min(12, 2 + depth * 2);
    const swatch: Swatch = {
      kind: 'line', width: 4 / (1 + depth * 0.65), opacity: 1 / (1 + depth * 0.13), dashed: depth > 0,
      color: depth === 0 ? color.text : depth === 1 ? color.action : color.textSecondary
    };
    const filter: FilterSpecification = ['all', ['==', ['get', 'hierarchy'], hierarchy], ['==', ['get', 'depth'], depth]];
    groups.push({ id, label: name + ' boundaries', minZoom, optional: depth > 0, swatch, layers: [
      { id, type: 'line', ...units, filter, minzoom: minZoom,
        paint: { 'line-color': swatch.color, 'line-width': swatch.width, 'line-opacity': swatch.opacity,
          ...(depth > 0 ? { 'line-dasharray': [Math.max(2, 9 - depth * 2), 2] } : {}) } }
    ] });
    if (!info.levels.some(other => other.hierarchy === hierarchy && other.depth > depth)) {
      const labelId = 'label-' + hierarchy + '-' + depth;
      const labelZoom = Math.max(11, minZoom);
      groups.push({ id: labelId, label: name + ' names', optional: true, minZoom: labelZoom, swatch: { kind: 'label' }, layers: [
        { id: labelId, type: 'symbol', ...units, 'source-layer': 'unit_labels', filter, minzoom: labelZoom,
          layout: { 'text-field': ['get', 'name'], 'text-font': ['sans-serif'], 'text-size': 14 },
          paint: { 'text-color': color.text, 'text-halo-color': color.surface, 'text-halo-width': 1 } }
      ] });
    }
  }
  return groups;
}
