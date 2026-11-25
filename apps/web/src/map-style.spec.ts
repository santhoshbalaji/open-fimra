import { describe, expect, it } from 'vitest';
import { boundaryKey, referenceLayers, type WorkspaceMap } from './map-style';

const info: WorkspaceMap = {
  version: 'test', boundary: [80, 20, 81, 21],
  levels: ['Region', 'District', 'Sub-district'].map((en, depth) => ({
    hierarchy: 'territorial', depth, slug: ['state', 'division', 'beat'][depth], labels: { en }
  }))
};

describe('Workspace reference layers', () => {
  it.each(['light', 'dark'] as const)('uses workspace labels and depth in %s mode', theme => {
    const rows = boundaryKey(info.levels, theme);
    expect(rows.map(row => row.labels.en)).toEqual(['Region', 'District', 'Sub-district']);
    expect(rows[0].width).toBeGreaterThan(rows[1].width);
    expect(rows[1].width).toBeGreaterThan(rows[2].width);
    const layers = referenceLayers(info, theme, false);
    expect(layers.filter(layer => layer.type === 'symbol')).toMatchObject([
      { id: 'label-territorial-2', minzoom: 11, 'source-layer': 'units' }
    ]);
    expect(referenceLayers(info, theme, true)).toMatchObject([
      { id: 'unit-territorial-0', type: 'line', source: 'reference' }
    ]);
  });
});
