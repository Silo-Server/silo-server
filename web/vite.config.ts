import { readFileSync } from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import { promisify } from "node:util";
import { brotliCompress, constants, gzip } from "node:zlib";
import { defineConfig, loadEnv, transformWithEsbuild, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "path";
import os from "os";

/// <reference types="vitest" />

const PRECOMPRESS_MIN_BYTES = 1024;

const brotliCompressAsync = promisify(brotliCompress);
const gzipAsync = promisify(gzip);

// Exported for vite.config.test.ts.
export function precompressStaticAssets(): Plugin {
  return {
    name: "precompress-static-assets",
    apply: "build",
    async writeBundle(options, bundle) {
      const dir = options.dir;
      if (!dir) return;

      // The async zlib calls run on the libuv thread pool, so files compress
      // in parallel. Brotli-11 on the two 2 MB JASSUB WASM files alone takes
      // longer than every JS and CSS file together, so compressing one file
      // at a time would roughly double this step.
      await Promise.all(
        Object.values(bundle).map(async (output) => {
          // WASM compresses well (the JASSUB subtitle renderer shrinks from
          // 2.1 MB to 0.7 MB with brotli). WOFF/WOFF2 fonts are already
          // compressed, so sidecars would only add weight to the binary.
          if (!/\.(?:css|js|wasm)$/.test(output.fileName)) return;

          // Read the written file rather than the generateBundle value: later
          // Rollup hooks can still finalize chunk bytes before they reach disk.
          const filePath = path.resolve(dir, output.fileName);
          const bytes = await readFile(filePath);
          if (bytes.byteLength < PRECOMPRESS_MIN_BYTES) return;

          const [br, gz] = await Promise.all([
            brotliCompressAsync(bytes, {
              params: { [constants.BROTLI_PARAM_QUALITY]: 11 },
            }),
            gzipAsync(bytes, { level: 9 }),
          ]);
          await Promise.all([writeFile(`${filePath}.br`, br), writeFile(`${filePath}.gz`, gz)]);
        }),
      );
    },
  };
}

const THEME_BOOT_SOURCE = "src/themeBoot.js";

/**
 * Loads src/themeBoot.js as a classic, render-blocking script at the top of
 * <head>, so <html> carries the cached theme before first paint.
 *
 * It cannot be an ordinary entry: Vite bundles only module scripts, and those
 * run after the document is parsed, by which time the shell may already have
 * painted. It cannot be inline either, because the server's CSP
 * (internal/server/frontend.go) allows scripts from 'self' only. So the build
 * emits it as a content-hashed /assets/ file, cached immutably like every
 * other asset, and the dev server serves the source file.
 */
function themeBootScript(): Plugin {
  let base = "/";
  let isBuild = false;
  let builtFileName: string | undefined;
  return {
    name: "theme-boot-script",
    configResolved(config) {
      base = config.base;
      isBuild = config.command === "build";
    },
    generateBundle: {
      // Must run before vite:build-html's generateBundle, which applies the
      // transformIndexHtml hook below, so the hashed name is known by then.
      order: "pre",
      async handler() {
        // Emitted assets skip the build's minifier, so minify it here: the
        // file sits on the render-blocking path and its source is half comment.
        const { code } = await transformWithEsbuild(
          readFileSync(path.resolve(__dirname, THEME_BOOT_SOURCE), "utf8"),
          THEME_BOOT_SOURCE,
          { minify: true },
        );
        const referenceId = this.emitFile({
          type: "asset",
          name: path.basename(THEME_BOOT_SOURCE),
          source: code,
        });
        builtFileName = this.getFileName(referenceId);
      },
    },
    transformIndexHtml: {
      order: "post",
      handler: () => {
        // The source path works only on the dev server. In a built shell it
        // falls through to the SPA fallback, which the browser refuses to run
        // as a script, so the cached theme would silently stop painting.
        if (isBuild && builtFileName === undefined) {
          throw new Error(
            "theme-boot-script: index.html was transformed before the boot script was emitted",
          );
        }
        return [
          {
            tag: "script",
            attrs: { src: base + (builtFileName ?? THEME_BOOT_SOURCE) },
            injectTo: "head-prepend",
          },
        ];
      },
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const apiProxyTarget = env.VITE_API_PROXY_TARGET || "http://localhost:8090";
  const hmrClientPort = Number(env.VITE_HMR_CLIENT_PORT || "");
  // Vite only lets bare IPv4 hosts through unlisted, so a dev server reached by
  // name — the local mDNS name, or a Tailscale MagicDNS name when someone views
  // the dev UI from another device on the tailnet — has to be allowed here.
  // VITE_ALLOWED_HOSTS adds any others (comma-separated).
  const allowedHosts = [
    "silo.local",
    ".ts.net",
    os.hostname(),
    ...(env.VITE_ALLOWED_HOSTS || "")
      .split(",")
      .map((host) => host.trim())
      .filter(Boolean),
  ];
  // Remote backends (e.g. the hosted dev server) sit behind vhost-routing
  // proxies that reject a localhost Host header; local backends don't care
  // either way but keeping Host intact preserves existing behavior.
  const apiProxyIsLocal = /^https?:\/\/(localhost|127\.0\.0\.1|\[?::1\]?)(:|\/|$)/.test(
    apiProxyTarget,
  );

  const webVersion = (
    JSON.parse(readFileSync(path.resolve(__dirname, "package.json"), "utf8")) as { version: string }
  ).version;

  return {
    plugins: [react(), tailwindcss(), themeBootScript(), precompressStaticAssets()],
    define: {
      // Reported in X-Silo-Client-Version on every v2 request (src/api/v2/request.ts).
      __SILO_WEB_VERSION__: JSON.stringify(webVersion),
    },
    build: {
      // A font inlined into the CSS downloads for everyone, defeating the
      // unicode-range subsets in src/fonts.css, so fonts always stay files.
      assetsInlineLimit: (filePath: string) => (/\.woff2?$/.test(filePath) ? false : undefined),
    },
    worker: {
      format: "es",
    },
    optimizeDeps: {
      // jassub spawns its own module worker with import.meta.url paths; the
      // dep optimizer rewrites those into .vite/deps where the worker file
      // doesn't exist, so the ASS renderer never initializes in dev.
      exclude: ["jassub"],
      // CJS deps of the excluded package still need prebundling for ESM interop.
      include: ["jassub > throughput", "jassub > rvfc-polyfill"],
    },
    resolve: {
      alias: {
        "@": path.resolve(__dirname, "./src"),
        "@pdfjs": path.resolve(__dirname, "./public/vendor/pdfjs"),
      },
    },
    server: {
      host: "0.0.0.0",
      allowedHosts,
      hmr:
        Number.isFinite(hmrClientPort) && hmrClientPort > 0
          ? { clientPort: hmrClientPort }
          : undefined,
      proxy: {
        "/api": {
          target: apiProxyTarget,
          changeOrigin: !apiProxyIsLocal,
          secure: true,
          ws: true,
        },
      },
    },
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test-setup.ts"],
    },
  };
});
