import { ChangeDetectionStrategy, Component, input } from '@angular/core';

/** Keep mounted and change the message when an operation has meaningful feedback. */
@Component({
  selector: 'fimra-live-status',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { role: 'status', 'aria-live': 'polite', 'aria-atomic': 'true' },
  template: '{{ message().trim() }}'
})
export class LiveStatus {
  readonly message = input('');
}
