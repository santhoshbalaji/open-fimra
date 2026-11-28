import "./tour";

const root = document.documentElement;

const header = document.querySelector<HTMLElement>(".site-header")!;
let headerHeight = header.offsetHeight;
new ResizeObserver(([entry]) => {
  headerHeight = entry.borderBoxSize[0].blockSize;
  if (window.innerWidth > 540) root.style.setProperty("--header-height", `${headerHeight}px`);
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
update();
window.addEventListener("scroll", schedule, { passive: true });
window.addEventListener("resize", schedule);

const seasonStage = document.querySelector<HTMLElement>("#scale .map-stage")!;
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
seasonObserver.observe(seasonStage);

const journeySteps = [...document.querySelectorAll("[data-journey-step]")];
let journeyObserver: IntersectionObserver | undefined;

function observeJourney() {
  journeyObserver?.disconnect();
  const top = Math.round(window.innerHeight * .5);
  const bottom = window.innerHeight - top - 1;
  journeyObserver = new IntersectionObserver(entries => {
    for (const entry of entries) entry.target.toggleAttribute("data-active", entry.isIntersecting);
  }, { rootMargin: `-${top}px 0px -${bottom}px 0px` });
  journeySteps.forEach(step => journeyObserver!.observe(step));
}
observeJourney();
window.addEventListener("resize", observeJourney);
