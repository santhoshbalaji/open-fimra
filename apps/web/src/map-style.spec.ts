import { describe, expect, it } from 'vitest';
import { referenceGroups, type WorkspaceMap } from './map-style';

const info: WorkspaceMap = {
  version: 'test', boundary: [80, 20, 81, 21],
  levels: ['Region', 'District', 'Sub-district'].map((en, depth) => ({
    hierarchy: 'territorial', depth, slug: ['state', 'division', 'beat'][depth], labels: { en }
  }))
};

describe('Workspace reference layers', () => {
  it.each(['light', 'dark'] as const)('matches the %s legend to the rendered layers', theme => {
    const groups = referenceGroups(info, theme);
    expect(groups.filter(group => !group.optional).map(group => group.label)).toEqual(['Region boundaries']);
    const unitGroups = groups.filter(group => group.swatch?.kind === 'line');
    expect(unitGroups.map(group => group.label)).toEqual(['Sub-district boundaries', 'District boundaries', 'Region boundaries']);
    for (const group of unitGroups) {
      expect(group.layers[0]).toMatchObject({ type: 'line', minzoom: group.minZoom, source: 'reference' });
    }
    expect(groups.find(group => group.id === 'label-territorial-2')).toMatchObject({
      label: 'Sub-district names', minZoom: 11, layers: [{ type: 'symbol', minzoom: 11, 'source-layer': 'units' }]
    });
  });
});
