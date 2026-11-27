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

function startScrollStory() {
  window.removeEventListener("scroll", startScrollStory);
  void import("./scroll-story");
}

if (window.scrollY || (location.hash && location.hash !== "#opening")) startScrollStory();
else window.addEventListener("scroll", startScrollStory, { passive: true });

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
