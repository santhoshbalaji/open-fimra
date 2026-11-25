import { afterEach, describe, expect, it, vi } from 'vitest';
import { isApiRequest, loadConfig } from './config';

describe('Bearer token destination', () => {
  const base = 'https://api.example.test/v1/';
  const page = 'https://web.example.test/';
  it.each([
    [base, true],
    ['https://api.example.test/v1', true],
    [base + 'units?offset=2', true],
    ['https://api.example.test/v10/units', false],
    ['https://api.example.test.evil.test/v1', false],
    ['http://api.example.test/v1', false],
    ['https://api.example.test:8443/v1', false],
    [base + '../outside', false],
    ['/demo-state/beats.geojson', false],
  ])('checks %s', (url, allowed) => {
    expect(isApiRequest(url, base, page)).toBe(allowed);
  });
});

describe('Runtime config', () => {
  afterEach(() => vi.unstubAllGlobals());
  const config = { issuer: 'https://login.example.test/realm', clientId: 'web', apiBaseUrl: 'https://api.example.test' };

  it('loads the public settings', async () => {
    vi.stubGlobal('fetch', async () => Response.json(config));
    expect(await loadConfig()).toEqual(config);
  });

  it('rejects an incomplete sign-in configuration', async () => {
    vi.stubGlobal('fetch', async () => Response.json({ ...config, clientId: '' }));
    await expect(loadConfig()).rejects.toThrow('Missing sign-in client');
  });
});
