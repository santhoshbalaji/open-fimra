import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ShellSidebar } from './shell-sidebar';

describe('ShellSidebar', () => {
  function viewport(width: number, height: number): void {
    vi.stubGlobal('innerWidth', width);
    vi.stubGlobal('innerHeight', height);
    window.dispatchEvent(new Event('resize'));
  }

  beforeEach(() => {
    localStorage.removeItem('fimra.sidebar');
    TestBed.configureTestingModule({ providers: [ShellSidebar] });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.removeItem('fimra.sidebar');
  });

  it.each([
    [2560, 1440, 'expanded'],
    [1920, 1080, 'expanded'],
    [1536, 864, 'expanded'],
    [1366, 768, 'rail'],
    [1280, 800, 'rail'],
    [1024, 768, 'rail'],
    [768, 1024, 'drawer'],
    [390, 844, 'drawer'],
    [844, 390, 'drawer']
  ])('defaults to %s × %s as %s', (width, height, mode) => {
    viewport(width, height);
    const sidebar = TestBed.inject(ShellSidebar);
    expect(sidebar.drawer()).toBe(mode === 'drawer');
    if (!sidebar.drawer()) expect(sidebar.collapsed()).toBe(mode === 'rail');
  });

  it('follows the viewport until the user chooses a layout, and remembers the choice', () => {
    viewport(1366, 768);
    const sidebar = TestBed.inject(ShellSidebar);
    expect(sidebar.collapsed()).toBe(true);
    viewport(1920, 1080);
    expect(sidebar.collapsed()).toBe(false);
    sidebar.toggle();
    expect(sidebar.collapsed()).toBe(true);
    expect(localStorage.getItem('fimra.sidebar')).toBe('collapsed');
    viewport(768, 1024);
    expect(sidebar.drawer()).toBe(true);
    sidebar.toggle();
    expect(localStorage.getItem('fimra.sidebar')).toBe('collapsed');
    viewport(1920, 1080);
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({ providers: [ShellSidebar] });
    expect(TestBed.inject(ShellSidebar).collapsed()).toBe(true);
  });

  it('falls back to the viewport for invalid saved data', () => {
    viewport(1920, 1080);
    localStorage.setItem('fimra.sidebar', 'invalid');
    expect(TestBed.inject(ShellSidebar).collapsed()).toBe(false);
  });

  it('toggles for the current tab even when storage is unavailable', () => {
    viewport(1366, 768);
    const unavailable = () => { throw new Error('Storage unavailable'); };
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(unavailable);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(unavailable);
    const sidebar = TestBed.inject(ShellSidebar);
    expect(sidebar.collapsed()).toBe(true);
    sidebar.toggle();
    expect(sidebar.collapsed()).toBe(false);
  });
});
