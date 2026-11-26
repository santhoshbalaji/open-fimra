import { Injectable, computed, inject } from '@angular/core';
import { NavigationEnd, Router, type NavigationBehaviorOptions, type Params } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { filter, map } from 'rxjs';
import { classes } from './detection-classes';
import { WorkspaceService } from './workspace';

export const destinations = [
  { path: '/monitor/map', name: 'Map', icon: 'map', group: 'Monitor', filters: true },
  { path: '/monitor/alerts', name: 'Alerts', icon: 'triangle-alert', group: 'Monitor', filters: true },
  { path: '/monitor/incidents', name: 'Incidents', icon: 'flame', group: 'Monitor', filters: true },
  { path: '/reports/field', name: 'Field reports', icon: 'file-pen-line', group: 'Reports', filters: true },
  { path: '/reports/operations', name: 'Referrals and preparedness', icon: 'file-pen-line', group: 'Reports' },
  { path: '/reports/daily', name: 'Daily situation return', icon: 'file-chart-column', group: 'Reports' },
  { path: '/reports/situation', name: 'Situation report', icon: 'file-chart-column', group: 'Reports' },
  { path: '/dashboard', name: 'Dashboard', icon: 'layout-dashboard', group: 'Dashboard', filters: true },
  { path: '/settings/layers', name: 'Layers', icon: 'layers', group: 'Settings', grant: 'layers' },
  { path: '/settings/forms', name: 'Form templates', icon: 'list-ordered', group: 'Settings', grant: 'forms' },
  { path: '/settings/setup', name: 'Setup', icon: 'settings', group: 'Settings', grant: 'layers' },
  { path: '/settings/duty', name: 'Duty roster', icon: 'clock', group: 'Settings', grant: 'layers' },
  { path: '/settings/incident-response', name: 'Incident response', icon: 'flame', group: 'Settings', grant: 'layers' },
  { path: '/settings/gis', name: 'GIS link', icon: 'key-round', group: 'Settings' },
  { path: '/settings/devices', name: 'Field phones', icon: 'smartphone', group: 'Settings' }
] as const;

const groups = ['Monitor', 'Reports', 'Dashboard', 'Settings'] as const;

export type Destination = typeof destinations[number];
export type DestinationPath = Destination['path'];

export function destinationAt(path: string): Destination | undefined {
  return destinations.find(item => item.path === path);
}

export function allowsDestination(workspace: WorkspaceService, destination: Destination): boolean {
  return !!workspace.current() &&
    (!('grant' in destination) || (destination.grant === 'layers' ? workspace.canManageLayers() : workspace.canDesignForms()));
}

export function homePath(workspace: WorkspaceService): DestinationPath {
  if (workspace.setupPending()) return '/settings/setup';
  return '/monitor/map';
}

export function recordId(value: string | null): string {
  return value && /^[1-9][0-9]{0,18}$/.test(value) ? value : '';
}

export function submissionId(value: string | null): string {
  return value && /^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i.test(value) ? value : '';
}

