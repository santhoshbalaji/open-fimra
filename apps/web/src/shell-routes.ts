import { Component, inject } from '@angular/core';
import { toObservable } from '@angular/core/rxjs-interop';
import { RedirectCommand, Router, type CanActivateFn, type Routes } from '@angular/router';
import { filter, map, take } from 'rxjs';
import { AuthService } from './auth';
import { allowedParams, allowsDestination, destinations, homePath, recordId, type DestinationPath } from './shell-navigation';
import { WorkspaceService } from './workspace';

@Component({ template: '' })
class Landing {}

export const shellGuard: CanActivateFn = (route, state) => {
  const auth = inject(AuthService);
  const workspace = inject(WorkspaceService);
  const router = inject(Router);
  const input = route.queryParams;
  const path = state.url.split('?')[0];
  const redirect = (target: string, queryParams = {}) =>
    new RedirectCommand(router.createUrlTree([target], { queryParams }));

  if (!auth.user()) return route.data['landing'] === true;
  return toObservable(workspace.profileReady).pipe(
    filter(Boolean),
    take(1),
    map(() => {
      if (workspace.failed()) return route.data['landing'] === true;
      if (typeof input['workspace'] === 'string' && workspace.current() && !workspace.select(input['workspace'])) {
        return redirect('/restricted');
      }
      if (!workspace.current()) return route.data['landing'] === true || redirect('/');
      if (path === '/restricted') return true;
      let target: DestinationPath;
      if (route.data['landing']) {
        target = recordId(route.queryParamMap.get('alert')) ? '/monitor/alerts' :
          input['view'] === 'feed-health' ? '/dashboard' : homePath(workspace);
      } else {
        target = path as DestinationPath;
      }
      const destination = destinations.find(item => item.path === target);
      if (!destination || !allowsDestination(workspace, destination) ||
          (target === '/dashboard' && input['view'] === 'feed-health' && !workspace.canManageLayers())) return redirect('/restricted');
      const params = allowedParams(target, input, workspace);
      if (target === path && Object.keys(input).length === Object.keys(params).length &&
          Object.keys(params).every(key => input[key] === params[key])) return true;
      return redirect(target, params);
    })
  );
};

export const shellRoutes: Routes = [
  { path: '', component: Landing, pathMatch: 'full', data: { landing: true }, canActivate: [shellGuard], runGuardsAndResolvers: 'always' },
  { path: 'restricted', component: Landing },
  { matcher: segments => segments.length === 2 && segments[0].path === 'monitor' &&
      ['map', 'alerts'].includes(segments[1].path) ? { consumed: segments } : null,
    canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./demo-map').then(module => module.DemoMap) },
  { path: 'monitor/incidents', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./shell-incidents').then(module => module.IncidentScreen) },
  { path: 'reports/field', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./shell-field-reports').then(module => module.FieldReportScreen) },
  { path: 'reports/situation', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./situation-report').then(module => module.SituationReportView) },
  { path: 'dashboard', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./dashboard').then(module => module.Dashboard) },
  { path: 'settings/layers', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./layers').then(module => module.Layers) },
  { path: 'settings/forms', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./forms/forms-screen').then(module => module.FormsScreen) },
  { path: 'settings/setup', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./shell-setup').then(module => module.SetupScreen) },
  { path: 'settings/gis', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./access-keys').then(module => module.AccessKeys) },
  { path: 'settings/devices', canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./devices').then(module => module.Devices) },
  { path: '**', redirectTo: '' }
];
