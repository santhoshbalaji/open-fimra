import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { InteractivityChecker } from '@angular/cdk/a11y';
import { signal } from '@angular/core';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AccessKeys } from './access-keys';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { stubLayoutApis } from './overlay-testing';
import { WorkspaceService } from './workspace';

const keysURL = 'https://api.test/api/v1/access-keys';
const saved = {
  id: 'k1', name: 'Office laptop', hint: 'Ab12Cd', createdAt: '2026-11-01T10:00:00Z',
  expiresAt: '2027-01-30T10:00:00Z', lastUsedAt: null, expired: false
};

describe('AccessKeys', () => {
  let fixture: ComponentFixture<AccessKeys>;
  let http: HttpTestingController;
  const sessionEnded = vi.fn();
  const workspace = signal<{ slug: string } | null>({ slug: 'demo' });
  const text = () => document.body.textContent as string;
  const button = (label: string): HTMLButtonElement | undefined =>
    [...document.querySelectorAll('button')].find(element => element.textContent.trim() === label);
  const submit = () => {
    (fixture.nativeElement.querySelector('.key-form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }));
    fixture.detectChanges();
  };
  const typeName = (name: string) => {
    (fixture.nativeElement.querySelector('#key-name') as HTMLInputElement).value = name;
  };

  beforeEach(() => {
    stubLayoutApis();
    workspace.set({ slug: 'demo' });
    TestBed.configureTestingModule({ imports: [AccessKeys], providers: [
      provideHttpClient(), provideHttpClientTesting(), provideSpartanHlm(),
      { provide: AuthService, useValue: { sessionEnded } },
      { provide: WorkspaceService, useValue: { current: workspace } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test/' } }
    ] });
    vi.spyOn(TestBed.inject(InteractivityChecker), 'isVisible').mockReturnValue(true);
    fixture = TestBed.createComponent(AccessKeys);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    sessionEnded.mockReset();
    vi.restoreAllMocks();
  });

  it('shows the workspace address and the saved keys without their secrets', () => {
    http.expectOne(keysURL).flush({ items: [saved, { ...saved, id: 'k2', name: 'Old', expired: true }], limit: 20 });
    fixture.detectChanges();
    const disclosure = fixture.nativeElement.querySelector('details:has(.service-address)') as HTMLDetailsElement;
    expect(disclosure.open).toBe(false);
    expect(fixture.nativeElement.querySelector('.service-address').textContent).toBe('https://api.test/ogc/workspaces/demo');
    const rows = [...fixture.nativeElement.querySelectorAll('tbody tr')].map(row => (row as HTMLElement).textContent);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain('fimra_Ab12Cd…');
    expect(rows[0]).toContain('Not yet');
    expect(rows[1]).toContain('Expired');
  });

  it('creates a key, copies it with an announcement and returns focus after dismissal', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    http.expectOne(keysURL).flush({ items: [], limit: 20 });
    fixture.detectChanges();
    expect(text()).toContain('No keys yet');
    typeName('  Office laptop ');
    submit();
    const request = http.expectOne(keysURL);
    expect(request.request.method).toBe('POST');
    expect(request.request.body).toEqual({ name: 'Office laptop', expiresInDays: 90 });
    request.flush({ ...saved, key: 'fimra_Ab12Cd-secret' }, { status: 201, statusText: 'Created' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('.new-key').textContent).toBe('fimra_Ab12Cd-secret');
    expect(fixture.nativeElement.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(document.activeElement?.textContent).toBe('Copy the key Office laptop now');
    button('Copy key')?.click();
    await vi.waitFor(() => {
      fixture.detectChanges();
      expect(document.querySelector('fimra-live-status[role=status]:not(:empty)')?.textContent).toContain('Key copied.');
    });
    expect(writeText).toHaveBeenCalledWith('fimra_Ab12Cd-secret');
    Reflect.deleteProperty(navigator, 'clipboard');
    button('I have saved it')?.click();
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('.new-key')).toBeNull();
    expect(text()).not.toContain('fimra_Ab12Cd-secret');
    expect(document.activeElement?.id).toBe('key-name');
  });

  it('asks for a name before making a key', () => {
    http.expectOne(keysURL).flush({ items: [], limit: 20 });
    fixture.detectChanges();
    typeName(' ');
    submit();
    expect(text()).toContain('Give the key a name');
  });

  it('returns focus on Escape and revokes only after explicit confirmation', async () => {
    http.expectOne(keysURL).flush({ items: [saved], limit: 20 });
    fixture.detectChanges();
    const trigger = fixture.nativeElement.querySelector('[aria-label="Revoke Office laptop"]') as HTMLButtonElement;
    trigger.focus();
    trigger.click();
    TestBed.tick();
    const cancel = button('Cancel')!;
    expect(document.activeElement).toBe(cancel);
    cancel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    TestBed.tick();
    await vi.waitFor(() => expect(document.activeElement).toBe(trigger));
    http.expectNone(keysURL + '/k1');
    await Promise.resolve();

    trigger.click();
    TestBed.tick();
    expect(document.querySelector('[role=alertdialog]')?.textContent).toContain('stops working at once');
    button('Revoke key')!.click();
    TestBed.tick();
    const request = await vi.waitFor(() => http.expectOne(keysURL + '/k1'));
    expect(request.request.method).toBe('DELETE');
    request.flush(null, { status: 204, statusText: 'No Content' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelectorAll('tbody tr')).toHaveLength(0);
    expect(text()).toContain('No keys yet');
    expect(document.activeElement?.id).toBe('keys-heading');
  });

  it('stops making keys at the limit and reports errors', () => {
    http.expectOne(keysURL).flush({ items: [saved], limit: 1 });
    fixture.detectChanges();
    expect(text()).toContain('limit of 1 keys');
    expect((fixture.nativeElement.querySelector('fieldset') as HTMLFieldSetElement).disabled).toBe(true);
    fixture.componentInstance.load();
    http.expectOne(keysURL).flush({ error: 'Signed out.' }, { status: 401, statusText: 'Unauthorized' });
    fixture.detectChanges();
    expect(sessionEnded).toHaveBeenCalled();
    expect(text()).toContain('Signed out.');
  });

  it('explains that a person without a workspace sees nothing', () => {
    workspace.set(null);
    http.expectOne(keysURL).flush({ items: [], limit: 20 });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('.service-address')).toBeNull();
    expect(text()).toContain('You need a role in a workspace');
  });
});
