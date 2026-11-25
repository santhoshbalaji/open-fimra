import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { noAccess, unavailable, WorkspaceService } from './workspace';

describe('Signed-in workspace', () => {
  const sessionEnded = vi.fn();
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { user: signal({ profile: { sub: 'demo' } }), sessionEnded } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
  });
  function lookup() {
    const service = TestBed.inject(WorkspaceService);
    TestBed.tick();
    const request = TestBed.inject(HttpTestingController).expectOne('https://api.test/api/v1/me');
    return { service, request };
  }
  it('shows the no-access message and supplies no tile workspace without grants', () => {
    const { service, request } = lookup();
    request.flush({ roleAssignments: [] });
    expect(service.current()).toBeNull();
    expect(service.message()).toBe(noAccess);
  });
  it('uses the first workspace and clears it when tile access is denied', () => {
    const { service, request } = lookup();
    const first = { slug: 'first', name: 'First' };
    request.flush({ roleAssignments: [{ workspace: first }, { workspace: { slug: 'second', name: 'Second' } }] });
    expect(service.current()).toEqual(first);
    service.deny();
    expect(service.current()).toBeNull();
    expect(service.message()).toBe(noAccess);
  });
  it('shows Layers only for a state administrator at the current workspace root', () => {
    const { service, request } = lookup();
    const workspace = { slug: 'demo', name: 'Demo State' };
    request.flush({ roleAssignments: [
      { workspace, role: 'state_admin', unit: { path: 'ds.c1' } },
      { workspace: { slug: 'other', name: 'Other' }, role: 'state_admin', unit: { path: 'os' } }
    ] });
    expect(service.canManageLayers()).toBe(false);
    service.current.set({ slug: 'other', name: 'Other' });
    expect(service.canManageLayers()).toBe(true);
    service.deny();
    expect(service.canManageLayers()).toBe(false);
  });
  it('retries a failed workspace lookup after one minute', () => {
    vi.useFakeTimers();
    try {
      const { service, request } = lookup();
      request.flush({}, { status: 503, statusText: 'Unavailable' });
      expect(service.message()).toBe(unavailable);
      vi.advanceTimersByTime(60_000);
      TestBed.inject(HttpTestingController).expectOne('https://api.test/api/v1/me').flush({
        roleAssignments: [{ workspace: { slug: 'demo', name: 'Demo State' } }]
      });
      expect(service.current()?.slug).toBe('demo');
      expect(service.message()).toBe('');
    } finally { vi.useRealTimers(); }
  });
  it('starts sign-in again for a rejected token', () => {
    lookup().request.flush({}, { status: 401, statusText: 'Unauthorized' });
    expect(sessionEnded).toHaveBeenCalledOnce();
  });
});
