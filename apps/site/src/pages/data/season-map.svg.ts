import { seasonHeat } from "../../lib/heat";
import detections from "../../../public/data/hexagons.json";
import { cellPath, hexPoster } from "../../lib/hex-poster";

export function GET() {
  return hexPoster(detections.features.map(({geometry, properties}) => ({
    path: cellPath(geometry.coordinates[0].slice(0, -1)),
    count: properties.counts.reduce((total, count) => total + count, 0),
  })), seasonHeat);
}
