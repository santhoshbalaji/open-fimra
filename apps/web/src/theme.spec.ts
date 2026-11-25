import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ThemeService } from './theme';

describe('ThemeService', () => {
  let system: EventTarget & { matches: boolean };

  beforeEach(() => {
    system = Object.assign(new EventTarget(), { matches: false });
    vi.stubGlobal('matchMedia', () => system);
    localStorage.clear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('follows system changes until the user chooses a theme', () => {
    const service = TestBed.inject(ThemeService);
    expect(service.theme()).toBe('light');
    system.matches = true;
    system.dispatchEvent(new Event('change'));
    expect(service.theme()).toBe('dark');
    service.toggle();
    system.dispatchEvent(new Event('change'));
    expect(service.theme()).toBe('light');
    expect(localStorage.getItem('fimra.theme')).toBe('light');
    TestBed.tick();
    expect(document.documentElement.dataset['theme']).toBe('light');
  });

  it('ignores an unknown saved theme', () => {
    localStorage.setItem('fimra.theme', 'sepia');
    system.matches = true;
    expect(TestBed.inject(ThemeService).theme()).toBe('dark');
  });

  it('restores a saved choice over the system setting', () => {
    localStorage.setItem('fimra.theme', 'dark');
    expect(TestBed.inject(ThemeService).theme()).toBe('dark');
  });

  it('can switch themes when storage access is blocked', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error(); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error(); });
    const service = TestBed.inject(ThemeService);
    service.toggle();
    expect(service.theme()).toBe('dark');
  });
});
