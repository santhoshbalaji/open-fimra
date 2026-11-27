const root = document.documentElement;
const theme = document.querySelector<HTMLSelectElement>("#theme")!;
const motion = matchMedia("(prefers-reduced-motion: reduce)");

theme.value = root.dataset.theme ?? "system";
theme.addEventListener("change", () => {
  if (theme.value === "system") delete root.dataset.theme;
  else root.dataset.theme = theme.value;
  try {
    localStorage.setItem("fimra-site-theme", theme.value);
  } catch {}
});

const upcoming = document.querySelector<HTMLDetailsElement>(".upcoming")!;
document.addEventListener("click", event => {
  if (event.target instanceof Node && !upcoming.contains(event.target)) upcoming.open = false;
});
upcoming.addEventListener("keydown", event => {
  if (event.key === "Escape" && upcoming.open) {
    upcoming.open = false;
    upcoming.querySelector("summary")?.focus();
  }
});

upcoming.querySelectorAll("a").forEach(link => link.addEventListener("click", () => {
  upcoming.open = false;
  const target = document.getElementById(link.hash.slice(1));
  target?.setAttribute("tabindex", "-1");
  target?.focus({ preventScroll: true });
}));

const header = document.querySelector<HTMLElement>(".site-header")!;
let headerHeight = 0;
new ResizeObserver(([entry]) => {
  headerHeight = entry.borderBoxSize[0].blockSize;
  root.style.setProperty("--header-height", `${headerHeight}px`);
}).observe(header);

const space = document.querySelector<HTMLElement>("#space")!;
const story = space.querySelector<HTMLElement>(".space-story")!;
const links = [...document.querySelectorAll<HTMLAnchorElement>(".chapter-nav a[href^='#']")];
const chapters = links.flatMap(link => {
  const section = document.getElementById(link.hash.slice(1));
  return section ? [{ link, section }] : [];
});
const spaceFrames = [...story.querySelectorAll<HTMLElement>("[data-frame]")];
const steps = [...story.querySelectorAll<HTMLElement>("[data-step]")];
const spaceCount = document.querySelector<HTMLElement>("#space-count")!;
let renderFrame = 0;
let current = 0;

function update() {
  renderFrame = 0;
  const length = root.scrollHeight - window.innerHeight;
  root.style.setProperty("--progress", String(length > 0 ? window.scrollY / length : 0));
  const edge = headerHeight + 36;
  const activeChapter = chapters.findLast(({section}) => section.getBoundingClientRect().top <= edge);
  links.forEach(link => {
    if (link === activeChapter?.link) link.setAttribute("aria-current", "location");
    else link.removeAttribute("aria-current");
  });

  const bounds = space.getBoundingClientRect();
  if (bounds.bottom < 0 || bounds.top > window.innerHeight) return;
  const threshold = window.innerHeight * .56;
  const index = Math.max(0, steps.findLastIndex(step => step.getBoundingClientRect().top <= threshold));
  if (current !== index) {
    spaceFrames[current]?.removeAttribute("data-active");
    spaceFrames[index].setAttribute("data-active", "");
    spaceCount.textContent = `0${index + 1} / 04`;
    current = index;
  }
}

function schedule() {
  if (!renderFrame) renderFrame = requestAnimationFrame(update);
}
story.setAttribute("data-enhanced", "");
if (location.hash) schedule();
window.addEventListener("scroll", schedule, { passive: true });
window.addEventListener("resize", schedule);

const seasonObserver = new IntersectionObserver(async ([entry], observer) => {
  if (!entry.isIntersecting) return;
  observer.disconnect();
  try {
    const { initSeasonMap } = await import("./season-map");
    await initSeasonMap();
    schedule();
  } catch {
    document.querySelector("#cell-info")!.textContent = "Interactive map unavailable. The full season is shown.";
  }
}, { rootMargin: "160px 0px" });
seasonObserver.observe(document.querySelector(".season-map")!);

