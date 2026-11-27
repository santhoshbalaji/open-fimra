import { asset } from "./paths";
import { Map } from "maplibre-gl";
import type { FeatureCollection, LineString, Point } from "geojson";
import { detections } from "./detections";

type Position = [number, number];
interface Orbit {
  track: Position[];
  left: Position[];
  right: Position[];
}

const xmlns = "http://www.w3.org/2000/svg";
const duration = 20_000;
const centre: Position = [81, 18];

export async function initGlobe(visual: HTMLElement) {
  const container = visual.querySelector<HTMLElement>(".globe")!;
  const [archive, orbit] = await Promise.all([
    detections(),
    fetch(asset("data/orbit.json")).then(response => {
      if (!response.ok) throw new Error("Orbit unavailable");
      return response.json() as Promise<Orbit>;
    }),
  ]);

  const points: FeatureCollection<Point, Record<string, number>> = {
    type: "FeatureCollection",
    features: archive.features.map(({ geometry, properties }) => {
      const edges = geometry.coordinates[0].slice(0, -1);
      const average = (index: number) => edges.reduce((total, point) => total + point[index], 0) / edges.length;
      let cumulative = 0;
      return {
        type: "Feature",
        geometry: { type: "Point", coordinates: [average(0), average(1)] },
        properties: Object.fromEntries(properties.counts.map((count, month) => [`m${month}`, cumulative += count])),
      };
    }),
  };
  const line: LineString = { type: "LineString", coordinates: orbit.track };
  const zoom = () => Math.log2(container.clientWidth * .69 * Math.PI / 512);
  const map = new Map({
    container,
    center: centre,
    zoom: zoom(),
    minZoom: -1,
    interactive: false,
    attributionControl: false,
    fadeDuration: 200,
    renderWorldCopies: false,
    style: {
      version: 8,
      projection: { type: "globe" },
      sky: { "atmosphere-blend": 1, "sky-color": "#0a1721", "horizon-color": "#527c99" },
      sources: {
        marble: {
          type: "raster",
          tiles: [new URL(asset("media/night/{z}/{x}/{y}.webp"), window.location.href).href.replaceAll("%7B", "{").replaceAll("%7D", "}")],
          tileSize: 256,
          maxzoom: 3,
        },
        track: { type: "geojson", data: { type: "Feature", properties: {}, geometry: line } },
        heat: { type: "geojson", data: points },
      },
      layers: [
        { id: "night", type: "raster", source: "marble", paint: { "raster-brightness-max": .86 } },
        { id: "track", type: "line", source: "track", paint: { "line-color": "#afd5e8", "line-width": 1, "line-dasharray": [2, 3], "line-opacity": .75 } },
        { id: "heat-halo", type: "circle", source: "heat", paint: { "circle-color": "#fa8c79", "circle-radius": 5, "circle-blur": 1, "circle-opacity": .6 } },
        { id: "heat-core", type: "circle", source: "heat", paint: { "circle-color": ["step", ["get", "m0"], "#c9958a", 30, "#ffaf97", 150, "#ffe6bc"], "circle-radius": ["step", ["get", "m0"], 1, 30, 1.4, 150, 2.5], "circle-opacity": .9 } },
      ],
    },
  });

  try {
    await new Promise<void>((resolve, reject) => {
      const timeout = window.setTimeout(() => reject(new Error("Globe timed out")), 10_000);
      map.once("load", () => {
        window.clearTimeout(timeout);
        resolve();
      });
    });
  } catch (error) {
    map.remove();
    throw error;
  }

  const scan = document.createElementNS(xmlns, "svg");
  scan.classList.add("orbit-scan");
  const area = document.createElementNS(xmlns, "polygon");
  const edge = document.createElementNS(xmlns, "line");
  scan.append(area, edge);
  const satellite = document.createElement("div");
  satellite.className = "moving-satellite";
  satellite.append(visual.querySelector(".satellite-drawing")!.cloneNode(true));
  container.append(scan, satellite);

  const canvas = map.getCanvas();
  canvas.removeAttribute("tabindex");
  canvas.removeAttribute("aria-label");
  let playing = false;
  let onscreen = false;
  let elapsed = 0;
  let previousFrame: number | undefined;
  let raf = 0;
  let previousMonth = -1;
  const period = visual.querySelector(".orbit-period")!;
  function position(track: Position[], sample: number): Position {
    const index = Math.floor(sample);
    const point = track[index];
    const next = track[index + 1] ?? point;
    const fraction = sample - index;
    return [point[0] + (next[0] - point[0]) * fraction, point[1] + (next[1] - point[1]) * fraction];
  }

  function draw(time: number) {
    raf = 0;
    if (!playing || !onscreen || document.hidden) return;
    raf = requestAnimationFrame(draw);
    elapsed = (elapsed + (previousFrame === undefined ? 0 : time - previousFrame)) % duration;
    previousFrame = time;
    const progress = elapsed / duration;
    const month = Math.min(7, Math.floor(progress * 10));
    if (month !== previousMonth) {
      const count = ["get", `m${month}`] as const;
      for (const layer of ["heat-halo", "heat-core"]) map.setFilter(layer, [">", [...count], 0]);
      map.setPaintProperty("heat-core", "circle-color", ["step", count, "#c9958a", 30, "#ffaf97", 150, "#ffe6bc"]);
      map.setPaintProperty("heat-core", "circle-radius", ["step", count, 1, 30, 1.4, 150, 2.5]);
      previousMonth = month;
      period.textContent = `${["NOV", "DEC", "JAN", "FEB", "MAR", "APR", "MAY", "JUN"][month]} · CUMULATIVE`;
    }
    const index = progress * (orbit.track.length - 1);
    const start = Math.max(0, index - 7);
    const [a, b, c, d] = [
      position(orbit.left, start), position(orbit.left, index),
      position(orbit.right, index), position(orbit.right, start),
    ].map(point => map.project(point));
    area.setAttribute("points", [a, b, c, d].map(point => `${point.x},${point.y}`).join(" "));
    edge.setAttribute("x1", String(b.x));
    edge.setAttribute("y1", String(b.y));
    edge.setAttribute("x2", String(c.x));
    edge.setAttribute("y2", String(c.y));
    const loc = map.project(position(orbit.track, index));
    // Lift the illustration radially from the sub-satellite point. The spacecraft
    // and cross-track strip use the same sample from the propagated orbit.
    const x = loc.x + (loc.x - container.clientWidth / 2) * .13 + 12;
    const y = loc.y + (loc.y - container.clientHeight / 2) * .13 - 28;
    satellite.style.transform = `translate3d(${x}px,${y}px,0) translate(-50%,-50%) rotate(-13deg)`;
  }

  function schedule() {
    if (playing && onscreen && !document.hidden && !raf) raf = requestAnimationFrame(draw);
    else if (!playing || !onscreen || document.hidden) {
      cancelAnimationFrame(raf);
      raf = 0;
      previousFrame = undefined;
    }
  }

  new IntersectionObserver(([entry]) => {
    onscreen = entry.isIntersecting;
    schedule();
  }).observe(visual);
  new ResizeObserver(() => {
    map.resize();
    map.jumpTo({ zoom: zoom() });
  }).observe(container);
  document.addEventListener("visibilitychange", schedule);

  return {
    setPlaying(next: boolean) {
      playing = next;
      schedule();
    },
  };
}
