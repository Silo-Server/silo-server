// @vitest-environment node

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { describe, expect, it } from "vitest";

describe("vendored PDF.js bundle compatibility", () => {
  it("loads when the browser does not provide the global Iterator helper", async () => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, "Iterator");
    Object.defineProperty(globalThis, "Iterator", {
      configurable: true,
      value: undefined,
      writable: true,
    });

    try {
      const bundleURL = pathToFileURL(resolve(process.cwd(), "public/vendor/pdfjs/pdf.min.mjs"));
      bundleURL.searchParams.set("iterator-compat", String(Date.now()));

      const pdfjs = await import(bundleURL.href);

      expect(pdfjs.getDocument).toBeTypeOf("function");
      // The reader reads the global the bundle installs, not the module export.
      expect(
        (globalThis as { pdfjsLib?: { getDocument?: unknown } }).pdfjsLib?.getDocument,
      ).toBeTypeOf("function");
    } finally {
      if (descriptor) Object.defineProperty(globalThis, "Iterator", descriptor);
      else delete (globalThis as Record<string, unknown>).Iterator;
    }
  });

  it("matches the pdfjs-dist version the app depends on, worker included", () => {
    // The bundle and worker are copied in by hand. A dependency bump that skips
    // the copy leaves the reader on the old release, and a worker from another
    // release refuses to start.
    const read = (path: string) => readFileSync(resolve(process.cwd(), path), "utf8");
    const { version } = JSON.parse(read("node_modules/pdfjs-dist/package.json")) as {
      version: string;
    };
    expect(read("public/vendor/pdfjs/pdf.min.mjs")).toContain(`"${version}"`);
    expect(read("public/vendor/pdfjs/pdf.worker.min.mjs")).toContain(`"${version}"`);
  });
});
