import { InjectionToken } from '@angular/core';

export interface WebConfig {
  issuer: string;
  clientId: string;
  apiBaseUrl: string;
  languages?: string[];
  workspaceLanguages?: Record<string, string>;
}

// Defined only by make web-dev in shared worktree mode.
declare const FIMRA_DEV_CONFIG: WebConfig | undefined;

export const WEB_CONFIG = new InjectionToken<WebConfig>('Web configuration');

export function isApiRequest(request: string, base: string, page: string): boolean {
  const url = new URL(request, page);
  const api = new URL(base);
  const path = api.pathname.replace(/\/$/, '');
  return url.origin === api.origin &&
    (url.pathname === path || url.pathname.startsWith(path + '/'));
}

export async function loadConfig(): Promise<WebConfig> {
  let config = typeof FIMRA_DEV_CONFIG !== 'undefined' ? FIMRA_DEV_CONFIG : undefined;
  if (!config) {
    const response = await fetch('config.json', { cache: 'no-store' });
    if (!response.ok) throw new Error('Cannot load web configuration');
    config = await response.json();
  }
  if (!config?.clientId || typeof config.clientId !== 'string') {
    throw new Error('Missing sign-in client');
  }
  for (const address of [config.issuer, config.apiBaseUrl]) {
    const url = new URL(address);
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
      throw new Error('Invalid service address');
    }
  }
  return config;
}
