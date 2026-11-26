import { InteractivityChecker } from '@angular/cdk/a11y';
import { TestBed } from '@angular/core/testing';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { stubLayoutApis } from '../overlay-testing';
import { ConfirmDialog } from './confirm-dialog';

describe('ConfirmDialog', () => {
  const trigger = document.createElement('button');

  beforeEach(() => {
    stubLayoutApis();
    TestBed.configureTestingModule({ providers: [provideSpartanHlm()] });
    // The test DOM has no layout. The browser check exercises real Tab navigation.
    vi.spyOn(TestBed.inject(InteractivityChecker), 'isVisible').mockReturnValue(true);
    document.body.append(trigger);
    trigger.focus();
  });

  afterEach(() => {
    trigger.remove();
    vi.restoreAllMocks();
  });

  function open() {
    return TestBed.inject(ConfirmDialog).open({
      heading: 'Close the demo alert?', detail: 'This ends its workflow.', confirmLabel: 'Close alert', trigger
    });
  }

  it('traps focus, cancels with Escape and restores focus to the trigger', async () => {
    const result = open();
    TestBed.tick();
    const modal = document.querySelector('[role=alertdialog]')!;
    const [cancel, confirm] = modal.querySelectorAll('button');
    const [before, after] = modal.parentElement!.querySelectorAll<HTMLElement>('.cdk-focus-trap-anchor');
    expect(trigger.contains(modal)).toBe(false);
    expect(document.activeElement).toBe(cancel);
    before.focus();
    expect(document.activeElement).toBe(confirm);
    after.focus();
    expect(document.activeElement).toBe(cancel);
    cancel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    TestBed.tick();
    expect(await result).toBe(false);
    expect(document.querySelector('[role=alertdialog]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('returns true when the user confirms', async () => {
    const result = open();
    TestBed.tick();
    const buttons = document.querySelectorAll<HTMLButtonElement>('[role=alertdialog] button');
    buttons[1].click();
    TestBed.tick();
    expect(await result).toBe(true);
  });
});