const countObserver = new IntersectionObserver(entries => {
  for (const entry of entries) {
    if (!entry.isIntersecting) continue;
    countObserver.unobserve(entry.target);
    if (motion.matches || !(entry.target instanceof HTMLElement)) continue;
    const element = entry.target;
    const value = Number(element.dataset.count);
    const decimals = Number(element.dataset.decimals ?? 0);
    const format = new Intl.NumberFormat("en-IN", { minimumFractionDigits: decimals, maximumFractionDigits: decimals });
    const suffix = element.dataset.suffix ?? "";
    const text = document.createElement("span");
    text.className = "sr-only";
    text.textContent = element.textContent;
    const visual = document.createElement("span");
    visual.setAttribute("aria-hidden", "true");
    element.replaceChildren(text, visual);
    const start = performance.now();
    function animate(time: number) {
      const progress = motion.matches ? 1 : Math.min(1, (time - start) / 620);
      visual.textContent = format.format(value * (1 - (1 - progress) ** 3)) + suffix;
      if (progress < 1) requestAnimationFrame(animate);
    }
    requestAnimationFrame(animate);
  }
}, { threshold: .5 });
document.querySelectorAll("[data-count]").forEach(node => countObserver.observe(node));

const visual = document.querySelector<HTMLElement>(".hero-visual")!;
const toggle = document.querySelector<HTMLButtonElement>(".globe-toggle")!;
const globeStatus = document.querySelector<HTMLElement>("#globe-status")!;
let globe: Awaited<ReturnType<typeof import("./globe")["initGlobe"]>> | undefined;
let playing = false;
let loading = false;
function setPlaying(next: boolean) {
  playing = next;
  visual.toggleAttribute("data-live", Boolean(globe) && !motion.matches);
  toggle.textContent = next ? "Pause orbit" : "Play orbit";
  globe?.setPlaying(next);
}
toggle.hidden = motion.matches;
motion.addEventListener("change", () => {
  toggle.hidden = motion.matches;
  if (motion.matches) setPlaying(false);
});
toggle.addEventListener("click", async () => {
  if (globe) {
    setPlaying(!playing);
    return;
  }
  if (loading) return;
  loading = true;
  toggle.setAttribute("aria-disabled", "true");
  toggle.textContent = "Loading…";
  try {
    const context = document.createElement("canvas").getContext("webgl2");
    if (!context) throw new Error("WebGL 2 unavailable");
    context.getExtension("WEBGL_lose_context")?.loseContext();
    const { initGlobe } = await import("./globe");
    globe = await initGlobe(visual);
    if (!motion.matches) setPlaying(true);
  } catch {
    toggle.hidden = true;
    globeStatus.textContent = "Orbit animation is unavailable. The Earth-at-night image is shown.";
    return;
  } finally {
    loading = false;
    toggle.removeAttribute("aria-disabled");
  }
});

const journeyObserver = new IntersectionObserver(entries => {
  for (const entry of entries) entry.target.toggleAttribute("data-active", entry.isIntersecting);
}, { rootMargin: "-22% 0px -25% 0px" });
document.querySelectorAll("[data-journey-step]").forEach(step => journeyObserver.observe(step));

const timestamp = document.querySelector<HTMLTimeElement>("[data-snapshot]");
const stale = document.querySelector<HTMLElement>("[data-stale]");
const ticker = document.querySelector<HTMLElement>("[data-ticker-age]");
if (timestamp && stale && ticker) {
  const updateAge = () => {
    const hours = Math.max(0, Math.floor((Date.now() - Date.parse(timestamp.dateTime)) / 3_600_000));
    const age = hours < 1 ? "under an hour ago" : hours < 48 ? `${hours} h ago` : `${Math.floor(hours / 24)} d ago`;
    const isStale = hours >= 7 || stale.hasAttribute("data-refresh-failed");
    ticker.textContent = `${isStale ? "Last available snapshot" : "Updated"} · ${age}`;
    stale.hidden = !isStale;
  };
  updateAge();
  window.setInterval(updateAge, 60_000);
}
