import { Injectable, inject, signal } from '@angular/core';
import type { HttpInterceptorFn } from '@angular/common/http';
import { User, UserManager } from 'oidc-client-ts';
import { WEB_CONFIG, isApiRequest } from './config';

@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly config = inject(WEB_CONFIG);
  private readonly manager = new UserManager({
    authority: this.config.issuer,
    client_id: this.config.clientId,
    redirect_uri: new URL('callback', document.baseURI).href,
    post_logout_redirect_uri: new URL('signed-out', document.baseURI).href,
    response_type: 'code',
    scope: 'openid profile email',
    monitorSession: false,
    automaticSilentRenew: true
  });
  readonly user = signal<User | null>(null);
  readonly error = signal('');
  readonly signedOut = signal(false);

  constructor() {
    this.manager.events.addUserLoaded(user => this.user.set(user));
    this.manager.events.addUserUnloaded(() => this.user.set(null));
    this.manager.events.addAccessTokenExpired(() => this.sessionEnded());
  }

  sessionEnded(): void {
    if (!this.user()) return;
    this.user.set(null);
    void this.signIn();
  }

  async open(): Promise<void> {
    try {
      if (location.pathname === '/signed-out') {
        await this.manager.signoutRedirectCallback();
        history.replaceState(null, '', '/');
        this.signedOut.set(true);
        return;
      }
      const callback = location.pathname === '/callback';
      const user = callback ? await this.manager.signinRedirectCallback() : await this.manager.getUser();
      if (callback) {
        const search = typeof user?.state === 'string' && user.state.startsWith('?') ? user.state : '';
        history.replaceState(null, '', new URL(search || '.', document.baseURI));
      }
      if (user && !user.expired) this.user.set(user);
      else await this.signIn();
    } catch {
      this.error.set('Sign-in could not finish. Try again.');
    }
  }

  async signIn(): Promise<void> {
    this.error.set('');
    try {
      await this.manager.signinRedirect({ state: location.search });
    } catch {
      this.error.set('Cannot reach the sign-in service. Try again.');
    }
  }

  async signOut(): Promise<void> {
    try {
      await this.manager.signoutRedirect();
    } catch {
      this.error.set('Sign-out could not finish. Sign in again.');
      this.user.set(null);
    }
  }
}

export const bearerInterceptor: HttpInterceptorFn = (request, next) => {
  if (!isApiRequest(request.url, inject(WEB_CONFIG).apiBaseUrl, document.baseURI)) return next(request);
  const user = inject(AuthService).user();
  if (!user || user.expired) return next(request);
  return next(request.clone({ setHeaders: { Authorization: 'Bearer ' + user.access_token } }));
};
