// @vitest-environment node

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

      expect(pdfjs.version).toBe("6.2.108");
    } finally {
      if (descriptor) Object.defineProperty(globalThis, "Iterator", descriptor);
      else delete (globalThis as Record<string, unknown>).Iterator;
    }
  });
});
