import { Injectable, computed, signal } from '@angular/core';
import { classes, ForestClass } from './detections';

const storageKey = 'fimra.lowBandwidth';
export const defaultDashboardHours = 168;

@Injectable({ providedIn: 'root' })
export class DashboardFilters {
  readonly hours = signal(defaultDashboardHours);
  readonly forestClass = signal<ForestClass | ''>('');
  readonly unitPath = signal('');
  readonly lowBandwidth = signal(false);
  readonly params = computed(() => {
    const params: Record<string, string | number> = {hours: this.hours()};
    if (this.forestClass()) params['class'] = this.forestClass();
    if (this.unitPath()) params['unitPath'] = this.unitPath();
    return params;
  });
  readonly tileClasses = computed(() => this.forestClass() || Object.keys(classes).join(','));

  constructor() {
    try { this.lowBandwidth.set(localStorage.getItem(storageKey) === 'true'); } catch { /* Storage is optional. */ }
  }

  setLowBandwidth(enabled: boolean): void {
    this.lowBandwidth.set(enabled);
    try { localStorage.setItem(storageKey, String(enabled)); } catch { /* Keep the session preference. */ }
  }
}
