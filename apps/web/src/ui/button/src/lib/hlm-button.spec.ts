import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { HlmButton } from './hlm-button';

@Component({
  imports: [HlmButton],
  template: `
    <form (submit)="submit($event)">
      <button hlmBtn [busy]="busy()" (click)="activate()">Save</button>
    </form>
  `
})
class Host {
  readonly busy = signal(false);
  readonly activate = vi.fn();
  readonly submit = vi.fn((event: Event) => event.preventDefault());
}

describe('HlmButton', () => {
  it('keeps the name and focus, and blocks activation and submission while busy', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    const button: HTMLButtonElement = fixture.nativeElement.querySelector('button');
    button.focus();
    fixture.componentInstance.busy.set(true);
    fixture.detectChanges();
    expect(button.getAttribute('aria-busy')).toBe('true');
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.textContent).toBe('Save');
    expect(document.activeElement).toBe(button);
    button.click();
    expect(fixture.componentInstance.activate).not.toHaveBeenCalled();
    expect(fixture.componentInstance.submit).not.toHaveBeenCalled();
    fixture.componentInstance.busy.set(false);
    fixture.detectChanges();
    button.click();
    expect(fixture.componentInstance.activate).toHaveBeenCalledOnce();
    expect(fixture.componentInstance.submit).toHaveBeenCalledOnce();
  });
});
