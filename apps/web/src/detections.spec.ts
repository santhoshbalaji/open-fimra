import { describe, expect, it, vi } from 'vitest';
import { exclusionOpacity } from './map-style';
import { ageColours, forestOpacity, interfaceColours, mapColours } from './token-values';
import { confidenceLabel, detectionLayer, mapRequest, placesOf, readDetection, sourceLabel, symbolStyle, tileErrorStatus, tileUrl } from './detections';

function rgb(color: string): number[] {
  return color.slice(1).match(/../g)!.map(channel => parseInt(channel, 16) / 255);
}

function mix(a: number[], b: number[], alpha: number): number[] {
  return a.map((channel, i) => channel * (1 - alpha) + b[i] * alpha);
}

function contrast(a: number[], b: number[]): number {
  const luminance = (channels: number[]) => channels.map(channel => channel <= 0.04045
    ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
    .reduce((sum, channel, i) => sum + channel * [0.2126, 0.7152, 0.0722][i], 0);
  const [dark, light] = [luminance(a), luminance(b)].sort((x, y) => x - y);
  return (light + 0.05) / (dark + 0.05);
}

describe('Detection symbols', () => {
  it.each(['light', 'dark'] as const)('uses the %s palette and distinct class shapes', theme => {
    for (const [age, color] of ageColours[theme].entries()) {
      expect(symbolStyle('inside', age, theme)).toMatchObject({ shape: 'circle', color });
      expect(symbolStyle('near_edge', age, theme)).toMatchObject({ shape: 'ring', color });
      expect(symbolStyle('fixed_heat_source', age, theme)).toMatchObject({ shape: 'square', color: mapColours[theme].neutralMark });
      expect(symbolStyle('outside', age, theme)).toEqual({ shape: 'circle', radius: 3, color: mapColours[theme].neutralMark });
    }
    expect(detectionLayer(theme).layout?.['symbol-sort-key']).toEqual(['get', 'observed_at']);
  });
  it.each(['light', 'dark'] as const)('keeps every mark and selection visible on %s map surfaces', theme => {
    const { canvas, land, forest, neutralMark, ageOutline, selection } = mapColours[theme];
    const surfaces = [
      rgb(canvas), rgb(land), mix(rgb(land), rgb(forest), forestOpacity),
      mix(rgb(canvas), rgb(forest), forestOpacity), rgb(interfaceColours[theme].selected)
    ];
    for (const surface of [...surfaces, ...surfaces.map(base => mix(base, rgb(neutralMark), exclusionOpacity))]) {
      for (const fill of [...ageColours[theme], neutralMark, selection]) {
        // The stroke is continuous even when an age fill blends with the background.
        expect(Math.max(contrast(rgb(fill), surface), contrast(rgb(ageOutline), surface))).toBeGreaterThanOrEqual(3);
      }
      expect(contrast(rgb(ageOutline), surface)).toBeGreaterThanOrEqual(3);
    }
    expect(contrast(rgb(selection), rgb(land))).toBeGreaterThanOrEqual(3);
  });
});

describe('Detection identity', () => {
  it('keeps the point coordinates for the resized map when opening details', () => {
    expect(readDetection({ id: 42, properties: {}, geometry: { type: 'Point', coordinates: [86, 16] } }))
      .toMatchObject({ id: 42, longitude: 86, latitude: 16 });
  });

  it('reads both property IDs and native tile feature IDs', () => {
    expect(readDetection({ id: 12, properties: { id: 'sample' } }).id).toBe('sample');
    expect(readDetection({ id: 12, properties: {} }).id).toBe(12);
  });
});

describe('Tile requests', () => {
  const base = 'https://api.example.test/core/';
  const page = 'https://web.example.test';
  const url = tileUrl(base, 'demo state', 72, 'inside', 'ds.c1');
  it('sends the shared window, forest class and unit path', () => {
    expect(url).toBe(base + 'api/v1/workspaces/demo%20state/tiles/detections/{z}/{x}/{y}.mvt?hours=72&classes=inside&unitPath=ds.c1');
  });
  it('recognises rejected tile responses across the worker boundary', () => {
    expect(tileErrorStatus({ status: 403, message: 'Forbidden' })).toBe(403);
    expect(tileErrorStatus({ message: 'AJAXError: Unauthorized (401): ' + url })).toBe(401);
    expect(tileErrorStatus({ message: 'Failed to fetch' })).toBeUndefined();
  });
  it('reads the current token for API tiles only', () => {
    const token = vi.fn(() => 'first');
    for (const asset of ['/map/maplibre-gl-worker.mjs', base + '../other', 'https://api.example.test.evil.test/core/']) {
      expect(mapRequest(asset, base, page, token)).toEqual({ url: asset });
    }
    expect(token).not.toHaveBeenCalled();
    expect(mapRequest(url, base, page, token).headers).toEqual({ Authorization: 'Bearer first' });
    token.mockReturnValue('renewed');
    expect(mapRequest(url, base, page, token).headers).toEqual({ Authorization: 'Bearer renewed' });
    expect(mapRequest(url, base, page, () => undefined).headers).toBeUndefined();
  });
});

describe('Places and confidence', () => {
  const levels = [
    { hierarchy: 'territorial', depth: 0, slug: 'state', labels: { en: 'State' } },
    { hierarchy: 'territorial', depth: 1, slug: 'division', labels: { en: 'District' } },
    { hierarchy: 'territorial', depth: 2, slug: 'beat', labels: { en: 'Sub-district' } },
    { hierarchy: 'other', depth: 1, slug: 'zone', labels: { en: 'Zone' } }
  ];

  it('labels places with the workspace levels, smallest first and without the workspace itself', () => {
    const places = { state: { code: 'S', name: 'Demo' }, division: { code: 'D1', name: 'Hill' }, beat: { code: 'B5', name: 'Beat 05' } };
    expect(placesOf({ place_hierarchy: 'territorial', places }, levels)).toEqual([
      { label: 'Sub-district', name: 'Beat 05', code: 'B5' },
      { label: 'District', name: 'Hill', code: 'D1' }
    ]);
    expect(placesOf({ place_hierarchy: 'territorial', places: JSON.stringify(places) }, levels)[1].name).toBe('Hill');
  });

  it('keeps a level with no matched unit and falls back when the levels are not loaded', () => {
    expect(placesOf({ place_hierarchy: 'territorial', places: { division: { name: 'Hill' } } }, levels)[0])
      .toEqual({ label: 'Sub-district', name: '', code: '' });
    expect(placesOf({ place_hierarchy: 'territorial', places: '{"beat":{"name":"B"},"range":{"name":"R"}}' }, []).map(p => p.label))
      .toEqual(['Beat', 'Range']);
    expect(placesOf({ places: 'not json' }, levels)).toEqual([]);
  });

  it('names detection sources', () => {
    expect(['firms', 'upload', '', undefined].map(sourceLabel)).toEqual(['NASA FIRMS', 'upload', 'Unknown', 'Unknown']);
  });

  it('names satellite confidence in words', () => {
    expect(['l', 'n', 'h', 'N', 62, '80', '', null, undefined, 'odd'].map(confidenceLabel))
      .toEqual(['Low', 'Nominal', 'High', 'Nominal', '62%', '80%', 'Unknown', 'Unknown', 'Unknown', 'odd']);
  });
});
