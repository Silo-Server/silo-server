// Render the actual Activity component's synthetic test snapshots with the
// built stylesheet. No production API, credentials, or screenshots are used.
import { readFile, readdir } from "node:fs/promises";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { join, resolve, sep } from "node:path";

const evidence = process.env.SILO_ACTIVITY_EVIDENCE_DIR;
const browserDirectory = process.env.SILO_ACTIVITY_BROWSER_DIR;
if (!evidence || !browserDirectory) throw new Error("Evidence and browser directories are required");
const require = createRequire(import.meta.url);
const { chromium } = require(join(browserDirectory, "node_modules", "playwright"));
const dist = resolve("web/dist");
const styles = (await readdir(join(dist, "assets"))).filter((name) => name.endsWith(".css"));
const server = createServer(async (request, response) => {
  try {
    const pathname = new URL(request.url, "http://localhost").pathname;
    if (/^\/(before|after)\/(collapsed|expanded)\.html$/.test(pathname)) {
      const markup = await readFile(join(evidence, pathname), "utf8");
      response.setHeader("content-type", "text/html; charset=utf-8");
      response.end(`<!doctype html><html lang="en" data-theme="midnight-cinema"><head><meta charset="utf-8"><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Outfit:wght@300..900&display=swap">${styles.map((name) => `<link rel="stylesheet" href="/assets/${name}">`).join("")}</head><body><main style="padding:32px">${markup}</main></body></html>`);
      return;
    }
    const asset = resolve(dist, `.${pathname}`);
    if (!asset.startsWith(dist + sep)) throw new Error("Invalid asset path");
    response.setHeader("content-type", asset.endsWith(".css") ? "text/css" : "application/octet-stream");
    response.end(await readFile(asset));
  } catch {
    response.writeHead(404).end();
  }
});
await new Promise((done) => server.listen(0, "127.0.0.1", done));
let browser;
try {
  browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 });
  const origin = `http://127.0.0.1:${server.address().port}`;
  for (const version of ["before", "after"]) {
    for (const state of ["collapsed", "expanded"]) {
      const result = await page.goto(`${origin}/${version}/${state}.html`, { waitUntil: "networkidle" });
      if (!result.ok()) throw new Error("Fixture could not be served");
      await page.evaluate(() => document.fonts.ready);
      await page.evaluate(() => {
        const theme = getComputedStyle(document.documentElement);
        if (!theme.getPropertyValue("--background").trim() || !theme.getPropertyValue("--destructive").trim()) {
          throw new Error("Activity evidence must use the application's initialized theme");
        }
      });
      await page.screenshot({ path: join(evidence, version, `${state}.png`), fullPage: true });
    }
  }
} finally {
  await browser?.close();
  await new Promise((done) => server.close(done));
}
