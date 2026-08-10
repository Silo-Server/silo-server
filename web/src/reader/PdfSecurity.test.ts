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
});
