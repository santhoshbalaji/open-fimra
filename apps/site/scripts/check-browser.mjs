import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";

import AxeBuilder from "@axe-core/playwright";
import { chromium } from "playwright";
import { serveDist } from "./serve-dist.mjs";

const { server, url } = await serveDist({ base: process.env.SITE_BASE ?? "/open-fimra/" });
const browser = await chromium.launch({ args: ["--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
const captures = process.env.SITE_CAPTURES;
const chapters = ["opening", "scale", "space", "live", "journey", "roles", "handoffs",
  "product", "deploy", "open-source", "involved", "footer"];
const sizes = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
if (captures) await mkdir(captures, { recursive: true });

async function axe(page, label) {
  const { violations } = await new AxeBuilder({ page }).analyze();
  assert.deepEqual(violations.map(({ id, nodes }) => ({ id, nodes: nodes.map(n => n.target) })), [], label);
  console.log(`${label}: zero axe violations`);
}

async function load(page, destination = url) {
  const response = await page.goto(destination);
  assert.equal(response?.status(), 200);
  await page.evaluate(() => document.fonts.ready);
}

async function selectMarch(page) {
  // The stage is lazy-loaded when it intersects the viewport. Visiting the
  // chapter heading alone can leave the stage and its hidden controls below it.
  await page.locator("#scale .map-stage").scrollIntoViewIfNeeded();
  await page.getByRole("combobox", { name: "Detection month" }).selectOption("4");
  assert.equal(await page.locator("#map-period").innerText(), "March 2026");
  assert.equal(await page.locator("#month-count").innerText(), "71,296 detections");
}

async function transfers(page) {
  return page.evaluate(() => {
    const resources = [
      ...performance.getEntriesByType("navigation"),
      ...performance.getEntriesByType("resource"),
    ];
    return resources.filter(entry => !/\.(webm|mp4)$/.test(entry.name))
      .reduce((sum, entry) => sum + entry.transferSize, 0);
  });
}

function collectProblems(page) {
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("response", response => {
    if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`);
  });
  page.on("request", request => {
    if (!request.url().startsWith(url) && !request.url().startsWith("blob:")) errors.push(request.url());
  });
  return errors;
}

try {
  for (const [size, viewport] of Object.entries(sizes)) {
    for (const colorScheme of ["light", "dark"]) {
      const context = await browser.newContext({ viewport, colorScheme, reducedMotion: "reduce" });
      const page = await context.newPage();
      const issues = collectProblems(page);
      await load(page);
      await page.waitForLoadState("networkidle");
      const initial = await transfers(page);
      assert.ok(initial <= 300_000, `First view uses ${initial} bytes`);

      await page.keyboard.press("Tab");
      assert.equal(await page.locator(":focus").innerText(), "Skip to content");
      await page.keyboard.press("Enter");
      assert.equal(await page.locator(":focus").getAttribute("id"), "main");
      await page.locator(".upcoming summary").focus();
      await page.keyboard.press("Enter");
      await page.keyboard.press("Tab");
      assert.equal(await page.locator(":focus").innerText(), "Live this week");
      await page.keyboard.press("Enter");
      assert.equal(new URL(page.url()).hash, "#live");
      assert.equal(await page.locator(":focus").getAttribute("id"), "live");
      for (const chapter of chapters) {
        const target = page.locator(chapter === "footer" ? "footer" : `#${chapter}`);
        if (!await target.count()) continue;
        if (chapter === "opening") await page.evaluate(() => window.scrollTo(0, 0));
        else await target.evaluate(element => element.scrollIntoView({ block: "start" }));
        await page.waitForLoadState("networkidle");
        if (captures) {
          if (chapter === "scale") {
            await page.waitForFunction(() => document.querySelector("#hex-map polygon") ||
              document.querySelector("#scale .hex-poster").naturalWidth > 0);
          }
          await page.screenshot({ path: join(captures, `${size}-${colorScheme}-${chapter}.png`) });
        }
      }
      await selectMarch(page);
      assert.equal(await page.locator(".globe-toggle").isVisible(), false);
      assert.equal(await page.locator(".static-frame:visible").count(), 4);
      await axe(page, `${size} ${colorScheme}`);
      const complete = await transfers(page);
      assert.ok(complete <= 2_000_000, `Scroll uses ${complete} bytes`);
      assert.deepEqual(issues, []);
      console.log(`${size} ${colorScheme}: ${initial} bytes first view, ${complete} bytes after scrolling`);
      if (captures) {
        for (const [name, selector] of [
          ["season-map", ".scale .season-map"], ["weekly-map", ".live-map"],
          ["journey-dispatch", ".timing-note"], ["tour-phone", ".tour-phone .device-mat"],
          ["tour-dashboard", ".tour:last-child .device-mat"],
        ]) {
          await page.locator(selector).evaluate(element => element.scrollIntoView({ block: "center" }));
          await page.waitForLoadState("networkidle");
          await page.screenshot({ path: join(captures, `${size}-${colorScheme}-${name}.png`) });
        }
      }
      await page.locator("#hex-map").focus();
      await page.keyboard.press("ArrowRight");
      assert.match(await page.locator("#cell-status").innerText(), /cell centre/);
      await load(page, url + "methods/");
      await axe(page, `methods ${size} ${colorScheme}`);
      assert.deepEqual(issues, []);
      if (captures) await page.screenshot({ path: join(captures, `${size}-${colorScheme}-methods.png`) });
      await context.close();
    }
  }

  const playback = await browser.newPage({ viewport: sizes.phone });
  await load(playback);
  await playback.getByRole("button", { name: "Play orbit" }).click();
  await playback.locator(".hero-visual[data-live]").waitFor();
  await playback.getByRole("button", { name: "Pause orbit" }).click();
  await playback.locator("#product").scrollIntoViewIfNeeded();
  for (const title of ["monitor and route", "acknowledge in the field", "analyse and export"]) {
    const clip = playback.getByRole("button", { name: `Play clip (${title})` });
    await clip.click();
    await playback.getByRole("button", { name: `Pause clip (${title})` }).click();
    const video = playback.locator(".tour-figure").filter({ has: clip }).locator("video");
    assert.equal(await video.evaluate(element => element.paused), true);
  }
  await playback.close();

  const zoom = await browser.newPage({ viewport: { width: 320, height: 700 }, reducedMotion: "reduce" });
  for (const path of ["", "methods/"]) {
    await load(zoom, url + path);
    await zoom.addStyleTag({ content: "html { font-size: 200% !important; }" });
    const width = await zoom.evaluate(() => document.documentElement.scrollWidth);
    assert.ok(width <= 320, `${path || "landing"} reflows to ${width}px`);
  }
  await zoom.close();

  const staticPage = await browser.newPage({ javaScriptEnabled: false, viewport: sizes.phone });
  await load(staticPage);
  assert.equal(await staticPage.locator(".hex-poster").isVisible(), true);
  assert.equal(await staticPage.locator("#month").isVisible(), false);
  assert.equal(await staticPage.locator(".static-frame:visible").count(), 4);
  assert.equal(await staticPage.locator(".journey-list li:visible").count(), 7);
  assert.equal(await staticPage.locator(".globe-toggle").isVisible(), false);
  await staticPage.close();

  const missingArchive = await browser.newPage({ viewport: sizes.phone });
  await missingArchive.route("**/data/hexagons.json", route => route.abort());
  await load(missingArchive, url + "#scale");
  await missingArchive.locator("#scale .season-map").scrollIntoViewIfNeeded();
  await missingArchive.getByText("Interactive map unavailable. The full season is shown.").waitFor();
  await missingArchive.locator(".hex-poster").waitFor();
  assert.equal(await missingArchive.locator("#month").isVisible(), false);
  await missingArchive.close();

  const noGl = await browser.newPage({ viewport: sizes.desktop });
  await noGl.addInitScript(() => {
    const getContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (type, ...args) {
      return type.startsWith("webgl") ? null : getContext.call(this, type, ...args);
    };
  });
  await load(noGl);
  await noGl.getByRole("button", { name: "Play orbit" }).click();
  await noGl.waitForFunction(() => document.getElementById("globe-status").textContent.includes("unavailable"));
  assert.equal(await noGl.locator(".hero-visual[data-live]").count(), 0);
  await selectMarch(noGl);
  await noGl.close();
  console.log("Keyboard, 320px at 200% text, reduced motion, no-JS and WebGL fallback checks passed");
} finally {
  await browser.close();
  server.close();
}
