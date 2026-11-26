import { Component, signal } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { DataTable, TableColumn, TablePage, TablePosition } from './data-table';
import { DataState } from './state-region';

interface Row { id: string; place: string }
const longPlace = 'উত্তর পাহাড় সংরক্ষিত বনাঞ্চলের পূর্ব প্রান্তের পর্যবেক্ষণ কেন্দ্র';

@Component({
  imports: [DataTable],
  template: `
    <fimra-data-table caption="Alerts" [columns]="columns" [page]="page()" [state]="state()"
      [rowKey]="key" [rowLabel]="label" [rowLanguage]="language" [selected]="selected()"
      (selectionChange)="selected.set($event)" (pageRequest)="request = $event" />
  `
})
class Host {
  readonly columns: readonly TableColumn<Row>[] = [
    { id: 'id', label: 'Alert ID', value: row => row.id, rowHeading: true },
    { id: 'place', label: 'Place', value: row => row.place, sortable: true },
    { id: 'unit', label: 'Unit', value: () => 'Demo range', sortable: true }
  ];
  readonly key = (row: Row) => row.id;
  readonly label = (row: Row) => row.place;
  readonly language = () => 'bn';
  readonly page = signal<TablePage<Row>>({
    rows: [{ id: 'A1', place: longPlace }, { id: 'A2', place: 'East ridge' }],
    nextCursor: 'second', position: { cursor: null, previousCursors: [] }
  });
  readonly state = signal<DataState>({ kind: 'ready' });
  readonly selected = signal<ReadonlySet<string>>(new Set());
  request?: TablePosition;
}

function setup() {
  const fixture = TestBed.createComponent(Host);
  fixture.detectChanges();
  const element: HTMLElement = fixture.nativeElement;
  return { fixture, element, host: fixture.componentInstance };
}

function press(fixture: ComponentFixture<Host>, name: string) {
  const root: HTMLElement = fixture.nativeElement;
  const button = [...root.querySelectorAll('button')].find(candidate => candidate.textContent?.trim().startsWith(name));
  expect(button, name).toBeDefined();
  button?.click();
  fixture.detectChanges();
}

describe('DataTable', () => {
  it('requests sorting and marks only the successfully sorted header', () => {
    const { fixture, element, host } = setup();
    expect(element.querySelector('[aria-sort]')).toBeNull();
    press(fixture, 'Place');
    expect(host.request).toEqual({ cursor: null, previousCursors: [], sort: { columnId: 'place', direction: 'ascending' } });
    expect(element.querySelector('[aria-sort]')).toBeNull();
    host.page.update(page => ({ ...page, position: host.request! }));
    fixture.detectChanges();
    expect(element.querySelector('[aria-sort]')?.getAttribute('aria-sort')).toBe('ascending');
    press(fixture, 'Place');
    expect(host.request?.sort?.direction).toBe('descending');
    host.page.update(page => ({ ...page, position: host.request! }));
    fixture.detectChanges();
    expect(element.querySelectorAll('[aria-sort]')).toHaveLength(1);
    expect(element.querySelector('[aria-sort]')?.getAttribute('aria-sort')).toBe('descending');
  });

  it('toggles columns with named checkboxes and keeps the row heading', () => {
    const { fixture, element } = setup();
    const [heading, place] = element.querySelectorAll<HTMLInputElement>('fieldset input');
    expect(heading.checked && heading.disabled).toBe(true);
    expect(place.labels?.[0].textContent?.trim()).toBe('Place');
    expect(place.checked).toBe(true);
    place.click();
    fixture.detectChanges();
    expect(place.checked).toBe(false);
    expect(element.querySelector('thead')?.textContent).not.toContain('Place');
    place.click();
    fixture.detectChanges();
    expect(element.querySelector('thead')?.textContent).toContain('Place');
  });

  it('keeps a long Bengali label intact and keeps rows visible while refreshing', () => {
    const { fixture, element, host } = setup();
    expect(element.querySelector('tbody td:nth-of-type(2)')?.textContent?.trim()).toBe(longPlace);
    host.state.set({ kind: 'stale', observedAt: { dateTime: '2026-08-01T09:00:00Z', label: '2:30 pm IST' } });
    fixture.detectChanges();
    expect(element.querySelector('tbody td:nth-of-type(2)')?.textContent?.trim()).toBe(longPlace);
    expect(element.querySelector('table')?.closest('[hidden]')).toBeNull();
    press(fixture, 'Next');
    expect(host.request).toBeUndefined();
  });

  it('goes back through visited cursors and retains position after a failed request', () => {
    const { fixture, host } = setup();
    press(fixture, 'Next');
    expect(host.request).toEqual({ cursor: 'second', previousCursors: [null] });
    host.page.update(page => ({ ...page, position: host.request!, nextCursor: 'third' }));
    fixture.detectChanges();
    press(fixture, 'Next');
    expect(host.request).toEqual({ cursor: 'third', previousCursors: [null, 'second'] });
    host.state.set({ kind: 'error', keepContent: true });
    fixture.detectChanges();
    expect(host.page().position.cursor).toBe('second');
    press(fixture, 'Next');
    host.page.update(page => ({ ...page, position: host.request!, nextCursor: null }));
    host.state.set({ kind: 'ready' });
    fixture.detectChanges();
    press(fixture, 'Previous');
    expect(host.request).toEqual({ cursor: 'second', previousCursors: [null] });
    host.page.update(page => ({ ...page, position: host.request!, nextCursor: 'third' }));
    fixture.detectChanges();
    press(fixture, 'Previous');
    expect(host.request).toEqual({ cursor: null, previousCursors: [] });
  });

  it('supports individual and page selection without clearing rows on other pages', () => {
    const { fixture, element, host } = setup();
    host.selected.set(new Set(['outside']));
    fixture.detectChanges();
    const [page, first, second] = element.querySelectorAll<HTMLInputElement>('table input');
    first.click();
    fixture.detectChanges();
    expect(host.selected()).toEqual(new Set(['outside', 'A1']));
    expect(page.indeterminate).toBe(true);
    page.click();
    fixture.detectChanges();
    expect(page.checked && first.checked && second.checked).toBe(true);
    page.click();
    fixture.detectChanges();
    expect(host.selected()).toEqual(new Set(['outside']));
  });
});
