import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it, vi } from 'vitest';
import { HlmButton } from './hlm-button';

@Component({
  imports: [HlmButton],
  template: `
    <form (submit)="submit($event)">
      <button hlmBtn [busy]="busy()" [ariaDisabled]="ariaDisabled()" (click)="activate()">Save</button>
    </form>
  `
})
class Host {
  readonly busy = signal(false);
  readonly ariaDisabled = signal(false);
  readonly activate = vi.fn();
  readonly submit = vi.fn((event: Event) => event.preventDefault());
}

describe('HlmButton', () => {
  it('blocks activation when disabled on first render and keeps the button focusable without marking it busy', () => {
    const fixture = TestBed.createComponent(Host);
    const host = fixture.componentInstance;
    host.ariaDisabled.set(true);
    fixture.detectChanges();
    const button: HTMLButtonElement = fixture.nativeElement.querySelector('button');
    button.focus();
    expect(document.activeElement).toBe(button);
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.hasAttribute('aria-busy')).toBe(false);
    button.click();
    expect(host.activate).not.toHaveBeenCalled();
    expect(host.submit).not.toHaveBeenCalled();

    host.ariaDisabled.set(false);
    fixture.detectChanges();
    expect(document.activeElement).toBe(button);
    expect(button.hasAttribute('aria-disabled')).toBe(false);
    button.click();
    expect(host.activate).toHaveBeenCalledOnce();
    expect(host.submit).toHaveBeenCalledOnce();
  });

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
