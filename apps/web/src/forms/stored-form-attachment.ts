import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { ChangeDetectionStrategy, Component, DestroyRef, computed, effect, inject, input, signal } from '@angular/core';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { firstValueFrom } from 'rxjs';
import { HlmButtonImports } from '@spartan-ng/helm/button';
import { AuthService } from '../auth';
import { LiveStatus } from '../ui/live-status';

@Component({
  selector: 'fimra-stored-form-attachment',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButtonImports, LiveStatus],
  template: `
    @if (preview(); as source) {
      <img class="max-h-64 max-w-full rounded-control object-contain" [src]="source" [alt]="field() + ', ' + label()">
    }
    <button hlmBtn type="button" variant="link" size="sm" class="h-auto px-0 py-1"
      [disabled]="busy()" (click)="download()" [attr.aria-label]="'Download ' + label() + ' for ' + field()">
      Download {{ label() }}
    </button>
    <fimra-live-status class="block text-label text-text-secondary" [message]="message()" />
  `
})
export class StoredFormAttachment {
  readonly workspaceUrl = input.required<string>();
  readonly fileId = input.required<string>();
  readonly kind = input.required<'photo' | 'file' | 'signature'>();
  readonly field = input.required<string>();
  readonly label = input.required<string>();

  protected readonly preview = signal('');
  protected readonly message = signal('');
  protected readonly busy = signal(false);
  private readonly url = computed(() => this.workspaceUrl() + '/attachments/' + encodeURIComponent(this.fileId()));
  private readonly http = inject(HttpClient);
  private readonly auth = inject(AuthService);
  private readonly lifetime = inject(DestroyRef);

  constructor() {
    effect(onCleanup => {
      const url = this.url();
      this.preview.set('');
      if (this.kind() === 'file') return;
      let objectUrl = '';
      const request = this.http.get(url, { responseType: 'blob' }).subscribe({
        next: blob => {
          if (['image/jpeg', 'image/png', 'image/webp'].includes(blob.type)) {
            objectUrl = URL.createObjectURL(blob);
            this.preview.set(objectUrl);
          }
        },
        error: (error: HttpErrorResponse) => {
          if (error.status === 401) this.auth.sessionEnded();
        }
      });
      onCleanup(() => {
        request.unsubscribe();
        if (objectUrl) URL.revokeObjectURL(objectUrl);
      });
    });

  }

  protected async download(): Promise<void> {
    if (this.busy()) return;
    const url = this.url();
    this.busy.set(true);
    this.message.set('');
    try {
      const response = await firstValueFrom(this.http.get(url, { responseType: 'blob', observe: 'response' })
        .pipe(takeUntilDestroyed(this.lifetime)));
      if (url !== this.url() || !response.body) return;
      const named = /filename\*=UTF-8''([^;]+)/i.exec(response.headers.get('Content-Disposition') ?? '')?.[1];
      const link = document.createElement('a');
      link.download = this.label();
      if (named) {
        try { link.download = decodeURIComponent(named); } catch { /* Keep the default name. */ }
      }
      const objectUrl = URL.createObjectURL(response.body);
      link.href = objectUrl;
      link.click();
      setTimeout(() => URL.revokeObjectURL(objectUrl), 60_000);
    } catch (error) {
      if (this.lifetime.destroyed || url !== this.url()) return;
      if ((error as HttpErrorResponse).status === 401) this.auth.sessionEnded();
      this.message.set('The file could not download. Try again.');
    } finally {
      this.busy.set(false);
    }
  }
}
