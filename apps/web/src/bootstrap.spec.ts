import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthService } from './auth';
import { Bootstrap, slugFrom } from './bootstrap';
import { WEB_CONFIG } from './config';
import { WorkspaceService } from './workspace';

describe('Bootstrap', () => {
  let fixture: ComponentFixture<Bootstrap>;
  let http: HttpTestingController;
  const reload = vi.fn();
  const field = (name: string) => fixture.nativeElement.querySelector(`[name=${name}]`) as HTMLInputElement;
  const type = (name: string, value: string) => {
    field(name).value = value;
    field(name).dispatchEvent(new Event('input'));
    fixture.detectChanges();
  };

  beforeEach(() => {
    reload.mockReset();
    TestBed.configureTestingModule({ imports: [Bootstrap], providers: [
      provideHttpClient(), provideHttpClientTesting(),
      { provide: AuthService, useValue: { sessionEnded: vi.fn() } },
      { provide: WorkspaceService, useValue: { reload } },
      { provide: WEB_CONFIG, useValue: { apiBaseUrl: 'https://api.test/' } }
    ] });
    fixture = TestBed.createComponent(Bootstrap);
    http = TestBed.inject(HttpTestingController);
    fixture.detectChanges();
  });

  afterEach(() => http.verify());

  it('makes a web address from the workspace name until it is edited', () => {
    expect(slugFrom('  Demo State — Forest Dept. ')).toBe('demo-state-forest-dept');
    type('workspaceName', 'Demo State');
    expect(field('workspaceSlug').value).toBe('demo-state');
    type('workspaceSlug', 'demo');
    type('workspaceName', 'Another name');
    expect(field('workspaceSlug').value).toBe('demo');
  });

  it('offers state mode only and says why national mode is off', () => {
    const national = fixture.nativeElement.querySelector('[value=national]') as HTMLInputElement;
    expect(national.disabled).toBe(true);
    expect(fixture.nativeElement.textContent).toContain('Phase 1 supports one state per installation');
  });

  it('claims the install with the setup code and reloads the profile', () => {
    type('setupCode', ' abcd-efgh-jkmn-pqrs ');
    type('workspaceName', 'Demo State');
    type('stateCode', 'DS');
    (fixture.nativeElement.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }));
    const request = http.expectOne('https://api.test/api/v1/setup/bootstrap');
    expect(request.request.body).toEqual({
      setupCode: 'abcd-efgh-jkmn-pqrs', mode: 'state', workspaceName: 'Demo State', workspaceSlug: 'demo-state', stateCode: 'DS'
    });
    request.flush({ workspace: { slug: 'demo-state', name: 'Demo State' } }, { status: 201, statusText: 'Created' });
    expect(reload).toHaveBeenCalledOnce();
  });

  it('shows the reason when the code is wrong', () => {
    type('setupCode', 'WRONG-CODE-0000-0000');
    type('workspaceName', 'Demo State');
    type('stateCode', 'DS');
    (fixture.nativeElement.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit', { cancelable: true }));
    http.expectOne('https://api.test/api/v1/setup/bootstrap')
      .flush({ error: 'The setup code is not correct.' }, { status: 403, statusText: 'Forbidden' });
    fixture.detectChanges();
    expect(fixture.nativeElement.querySelector('[role=alert]').textContent).toContain('The setup code is not correct.');
    expect(reload).not.toHaveBeenCalled();
  });
});
