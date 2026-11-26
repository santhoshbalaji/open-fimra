import { DOCUMENT } from '@angular/common';
import { DestroyRef, Injectable, computed, inject, signal } from '@angular/core';

const storageKey = 'fimra.sidebar';
type SidebarLayout = 'drawer' | 'rail' | 'expanded';

@Injectable()
export class ShellSidebar {
  private readonly browser = inject(DOCUMENT).defaultView;
  private readonly layout = signal(this.viewportLayout());
  private readonly preference = signal(this.readPreference());
  readonly drawer = computed(() => this.layout() === 'drawer');
  readonly collapsed = computed(() => this.preference() === null ?
    this.layout() !== 'expanded' : this.preference() === 'collapsed');

  constructor() {
    const onResize = () => this.layout.set(this.viewportLayout());
    this.browser?.addEventListener('resize', onResize);
    inject(DestroyRef).onDestroy(() => this.browser?.removeEventListener('resize', onResize));
  }

  toggle(): void {
    if (this.drawer()) return;
    const preference = this.collapsed() ? 'expanded' : 'collapsed';
    this.preference.set(preference);
    try {
      this.browser?.localStorage.setItem(storageKey, preference);
    } catch {
      // The choice still applies while this tab is open.
    }
  }

  private viewportLayout(): SidebarLayout {
    const width = this.browser?.innerWidth ?? 0;
    const height = this.browser?.innerHeight ?? 0;
    if (width < 768 || (width <= 1024 && height >= width) || (width < 1024 && height < 480)) return 'drawer';
    return width >= 1440 ? 'expanded' : 'rail';
  }

  private readPreference(): string | null {
    try {
      const value = this.browser?.localStorage.getItem(storageKey);
      return value === 'expanded' || value === 'collapsed' ? value : null;
    } catch {
      return null;
    }
  }
}
