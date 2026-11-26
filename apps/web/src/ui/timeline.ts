import { ChangeDetectionStrategy, Component, computed, input, signal } from '@angular/core';
import { HlmButton } from '@spartan-ng/helm/button';
import { defaultWorkspaceFormat, WorkspaceFormat } from '../workspace-format';

export interface TimelineEvent {
  id: string;
  title: string;
  at: Date;
  milestone?: boolean;
  detail?: string;
}

let timelineId = 0;

@Component({
  selector: 'fimra-timeline',
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [HlmButton],
  host: { class: 'block min-w-0' },
  styleUrl: './workspace-target.css',
  template: `
    <div class="grid gap-3">
      @if (hasOtherActivity()) {
        <button type="button" hlmBtn variant="outline" class="justify-self-start"
          [attr.aria-controls]="activityId" [attr.aria-expanded]="showAll()" (click)="showAll.set(!showAll())">
          {{ showAll() ? milestonesLabel() : allActivityLabel() }}
        </button>
      }
      @if (visibleEvents().length === 0) {
        <p class="text-body text-text-secondary">{{ emptyLabel() }}</p>
      }
      <ol [id]="activityId" role="list" class="grid gap-2">
        @for (event of visibleEvents(); track event.id) {
          <li class="grid gap-1 border-s-2 py-1 ps-3 wrap-anywhere"
            [class.border-action]="event.milestone" [class.border-divider]="!event.milestone">
            <p class="font-semibold">{{ event.title }}</p>
            <p class="text-label text-text-secondary">
              <time [attr.datetime]="event.at.toISOString()">{{ format().dateTime(event.at) }}</time>
              <span aria-hidden="true"> · </span>{{ format().age(event.at, now()) }}
            </p>
            @if (event.detail) { <p class="text-body whitespace-pre-line">{{ event.detail }}</p> }
          </li>
        }
      </ol>
    </div>
  `
})
export class Timeline {
  readonly events = input.required<readonly TimelineEvent[]>();
  readonly now = input.required<Date>();
  readonly format = input<WorkspaceFormat>(defaultWorkspaceFormat);
  readonly milestonesLabel = input('Show milestones');
  readonly allActivityLabel = input('Show all activity');
  readonly emptyLabel = input('No milestones yet.');

  protected readonly activityId = `fimra-activity-${++timelineId}`;
  protected readonly showAll = signal(false);
  private readonly milestones = computed(() => this.events().filter(event => event.milestone));
  protected readonly hasOtherActivity = computed(() => this.milestones().length < this.events().length);
  protected readonly visibleEvents = computed(() => this.showAll() ? this.events() : this.milestones());
}
