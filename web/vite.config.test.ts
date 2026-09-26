// @vitest-environment node

import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { brotliDecompressSync, gunzipSync } from "node:zlib";
import type { Rollup } from "vite";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { precompressStaticAssets } from "./vite.config";

/**
 * The Go frontend handler (internal/server/frontend.go) serves an /assets/
 * file's .br or .gz sidecar when the client accepts it, so any asset type this
 * plugin skips ships uncompressed.
 */
describe("precompressStaticAssets", () => {
  let dir: string;

  beforeEach(() => {
    dir = mkdtempSync(path.join(tmpdir(), "silo-precompress-"));
    mkdirSync(path.join(dir, "assets"));
  });

  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  async function writeBundle(files: Record<string, Buffer>) {
    for (const [fileName, bytes] of Object.entries(files)) {
      writeFileSync(path.join(dir, fileName), bytes);
    }
    const hook = precompressStaticAssets().writeBundle;
    if (typeof hook !== "function") throw new Error("writeBundle is not a plain hook");
    const bundle = Object.fromEntries(
      Object.keys(files).map((fileName) => [fileName, { fileName }]),
    );
    await hook.call(
      {} as Rollup.PluginContext,
      { dir } as Rollup.NormalizedOutputOptions,
      bundle as unknown as Rollup.OutputBundle,
    );
  }

  const compressible = Buffer.from("(module (func $render))\n".repeat(200));

  it("writes brotli and gzip sidecars for js, css, and wasm", async () => {
    const names = ["assets/app.js", "assets/app.css", "assets/jassub-worker-modern.wasm"];
    await writeBundle(Object.fromEntries(names.map((name) => [name, compressible])));

    for (const name of names) {
      const file = path.join(dir, name);
      expect(brotliDecompressSync(readFileSync(`${file}.br`)), `${name}.br`).toEqual(compressible);
      expect(gunzipSync(readFileSync(`${file}.gz`)), `${name}.gz`).toEqual(compressible);
    }
  });

  it("skips already-compressed fonts and files under the size floor", async () => {
    const files = {
      "assets/font.woff": compressible,
      "assets/font.woff2": compressible,
      "assets/tiny.wasm": compressible.subarray(0, 512),
    };
    await writeBundle(files);

    for (const name of Object.keys(files)) {
      expect(existsSync(path.join(dir, `${name}.br`)), `${name}.br`).toBe(false);
      expect(existsSync(path.join(dir, `${name}.gz`)), `${name}.gz`).toBe(false);
    }
  });
});
