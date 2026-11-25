import { provideHttpClient, withInterceptors } from '@angular/common/http';
import { bootstrapApplication } from '@angular/platform-browser';
import { App } from './app';
import { bearerInterceptor } from './auth';
import { WEB_CONFIG, loadConfig } from './config';

loadConfig().then(config => bootstrapApplication(App, {
  providers: [
    { provide: WEB_CONFIG, useValue: config },
    provideHttpClient(withInterceptors([bearerInterceptor]))
  ]
})).catch(() => {
  document.body.textContent = 'Open FIMRA could not start. Check the web configuration and reload.';
});
