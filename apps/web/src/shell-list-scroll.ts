import { Directive, ElementRef, effect, inject } from '@angular/core';
import { ShellNavigation } from './shell-navigation';

/** Restores the scroll offset after a paged list has rendered its rows. */
@Directive({
  selector: '[fimraListScroll]'
})
export class ShellListScroll {
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly list = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  constructor() {
    effect(onCleanup => {
      const url = this.navigation?.url();
      if (!url) return;
      const key = 'fimra.scroll:' + url;
      let offset = 0;
      try { offset = Number(sessionStorage.getItem(key)) || 0; } catch { /* Storage is optional. */ }
      let initial = requestAnimationFrame(restore);
      const observer = new MutationObserver(() => {
        cancelAnimationFrame(initial);
        initial = requestAnimationFrame(restore);
      });
      const list = this.list;

      function restore() {
        if (list.clientHeight && list.scrollHeight - list.clientHeight >= offset) {
          list.scrollTop = offset;
          observer.disconnect();
        }
      }
      const save = () => {
        if (this.navigation?.url() !== url || !list.clientHeight || list.scrollHeight - list.clientHeight < offset) return;
        offset = list.scrollTop;
        try { sessionStorage.setItem(key, String(offset)); } catch { /* Storage is optional. */ }
      };
      observer.observe(list, { childList: true, subtree: true });
      list.addEventListener('scroll', save, { passive: true });
      onCleanup(() => {
        list.removeEventListener('scroll', save);
        cancelAnimationFrame(initial);
        observer.disconnect();
      });
    });
  }
}
