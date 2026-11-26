import { NgTemplateOutlet } from '@angular/common';
import { ChangeDetectionStrategy, Component, TemplateRef, computed, input, output } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { Icon, IconName } from './icon';
import { LiveStatus } from './live-status';

interface StateMessage {
  heading?: string;
  detail?: string;
}

export type DataState =
  | { kind: 'ready'; announcement?: string }
  | ({ kind: 'loading' | 'empty' | 'forbidden' } & StateMessage)
  | ({ kind: 'stale'; observedAt: { dateTime: string; label: string } } & StateMessage)
  | ({ kind: 'error'; keepContent?: boolean } & StateMessage);

const messages = {
  loading: { heading: 'Loading', icon: 'loader-circle' },
  empty: { heading: 'No results', icon: 'info' },
  forbidden: { heading: 'You do not have access', icon: 'lock' },
  stale: { heading: 'Refreshing', icon: 'loader-circle' },
  error: { heading: 'Could not load data', icon: 'circle-x' }
} satisfies Record<Exclude<DataState['kind'], 'ready'>, { heading: string; icon: IconName }>;

@Component({
  selector: 'fimra-state-region',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton, Icon, LiveStatus, NgTemplateOutlet],
  template: `
    <fimra-live-status class="sr-only" [message]="announcement()" />
    <div #region tabindex="-1" class="rounded-control focus-visible:outline-2 focus-visible:outline-ring">
      @if (placeholder(); as template) {
        <div aria-hidden="true"><ng-container [ngTemplateOutlet]="template" /></div>
      } @else if (message(); as message) {
        <div class="flex flex-wrap items-start justify-between gap-3 rounded-control border border-border bg-card p-4">
          <div class="flex min-w-0 items-start gap-2">
            <svg [fimraIcon]="message.icon" aria-hidden="true" class="mt-0.5 size-4 shrink-0"
              [class.motion-safe:animate-spin]="message.icon === 'loader-circle'"></svg>
            <div>
              <p class="font-medium">{{ message.heading }}</p>
              @if (observedAt(); as time) {
                <p class="text-text-secondary">Showing data from <time [attr.datetime]="time.dateTime">{{ time.label }}</time>, refreshing.</p>
              }
              @if (message.detail) { <p class="text-text-secondary">{{ message.detail }}</p> }
            </div>
          </div>
          @if (state().kind === 'error') {
            <button type="button" hlmBtn variant="outline" (click)="region.focus(); retry.emit()">Retry</button>
          }
        </div>
      }
      <div [hidden]="!showContent()"><ng-content /></div>
    </div>
  `
})
export class StateRegion {
  readonly state = input.required<DataState>();
  /** An optional decorative skeleton for the initial load. Supply no interactive content. */
  readonly loadingPlaceholder = input<TemplateRef<unknown>>();
  readonly retry = output<void>();

  protected readonly placeholder = computed(() => this.state().kind === 'loading' ? this.loadingPlaceholder() : undefined);

  protected readonly message = computed(() => {
    const state = this.state();
    if (state.kind === 'ready') return null;
    const fallback = messages[state.kind];
    const detail = state.detail?.trim() ||
      (state.kind === 'error' && state.keepContent ? 'Showing the last available data.' : undefined);
    return { heading: state.heading?.trim() || fallback.heading, detail, icon: fallback.icon };
  });

  protected readonly showContent = computed(() => {
    const state = this.state();
    return state.kind === 'ready' || state.kind === 'stale' || (state.kind === 'error' && state.keepContent);
  });

  protected readonly observedAt = computed(() => {
    const state = this.state();
    return state.kind === 'stale' ? state.observedAt : null;
  });

  protected readonly announcement = computed(() => {
    const message = this.message();
    // The timestamp stays available in the visible message without announcing every change to it.
    const state = this.state();
    return state.kind === 'ready' ? state.announcement ?? '' :
      [message?.heading, state.kind === 'stale' ? 'Showing the last available data' : '', message?.detail]
        .filter(Boolean).join('. ');
  });
}
