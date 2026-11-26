import { classes } from './detections';

const seriesSlots = 8;

export function detectionCatalog(...counts: Readonly<Record<string, number>>[]): Record<string, string> {
  const unknown = [...new Set(counts.flatMap(group => Object.keys(group)))].sort();
  return { ...classes, ...Object.fromEntries(unknown.filter(id => !Object.hasOwn(classes, id)).map(id => [id, id])) };
}

export interface DetectionSeries {
  id: string;
  label: string;
  slot: number;
  count: number;
}

/** Assign slots from the full catalog before choosing which series to show. */
export function detectionSeries(
  counts: Readonly<Record<string, number>>,
  selected = '',
  catalog: Readonly<Record<string, string>> = classes
): DetectionSeries[] {
  const keys = [...new Set([...Object.keys(catalog), ...Object.keys(counts).sort()])];
  const separate = keys.length > seriesSlots ? seriesSlots - 1 : seriesSlots;
  const visible = (key: string) => !selected || selected === key;
  const rows = keys.slice(0, separate).flatMap((id, index) => visible(id)
    ? [{ id, label: Object.hasOwn(catalog, id) ? catalog[id] : id, slot: index + 1, count: counts[id] ?? 0 }]
    : []);
  const other = keys.slice(separate).filter(visible);
  if (other.length) {
    rows.push({ id: '__other__', label: 'Other', slot: seriesSlots,
      count: other.reduce((sum, key) => sum + (counts[key] ?? 0), 0) });
  }
  return rows;
}
