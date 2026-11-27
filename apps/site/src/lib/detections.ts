import { asset } from "./paths";
import type { FeatureCollection, Polygon } from "geojson";

export type Detections = FeatureCollection<Polygon, { cell: string; counts: number[] }>;

let request: Promise<Detections> | undefined;
export function detections() {
  request ??= fetch(asset("data/hexagons.json")).then(response => {
    if (!response.ok) throw new Error("The detection archive could not be loaded");
    return response.json() as Promise<Detections>;
  }).catch(error => {
    request = undefined;
    throw error;
  });
  return request;
}