export function allowedParams(path: string, input: Params, workspace: WorkspaceService): Params {
  const params: Params = {};
  const validCursor = /^[0-9T:.Z+\-,a-f]{1,128}$/;
  const current = workspace.current();
  if (current) params['workspace'] = current.slug;
  const { unitPath, hours, alert, detection, incident, report, class: forest, queue, cursor, decision, form, view } = input;
  if (typeof unitPath === 'string' && workspace.allowsScope(unitPath)) params['unitPath'] = unitPath;
  if (typeof hours === 'string' && /^\d{1,3}$/.test(hours) && +hours >= 1 && +hours <= 720) params['hours'] = String(+hours);
  if (typeof forest === 'string' && Object.hasOwn(classes, forest)) params['class'] = forest;
  if (path.startsWith('/monitor/') || path === '/reports/field') {
    if (typeof cursor === 'string' && validCursor.test(cursor)) params['cursor'] = cursor;
  }
  if (path === '/monitor/map' || path === '/monitor/alerts') {
    if (input['alertSort'] === 'oldest' || input['alertSort'] === 'overdue') params['alertSort'] = input['alertSort'];
    const trail = input['alertTrail'];
    if (params['cursor'] && typeof trail === 'string') {
      const [first, ...earlier] = trail.split('|');
      if (first === '-' && earlier.every(value => validCursor.test(value) && value !== '-')) params['alertTrail'] = trail;
    }
    if (typeof alert === 'string' && recordId(alert)) params['alert'] = alert;
    if (!params['alert'] && typeof detection === 'string' && recordId(detection)) params['detection'] = detection;
    if (queue === 'unit' || queue === 'triage' || queue === 'edge_review') {
      params['queue'] = queue;
    }
    if (queue === 'all') params['queue'] = queue;
    if (['active', 'overdue', 'pending_reports'].includes(input['group'])) params['group'] = input['group'];
    if (typeof input['status'] === 'string' && /^[a-z][a-z0-9_]{0,63}$/.test(input['status'])) {
      params['status'] = input['status'];
    }
  }
  if ((path === '/monitor/incidents' || path === '/monitor/map') && typeof incident === 'string' && recordId(incident)) params['incident'] = incident;
  if (path === '/monitor/incidents') {
    if (params['incident'] && typeof input['member'] === 'string' && recordId(input['member'])) params['member'] = input['member'];
    if (input['sort'] === 'activity-asc' || input['sort'] === 'activity-desc') params['sort'] = input['sort'];
  }
  if (path === '/reports/field') {
    const { reportSort, reportPages, reportColumns } = input;
    if (reportSort === 'oldest') params['reportSort'] = reportSort;
    if (params['cursor'] && typeof reportPages === 'string' &&
        reportPages.split('|').every((page, index) => index === 0 ? page === '' : /^[0-9T:.Z+\-,a-f]{1,128}$/.test(page))) {
      params['reportPages'] = reportPages;
    }
    if (typeof reportColumns === 'string' &&
        reportColumns.split(',').every(column => ['status', 'sender', 'received', 'alert'].includes(column))) {
      params['reportColumns'] = reportColumns;
    }
    if (typeof report === 'string' && submissionId(report)) params['report'] = report;
    if (decision === 'all' || decision === 'pending' || decision === 'approved' || decision === 'returned') params['decision'] = decision;
    if (typeof form === 'string' && /^[\w-]{1,128}$/.test(form)) params['form'] = form;
  }
  if (path === '/reports/operations') {
    const { operation, operationCase } = input;
    if (typeof operation === 'string' && submissionId(operation)) params['operation'] = operation;
    if (typeof operationCase === 'string' && recordId(operationCase)) params['operationCase'] = operationCase;
  }
  if (path === '/reports/daily') {
    if (typeof input['day'] === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(input['day'])) params['day'] = input['day'];
    if (typeof input['unit'] === 'string' && /^[\w.]{1,2000}$/.test(input['unit'])) params['unit'] = input['unit'];
  }
  if (path === '/reports/situation') {
    for (const key of ['from', 'to']) {
      if (typeof input[key] === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(input[key])) params[key] = input[key];
    }
    if (['day', 'range', 'hours'].includes(input['period'])) params['period'] = input['period'];
    if (typeof input['hierarchy'] === 'string' && /^[a-zA-Z0-9_-]{1,80}$/.test(input['hierarchy']) && params['unitPath']) {
      params['hierarchy'] = input['hierarchy'];
    }
    const hours = input['reportHours'];
    if (typeof hours === 'string' && /^\d{1,3}$/.test(hours) && +hours >= 1 && +hours <= 720) params['reportHours'] = String(+hours);
    if (input['show'] === '1') params['show'] = '1';
  }
  if (path === '/dashboard' && view === 'feed-health' && workspace.canManageLayers()) params['view'] = view;
  return params;
}

@Injectable()
export class ShellNavigation {
  private readonly router = inject(Router);
  private readonly workspace = inject(WorkspaceService);
  readonly url = toSignal(this.router.events.pipe(
    filter(event => event instanceof NavigationEnd),
    map(event => event.urlAfterRedirects)
  ), { initialValue: this.router.url });
  private readonly tree = computed(() => this.router.parseUrl(this.url()));
  readonly path = computed(() => this.url().split('?')[0]);
  readonly query = computed(() => this.tree().queryParamMap);
  readonly destination = computed(() => destinationAt(this.path()));
  readonly authorized = computed(() => destinations.filter(item => allowsDestination(this.workspace, item)));

  readonly grouped = computed(() => {
    const authorized = this.authorized();
    return groups.map(name => ({ name, items: authorized.filter(item => item.group === name) }))
      .filter(group => group.items.length > 0);
  });

  href(path: DestinationPath, extra: Params = {}) {
    const query = this.tree().queryParams;
    const shared = { workspace: query['workspace'], unitPath: query['unitPath'], hours: query['hours'], class: query['class'] };
    const destination = path === this.path() ? query : shared;
    return this.router.createUrlTree([path], { queryParams: { ...destination, ...extra } });
  }

  go(path: DestinationPath, extra: Params = {}, replaceUrl = false): void {
    void this.router.navigateByUrl(this.href(path, extra), { replaceUrl });
  }

  update(changes: Params, options: Pick<NavigationBehaviorOptions, 'state' | 'replaceUrl'> = {}): void {
    void this.router.navigate([], { queryParams: changes, queryParamsHandling: 'merge', ...options });
  }

  monitor(): void {
    this.go('/monitor/map');
  }
}
