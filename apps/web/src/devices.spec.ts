import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { WEB_CONFIG } from './config';
import { Device, Devices } from './devices';
import { stubLayoutApis } from './overlay-testing';
import { WorkspaceService } from './workspace';

const mineURL = 'https://api.test/api/v1/devices';
const areaURL = 'https://api.test/api/v1/workspaces/demo/devices';
const phone: Device = {
  id: '6f1c4a52-1b2c-4d3e-8f90-0123456789ab', platform: 'android', appVersion: '0.2.0+2', push: true, status: 'active',
  registeredAt: '2026-11-25T08:00:00Z', lastSeenAt: '2026-11-25T09:30:00Z', wipeRequestedAt: null, wipedAt: null
};
const guardPhone: Device = { ...phone, id: '7a2d5b63-2c3d-4e4f-9a01-123456789abc', owner: { username: 'beat.guard', name: 'Demo Beat Guard' } };

describe('Devices', () => {
  let fixture: ComponentFixture<Devices>;
  let http: HttpTestingController;
  const sessionEnded = vi.fn();
  const workspace = signal<{ slug: string } | null>({ slug: 'demo' });
  const text = () => document.body.textContent as string;
  const button = (label: string): HTMLButtonElement | undefined =>
    [...document.querySelectorAll('button')].find(element => element.textContent.trim() === label);
  const labelled = (label: string) => fixture.nativeElement.querySelector(`[aria-label="${label}"]`) as HTMLButtonElement;

  beforeEach(() => {
    stubLayoutApis();
    workspace.set({ slug: 'demo' });
    TestBed.configureTestingModule({ imports: [Devices], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded } },
      { provide: WorkspaceService, useValue: { current: workspace } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test/' } }
    ] });
    fixture = TestBed.createComponent(Devices);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    http.verify();
    sessionEnded.mockReset();
  });

  it('lists your phones with a status label and hides the area for people who manage none', () => {
    http.expectOne(mineURL).flush({ items: [phone, { ...phone, id: 'x', status: 'wiped', push: false }] });
    http.expectOne(areaURL).flush({ error: 'A state administrator or division officer role is required.' },
      { status: 403, statusText: 'Forbidden' });
    fixture.detectChanges();
    const rows = [...fixture.nativeElement.querySelectorAll('.my-phones tbody tr')].map(row => (row as HTMLElement).textContent);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain('Android phone');
    expect(rows[0]).toContain('Signed in');
    expect(rows[1]).toContain('Data deleted');
    expect(text()).not.toContain('Phones in your area');
    expect(text()).not.toContain('could not load');
  });

  it('wipes a phone in the area after confirmation', () => {
    http.expectOne(mineURL).flush({ items: [] });
    http.expectOne(areaURL).flush({ items: [guardPhone] });
    fixture.detectChanges();
    expect(text()).toContain('Phones in your area');
    expect(text()).toContain('Demo Beat Guard');
    labelled('Wipe the phone of Demo Beat Guard').click();
    fixture.detectChanges();
    expect(text()).toContain('This cannot be undone');
    button('Wipe phone')?.click();
    const request = http.expectOne(areaURL + '/' + guardPhone.id + '/wipe');
    expect(request.request.method).toBe('POST');
    request.flush({ device: { ...guardPhone, status: 'wipe_pending' } });
    http.expectOne(mineURL).flush({ items: [] });
    http.expectOne(areaURL).flush({ items: [{ ...guardPhone, status: 'wipe_pending', wipeRequestedBy: 'Demo Division Officer' }] });
    fixture.detectChanges();
    expect(text()).toContain('Wipe requested');
    expect(text()).toContain('By Demo Division Officer');
    expect(fixture.nativeElement.querySelector('[aria-label="Wipe the phone of Demo Beat Guard"]')).toBeNull();
  });

  it('removes one of your phones after confirmation', () => {
    http.expectOne(mineURL).flush({ items: [phone] });
    http.expectOne(areaURL).flush({ items: [] });
    fixture.detectChanges();
    labelled('Remove Android phone, app 0.2.0+2').click();
    fixture.detectChanges();
    expect(text()).toContain('stops getting notifications at once');
    button('Remove phone')?.click();
    const request = http.expectOne(mineURL + '/' + phone.id);
    expect(request.request.method).toBe('DELETE');
    request.flush(null, { status: 204, statusText: 'No Content' });
    http.expectOne(mineURL).flush({ items: [{ ...phone, status: 'removed' }] });
    http.expectOne(areaURL).flush({ items: [] });
    fixture.detectChanges();
    expect(text()).toContain('Removed');
  });

  it('ends the session when the API refuses the token', () => {
    http.expectOne(mineURL).flush({ error: 'Signed out.' }, { status: 401, statusText: 'Unauthorized' });
    http.expectOne(areaURL).flush({ items: [] });
    fixture.detectChanges();
    expect(sessionEnded).toHaveBeenCalled();
    expect(text()).toContain('Signed out.');
  });
});
