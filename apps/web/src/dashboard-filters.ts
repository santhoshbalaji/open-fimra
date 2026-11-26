import { workspaceFormat } from './workspace-format';
import { translate } from './language';
import { HttpClient, type HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, effect, inject, signal } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { classes, ForestClass } from './detection-classes';
import type { ShellNavigation } from './shell-navigation';
import type { ReportUnit } from './situation-report';
import { WorkspaceService } from './workspace';

import { DashboardFilters, defaultDashboardHours } from './dashboard-filter-state';
export { DashboardFilters, defaultDashboardHours } from './dashboard-filter-state';

export const filterPeriods = [
  { hours: 24, label: 'Last 24 hours' },
  { hours: 72, label: 'Last 3 days' },
  { hours: defaultDashboardHours, label: 'Last 7 days' },
  { hours: 720, label: 'Last 30 days' }
];

export function filterPeriodLabel(hours: number): string {
  const preset = filterPeriods.find(period => period.hours === hours);
  if (preset) return translate(preset.label);
  return hours % 24 === 0
    ? translate('Last {0} days', [workspaceFormat.number(hours / 24)])
    : translate(hours === 1 ? 'Last {0} hour' : 'Last {0} hours', [workspaceFormat.number(hours)]);
}

export type ScopeFilterKey = 'hours' | 'class' | 'unitPath';

interface UnitChoice { path: string; label: string }

@Injectable({ providedIn: 'root' })
export class ScopeFilters {
  private readonly filters = inject(DashboardFilters);
  private readonly workspace = inject(WorkspaceService);
  private readonly auth = inject(AuthService);
  private readonly http = inject(HttpClient);
  private readonly base = inject(WEB_CONFIG).apiBaseUrl.replace(/\/$/, '');
  private readonly attempt = signal(0);
  private readonly result = signal<{ slug: string; units: readonly UnitChoice[]; failed: boolean } | null>(null);
  private readonly current = computed(() => {
    const result = this.result();
    return result?.slug === this.workspace.current()?.slug ? result : null;
  });

  readonly units = computed(() => this.current()?.units ?? []);
  readonly loading = computed(() => !this.current());
  readonly failed = computed(() => this.current()?.failed ?? false);
  readonly unitLabel = computed(() => {
    const path = this.filters.unitPath();
    if (!path) return translate('All assigned units');
    return this.units().find(unit => unit.path === path)?.label ??
      this.workspace.jurisdictions().find(([grant]) => grant === path)?.[1] ?? 'Selected unit';
  });
  readonly active = computed(() => {
    const active: { id: ScopeFilterKey; label: string }[] = [];
    const hours = this.filters.hours();
    if (hours !== defaultDashboardHours) active.push({ id: 'hours', label: filterPeriodLabel(hours) });
    const forest = this.filters.forestClass();
    if (forest) active.push({ id: 'class', label: classes[forest] });
    if (this.filters.unitPath()) active.push({ id: 'unitPath', label: this.unitLabel() });
    return active;
  });

  constructor() {
    effect(onCleanup => {
      const workspace = this.workspace.current();
      this.attempt();
      this.result.set(null);
      if (!workspace) return;
      const endpoint = this.base + '/api/v1/workspaces/' + encodeURIComponent(workspace.slug);
      const request = this.http.get<{ units: ReportUnit[] }>(endpoint + '/situation-report/units').subscribe({
        next: ({ units }) => {
          // The shared URL selects a path. If hierarchies share it, show each name.
          const names = new Map<string, Set<string>>();
          for (const { path, name, levelLabel } of units) {
            if (!this.workspace.allowsScope(path)) continue;
            const labels = names.get(path) ?? new Set<string>();
            labels.add(levelLabel ? `${name} (${levelLabel})` : name);
            names.set(path, labels);
          }
          this.result.set({ slug: workspace.slug,
            units: [...names].map(([path, labels]) => ({ path, label: [...labels].join(' / ') })), failed: false });
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
          this.result.set({ slug: workspace.slug, units: [], failed: true });
        }
      });
      onCleanup(() => request.unsubscribe());
    });
  }

  retry(): void {
    this.attempt.update(value => value + 1);
  }
}

export function setScopeFilters(
  filters: DashboardFilters, navigation: ShellNavigation | null, changes: Partial<Record<ScopeFilterKey, string | null>>
): void {
  if (navigation) {
    navigation.update({ ...changes, cursor: null, alertTrail: null, reportPages: null,
      alert: null, detection: null, incident: null, member: null, report: null });
    return;
  }
  if ('hours' in changes) filters.hours.set(Number(changes.hours ?? defaultDashboardHours));
  if ('class' in changes) filters.forestClass.set((changes.class ?? '') as ForestClass | '');
  if ('unitPath' in changes) filters.unitPath.set(changes.unitPath ?? '');
}
