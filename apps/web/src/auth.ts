import { Injectable, inject, signal } from '@angular/core';
import type { HttpInterceptorFn } from '@angular/common/http';
import type { User, UserManager } from 'oidc-client-ts';
import { WEB_CONFIG, isApiRequest } from './config';

@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly config = inject(WEB_CONFIG);
  private manager?: Promise<UserManager>;
  // Keep the default OIDC storage key so active sessions survive an upgrade.
  private readonly userStoreKey = `oidc.user:${this.config.issuer}:${this.config.clientId}`;
  readonly user = signal<User | null>(null);
  readonly error = signal('');
  readonly signedOut = signal(false);
  readonly opening = signal(true);

  private getManager(): Promise<UserManager> {
    return this.manager ??= import('oidc-client-ts').then(({ UserManager }) => {
      const manager = new UserManager({
        authority: this.config.issuer,
        client_id: this.config.clientId,
        redirect_uri: new URL('callback', document.baseURI).href,
        post_logout_redirect_uri: new URL('signed-out', document.baseURI).href,
        response_type: 'code',
        scope: 'openid profile email',
        monitorSession: false,
        automaticSilentRenew: true
      });
      manager.events.addUserLoaded(user => this.user.set(user));
      manager.events.addUserUnloaded(() => this.user.set(null));
      manager.events.addAccessTokenExpired(() => this.sessionEnded());
      return manager;
    }).catch(error => {
      this.manager = undefined;
      throw error;
    });
  }

  sessionEnded(): void {
    if (!this.user()) return;
    this.user.set(null);
    void this.signIn();
  }

  async open(): Promise<void> {
    try {
      const signedOut = location.pathname === '/signed-out';
      const callback = location.pathname === '/callback';
      if (!signedOut && !callback && !sessionStorage.getItem(this.userStoreKey)) return;
      const manager = await this.getManager();
      if (signedOut) {
        const response = await manager.signoutRedirectCallback();
        history.replaceState(null, '', '/');
        this.signedOut.set(true);
        if (response?.userState === 'switch-account') await this.signIn(true);
        return;
      }
      const user = callback ? await manager.signinRedirectCallback() : await manager.getUser();
      if (callback) {
        const target = typeof user?.state === 'string' && /^(?:\/(?!\/)|\?)/.test(user.state)
          ? new URL(user.state, document.baseURI) : new URL('.', document.baseURI);
        history.replaceState(null, '', target.origin === location.origin ? target : '/');
      }
      if (user && !user.expired) this.user.set(user);

    } catch {
      this.error.set('Sign-in could not finish. Try again.');
    } finally {
      this.opening.set(false);
    }
  }

  async signIn(switchAccount = false): Promise<void> {
    this.error.set('');
    this.opening.set(true);
    const target = new URL(location.href);
    for (const key of [...target.searchParams.keys()]) {
      if (!['workspace', 'view', 'alert', 'detection', 'incident', 'report', 'hours', 'class',
        'unitPath', 'queue', 'cursor', 'decision', 'form', 'period', 'from', 'to', 'hierarchy', 'reportHours', 'show'].includes(key)) target.searchParams.delete(key);
    }
    if (target.pathname === '/callback' || target.pathname === '/signed-out') target.pathname = '/';
    try {
      const manager = await this.getManager();
      await manager.signinRedirect({ state: target.pathname + target.search, prompt: switchAccount ? 'login' : undefined });
    } catch {
      this.error.set('Cannot reach the sign-in service. Try again.');
      this.opening.set(false);
    }
  }

  async signOut(switchAccount = false): Promise<void> {
    this.error.set('');
    try {
      const manager = await this.getManager();
      await manager.signoutRedirect({ state: switchAccount ? 'switch-account' : undefined });
    } catch {
      this.error.set('Sign-out could not finish. Try again.');
    }
  }
}

export const bearerInterceptor: HttpInterceptorFn = (request, next) => {
  if (!isApiRequest(request.url, inject(WEB_CONFIG).apiBaseUrl, document.baseURI)) return next(request);
  const user = inject(AuthService).user();
  if (!user || user.expired) return next(request);
  return next(request.clone({ setHeaders: { Authorization: 'Bearer ' + user.access_token } }));
};
