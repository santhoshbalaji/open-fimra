import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { expect, it, vi } from 'vitest';
import { AuthService } from '../auth';
import type { Submission } from './form-api';
import { SourceFeedback } from './source-feedback';

it('prepares feedback for the open alert and shows its receipt and attributable area', async () => {
  TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting(), provideSpartanHlm(),
    { provide: AuthService, useValue: { sessionEnded: vi.fn() } }] });
  const http = TestBed.inject(HttpTestingController);
  const fixture = TestBed.createComponent(SourceFeedback);
  const url = '/api/v1/workspaces/demo/alerts/7/source-feedback';
  const at = '2026-03-15T09:00:00Z';
  const report: Submission = {
    id: 'flash', alertId: '7', step: 'flash_update', incidentId: '5', version: 1, form: 'response-flash',
    values: { fire_seen: 'yes' }, location: null, submittedBy: 'Demo Guard', deviceTime: null, receivedAt: at,
    approval: { decision: 'approved', note: '', by: 'Demo Officer', at }
  };
  fixture.componentRef.setInput('url', url);
  fixture.componentRef.setInput('submissions', [
    report,
    { ...report, id: 'assessment', step: 'final_report', form: 'damage-assessment', values: { alert_area_ha: 2.5, area_ha: 8.5 } }
  ]);
  fixture.detectChanges();
  http.expectOne(url).flush({ externalAlertRef: 'DEMO-FSI-7', canPrepare: true, records: [] });
  fixture.detectChanges();
  const el = fixture.nativeElement as HTMLElement;
  expect(el.textContent).toContain('No source feedback has been prepared.');
  const verification = el.querySelector<HTMLSelectElement>('#feedback-verification')!;
  verification.value = 'flash';
  verification.dispatchEvent(new Event('change'));
  fixture.detectChanges();
  el.querySelector<HTMLSelectElement>('#feedback-assessment')!.value = 'assessment';
  el.querySelector('form')!.dispatchEvent(new Event('submit'));
  const request = http.expectOne(url);
  expect(request.request.method).toBe('POST');
  expect(request.request.body).toEqual({ verificationId: 'flash', assessmentId: 'assessment' });
  request.flush({ id: 'feedback' });
  await fixture.whenStable();
  fixture.detectChanges();
  http.expectOne(url).flush({ externalAlertRef: 'DEMO-FSI-7', canPrepare: true, records: [
    { id: 'feedback', status: 'accepted', submittedAt: at, decidedAt: at, providerReceipt: 'demo/7', payload: { areaHa: 2.5 } }
  ] });
  fixture.detectChanges();
  expect(el.textContent).toContain('Accepted by the source');
  expect(el.textContent).toContain('Source receipt demo/7');
  expect(el.textContent).toContain('2.5 ha attributable to this alert');
  expect(el.textContent).not.toContain('8.5 ha');
  http.verify();
});
