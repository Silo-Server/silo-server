// Checks the launch cost of a production build against perf-budget.json.
// Run it after `pnpm run build`:
//
//   eagerBrotliBytes           brotli-11 bytes of everything the browser must
//                              download before the app runs: the index.html
//                              entry chunk, its static-import closure, and
//                              their CSS, read from the Vite manifest that
//                              vite.config.ts moves out of dist to
//                              .bundle-manifest.json. Lazy chunks are
//                              excluded.
//   crossOriginRenderBlocking  stylesheets and classic scripts in
//                              dist/index.html that load from another origin
//                              and hold back first paint.
//
// The check fails when a value grows past its budget, and also when it falls
// below it, so the change that shrinks the launch bundle lowers the budget in
// the same PR. Byte counts get BYTE_TOLERANCE of slack either way. It also
// fails when the vendor chunk is missing or imports another chunk, since its
// URL then stops surviving releases.
//
// Usage: node scripts/check-bundle-budget.mjs [--update]
//   --update rewrites perf-budget.json from the current build.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { brotliCompressSync, constants } from "node:zlib";

export const BYTE_TOLERANCE = 1024;

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const distDir = path.join(webRoot, "dist");
const manifestPath = path.join(webRoot, ".bundle-manifest.json");
const budgetPath = path.join(webRoot, "perf-budget.json");

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

/** Compares measured values with the budget and returns one message per breach. */
export function budgetFailures(actual, budget) {
  const failures = [];
  const bytes = actual.eagerBrotliBytes;
  const byteBudget = budget.eagerBrotliBytes;
  if (bytes > byteBudget + BYTE_TOLERANCE) {
    failures.push(
      `eager launch bundle is ${bytes} B brotli, ${bytes - byteBudget} B over the ${byteBudget} B budget. ` +
        "Move the new code behind a lazy import, or raise the budget with `pnpm run budget:update` and say why in the PR.",
    );
  } else if (bytes < byteBudget - BYTE_TOLERANCE) {
    failures.push(
      `eager launch bundle is ${bytes} B brotli, ${byteBudget - bytes} B under the ${byteBudget} B budget. ` +
        "Lock the win in: run `pnpm run budget:update` and commit perf-budget.json.",
    );
  }
  const count = actual.crossOriginRenderBlocking;
  const countBudget = budget.crossOriginRenderBlocking;
  if (count > countBudget) {
    failures.push(
      `index.html has ${count} cross-origin render-blocking resources, budget ${countBudget}. ` +
        "Self-host the resource or load it without blocking first paint.",
    );
  } else if (count < countBudget) {
    failures.push(
      `index.html has ${count} cross-origin render-blocking resources, budget ${countBudget}. ` +
        "Lower the budget: run `pnpm run budget:update` and commit perf-budget.json.",
    );
  }
  return failures;
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
    values: {
      eagerBrotliBytes: files.reduce((total, file) => total + file.brotli, 0),
      crossOriginRenderBlocking: blocking.length,
    },
  };
}

function main(argv) {
  const update = argv.includes("--update");
  const unknown = argv.filter((arg) => arg !== "--update");
  if (unknown.length > 0) throw new Error(`unknown argument: ${unknown.join(" ")}`);

  const { files, blocking, chunkCount, vendorFailures, values } = measure();
  for (const { file, raw, brotli } of files) {
    console.log(`  ${file}  ${raw} B raw, ${brotli} B brotli`);
  }
  for (const url of blocking) console.log(`  render-blocking: ${url}`);
  console.log(
    `eager launch bundle: ${values.eagerBrotliBytes} B brotli in ${files.length} files; ` +
      `${chunkCount} JS chunks in the manifest; ` +
      `${values.crossOriginRenderBlocking} cross-origin render-blocking resources`,
  );

  const failures = [...vendorFailures];
  if (update) {
    writeFileSync(budgetPath, `${JSON.stringify(values, null, 2)}\n`);
    console.log(`wrote ${path.relative(webRoot, budgetPath)}`);
  } else {
    failures.push(...budgetFailures(values, JSON.parse(readFileSync(budgetPath, "utf8"))));
  }
  for (const failure of failures) console.error(`bundle budget: ${failure}`);
  if (failures.length === 0 && !update) console.log("bundle budget: within perf-budget.json");
  return failures.length === 0 ? 0 : 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = main(process.argv.slice(2));
}
