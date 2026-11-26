import { Directive, ElementRef, effect, inject, input } from '@angular/core';
import { ShellNavigation } from './shell-navigation';

/** Restores the scroll offset after a paged list has rendered its rows. */
@Directive({
  selector: '[fimraListScroll]'
})
export class ShellListScroll {
  readonly scrollKey = input<string | null | undefined>(undefined, { alias: 'fimraListScroll' });
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly list = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  private key() {
    return this.scrollKey() === undefined ? this.navigation?.url() : this.scrollKey();
  }

  constructor() {
    effect(onCleanup => {
      const url = this.key();
      if (!url) return;
      const key = 'fimra.scroll:' + url;
      let top = 0, left = 0;
      try {
        const [vertical, horizontal] = (sessionStorage.getItem(key) ?? '').split(',');
        top = Number(vertical) || 0;
        left = Number(horizontal) || 0;
      } catch { /* Storage is optional. */ }
      let initial = requestAnimationFrame(restore);
      const observer = new MutationObserver(() => {
        cancelAnimationFrame(initial);
        initial = requestAnimationFrame(restore);
      });
      const list = this.list;

      function restore() {
        if (list.clientHeight && list.scrollHeight - list.clientHeight >= top) {
          list.scrollTop = top;
          list.scrollLeft = left;
          observer.disconnect();
        }
      }
      const save = () => {
        if (this.key() !== url || !list.clientHeight || list.scrollHeight - list.clientHeight < top) return;
        top = list.scrollTop;
        left = list.scrollLeft;
        try { sessionStorage.setItem(key, `${top},${left}`); } catch { /* Storage is optional. */ }
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
