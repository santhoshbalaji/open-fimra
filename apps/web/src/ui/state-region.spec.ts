import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { DataState, StateRegion } from './state-region';

@Component({
  imports: [StateRegion],
  template: `<fimra-state-region [state]="state()" (retry)="retry()"><p id="data">Demo alert list</p></fimra-state-region>`
})
class Host {
  readonly state = signal<DataState>({ kind: 'ready' });
  readonly retry = vi.fn();
}

describe('StateRegion', () => {
  it('announces five states politely, shows the last data during refresh and offers a retry', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    const element: HTMLElement = fixture.nativeElement;
    const status = element.querySelector('[role=status]')!;
    const content = element.querySelector('#data')!;
    expect(status.getAttribute('aria-live')).toBe('polite');
    expect(status.getAttribute('aria-atomic')).toBe('true');
    expect(status.textContent?.trim()).toBe('');
    const variants: [DataState, string, boolean][] = [
      [{ kind: 'loading' }, 'Loading', false],
      [{ kind: 'empty' }, 'No results', false],
      [{ kind: 'forbidden' }, 'You do not have access', false],
      [{ kind: 'stale', observedAt: { dateTime: '14:05:00Z', label: '14:05 UTC' } },
        'Refreshing. Showing the last available data', true],
      [{ kind: 'error', keepContent: true }, 'Could not load data', true]
    ];
    for (const [state, announcement, visible] of variants) {
      fixture.componentInstance.state.set(state);
      fixture.detectChanges();
      expect(status.textContent?.trim()).toBe(announcement);
      expect(element.querySelector('[role=status]')).toBe(status);
      expect(element.querySelector('#data')).toBe(content);
      expect(content.parentElement?.hidden).toBe(!visible);
      if (state.kind === 'stale') {
        expect(element.querySelector('time')?.dateTime).toBe('14:05:00Z');
        expect(element.querySelector('time')?.textContent).toBe('14:05 UTC');
      }
    }
    element.querySelector('button')?.click();
    expect(fixture.componentInstance.retry).toHaveBeenCalledOnce();
    fixture.componentInstance.state.set({ kind: 'ready', announcement: 'Alert list loaded' });
    fixture.detectChanges();
    expect(status.textContent?.trim()).toBe('Alert list loaded');
    expect(element.querySelector('button')).toBeNull();
  });

  it('updates the visible timestamp without rewriting the live message', () => {
    const fixture = TestBed.createComponent(Host);
    const updateTime = (minute: string) => {
      fixture.componentInstance.state.set({
        kind: 'stale', observedAt: { dateTime: `14:${minute}:00Z`, label: `14:${minute} UTC` }
      });
      fixture.detectChanges();
    };
    updateTime('05');
    const element: HTMLElement = fixture.nativeElement;
    const status = element.querySelector('[role=status]')!;
    const observer = new MutationObserver(() => undefined);
    observer.observe(status, { childList: true, subtree: true, characterData: true });
    try {
      updateTime('06');
      expect(element.querySelector('time')?.textContent).toBe('14:06 UTC');
      expect(observer.takeRecords()).toHaveLength(0);
      fixture.componentInstance.state.set({ kind: 'error', detail: 'Check your connection.' });
      fixture.detectChanges();
      expect(observer.takeRecords().length).toBeGreaterThan(0);
      expect(status.textContent).toContain('Check your connection.');
    } finally {
      observer.disconnect();
    }
  });
});
