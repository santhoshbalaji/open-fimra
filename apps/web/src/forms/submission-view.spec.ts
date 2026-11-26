import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from '../auth';
import { ShellNavigation } from '../shell-navigation';
import { stubLayoutApis } from '../overlay-testing';
import type { Submission } from './form-api';
import { SubmissionView } from './submission-view';

import damageAssessment from '../../../../packages/starter-pack/forms/damage-assessment.json';
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
    TestBed.configureTestingModule({ providers: [provideSpartanHlm(), provideHttpClient(), provideHttpClientTesting(), provideRouter([]),
      { provide: ShellNavigation, useFactory: () => {
        const router = TestBed.inject(Router);
        return { href: (path: string, queryParams: object) => router.createUrlTree([path], { queryParams }) };
      } },
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
    http.expectOne(workspace + '/alerts/' + submission.alertId).flush({ beat_name: 'Beat 01', range_name: 'Demo Range' });
    // The report shows with the version it was filled in on.
    http.expectOne(workspace + '/forms/' + submission.form + '/versions/' + submission.version).flush({
      form: submission.form, version: 1, status: 'published',
      definition: submission.form === 'damage-assessment' ? damageAssessment : firstReport
    });
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
    const header = el.querySelector('.submission-header')!;
    expect(header.querySelector('h3')!.textContent).toBe('First report');
    expect(header.querySelector('p')!.textContent).toBe('Beat 01 · Demo Range');
    expect(header.querySelector('.submission-meta dd')!.textContent).toBe('Version 1');
    expect(Array.from(header.querySelectorAll('dt'), term => term.textContent)).toEqual(['Submitted by', 'Related alert', 'Received', 'Form version']);
    expect(header.querySelector('dd')!.textContent).toBe('Demo Beat Guard');
    expect(header.querySelector('a')!.getAttribute('href')).toBe('/monitor/alerts?alert=7');
    expect(header.querySelector('fimra-status-badge')!.textContent?.trim()).toBe('Awaiting review');
    const answers = el.querySelector('fimra-form-presentation')!;
    expect(answers.textContent).toContain('Under control');
    expect(answers.textContent).not.toContain('Draft for the Demo State starter pack');
    expect(el.querySelectorAll('h3')).toHaveLength(2);
    expect(header.compareDocumentPosition(answers) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(answers.compareDocumentPosition(button('Approve')!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
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
    expect(el.querySelector('fimra-timeline')!.textContent).toContain('Approved');
    expect(el.querySelector('fimra-timeline')!.textContent).toContain('Demo Division Officer');
    expect(el.querySelector('fimra-live-status')!.textContent).toBe('Report approved.');
    expect(button('Approve')).toBeUndefined();
    expect(document.activeElement).toBe(el.querySelector('.submission-header [tabindex="-1"]'));
  });

  it('requests version 1 and uses its rules after version 2 changes the form', () => {
    const original = {
      formatVersion: 1, id: 'first-report', version: 1, title: { en: 'Original report' },
      sections: [{ key: 'fire', title: { en: 'Fire' }, items: [
        { key: 'cause', type: 'text', label: { en: 'Cause' } },
        { key: 'crew', type: 'number', label: { en: 'Crew size' }, unit: 'people',
          showWhen: { op: 'equals', field: 'cause', value: 'lightning' } }
      ] }]
    };
    const latest = structuredClone(original);
    latest.version = 2;
    latest.title.en = 'Updated report';
    latest.sections[0].items[1].showWhen!.value = 'other';
    const fixture = TestBed.createComponent(SubmissionView);
    fixture.componentRef.setInput('workspaceUrl', workspace);
    fixture.componentRef.setInput('submissionId', 'demo-newer-report');
    fixture.detectChanges();
    http.expectOne(workspace + '/submissions/demo-newer-report').flush({
      ...report, id: 'demo-newer-report', version: 2, values: { cause: 'lightning' }
    });
    fixture.detectChanges();
    http.expectOne(workspace + '/alerts/' + report.alertId).flush({ beat_name: 'Beat 01' });
    http.expectOne(workspace + '/forms/first-report/versions/2').flush({ definition: latest });
    fixture.detectChanges();
    const el = fixture.nativeElement as HTMLElement;
    expect(el.textContent).toContain('Updated report');
    expect(el.textContent).not.toContain('Crew size');

    fixture.componentRef.setInput('submissionId', report.id);
    fixture.detectChanges();
    http.expectOne(workspace + '/submissions/' + report.id).flush({ ...report, values: { cause: 'lightning', crew: 4 } });
    fixture.detectChanges();
    http.expectOne(workspace + '/alerts/' + report.alertId).flush({ beat_name: 'Beat 01' });
    http.expectOne(workspace + '/forms/first-report/versions/1').flush({ definition: original });
    fixture.detectChanges();
    expect(el.textContent).toContain('Original report');
    expect(el.textContent).toContain('Crew size');
    expect(el.textContent).toContain('4 people');
    expect(el.textContent).not.toContain('Updated report');
  });

  it('shows the returned provisional area next to revised figures before approval', () => {
    const { el, button } = render({
      ...report, form: 'damage-assessment', step: 'final_report',
      values: { area_ha: 7.25, measurement: 'surveyed', method: 'Walked the perimeter', uncertainty: 'Clear track' },
      provisionalAssessment: {
        id: 'provisional', areaHa: 10.5, measurement: 'estimated', method: 'Estimated from the track',
        uncertainty: 'Far edge hidden', decision: 'returned'
      }
    });
    expect(el.querySelector('fimra-form-presentation')!.textContent).toContain('7.25');
    expect(el.textContent).toContain('Provisional damage assessment');
    expect(el.textContent).toContain('10.5 ha');
    expect(el.textContent).toContain('Far edge hidden');
    expect(el.textContent).toContain('Returned');
    expect(button('Approve')).toBeDefined();
  });

  it('needs a note to return a report', async () => {
    const { el, fixture, button } = render(report);
    button('Return with a note')!.click();
    fixture.detectChanges();
    const note = el.querySelector<HTMLTextAreaElement>('#return-note')!;
    button('Return the report')!.click();
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('#return-error')!.textContent).toContain('Say why the report is returned.');
    expect(note.getAttribute('aria-describedby')).toBe('return-hint return-error');
    expect(document.activeElement).toBe(note);
    http.expectNone(workspace + '/submissions/' + report.id + '/approval');
    note.value = '  Recount the teams.  ';
    button('Return the report')!.click();
    const post = http.expectOne(workspace + '/submissions/' + report.id + '/approval');
    expect(post.request.body).toEqual({ decision: 'returned', note: 'Recount the teams.' });
    post.flush({ ...report, approval: { decision: 'returned', note: 'Recount the teams.', by: 'Demo Division Officer', at: '2026-03-15T10:00:00Z' } });
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.querySelector('fimra-timeline li:last-child p:last-child')!.textContent?.trim()).toBe('Recount the teams.');
    expect(el.querySelector('fimra-live-status')!.textContent).toBe('Report returned.');
    expect(document.activeElement).toBe(el.querySelector('.submission-header [tabindex="-1"]'));
  });

  it('shows field staff the decision without the buttons', () => {
    const { el, button } = render({ ...report, canApprove: false });
    expect(button('Approve')).toBeUndefined();
    expect(button('Return with a note')).toBeUndefined();
    expect(el.textContent).toContain('Awaiting review');
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
    await fixture.whenStable();
    fixture.detectChanges();
    expect(el.textContent).toContain('This submission already has a decision.');
    expect(el.textContent).toContain('Demo State Admin');
  });
});
