import { Component, OnInit, inject, signal } from '@angular/core';
import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { BrnButton } from '@spartan-ng/brain/button';
import { LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun } from '@lucide/angular';
import { AuthService } from './auth';
import { ThemeService } from './theme';
import { WorkspaceService } from './workspace';
import { DemoMap } from './demo-map';
import { Layers } from './layers';

import { Dashboard } from './dashboard';
import { DashboardFilters } from './dashboard-filters';
import { classes, ForestClass } from './detections';

@Component({
  selector: 'fimra-root',
  imports: [CdkMenu, CdkMenuItem, CdkMenuTrigger, BrnButton, DemoMap, Layers, Dashboard,
    LucideChevronDown, LucideCircleUserRound, LucideLogOut, LucideMoon, LucideSun],
  templateUrl: './app.html'
})
export class App implements OnInit {
  readonly workspace = inject(WorkspaceService);
  readonly auth = inject(AuthService);
  readonly appearance = inject(ThemeService);
  readonly showLayers = signal(false);

  readonly dashboard = signal(false);
  readonly filters = inject(DashboardFilters);
  readonly classes = classes;
  readonly classKeys = Object.keys(classes) as ForestClass[];

  ngOnInit(): void {
    void this.auth.open();
  }
}
