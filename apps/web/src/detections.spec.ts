import { describe, expect, it, vi } from 'vitest';
import tokens from '../../../docs/design-system/tokens.json';
import { detectionLayer, mapRequest, readDetection, symbolStyle, tileErrorStatus, tileUrl } from './detections';

describe('Detection symbols', () => {
  it.each(['light', 'dark'] as const)('uses the %s palette and distinct class shapes', theme => {
    for (const [age, color] of tokens.color.fireAge[theme].entries()) {
      expect(symbolStyle('inside', age, theme)).toMatchObject({ shape: 'circle', color });
      expect(symbolStyle('near_edge', age, theme)).toMatchObject({ shape: 'ring', color });
      expect(symbolStyle('fixed_heat_source', age, theme)).toMatchObject({ shape: 'square', color: tokens.color[theme].neutralMark });
      expect(symbolStyle('outside', age, theme)).toEqual({ shape: 'circle', radius: 3, color: tokens.color[theme].neutralMark });
    }
    expect(detectionLayer(theme).layout?.['symbol-sort-key']).toEqual(['get', 'observed_at']);
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
    for (const asset of ['/demo-state/state.geojson', base + '../other', 'https://api.example.test.evil.test/core/']) {
      expect(mapRequest(asset, base, page, token)).toEqual({ url: asset });
    }
    expect(token).not.toHaveBeenCalled();
    expect(mapRequest(url, base, page, token).headers).toEqual({ Authorization: 'Bearer first' });
    token.mockReturnValue('renewed');
    expect(mapRequest(url, base, page, token).headers).toEqual({ Authorization: 'Bearer renewed' });
    expect(mapRequest(url, base, page, () => undefined).headers).toBeUndefined();
  });
});
