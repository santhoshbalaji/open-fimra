import { weeklyHeat } from "../../lib/heat";
import { cellPath, hexPoster } from "../../lib/hex-poster";
import { live } from "../../lib/live";

export function GET() {
  return hexPoster(live.cells.map(({ vertices, count }) => ({ path: cellPath(vertices), count })), weeklyHeat);
}
