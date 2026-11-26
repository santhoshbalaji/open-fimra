import { Directive, ElementRef, effect, inject, input } from '@angular/core';
import {
  lucideArrowDown, lucideArrowUp, lucideGripVertical, lucideTrash2, lucideUndo2, lucideBell, lucideBellOff, lucideCheck, lucideChevronDown, lucideChevronRight, lucideCircleAlert, lucideCircleCheck,
  lucideCircleX, lucideClock, lucideDownload, lucideEye, lucideEyeOff, lucideInfo, lucideKeyRound, lucideLayers,
  lucideFilePenLine, lucideListOrdered, lucideLoaderCircle, lucideLock, lucideLogOut, lucideMailCheck, lucideMailX, lucideMaximize, lucideMinus, lucideMoon,
  lucideMap, lucideFlame, lucideFileChartColumn, lucideLayoutDashboard, lucidePanelLeftClose, lucidePanelLeftOpen,
  lucideSmartphone, lucidePlus, lucideSettings, lucideSun, lucideTriangleAlert
} from '@ng-icons/lucide';

// Only the icons listed here are bundled. Add one by importing it above.
const icons = {
  'arrow-down': lucideArrowDown, 'arrow-up': lucideArrowUp, 'grip-vertical': lucideGripVertical, 'trash-2': lucideTrash2,
  'undo-2': lucideUndo2, 'bell': lucideBell, 'bell-off': lucideBellOff, 'check': lucideCheck, 'chevron-down': lucideChevronDown,
  'chevron-right': lucideChevronRight, 'circle-alert': lucideCircleAlert, 'circle-check': lucideCircleCheck,
  'circle-x': lucideCircleX, 'clock': lucideClock, 'download': lucideDownload, 'eye': lucideEye, 'eye-off': lucideEyeOff,
  'info': lucideInfo, 'key-round': lucideKeyRound, 'layers': lucideLayers, 'loader-circle': lucideLoaderCircle,
  'file-pen-line': lucideFilePenLine, 'list-ordered': lucideListOrdered, 'lock': lucideLock, 'log-out': lucideLogOut, 'mail-check': lucideMailCheck, 'mail-x': lucideMailX,
  'maximize': lucideMaximize, 'minus': lucideMinus, 'moon': lucideMoon, 'plus': lucidePlus, 'settings': lucideSettings,
  'map': lucideMap, 'flame': lucideFlame, 'file-chart-column': lucideFileChartColumn, 'layout-dashboard': lucideLayoutDashboard,
  'panel-left-close': lucidePanelLeftClose, 'panel-left-open': lucidePanelLeftOpen, 'smartphone': lucideSmartphone,
  'sun': lucideSun, 'triangle-alert': lucideTriangleAlert
} as const;
export type IconName = keyof typeof icons;

/** The shapes inside a bundled icon's svg, which are fixed strings from the icon package. */
export function iconShapes(name: IconName): string {
  const svg = icons[name];
  return svg.slice(svg.indexOf('>') + 1, svg.lastIndexOf('</svg>'));
}

/**
 * Draws a Lucide icon inside the host svg, for example `<svg fimraIcon="bell" aria-hidden="true" class="size-4">`.
 * Each icon is a short string, so the icons cost little in the first download.
 */
@Directive({
  selector: 'svg[fimraIcon]',
  host: {
    'width': '24', 'height': '24', 'viewBox': '0 0 24 24', 'fill': 'none', 'stroke': 'currentColor',
    'stroke-width': '2', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', '[attr.data-icon]': 'fimraIcon()'
  }
})
export class Icon {
  readonly fimraIcon = input.required<IconName>();

  constructor() {
    const svg = inject<ElementRef<SVGSVGElement>>(ElementRef).nativeElement;
    effect(() => { svg.innerHTML = iconShapes(this.fimraIcon()); });
  }
}
