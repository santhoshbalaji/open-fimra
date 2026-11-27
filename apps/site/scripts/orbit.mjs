import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { degreesLat, degreesLong, eciToGeodetic, gstime, propagate, twoline2satrec } from 'satellite.js';

const root = new URL('../', import.meta.url);
const endpoint = 'https://celestrak.org/NORAD/elements/gp.php?CATNR=43013&FORMAT=TLE';
const centralIndia = [81.335, 20.455];
const radius = 6371;
const radians = Math.PI / 180;
const minute = 60_000;

function readElements(input) {
  const lines = input.trim().split(/\r?\n/).filter(line => /^[12] 43013/.test(line));
  const checksum = (line) => [...line.slice(0, 68)].reduce((sum, character) =>
    sum + (character === '-' ? 1 : Number(character) || 0), 0) % 10;
  if (lines.length !== 2 || lines[0][0] !== "1" || lines[1][0] !== "2" || lines.some(line => line.length !== 69 || checksum(line) !== Number(line[68]))) {
    throw new Error('Expected two valid NOAA-20 element lines');
  }
  return lines;
}

let lines;
let origin = 'CelesTrak';
try {
  const response = await fetch(endpoint, { signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  lines = readElements(await response.text());
} catch {
  origin = 'archived CelesTrak elements';
  lines = readElements(await readFile(new URL('data/noaa20.tle', root), 'utf8'));
  console.warn('Using archived NOAA-20 elements.');
}
const record = twoline2satrec(...lines);
const shortYear = Number(lines[0].slice(18, 20));
const epoch = Date.UTC(shortYear >= 57 ? 1900 + shortYear : 2000 + shortYear, 0, 1) +
  (Number(lines[0].slice(20, 32)) - 1) * 1440 * minute;
const now = Date.now();
const archived = Math.abs(now - epoch) > 7 * 1440 * minute;
const start = Math.floor((archived ? epoch : now) / minute) * minute;

function coordinate(time) {
  const date = new Date(time);
  const result = propagate(record, date);
  if (!result?.position) throw new Error('NOAA-20 propagation failed');
  const position = eciToGeodetic(result.position, gstime(date));
  return [degreesLong(position.longitude), degreesLat(position.latitude)];
}

function distance([x, y], [a, b]) {
  const h = Math.sin((b - y) * radians / 2) ** 2 +
    Math.cos(y * radians) * Math.cos(b * radians) * Math.sin((a - x) * radians / 2) ** 2;
  return 2 * radius * Math.asin(Math.sqrt(h));
}

function daylight(time) {
  const date = new Date(time);
  const day = (time - Date.UTC(date.getUTCFullYear(), 0, 1)) / (1440 * minute);
  const declination = -23.44 * radians * Math.cos(2 * Math.PI * (day + 10) / 365.25);
  const hour = (date.getUTCHours() + date.getUTCMinutes() / 60 + centralIndia[0] / 15 - 12) * 15 * radians;
  const lat = centralIndia[1] * radians;
  return Math.sin(lat) * Math.sin(declination) + Math.cos(lat) * Math.cos(declination) * Math.cos(hour) > 0.1;
}

let closest;
let previous = distance(coordinate(start - minute), centralIndia);
let current = distance(coordinate(start), centralIndia);
for (let time = start; time < start + 4 * 1440 * minute; time += minute) {
  const next = distance(coordinate(time + minute), centralIndia);
  if (time >= (archived ? epoch : now) && current < 1520 && current <= previous && current < next && daylight(time)) {
    closest = time;
    break;
  }
  previous = current;
  current = next;
}
if (!closest) throw new Error('No daytime swath intersection found in the prediction period');

function bearing([lon, lat], [nextLon, nextLat]) {
  const delta = (nextLon - lon) * radians;
  const p = lat * radians;
  const q = nextLat * radians;
  return Math.atan2(Math.sin(delta) * Math.cos(q), Math.cos(p) * Math.sin(q) -
    Math.sin(p) * Math.cos(q) * Math.cos(delta));
}

function offset([lon, lat], angle, km) {
  const p = lat * radians;
  const d = km / radius;
  const lat2 = Math.asin(Math.sin(p) * Math.cos(d) + Math.cos(p) * Math.sin(d) * Math.cos(angle));
  const delta = Math.atan2(Math.sin(angle) * Math.sin(d) * Math.cos(p),
    Math.cos(d) - Math.sin(p) * Math.sin(lat2));
  return [lon + delta / radians, lat2 / radians];
}

const track = [];
const left = [];
const right = [];
const round = point => point.map(value => Math.round(value * 1e5) / 1e5);
for (let seconds = -360; seconds <= 360; seconds += 4) {
  const time = closest + seconds * 1000;
  const point = coordinate(time);
  const heading = bearing(point, coordinate(time + 1000));
  track.push(round(point));
  left.push(round(offset(point, heading - Math.PI / 2, 1520)));
  right.push(round(offset(point, heading + Math.PI / 2, 1520)));
}
const orbit = {
  time: new Date(closest).toISOString(),
  epoch: new Date(epoch).toISOString(),
  computedAt: new Date(now).toISOString(),
  archived, origin, track, left, right, target: centralIndia,
};
await mkdir(new URL('src/generated/', root), { recursive: true });
await mkdir(new URL('public/data/', root), { recursive: true });
const payload = JSON.stringify(orbit);
await writeFile(new URL('src/generated/orbit.json', root), payload);
await writeFile(new URL('public/data/orbit.json', root), payload);
console.log(`NOAA-20 pass: ${orbit.time} (${origin}, elements from ${orbit.epoch})`);
