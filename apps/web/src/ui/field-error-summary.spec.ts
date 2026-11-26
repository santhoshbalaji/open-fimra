import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { HlmFieldImports } from '@spartan-ng/helm/field';
import { HlmInput } from '@spartan-ng/helm/input';
import { describe, expect, it } from 'vitest';
import { FieldErrorSummary } from './field-error-summary';

@Component({
  imports: [FieldErrorSummary, HlmFieldImports, HlmInput],
  template: `
    <fimra-field-error-summary [errors]="errors()" />
    <hlm-field>
      <label hlmFieldLabel for="code">Unit code</label>
      <input hlmInput id="code" [forceInvalid]="invalid()">
      <p hlmFieldDescription id="code-hint">Use the code from the register.</p>
      <hlm-field-error id="code-error" [forceShow]="invalid()">Enter a unit code.</hlm-field-error>
    </hlm-field>
  `
})
class Host {
  readonly invalid = signal(false);
  errors() {
    return this.invalid() ? [{ fieldId: 'code', message: 'Enter a unit code.' }] : [];
  }
}

describe('Field messages and summary', () => {
  it('adds and removes the inline error association while keeping the hint', async () => {
    const fixture = TestBed.createComponent(Host);
    const root: HTMLElement = fixture.nativeElement;
    fixture.detectChanges();
    await fixture.whenStable();
    const control = root.querySelector('input')!;
    expect(control.getAttribute('aria-describedby')).toBe('code-hint');
    fixture.componentInstance.invalid.set(true);
    fixture.detectChanges();
    await fixture.whenStable();
    expect(control.getAttribute('aria-describedby')).toBe('code-hint code-error');
    expect(root.querySelector<HTMLElement>('#code-error')?.hidden).toBe(false);
    fixture.componentInstance.invalid.set(false);
    fixture.detectChanges();
    await fixture.whenStable();
    expect(control.getAttribute('aria-describedby')).toBe('code-hint');
    expect(root.querySelector<HTMLElement>('#code-error')?.hidden).toBe(true);
  });

  it('links to the invalid field and moves focus on activation', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.componentInstance.invalid.set(true);
    fixture.detectChanges();
    const root: HTMLElement = fixture.nativeElement;
    const link = root.querySelector('a')!;
    expect(link.getAttribute('href')).toBe('#code');
    expect(link.textContent).toBe('Enter a unit code.');
    link.click();
    expect(document.activeElement).toBe(root.querySelector('#code'));
  });
});
