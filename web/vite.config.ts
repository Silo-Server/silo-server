import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "path";

/// <reference types="vitest" />

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const apiProxyTarget = env.VITE_API_PROXY_TARGET || "http://localhost:8090";
  const hmrClientPort = Number(env.VITE_HMR_CLIENT_PORT || "");
  // Remote backends (e.g. the hosted dev server) sit behind vhost-routing
  // proxies that reject a localhost Host header; local backends don't care
  // either way but keeping Host intact preserves existing behavior.
  const apiProxyIsLocal = /^https?:\/\/(localhost|127\.0\.0\.1|\[?::1\]?)(:|\/|$)/.test(
    apiProxyTarget,
  );

  return {
    plugins: [react(), tailwindcss()],
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
      allowedHosts: ["silo.local"],
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
