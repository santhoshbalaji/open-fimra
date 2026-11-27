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

const header = document.querySelector<HTMLElement>(".site-header")!;
new ResizeObserver(() => root.style.setProperty("--header-height", `${header.offsetHeight}px`)).observe(header);

const scale = document.querySelector<HTMLElement>("#scale")!;
const space = document.querySelector<HTMLElement>("#space")!;
const methods = document.querySelector<HTMLElement>("#methods")!;
const story = space.querySelector<HTMLElement>(".space-story")!;
const links = [...document.querySelectorAll<HTMLAnchorElement>(".chapter-nav > a")];
const spaceFrames = [...story.querySelectorAll<HTMLElement>("[data-frame]")];
const steps = [...story.querySelectorAll<HTMLElement>("[data-step]")];
const spaceCount = document.querySelector<HTMLElement>("#space-count")!;
let renderFrame = 0;
let current = -1;

function update() {
  renderFrame = 0;
  const length = root.scrollHeight - window.innerHeight;
  root.style.setProperty("--progress", String(length > 0 ? window.scrollY / length : 0));
  const edge = header.offsetHeight + 36;
  const chapter = methods.getBoundingClientRect().top <= edge ? -1 :
    space.getBoundingClientRect().top <= edge ? 1 :
    scale.getBoundingClientRect().top <= edge ? 0 : -1;

  links.forEach((link, index) => {
    if (index === chapter) link.setAttribute("aria-current", "location");
    else link.removeAttribute("aria-current");
  });

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
update();
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
