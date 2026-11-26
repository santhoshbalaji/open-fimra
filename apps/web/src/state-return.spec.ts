import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { afterEach, describe, expect, it } from 'vitest';
import { WEB_CONFIG } from './config';
import { StateReturnView } from './state-return';
import { WorkspaceService } from './workspace';

describe('State return', () => {
  const base = 'https://api.test/api/v1/workspaces/demo/state-extracts';
  const from = '2026-03-08';
  const authority = { stateCode: 'XS-DEMO', unitCodeSystem: 'demo-forest-units' };
  const prior = { id: 'old', revision: 1, current: true, from, to: from, receipts: [] };
  const preview = {
    digest: 'preview-checksum',
    documents: {
      incident: {
        revision: 2, window: { from, to: from, cutoff: '18:00', timeZone: 'Asia/Kolkata' },
        coverage: { status: 'incomplete', reported: 1, expected: 2, units: [
          { code: 'D1', day: from, status: 'zero' }, { code: 'D2', day: from, status: 'not_reported' }
        ] }
      },
      preparedness: { preparedness: { status: 'not_reported', approvedRecords: 0 } }
    }
  };

  function open() {
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(), provideHttpClientTesting(),
        { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test' } },
        { provide: WorkspaceService, useValue: { current: signal({ slug: 'demo' }) } }
      ]
    });
    const fixture = TestBed.createComponent(StateReturnView);
    const http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
    http.expectOne({ method: 'GET', url: base }).flush({ authority, items: [prior] });
    fixture.detectChanges();
    return { fixture, http, view: fixture.componentInstance, page: fixture.nativeElement as HTMLElement };
  }

  afterEach(() => TestBed.inject(HttpTestingController).verify());

  it('shows coverage states and approves the preview as a correction', async () => {
    const { fixture, http, view, page } = open();
    view.chooseDates(from, from);
    const loading = view.requestPreview();
    const request = http.expectOne({ method: 'POST', url: base });
    expect(request.request.body).toEqual({ action: 'preview', from, to: from });
    request.flush(preview);
    await loading;
    fixture.detectChanges();
    const rows = [...page.querySelectorAll('tbody tr')];
    expect(rows.map(row => row.textContent)).toEqual([expect.stringContaining('Zero'), expect.stringContaining('Not reported')]);
    expect(page.textContent).toContain('Incomplete');

    const approving = view.sign();
    const approval = http.expectOne({ method: 'POST', url: base });
    expect(approval.request.body).toEqual({ action: 'approve', from, to: from, digest: preview.digest });
    approval.flush({
      id: 'new', revision: 2, current: true, supersedes: 'old', from, to: from, receipts: [
        { kind: 'incident', status: 'incomplete', keyId: 'key-id', sha256: 'digest', validatedAt: '2026-03-09T00:00:00Z' }
      ]
    });
    await approving;
    fixture.detectChanges();
    expect(page.textContent).toContain('State return signed.');
    expect(page.textContent).toContain('Superseded');
    expect(page.textContent).toContain('Download receipt');
  });

  it('clears the approval when the dates change', async () => {
    const { http, view } = open();
    view.chooseDates(from, from);
    const loading = view.requestPreview();
    http.expectOne({ method: 'POST', url: base }).flush(preview);
    await loading;
    view.chooseDates('2026-03-09', '2026-03-09');
    await view.sign();
    http.expectNone({ method: 'POST', url: base });
  });
});
