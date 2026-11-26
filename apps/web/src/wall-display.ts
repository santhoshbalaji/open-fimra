import { Injectable, signal } from '@angular/core';

const storageKey = 'fimra.wallDisplay';

function savedChoice(): boolean {
  try {
    return localStorage.getItem(storageKey) === 'true';
  } catch {
    return false;
  }
}

@Injectable({ providedIn: 'root' })
export class WallDisplay {
  private readonly choice = signal(savedChoice());
  readonly enabled = this.choice.asReadonly();

  toggle(): void {
    const enabled = !this.enabled();
    this.choice.set(enabled);
    try {
      localStorage.setItem(storageKey, String(enabled));
    } catch {
      // Keep the choice for this page when storage is blocked.
    }
  }
}
