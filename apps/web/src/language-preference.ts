import { Injectable, computed, effect, inject, signal } from '@angular/core';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { activateLanguage, preferredLanguage } from './language';
import { WorkspaceService } from './workspace';

@Injectable({ providedIn: 'root' })
export class LanguagePreference {
  private readonly auth = inject(AuthService);
  private readonly workspace = inject(WorkspaceService);
  private readonly config = inject(WEB_CONFIG);
  private readonly edits = signal<Record<string, string>>({});
  readonly supported = [...new Set(['en', 'hi', ...(this.config.languages ?? [])])];
  private readonly key = computed(() => {
    const user = this.auth.user();
    return user?.profile.sub ? 'fimra.language.' + user.profile.sub : '';
  });

  constructor() {
    effect(() => {
      const key = this.key();
      const slug = this.workspace.current()?.slug;
      const chosen = key && (this.edits()[key] ?? this.read(key));
      const fallback = preferredLanguage(this.supported, slug ? this.config.workspaceLanguages?.[slug] : undefined);
      void activateLanguage(chosen && this.supported.includes(chosen) ? chosen : fallback);
    });
  }

  choose(code: string): void {
    const key = this.key();
    if (!key || (code && !this.supported.includes(code))) return;
    this.edits.update(previous => ({ ...previous, [key]: code }));
    try {
      if (code) localStorage.setItem(key, code);
      else localStorage.removeItem(key);
    } catch {
      // The selection still applies while the app is open.
    }
  }

  private read(key: string): string | null {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  }
}
