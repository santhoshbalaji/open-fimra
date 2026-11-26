import { Component, Injectable, inject } from '@angular/core';
import { toObservable } from '@angular/core/rxjs-interop';
import { Title } from '@angular/platform-browser';
import { RedirectCommand, Router, TitleStrategy, type CanActivateFn, type Route, type RouterStateSnapshot, type Routes } from '@angular/router';
import { filter, map, take } from 'rxjs';
import { AuthService } from './auth';
import { allowedParams, allowsDestination, destinationAt, homePath, recordId, submissionId, type DestinationPath } from './shell-navigation';
import { WorkspaceService } from './workspace';

@Component({ template: '' })
class Landing {}

@Injectable()
export class ShellTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);
  private readonly workspace = inject(WorkspaceService);

  override updateTitle(state: RouterStateSnapshot): void {
    const page = this.buildTitle(state);
    const params = state.root.queryParamMap;
    const alert = recordId(params.get('alert'));
    const detection = recordId(params.get('detection'));
    const incident = recordId(params.get('incident'));
    const report = submissionId(params.get('report'));
    const member = recordId(params.get('member'));
    const selection = alert || member ? `Alert #${alert || member}` :
      detection ? `Detection #${detection}` : incident ? `Incident #${incident}` : report ? `Report ${report}` : '';
    const landing = this.workspace.failed() ? 'Workspace unavailable' :
      this.workspace.bootstrapAvailable() ? 'First-run setup' : 'Restricted access';
    const record = page && page !== 'Restricted access' ? selection : '';
    this.title.setTitle([page ?? landing, record, 'Open FIMRA'].filter(Boolean).join(' · '));
  }
}

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
      const destination = destinationAt(target);
      if (!destination || !allowsDestination(workspace, destination) ||
          (target === '/dashboard' && input['view'] === 'feed-health' && !workspace.canManageLayers())) return redirect('/restricted');
      const params = allowedParams(target, input, workspace);
      if (target === path && Object.keys(input).length === Object.keys(params).length &&
          Object.keys(params).every(key => input[key] === params[key])) return true;
      return redirect(target, params);
    })
  );
};

function destinationRoute(path: DestinationPath, loadComponent: Route['loadComponent']): Route {
  return { path: path.slice(1), title: destinationAt(path)?.name, canActivate: [shellGuard], runGuardsAndResolvers: 'always', loadComponent };
}

export const shellRoutes: Routes = [
  { path: '', component: Landing, pathMatch: 'full', data: { landing: true }, canActivate: [shellGuard], runGuardsAndResolvers: 'always' },
  { path: 'restricted', title: 'Restricted access', component: Landing },
  { matcher: segments => segments.length === 2 && segments[0].path === 'monitor' &&
      ['map', 'alerts'].includes(segments[1].path) ? { consumed: segments } : null,
    title: route => destinationAt('/' + route.url.join('/'))!.name,
    canActivate: [shellGuard], runGuardsAndResolvers: 'always',
    loadComponent: () => import('./demo-map').then(module => module.DemoMap) },
  destinationRoute('/monitor/incidents', () => import('./shell-incidents').then(module => module.IncidentScreen)),
  destinationRoute('/reports/field', () => import('./shell-field-reports').then(module => module.FieldReportScreen)),
  destinationRoute('/reports/situation', () => import('./situation-report').then(module => module.SituationReportView)),
  destinationRoute('/dashboard', () => import('./dashboard').then(module => module.Dashboard)),
  destinationRoute('/settings/layers', () => import('./layers').then(module => module.Layers)),
  destinationRoute('/settings/forms', () => import('./forms/forms-screen').then(module => module.FormsScreen)),
  destinationRoute('/settings/setup', () => import('./shell-setup').then(module => module.SetupScreen)),
  destinationRoute('/settings/gis', () => import('./access-keys').then(module => module.AccessKeys)),
  destinationRoute('/settings/devices', () => import('./devices').then(module => module.Devices)),
  { path: '**', redirectTo: '' }
];
