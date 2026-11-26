import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from '../auth';
import { stubLayoutApis } from '../overlay-testing';
import type { Submission } from './form-api';
import { SubmissionView } from './submission-view';

import firstReport from '../../../../packages/form-schema/examples/first-report.json';

const workspace = 'https://api.test/api/v1/workspaces/demo';
const report: Submission = {
  id: '5ca1ab1e-0000-4000-8000-0000000000a1', alertId: '7', incidentId: null, form: 'first-report', version: 1,
  step: 'first_report', submittedBy: 'Demo Beat Guard', deviceTime: null, receivedAt: '2026-03-15T09:00:00Z', location: null,
  values: { fire_status: 'controlled', teams: [{ team_name: 'Demo Team A', members: 6 }] }, approval: null, canApprove: true
};

describe('Report and approval', () => {
  let http: HttpTestingController;
  beforeEach(() => {
    stubLayoutApis();
    TestBed.configureTestingModule({ providers: [provideSpartanHlm(), provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } }] });
    http = TestBed.inject(HttpTestingController);
  });
  afterEach(() => http.verify());

  function render(submission: Submission) {
    const fixture = TestBed.createComponent(SubmissionView);
    fixture.componentRef.setInput('workspaceUrl', workspace);
    fixture.componentRef.setInput('submissionId', submission.id);
    fixture.detectChanges();
    http.expectOne(workspace + '/submissions/' + submission.id).flush(submission);
    fixture.detectChanges();
    // The report shows with the version it was filled in on.
    http.expectOne(workspace + '/forms/first-report/versions/' + submission.version).flush({ form: 'first-report', version: 1,
      status: 'published', definition: firstReport });
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    const button = (text: string) => Array.from(el.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent.trim() === text);
    return { fixture, el, button };
  }

  function dialogButton(text: string) {
    return Array.from(document.querySelectorAll<HTMLButtonElement>('[role=alertdialog] button'))
      .find(button => button.textContent.trim() === text)!;
  }

  it('lets a supervising officer approve a report once', async () => {
    const { el, fixture, button } = render(report);
    expect(el.textContent).toContain('Version 1 · sent by Demo Beat Guard');
    expect(el.textContent).toContain('Under control');
    expect(el.textContent).toContain('A decision cannot be changed later.');
    const approve = button('Approve')!;
    approve.focus();
    approve.click();
    fixture.detectChanges();
    expect(document.querySelector('[role=alertdialog]')?.textContent).toContain('Approve this report?');
    http.expectNone(workspace + '/submissions/' + report.id + '/approval');
    dialogButton('Cancel').click();
    await vi.waitFor(() => { fixture.detectChanges(); expect(document.activeElement).toBe(approve); });
    http.expectNone(workspace + '/submissions/' + report.id + '/approval');
    approve.click();
    fixture.detectChanges();
    const confirm = dialogButton('Approve report');
    confirm.click();
    confirm.click();
    await vi.waitFor(() => {
      fixture.detectChanges();
      expect(document.querySelector('[role=alertdialog]')).toBeNull();
    });
    await fixture.whenStable();
    const post = http.expectOne(workspace + '/submissions/' + report.id + '/approval');
    expect(post.request.body).toEqual({ decision: 'approved', note: '' });
    post.flush({ ...report, approval: { decision: 'approved', note: '', by: 'Demo Division Officer', at: '2026-03-15T10:00:00Z' } });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.approval')!.textContent).toContain('Approved');
    expect(el.querySelector('.approval')!.textContent).toContain('Demo Division Officer');
    expect(button('Approve')).toBeUndefined();
    expect(document.activeElement).toBe(el.querySelector('.approval [tabindex="-1"]'));
  });

  it('needs a note to return a report', async () => {
    const { el, fixture, button } = render(report);
    button('Return with a note')!.click();
    fixture.detectChanges();
    const note = el.querySelector<HTMLTextAreaElement>('#return-note')!;
    button('Return the report')!.click();
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('[role=alert]')!.textContent).toContain('Say why the report is returned.');
    http.expectNone(workspace + '/submissions/' + report.id + '/approval');
    note.value = '  Recount the teams.  ';
    button('Return the report')!.click();
    const post = http.expectOne(workspace + '/submissions/' + report.id + '/approval');
    expect(post.request.body).toEqual({ decision: 'returned', note: 'Recount the teams.' });
    post.flush({ ...report, approval: { decision: 'returned', note: 'Recount the teams.', by: 'Demo Division Officer', at: '2026-03-15T10:00:00Z' } });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('.approval-note')!.textContent).toBe('Recount the teams.');
  });

  it('shows field staff the decision without the buttons', () => {
    const { el, button } = render({ ...report, canApprove: false });
    expect(button('Approve')).toBeUndefined();
    expect(button('Return with a note')).toBeUndefined();
    expect(el.textContent).toContain('Waiting for a decision from a supervising officer.');
  });

  it('shows who decided first when another officer was quicker', async () => {
    const { el, fixture, button } = render(report);
    button('Approve')!.click();
    fixture.detectChanges();
    dialogButton('Approve report').click();
    await vi.waitFor(() => {
      fixture.detectChanges();
      expect(document.querySelector('[role=alertdialog]')).toBeNull();
    });
    await fixture.whenStable();
    http.expectOne(workspace + '/submissions/' + report.id + '/approval').flush(
      { error: 'This submission already has a decision. Decisions never change.' }, { status: 409, statusText: 'Conflict' });
    await fixture.whenStable();
    http.expectOne(workspace + '/submissions/' + report.id).flush({ ...report,
      approval: { decision: 'returned', note: 'Add the second team.', by: 'Demo State Admin', at: '2026-03-15T10:00:00Z' } });
    fixture.detectChanges();
    expect(el.textContent).toContain('This submission already has a decision.');
    expect(el.textContent).toContain('Demo State Admin');
  });
});
