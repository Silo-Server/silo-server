import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  annotationLayerConstructor: vi.fn(),
  annotationLayerRender: vi.fn(),
  getDocument: vi.fn(),
  textLayerRender: vi.fn(),
}));

vi.mock("@pdfjs/pdf.min.mjs", () => {
  class AnnotationLayer {
    constructor(options: unknown) {
      mocks.annotationLayerConstructor(options);
    }

    render(options: unknown) {
      return mocks.annotationLayerRender(options);
    }
  }

  class PDFDataRangeTransport {
    requestDataRange?: (begin: number, end: number) => void;
    onDataRange = vi.fn();

    constructor(
      readonly length: number,
      readonly initialData: unknown[],
    ) {}
  }

  class TextLayer {
    render() {
      return mocks.textLayerRender();
    }
  }

  (
    globalThis as typeof globalThis & {
      pdfjsLib?: Record<string, unknown>;
    }
  ).pdfjsLib = {
    AnnotationLayer,
    GlobalWorkerOptions: {},
    PDFDataRangeTransport,
    TextLayer,
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
    mocks.annotationLayerConstructor.mockReset();
    mocks.annotationLayerRender.mockReset().mockResolvedValue(undefined);
    mocks.getDocument.mockReset();
    mocks.textLayerRender.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
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

  it("provides PDF.js 6 annotation layers with attachment content", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ text: async () => ".textLayer{}.annotationLayer{}" })),
    );
    const attachment = new Uint8Array([1, 2, 3]);
    const getAttachmentContent = vi.fn(async () => attachment);
    const page = {
      cleanup: vi.fn(),
      getAnnotations: vi.fn(async () => []),
      getViewport: vi.fn(() => ({ height: 792, width: 612 })),
      render: vi.fn(() => ({ cancel: vi.fn(), promise: Promise.resolve() })),
      streamTextContent: vi.fn(async () => ({})),
    };
    mocks.getDocument.mockReturnValue({
      destroy: vi.fn(),
      promise: Promise.resolve({
        getAttachmentContent,
        getMetadata: vi.fn(async () => ({ info: {}, metadata: null })),
        getOutline: vi.fn(async () => null),
        getPage: vi.fn(async () => page),
        numPages: 1,
      }),
    });

    const book = await makePDF(new File(["%PDF-1.7"], "safe.pdf", { type: "application/pdf" }));
    const section = await book.sections[0].load();
    const iframe = document.createElement("iframe");
    document.body.append(iframe);
    const doc = iframe.contentDocument;
    if (!doc) throw new Error("missing iframe document");
    doc.body.innerHTML =
      '<div id="canvas"></div><div class="textLayer"></div><div class="annotationLayer"></div>';

    await section.onZoom({ doc, scale: 1 });

    const annotationOptions = mocks.annotationLayerConstructor.mock.calls[0]?.[0];
    expect(annotationOptions).toBeDefined();
    if (!annotationOptions) throw new Error("PDF.js did not construct an annotation layer");
    const { linkService } = annotationOptions as {
      linkService: { getAttachmentContent: (id: string) => Promise<Uint8Array | null> };
    };
    await expect(linkService.getAttachmentContent("attachment-1")).resolves.toBe(attachment);
    expect(getAttachmentContent).toHaveBeenCalledWith("attachment-1");

    iframe.remove();
    await book.destroy();
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
