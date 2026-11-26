import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { describe, expect, it } from 'vitest';
import { ActiveFilter, FilterBar } from './filter-bar';

@Component({
  imports: [FilterBar],
  template: `<fimra-filter-bar [filters]="filters()" (remove)="remove($event)" (clearAll)="filters.set([])" />`
})
class Host {
  readonly filters = signal<readonly ActiveFilter[]>([
    { id: 'state', label: 'Status: Open' }, { id: 'place', label: 'Place: Demo range' }
  ]);

  remove(id: string) {
    this.filters.update(filters => filters.filter(filter => filter.id !== id));
  }
}

describe('FilterBar', () => {
  it('announces active filters and removes filters with focus recovery', () => {
    const fixture = TestBed.createComponent(Host);
    fixture.detectChanges();
    const element: HTMLElement = fixture.nativeElement;
    const status = element.querySelector('[role=status]');
    expect(status?.textContent).toBe('2 active filters. Status: Open. Place: Demo range');
    const [first, next, clear] = element.querySelectorAll('button');
    first.click();
    fixture.detectChanges();
    expect(document.activeElement).toBe(next);
    expect(status?.textContent).toBe('1 active filter. Place: Demo range');
    clear.click();
    fixture.detectChanges();
    expect(document.activeElement).toBe(element.querySelector('[tabindex="-1"]'));
    expect(status?.textContent).toBe('No active filters');
    expect(element.querySelector('button')).toBeNull();
  });
});
