import { isDevMode } from '@angular/core';
import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { bootstrapApplication } from '@angular/platform-browser';
import { App } from './app';
import { bearerInterceptor } from './auth';
import { WEB_CONFIG, loadConfig } from './config';

const guide = location.pathname.replace(/\/$/, '') === '/design-system' &&
  (isDevMode() || new URLSearchParams(location.search).has('preview'));
const start = guide
  ? import('./design-system/guide').then(module => module.openGuide())
  : loadConfig().then(config => bootstrapApplication(App, {
    providers: [
      { provide: WEB_CONFIG, useValue: config },
      provideHttpClient(withInterceptors([bearerInterceptor]))
    ]
  }));
start.catch(() => {
  document.body.textContent = 'Open FIMRA could not start. Check the web configuration and reload.';
});
