import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getDocument: vi.fn(),
}));

vi.mock("@pdfjs/pdf.min.mjs", () => {
  class PDFDataRangeTransport {
    requestDataRange?: (begin: number, end: number) => void;
    onDataRange = vi.fn();

    constructor(
      readonly length: number,
      readonly initialData: unknown[],
    ) {}
  }

  (
    globalThis as typeof globalThis & {
      pdfjsLib?: Record<string, unknown>;
    }
  ).pdfjsLib = {
    GlobalWorkerOptions: {},
    PDFDataRangeTransport,
    getDocument: mocks.getDocument,
  };

  return {};
});

// Import the vendored source directly so Vitest applies the app's @pdfjs alias
// instead of externalizing the local package through Node's module loader.
// @ts-expect-error The vendored JavaScript module does not ship TypeScript declarations.
import { makePDF } from "../../vendor/foliate-js/pdf.js";

describe("PDF reader security", () => {
  beforeEach(() => {
    mocks.getDocument.mockReset();
  });

  it("disables embedded PDF scripting at the PDF.js boundary", async () => {
    const firstPage = {
      getViewport: vi.fn(() => ({ height: 792, width: 612 })),
    };
    mocks.getDocument.mockReturnValue({
      promise: Promise.resolve({
        destroy: vi.fn(),
        getMetadata: vi.fn(async () => ({ info: {}, metadata: null })),
        getOutline: vi.fn(async () => null),
        getPage: vi.fn(async () => firstPage),
        numPages: 1,
      }),
    });

    await makePDF(new File(["%PDF-1.7"], "safe.pdf", { type: "application/pdf" }));

    expect(mocks.getDocument).toHaveBeenCalledWith(
      expect.objectContaining({
        enableScripting: false,
        isEvalSupported: false,
      }),
    );
  });

  it("destroys the PDF.js loading task when the book closes", async () => {
    const destroy = vi.fn();
    const firstPage = {
      getViewport: vi.fn(() => ({ height: 792, width: 612 })),
    };
    mocks.getDocument.mockReturnValue({
      destroy,
      promise: Promise.resolve({
        getMetadata: vi.fn(async () => ({ info: {}, metadata: null })),
        getOutline: vi.fn(async () => null),
        getPage: vi.fn(async () => firstPage),
        numPages: 1,
      }),
    });

    const book = await makePDF(new File(["%PDF-1.7"], "safe.pdf", { type: "application/pdf" }));
    await book.destroy();

    expect(destroy).toHaveBeenCalledOnce();
  });

  it("ships only the PDF.js layers used by the embedded reader", () => {
    const pdfLayerCSS = readFileSync(
      resolve(process.cwd(), "public/vendor/pdfjs/pdf_layers.css"),
      "utf8",
    );

    expect(pdfLayerCSS).toContain(".textLayer{");
    expect(pdfLayerCSS).toContain(".textLayerImages{");
    expect(pdfLayerCSS).toContain(".annotationLayer{");
    expect(pdfLayerCSS).not.toMatch(
      /\.(?:annotationEditorLayer|dialog|pdfViewer|sidebar|toolbar|xfaLayer)\b/,
    );
  });
});
