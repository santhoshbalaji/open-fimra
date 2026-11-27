import { readFileSync, copyFileSync } from "node:fs";

const previous = process.argv[2];
const current = new URL("../public/data/live.json", import.meta.url);
try {
  const candidate = JSON.parse(readFileSync(previous, "utf8"));
  const saved = JSON.parse(readFileSync(current, "utf8"));
  const updated = Date.parse(candidate.updatedAt);
  const start = Date.parse(candidate.start);
  if (candidate.version !== 1 || !Number.isFinite(updated) || updated > Date.now() ||
      updated - start !== 7 * 86_400_000 ||
      !Array.isArray(candidate.cells) ||
      !candidate.cells.every(cell => Number.isSafeInteger(cell.count) && cell.count > 0 &&
        Array.isArray(cell.vertices) && cell.vertices.length >= 5 &&
        cell.vertices.every(vertex => Array.isArray(vertex) && vertex.length === 2 &&
          vertex.every(Number.isFinite))) ||
      candidate.cells.reduce((sum, cell) => sum + cell.count, 0) !== candidate.count) {
    throw new Error("invalid weekly snapshot");
  }
  if (updated > (Date.parse(saved.updatedAt) || 0)) copyFileSync(previous, current);
} catch (error) {
  console.warn(`::warning::Could not restore the published snapshot: ${error.message}`);
}
