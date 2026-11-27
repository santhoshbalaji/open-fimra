import { heat } from "./heat";

export const cellPath = (vertices: number[][]) =>
  vertices.map(([lon, lat], i) => `${i ? "L" : "M"}${((lon - 63) * 20).toFixed(1)} ${((42 - lat) * 20).toFixed(1)}`).join("") + "Z";

export function hexPoster(cells: { path: string; count: number }[], scale: Parameters<typeof heat>[1]) {
  const bands = new Map<string, string[]>();
  for (const {path, count} of cells) {
    const color = heat(count, scale);
    if (!bands.has(color)) bands.set(color, []);
    bands.get(color)!.push(path);
  }
  const paths = [...bands].map(([color, paths]) => `<path fill="${color}" d="${paths.join("")}"/>`);
  return new Response(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 780 780">${paths.join("")}</svg>`, {
    headers: { "Content-Type": "image/svg+xml" },
  });
}
