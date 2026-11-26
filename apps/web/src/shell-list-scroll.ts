import { Directive, ElementRef, effect, inject, input } from '@angular/core';
import { NavigationStart, Router } from '@angular/router';
import { ShellNavigation } from './shell-navigation';

/** Restores the scroll offset after a paged list has rendered its rows. */
@Directive({
  selector: '[fimraListScroll]'
})
export class ShellListScroll {
  readonly scrollKey = input<string | null | undefined>(undefined, { alias: 'fimraListScroll' });
  private readonly navigation = inject(ShellNavigation, { optional: true });
  private readonly router = inject(Router, { optional: true });
  private readonly list = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  private key() {
    const explicit = this.scrollKey();
    // A bare fimraListScroll attribute supplies an empty string. Null disables restoration.
    return explicit === undefined || explicit === '' ? this.navigation?.url() : explicit;
  }

  constructor() {
    effect(onCleanup => {
      const url = this.key();
      if (!url) return;
      const routeKey = 'fimra.scroll:' + url;
      // A table can scroll independently inside a route's list.
      const key = this.scrollKey() ? 'fimra.table-scroll:' + url : routeKey;
      let top = 0, left = 0;
      let restored = false;
      try {
        const [vertical, horizontal] = (sessionStorage.getItem(key) ?? sessionStorage.getItem(routeKey) ?? '').split(',');
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
          restored = true;
          observer.disconnect();
        }
      }
      const save = () => {
        if (!restored || this.key() !== url || !list.clientHeight || list.scrollHeight - list.clientHeight < top) return;
        top = list.scrollTop;
        left = list.scrollLeft;
        try { sessionStorage.setItem(key, `${top},${left}`); } catch { /* Storage is optional. */ }
      };
      observer.observe(list, { childList: true, subtree: true });
      list.addEventListener('scroll', save, { passive: true });
      // A row can open before the browser dispatches the scroll event that brought it into view.
      const navigation = this.router?.events.subscribe(event => {
        if (event instanceof NavigationStart) save();
      });
      onCleanup(() => {
        navigation?.unsubscribe();
        list.removeEventListener('scroll', save);
        cancelAnimationFrame(initial);
        observer.disconnect();
      });
    });
  }
}
