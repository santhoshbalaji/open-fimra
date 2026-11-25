import { DestroyRef, Injectable, effect, inject, signal } from '@angular/core';

export type Theme = 'light' | 'dark';
const key = 'fimra.theme';

function savedTheme(): Theme | undefined {
  try {
    const value = localStorage.getItem(key);
    if (value === 'light' || value === 'dark') return value;
  } catch {
    // Storage can be blocked in private browsing.
  }
  return undefined;
}

@Injectable({ providedIn: 'root' })
export class ThemeService {
  private readonly system = matchMedia('(prefers-color-scheme: dark)');
  private choice = savedTheme();
  readonly theme = signal<Theme>(this.choice ?? this.systemTheme());

  constructor() {
    const followSystem = () => this.theme.set(this.choice ?? this.systemTheme());
    this.system.addEventListener('change', followSystem);
    inject(DestroyRef).onDestroy(() => this.system.removeEventListener('change', followSystem));
    effect(() => { document.documentElement.dataset['theme'] = this.theme(); });
  }

  toggle(): void {
    this.choice = this.theme() === 'light' ? 'dark' : 'light';
    this.theme.set(this.choice);
    try {
      localStorage.setItem(key, this.choice);
    } catch {
      // Keep the choice for this page when the browser cannot save it.
    }
  }

  private systemTheme(): Theme {
    return this.system.matches ? 'dark' : 'light';
  }
}
