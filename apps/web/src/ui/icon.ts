import { Directive } from '@angular/core';
import { IconRenderer } from './icon-renderer';
import { shellIcons } from './shell-icon';
import {
  lucideArrowDown, lucideArrowUp, lucideGripVertical, lucideTrash2, lucideUndo2,
  lucideBell, lucideBellOff, lucideCheck, lucideChevronDown, lucideChevronRight,
  lucideCircleAlert, lucideCircleCheck, lucideCircleX, lucideDownload, lucideEye,
  lucideEyeOff, lucideInfo, lucideLoaderCircle, lucideLock, lucideLogOut,
  lucideMailCheck, lucideMailX, lucideMaximize, lucideMinus, lucidePlus
} from '@ng-icons/lucide';

const icons = {
  'arrow-down': lucideArrowDown,
  'arrow-up': lucideArrowUp,
  'grip-vertical': lucideGripVertical,
  'trash-2': lucideTrash2,
  'undo-2': lucideUndo2,
  'bell': lucideBell,
  'bell-off': lucideBellOff,
  'check': lucideCheck,
  'chevron-down': lucideChevronDown,
  'chevron-right': lucideChevronRight,
  'circle-alert': lucideCircleAlert,
  'circle-check': lucideCircleCheck,
  'circle-x': lucideCircleX,
  'download': lucideDownload,
  'eye': lucideEye,
  'eye-off': lucideEyeOff,
  'info': lucideInfo,
  'loader-circle': lucideLoaderCircle,
  'lock': lucideLock,
  'log-out': lucideLogOut,
  'mail-check': lucideMailCheck,
  'mail-x': lucideMailX,
  'maximize': lucideMaximize,
  'minus': lucideMinus,
  'plus': lucidePlus,
} as const;
export type IconName = keyof typeof shellIcons | keyof typeof icons;

@Directive({ selector: 'svg[fimraIcon]' })
export class Icon extends IconRenderer<IconName> {
  protected shapes(name: IconName): string {
    if (name in shellIcons) return shellIcons[name as keyof typeof shellIcons];
    const svg = icons[name as keyof typeof icons];
    return svg.slice(svg.indexOf('>') + 1, svg.lastIndexOf('</svg>'));
  }
}
