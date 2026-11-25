import { Component, OnInit, inject } from '@angular/core';
import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { BrnButton } from '@spartan-ng/brain/button';
import { LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun } from '@lucide/angular';
import { AuthService } from './auth';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';
import { DemoMap } from './demo-map';

@Component({
  selector: 'fimra-root',
  imports: [CdkMenu, CdkMenuItem, CdkMenuTrigger, BrnButton, DemoMap,
    LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun],
  templateUrl: './app.html'
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);

  ngOnInit(): void {
    void this.auth.open();
  }
}
