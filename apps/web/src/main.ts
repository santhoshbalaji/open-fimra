import { isDevMode } from '@angular/core';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { bootstrapApplication } from '@angular/platform-browser';
import { provideRouter, withDisabledInitialNavigation } from '@angular/router';
import { shellRoutes } from './shell-routes';
import { provideSpartanHlm } from '@spartan-ng/helm/utils';
import { App } from './app';
import { bearerInterceptor } from './auth';
import { WEB_CONFIG, loadConfig } from './config';

const guide = location.pathname.replace(/\/$/, '') === '/design-system' &&
  (isDevMode() || new URLSearchParams(location.search).has('preview'));
const start = guide
  ? import('./design-system/guide').then(module => module.openGuide())
  : loadConfig().then(config => bootstrapApplication(App, {
    providers: [
      provideSpartanHlm(),
      provideRouter(shellRoutes, withDisabledInitialNavigation()),
      { provide: WEB_CONFIG, useValue: config },
      provideHttpClient(withInterceptors([bearerInterceptor]))
    ]
  }));
start.catch(() => {
  const main = document.createElement('main');
  main.className = 'welcome';
  const heading = document.createElement('h1');
  heading.textContent = 'Open FIMRA is unavailable';
  const retry = document.createElement('button');
  retry.textContent = 'Reload';
  retry.onclick = () => location.reload();
  main.append(heading, retry);
  document.body.replaceChildren(main);
});
