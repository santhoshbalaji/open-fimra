import { detections } from "./detections";

const fmt = new Intl.NumberFormat("en-IN");
const svgNS = "http://www.w3.org/2000/svg";
const sum = (counts: number[]) => counts.reduce((a, b) => a + b, 0);
const heat = (count: number) => count >= 150 ? "#ffe0ae" : count >= 45 ? "#ffb69b" : count >= 10 ? "#e87f77" : "#b25765";

export async function initSeasonMap() {
  const map = document.querySelector<SVGSVGElement>("#hex-map")!;
  const poster = document.querySelector<HTMLElement>(".hex-poster")!;
  const controls = document.querySelector<HTMLElement>("#map-controls")!;
  const picker = document.querySelector<HTMLSelectElement>("#month")!;
  const play = document.querySelector<HTMLButtonElement>("#play-months")!;
  const strip = document.querySelector<HTMLElement>(".month-strip")!;
  const buttons = Array.from(strip.querySelectorAll<HTMLButtonElement>("button"));
  const countLabel = document.querySelector<HTMLElement>("#month-count")!;
  const periodLabel = document.querySelector<HTMLElement>("#map-period")!;
  const cellInfo = document.querySelector<HTMLElement>("#cell-info")!;
  const keyboardStatus = document.querySelector<HTMLElement>("#cell-status")!;
  const data = await detections();
  const cells = data.features.map(feature => {
    const [vertices] = feature.geometry.coordinates;
    const outline = document.createElementNS(svgNS, "polygon");
    outline.setAttribute("points", vertices.map(([lon, lat]) =>
      `${((lon - 63) * 20).toFixed(1)},${((42 - lat) * 20).toFixed(1)}`).join(" "));
    outline.classList.add("hex-cell");
    const edges = vertices.slice(0, -1);
    const lat = sum(edges.map(vertex => vertex[1])) / edges.length;
    const lon = sum(edges.map(vertex => vertex[0])) / edges.length;
    return { outline, counts: feature.properties.counts,
      place: `${lat.toFixed(2)}° N, ${lon.toFixed(2)}° E`, count: 0 };
  });
  let visible = cells;
  let active: typeof cells[number] | undefined;
  let selected: typeof cells[number] | undefined;
  let month = -1;
  let interval: number | undefined;

  function show(cell: typeof cells[number] | undefined, announce = false) {
    active?.outline.removeAttribute("data-selected");
    active = cell;
    cell?.outline.setAttribute("data-selected", "");
    const label = cell ? `${fmt.format(cell.count)} detections · ${cell.place} (cell centre)` : "Select a month or inspect a hexagon.";
    cellInfo.textContent = label;
    if (announce) keyboardStatus.textContent = label;
  }

  function render(value: number, announce = false) {
    month = value;
    picker.value = String(value);
    const nodes = document.createDocumentFragment();
    let total = 0;
    visible = cells.filter(cell => {
      const count = value < 0 ? sum(cell.counts) : cell.counts[value];
      cell.count = count;
      if (!count) return false;
      total += count;
      cell.outline.setAttribute("fill", heat(count));
      nodes.append(cell.outline);
      return true;
    });
    map.replaceChildren(nodes);
    const period = picker.selectedOptions[0].textContent;
    periodLabel.textContent = period;
    countLabel.textContent = `${fmt.format(total)} detections`;
    buttons.forEach((button, index) => button.setAttribute("aria-pressed", String(index === value)));
    if (selected && !selected.count) selected = undefined;
    show(selected);
    if (announce) keyboardStatus.textContent = `${period}. ${countLabel.textContent}.`;
  }

  for (const cell of cells) {
    cell.outline.addEventListener("pointerenter", () => show(cell));
    cell.outline.addEventListener("pointerleave", () => show(selected));
    cell.outline.addEventListener("click", () => {
      selected = cell;
      show(cell, true);
    });
  }
  map.addEventListener("keydown", event => {
    if (!visible.length || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const index = active ? visible.indexOf(active) : -1;
    const step = event.key === "ArrowLeft" ? -1 : 1;
    const next = event.key === "Home" ? 0 : event.key === "End" ? visible.length - 1 :
      index < 0 ? (step > 0 ? 0 : visible.length - 1) : (index + step + visible.length) % visible.length;
    selected = visible[next];
    show(selected, true);
  });

  function stop() {
    window.clearInterval(interval);
    interval = undefined;
    play.textContent = "▶";
    play.setAttribute("aria-label", "Play monthly time-lapse");
  }
  function pick(value: number) {
    stop();
    render(value, true);
  }
  play.addEventListener("click", () => {
    if (interval !== undefined) {
      stop();
      return;
    }
    render(0, true);
    play.textContent = "Ⅱ";
    play.setAttribute("aria-label", "Pause monthly time-lapse");
    interval = window.setInterval(() => {
      render(month + 1);
      if (month === 7) stop();
    }, 1050);
  });
  buttons.forEach((button, i) => button.addEventListener("click", () => pick(i)));
  picker.addEventListener("change", () => pick(Number(picker.value)));
  render(-1);
  map.setAttribute("tabindex", "0");
  controls.hidden = strip.hidden = false;
  poster.hidden = true;
}
