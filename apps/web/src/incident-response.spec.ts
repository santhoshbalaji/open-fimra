import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { beforeEach, describe, expect, it } from 'vitest';
import { IncidentIntake } from './incident-intake';
import { IncidentMembership } from './incident-membership';

describe('Incident response', () => {
  const base = '/api/v1/workspaces/demo';
  beforeEach(() => TestBed.configureTestingModule({
    providers: [provideHttpClient(), provideHttpClientTesting(), provideRouter([])]
  }));

  it('records the source and observation time and opens the returned incident', async () => {
    const fixture = TestBed.createComponent(IncidentIntake);
    fixture.componentRef.setInput('endpoint', base);
    fixture.componentRef.setInput('incidentId', '42');
    const selected: string[] = [];
    fixture.componentInstance.saved.subscribe(id => selected.push(id));
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    el.querySelector<HTMLButtonElement>('button')!.click();
    fixture.detectChanges();
    el.querySelector<HTMLSelectElement>('select')!.value = 'radio';
    el.querySelector<HTMLInputElement>('[type=datetime-local]')!.value = '2026-01-01T18:00';
    const [latitude, longitude] = el.querySelectorAll<HTMLInputElement>('[type=number]');
    latitude.value = '16.42';
    longitude.value = '86.295';
    el.querySelector<HTMLInputElement>('[type=checkbox]')!.checked = true;
    el.querySelector<HTMLButtonElement>('[type=submit]')!.click();

    const http = TestBed.inject(HttpTestingController);
    const request = http.expectOne(base + '/fire-intake');
    expect(request.request.body).toMatchObject({
      source: 'radio', observedAt: new Date('2026-01-01T18:00').toISOString(),
      latitude: 16.42, longitude: 86.295, incidentId: '42'
    });
    request.flush({ incidentId: '42' });
    await fixture.whenStable();
    expect(selected).toEqual(['42']);
    expect(el.textContent).toContain('Report added to incident #42.');
    http.verify();
  });

  it('previews the split and sends back the preview key with the officer’s reason', async () => {
    const fixture = TestBed.createComponent(IncidentMembership);
    const url = base + '/incidents/42';
    fixture.componentRef.setInput('url', url);
    fixture.componentRef.setInput('enabled', true);
    fixture.componentRef.setInput('members', [{ id: '1' }, { id: '2' }]);
    const router = TestBed.inject(Router);
    fixture.componentRef.setInput('incidentLink', (id: string) => router.createUrlTree(['/incidents'], { queryParams: { incident: id } }));
    fixture.detectChanges();

    const el = fixture.nativeElement as HTMLElement;
    Array.from(el.querySelectorAll('button')).find(button => button.textContent.trim() === 'Split incident')!.click();
    fixture.detectChanges();
    el.querySelectorAll<HTMLInputElement>('input[type=checkbox]')[1].click();
    el.querySelector<HTMLTextAreaElement>('textarea')!.value = 'Separate fire confirmed on site.';
    fixture.detectChanges();
    const submit = el.querySelector<HTMLButtonElement>('[type=submit]')!;
    submit.click();
    const http = TestBed.inject(HttpTestingController);
    const preview = http.expectOne(url + '/membership?preview=true');
    expect(preview.request.body).toEqual({ operation: 'split', alertIds: ['2'] });
    preview.flush({ source: { id: '42', step: 'Watch', alertIds: ['1', '2'] }, moving: ['2'], previewKey: 'preview' });
    await fixture.whenStable();
    expect(el.textContent).toContain('Source incident #42 is at Watch.');
    submit.click();

    const save = http.expectOne(url + '/membership');
    expect(save.request.body).toEqual({
      operation: 'split', alertIds: ['2'], previewKey: 'preview', reason: 'Separate fire confirmed on site.'
    });
    save.flush({ targetId: '43', sourceId: '42', moving: ['2'] });
    await fixture.whenStable();
    expect(el.textContent).toContain('Open incident #43');
    http.verify();
  });
});
