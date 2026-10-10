// Reports the launch cost of a production build and checks the parts of it
// that a config change can break. Run it after `pnpm run build`.
//
// It prints the brotli-11 size of everything the browser must download before
// the app runs: the index.html entry chunk, its static-import closure, and
// their CSS, read from the Vite manifest that vite.config.ts moves out of dist
// to .bundle-manifest.json. Lazy chunks are excluded. The size is reported, not
// gated.
//
// It fails when the vendor chunk is missing or imports another chunk, since
// its URL then stops surviving releases, and when dist/index.html loads a
// stylesheet or classic script from another origin that holds back first
// paint.
//
// Usage: node scripts/check-bundle-budget.mjs
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { brotliCompressSync, constants } from "node:zlib";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const distDir = path.join(webRoot, "dist");
const manifestPath = path.join(webRoot, ".bundle-manifest.json");

/**
 * Returns the output files the entry loads before it can run: its own chunk,
 * every chunk reachable through static imports, and the CSS those chunks
 * carry. Dynamic imports are lazy and stay out.
 */
export function eagerFiles(manifest, entryKey = "index.html") {
  if (!manifest[entryKey]) throw new Error(`manifest has no ${entryKey} entry`);
  const js = [];
  const css = new Set();
  const seen = new Set();
  const visit = (key) => {
    if (seen.has(key)) return;
    seen.add(key);
    const chunk = manifest[key];
    if (!chunk) throw new Error(`manifest references missing chunk ${key}`);
    js.push(chunk.file);
    for (const file of chunk.css ?? []) css.add(file);
    for (const imported of chunk.imports ?? []) visit(imported);
  };
  visit(entryKey);
  return { js, css: [...css] };
}

/** Name vite.config.ts gives the chunk that holds React and the router. */
export const VENDOR_CHUNK_NAME = "vendor-react";

/**
 * The vendor chunk keeps its URL across releases only while it imports no
 * other chunk: an import pulls the imported chunk's content hash into its own.
 * Rollup puts the dependencies of the vendor packages into the vendor chunk by
 * itself, so an import only appears after a config change splits vendor code
 * across chunks, such as a second manual chunk that claims a shared module.
 */
export function vendorChunkFailures(manifest) {
  const vendor = Object.values(manifest).find((chunk) => chunk.name === VENDOR_CHUNK_NAME);
  if (!vendor) {
    return [
      `the manifest has no ${VENDOR_CHUNK_NAME} chunk. Check manualChunks in vite.config.ts.`,
    ];
  }
  if (!vendor.imports?.length) return [];
  return [
    `${vendor.file} imports ${vendor.imports.join(", ")}, so its hash changes with the app. ` +
      "Check manualChunks in vite.config.ts: another chunk now holds code the vendor chunk depends on.",
  ];
}

const tagPattern = /<(link|script)\b([^>]*)>/gi;
const attributePattern = /([^\s=/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;

function parseAttributes(source) {
  const attributes = new Map();
  for (const match of source.matchAll(attributePattern)) {
    attributes.set(match[1].toLowerCase(), match[2] ?? match[3] ?? match[4] ?? "");
  }
  return attributes;
}

function isCrossOrigin(url) {
  return /^(?:[a-z][a-z0-9+.-]*:)?\/\//i.test(url.trim());
}

/**
 * Lists the cross-origin stylesheets and classic scripts in an HTML document
 * that block rendering. Module, async, and deferred scripts do not block, and
 * neither do print-only stylesheets or preconnect and preload hints.
 */
export function crossOriginRenderBlocking(html) {
  const blocking = [];
  for (const [, tag, source] of html.matchAll(tagPattern)) {
    const attributes = parseAttributes(source);
    if (tag.toLowerCase() === "link") {
      const rel = (attributes.get("rel") ?? "").toLowerCase().split(/\s+/);
      const href = attributes.get("href") ?? "";
      const media = (attributes.get("media") ?? "").trim().toLowerCase();
      if (!rel.includes("stylesheet") || attributes.has("disabled") || media === "print") continue;
      if (isCrossOrigin(href)) blocking.push(href);
    } else {
      const src = attributes.get("src") ?? "";
      const type = (attributes.get("type") ?? "").trim().toLowerCase();
      if (type === "module" || attributes.has("async") || attributes.has("defer")) continue;
      if (isCrossOrigin(src)) blocking.push(src);
    }
  }
  return blocking;
}

/** Returns one message per cross-origin render-blocking resource. */
export function renderBlockingFailures(blocking) {
  return blocking.map(
    (url) =>
      `index.html blocks first paint on ${url}. ` +
      "Self-host the resource or load it without blocking first paint.",
  );
}

function brotliSize(bytes) {
  return brotliCompressSync(bytes, {
    params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
  }).byteLength;
}

function measure() {
  if (!existsSync(manifestPath)) {
    throw new Error(
      `${path.relative(webRoot, manifestPath)} is missing. Run \`pnpm run build\` first.`,
    );
  }
  const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  const { js, css } = eagerFiles(manifest);
  const files = [...js, ...css].map((file) => {
    const bytes = readFileSync(path.join(distDir, file));
    return { file, raw: bytes.byteLength, brotli: brotliSize(bytes) };
  });
  const html = readFileSync(path.join(distDir, "index.html"), "utf8");
  const blocking = crossOriginRenderBlocking(html);
  return {
    files,
    blocking,
    chunkCount: Object.values(manifest).filter((chunk) => chunk.file.endsWith(".js")).length,
    vendorFailures: vendorChunkFailures(manifest),
  };
}

function main(argv) {
  if (argv.length > 0) throw new Error(`unknown argument: ${argv.join(" ")}`);

  const { files, blocking, chunkCount, vendorFailures } = measure();
  for (const { file, raw, brotli } of files) {
    console.log(`  ${file}  ${raw} B raw, ${brotli} B brotli`);
  }
  const eagerBrotliBytes = files.reduce((total, file) => total + file.brotli, 0);
  console.log(
    `eager launch bundle: ${eagerBrotliBytes} B brotli in ${files.length} files; ` +
      `${chunkCount} JS chunks in the manifest`,
  );

  const failures = [...vendorFailures, ...renderBlockingFailures(blocking)];
  for (const failure of failures) console.error(`bundle check: ${failure}`);
  if (failures.length === 0) console.log("bundle check: passed");
  return failures.length === 0 ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
