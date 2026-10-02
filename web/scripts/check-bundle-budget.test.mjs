import { describe, expect, it } from "vitest";

import {
  BYTE_TOLERANCE,
  budgetFailures,
  crossOriginRenderBlocking,
  eagerFiles,
  vendorChunkFailures,
} from "./check-bundle-budget.mjs";

describe("eagerFiles", () => {
  it("follows static imports and their CSS but not dynamic imports", () => {
    const manifest = {
      "index.html": {
        file: "assets/index-a.js",
        isEntry: true,
        imports: ["_vendor-react-b.js", "_shared-c.js"],
        dynamicImports: ["src/pages/Catalog.tsx"],
        css: ["assets/index-a.css"],
      },
      "_vendor-react-b.js": { file: "assets/vendor-react-b.js" },
      "_shared-c.js": {
        file: "assets/shared-c.js",
        imports: ["_vendor-react-b.js"],
        css: ["assets/shared-c.css", "assets/index-a.css"],
      },
      "src/pages/Catalog.tsx": {
        file: "assets/Catalog-d.js",
        isDynamicEntry: true,
        imports: ["_shared-c.js"],
        css: ["assets/Catalog-d.css"],
      },
    };

    expect(eagerFiles(manifest)).toEqual({
      js: ["assets/index-a.js", "assets/vendor-react-b.js", "assets/shared-c.js"],
      css: ["assets/index-a.css", "assets/shared-c.css"],
    });
  });

  it("rejects a manifest without the entry", () => {
    expect(() => eagerFiles({})).toThrow("manifest has no index.html entry");
  });
});

describe("vendorChunkFailures", () => {
  it("accepts a vendor chunk that imports nothing", () => {
    const manifest = {
      "index.html": { file: "assets/index-a.js", imports: ["_vendor-react-b.js"] },
      "_vendor-react-b.js": { file: "assets/vendor-react-b.js", name: "vendor-react" },
    };

    expect(vendorChunkFailures(manifest)).toEqual([]);
  });

  it("fails when the vendor chunk imports an app chunk", () => {
    const manifest = {
      "index.html": { file: "assets/index-a.js", imports: ["_vendor-react-b.js"] },
      "_vendor-react-b.js": {
        file: "assets/vendor-react-b.js",
        name: "vendor-react",
        imports: ["index.html"],
      },
    };

    const failures = vendorChunkFailures(manifest);
    expect(failures).toHaveLength(1);
    expect(failures[0]).toContain("assets/vendor-react-b.js imports index.html");
  });

  it("fails when the vendor chunk is missing", () => {
    expect(vendorChunkFailures({ "index.html": { file: "assets/index-a.js" } })).toEqual([
      "the manifest has no vendor-react chunk. Check manualChunks in vite.config.ts.",
    ]);
  });
});

describe("crossOriginRenderBlocking", () => {
  it("counts cross-origin stylesheets and classic scripts only", () => {
    const html = `
      <link rel="preconnect" href="https://fonts.googleapis.com" />
      <link
        href="https://fonts.googleapis.com/css2?family=Outfit:wght@300..900&display=swap"
        rel="stylesheet"
      />
      <link rel="stylesheet" media="print" href="https://cdn.example.com/print.css" />
      <link rel="stylesheet" crossorigin href="/assets/index-a.css">
      <link rel="modulepreload" crossorigin href="/assets/vendor-react-b.js">
      <script src="//cdn.example.com/blocking.js"></script>
      <script async src="https://cdn.example.com/async.js"></script>
      <script type="module" src="https://cdn.example.com/module.js"></script>
      <script type="module" crossorigin src="/assets/index-a.js"></script>
    `;

    expect(crossOriginRenderBlocking(html)).toEqual([
      "https://fonts.googleapis.com/css2?family=Outfit:wght@300..900&display=swap",
      "//cdn.example.com/blocking.js",
    ]);
  });
});

describe("budgetFailures", () => {
  const budget = { eagerBrotliBytes: 300_000, crossOriginRenderBlocking: 1 };

  it("passes within the byte tolerance", () => {
    expect(
      budgetFailures(
        { eagerBrotliBytes: 300_000 + BYTE_TOLERANCE, crossOriginRenderBlocking: 1 },
        budget,
      ),
    ).toEqual([]);
    expect(
      budgetFailures(
        { eagerBrotliBytes: 300_000 - BYTE_TOLERANCE, crossOriginRenderBlocking: 1 },
        budget,
      ),
    ).toEqual([]);
  });

  it("fails growth past the budget", () => {
    const failures = budgetFailures(
      { eagerBrotliBytes: 300_000 + BYTE_TOLERANCE + 1, crossOriginRenderBlocking: 2 },
      budget,
    );
    expect(failures).toHaveLength(2);
    expect(failures[0]).toContain("over the 300000 B budget");
    expect(failures[1]).toContain("2 cross-origin render-blocking resources, budget 1");
  });

  it("asks for a lower budget once the bundle shrinks", () => {
    const failures = budgetFailures(
      { eagerBrotliBytes: 300_000 - BYTE_TOLERANCE - 1, crossOriginRenderBlocking: 0 },
      budget,
    );
    expect(failures).toHaveLength(2);
    expect(failures[0]).toContain("under the 300000 B budget");
    expect(failures[1]).toContain("Lower the budget");
  });
});
