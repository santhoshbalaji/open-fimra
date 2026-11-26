import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { RouterTestingHarness } from '@angular/router/testing';
import { describe, expect, it } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { ShellNavigation } from './shell-navigation';
import { shellRoutes } from './shell-routes';
import { WorkspaceService } from './workspace';

@Component({ template: '' })
class Screen {}

describe('Shell routes', () => {
  function prepare(role: string, path: string) {
    TestBed.configureTestingModule({ providers: [
      provideHttpClient(), provideHttpClientTesting(),
      provideRouter(shellRoutes.map(route => route.loadComponent ?
        { ...route, loadComponent: undefined, component: Screen } : route)),
      ShellNavigation,
      { provide: AuthService, useValue: { user: signal({ profile: { sub: 'synthetic' } }) } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } }
    ] });
    TestBed.inject(WorkspaceService);
    TestBed.tick();
    TestBed.inject(HttpTestingController).expectOne('https://api.test/api/v1/me').flush({
      roleAssignments: role ? [{ role, unit: { path }, workspace: { slug: 'demo', name: 'Demo State' } }] : []
    });
    return { router: TestBed.inject(Router), navigation: TestBed.inject(ShellNavigation) };
  }

  it.each([
    ['state.admin', 'state_admin', 'ds', true, true],
    ['division.officer', 'division_officer', 'ds.c1.d1', false, true],
    ['beat.guard', 'field_staff', 'ds.c1.d1.r1.b1', false, false],
    ['no.role', '', '', false, false]
  ])('filters routes for %s', async (_, role, path, layers, forms) => {
    const { router, navigation } = prepare(role, path);
    const harness = await RouterTestingHarness.create('/');
    expect(router.url).toBe(role ? '/monitor/map?workspace=demo' : '/');
    const links = navigation.authorized().map(item => item.name);
    expect(links.includes('Layers')).toBe(layers);
    expect(links.includes('Setup')).toBe(layers);
    expect(links.includes('Form templates')).toBe(forms);
    expect(links.includes('GIS link')).toBe(!!role);
    expect(links.includes('Field phones')).toBe(!!role);
    expect(links.includes('Field reports')).toBe(!!role);
    await harness.navigateByUrl('/settings/layers');
    expect(router.url).toBe(layers ? '/settings/layers?workspace=demo' : role ? '/restricted' : '/');
    await harness.navigateByUrl('/settings/forms');
    expect(router.url).toBe(forms ? '/settings/forms?workspace=demo' : role ? '/restricted' : '/');
    await harness.navigateByUrl('/reports/field');
    expect(router.url).toBe(role ? '/reports/field?workspace=demo' : '/');
  });

  it('restores a legacy alert link and permits only in-scope filters', async () => {
    const { router } = prepare('division_officer', 'ds.c1.d1');
    const harness = await RouterTestingHarness.create('/?workspace=demo&alert=42&hours=036&class=outside&unitPath=ds.c1.d1.r1&token=test');
    expect(router.url).toBe('/monitor/alerts?workspace=demo&unitPath=ds.c1.d1.r1&hours=36&class=outside&alert=42');
    await harness.navigateByUrl('/reports/field?workspace=demo&unitPath=ds.c2&decision=approved');
    expect(router.url).toBe('/reports/field?workspace=demo&decision=approved');
    await harness.navigateByUrl('/monitor/alerts?workspace=other&alert=42');
    expect(router.url).toBe('/restricted');
  });

  it('preserves authorized legacy feed-health links', async () => {
    const { router } = prepare('state_admin', 'ds');
    await RouterTestingHarness.create('/?workspace=demo&view=feed-health');
    expect(router.url).toBe('/dashboard?workspace=demo&view=feed-health');
  });
});
