document.querySelectorAll<HTMLElement>(".tour-figure").forEach(figure => {
  const video = figure.querySelector("video")!;
  const button = figure.querySelector("button")!;
  const label = button.querySelector<HTMLElement>("[data-clip-label]")!;
  const icon = button.querySelector<HTMLElement>("[data-clip-icon]")!;
  const status = figure.querySelector<HTMLElement>("[role=status]")!;
  let requested = false;
  const playLabel = button.getAttribute("aria-label")!;

  function pause() {
    requested = false;
    video.pause();
    label.textContent = "Play clip";
    icon.textContent = "▶";
    button.setAttribute("aria-label", playLabel);
  }

  button.hidden = false;
  button.addEventListener("click", async () => {
    if (requested) {
      pause();
      return;
    }
    requested = true;
    video.src ||= video.dataset.src!;
    try {
      await video.play();
      if (!requested) return;
      video.hidden = false;
      label.textContent = "Pause clip";
      icon.textContent = "Ⅱ";
      button.setAttribute("aria-label", playLabel.replace("Play", "Pause"));
    } catch {
      if (!requested) return;
      pause();
      video.hidden = true;
      status.textContent = "The clip could not be played. The screenshot is available.";
    }
  });
  const visibility = new IntersectionObserver(([entry]) => {
    if (!entry.isIntersecting && requested) pause();
  });
  visibility.observe(figure);
});
