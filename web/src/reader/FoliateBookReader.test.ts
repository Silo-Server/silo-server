import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";

import type { FileVersion } from "@/api/types";
import { DocumentLoader } from "@/reader/readest/libs/document";
import {
  cacheEbookReaderProgress,
  ebookProgressPath,
  ebookReaderProgressQueryKey,
  ebookReadPath,
  formatReaderProgress,
  isReaderSupportedFile,
  normalizeReaderSettings,
  readerRendererAttributes,
  readerStyles,
  restoreProgressTarget,
  progressFromRelocate,
  readerFileFormat,
  readerMimeType,
} from "./FoliateBookReader";

function version(overrides: Partial<FileVersion>): FileVersion {
  return {
    file_id: overrides.file_id ?? 7,
    file_name: overrides.file_name,
    file_path: overrides.file_path,
    resolution: "",
    codec_video: "",
    codec_audio: "",
    hdr: false,
    container: overrides.container ?? "",
    file_size: 1,
    duration: 0,
    bitrate: 0,
  };
}

describe("FoliateBookReader helpers", () => {
  it("builds the protected ebook read endpoint", () => {
    expect(ebookReadPath("ebook 1", 42)).toBe("/ebooks/ebook%201/files/42/read");
  });

  it("builds the protected ebook progress endpoint", () => {
    expect(ebookProgressPath("ebook 1")).toBe("/ebooks/ebook%201/progress");
  });

  it("caches saved reader progress under the shared detail query key", () => {
    const client = new QueryClient();
    const saved = {
      file_id: 42,
      location: "epubcfi(/6/4)",
      progress: 0.42,
      content_id: "ebook 1",
    };

    cacheEbookReaderProgress(client, "ebook 1", saved);

    expect(client.getQueryData(ebookReaderProgressQueryKey("ebook 1"))).toEqual(saved);
  });

  it("detects reader file formats from container or filename", () => {
    expect(readerFileFormat(version({ container: ".AZW3", file_name: "ignored.epub" }))).toBe(
      "azw3",
    );
    expect(readerFileFormat(version({ file_name: "book.fb2.zip" }))).toBe("fbz");
    expect(readerFileFormat(version({ container: "zip", file_name: "book.fb2.zip" }))).toBe("fbz");
    expect(readerFileFormat(version({ container: "zip", file_name: "comic.cbz" }))).toBe("cbz");
    expect(readerFileFormat(version({ container: "rar", file_name: "comic.cbr" }))).toBe("cbr");
    expect(readerFileFormat(version({ file_path: "/library/book.cbz" }))).toBe("cbz");
  });

  it("does not support plain text files in the reader", () => {
    expect(isReaderSupportedFile(version({ file_name: "notes.txt" }))).toBe(false);
  });

  it("rejects plain text when the document loader is called directly", async () => {
    const file = new File(["notes"], "notes.txt", { type: "text/plain" });

    await expect(new DocumentLoader(file).open()).rejects.toThrow("Unsupported book format");
  });

  it("maps readest formats to MIME types", () => {
    expect(readerMimeType("epub")).toBe("application/epub+zip");
    expect(readerMimeType("azw3")).toBe("application/vnd.amazon.mobi8-ebook");
    expect(readerMimeType("fbz")).toBe("application/x-zip-compressed-fb2");
    expect(readerMimeType("md")).toBe("text/markdown");
  });

  it("converts relocate events into saveable progress", () => {
    expect(
      progressFromRelocate(
        {
          cfi: "epubcfi(/6/4)",
          location: { current: 2, total: 10 },
        },
        42,
      ),
    ).toEqual({
      file_id: 42,
      location: "epubcfi(/6/4)",
      progress: 0.3,
    });
  });

  it("converts non-CFI relocate events into fraction-backed progress", () => {
    expect(
      progressFromRelocate(
        {
          location: { current: 4, total: 10 },
        },
        42,
      ),
    ).toEqual({
      file_id: 42,
      location: "fraction:0.500000",
      progress: 0.5,
    });
  });

  it("chooses a fraction restore target for non-CFI reader progress", () => {
    expect(
      restoreProgressTarget({
        file_id: 42,
        location: "fraction:0.500000",
        progress: 0.5,
      }),
    ).toEqual({ type: "fraction", fraction: 0.5 });
    expect(
      restoreProgressTarget({
        file_id: 42,
        location: "epubcfi(/6/4)",
        progress: 0.3,
      }),
    ).toEqual({ type: "location", location: "epubcfi(/6/4)" });
  });

  it("formats reader progress for compact controls", () => {
    expect(formatReaderProgress(null)).toBeNull();
    expect(formatReaderProgress(Number.NaN)).toBeNull();
    expect(formatReaderProgress(0)).toBe("0%");
    expect(formatReaderProgress(0.421)).toBe("42%");
    expect(formatReaderProgress(1)).toBe("100%");
    expect(formatReaderProgress(1.5)).toBe("100%");
  });

  it("ignores relocate events without a usable location", () => {
    expect(progressFromRelocate({ cfi: "epubcfi(/6/4)", location: { total: 0 } }, 42)).toBeNull();
  });

  it("normalizes reader settings into bounded values", () => {
    expect(
      normalizeReaderSettings({
        fontSize: 400,
        lineHeight: 0.2,
        margin: -10,
        maxWidth: 200,
        theme: "sepia",
        flow: "scrolled",
        spread: "none",
      }),
    ).toMatchObject({
      fontSize: 180,
      lineHeight: 1.1,
      margin: 0,
      maxWidth: 96,
      theme: "sepia",
      flow: "scrolled",
      spread: "none",
    });
  });

  it("builds reader styles from settings", () => {
    const styles = readerStyles(
      normalizeReaderSettings({
        theme: "dark",
        fontFamily: "Georgia, serif",
        fontSize: 128,
        fontWeight: 500,
        lineHeight: 1.8,
        maxWidth: 68,
        hyphenation: false,
      }),
    );

    expect(styles).toContain("color-scheme: dark");
    expect(styles).toContain("background: #111827 !important");
    expect(styles).toContain("font-family: Georgia, serif !important");
    expect(styles).toContain("font-size: 128% !important");
    expect(styles).toContain("font-weight: 500 !important");
    expect(styles).toContain("hyphens: none !important");
    expect(styles).toContain("line-height: 1.8 !important");
    expect(styles).toContain("max-width: 68ch !important");
  });

  it("uses full available width in scrolled flow", () => {
    expect(
      readerRendererAttributes(
        normalizeReaderSettings({
          flow: "paginated",
          maxWidth: 68,
          margin: 20,
          spread: "auto",
        }),
      ),
    ).toEqual({
      flow: null,
      gap: "20px",
      margin: "20px",
      maxColumnCount: "2",
      maxInlineSize: "68ch",
    });

    expect(
      readerRendererAttributes(
        normalizeReaderSettings({
          flow: "scrolled",
          maxWidth: 68,
          margin: 20,
          spread: "auto",
        }),
      ),
    ).toEqual({
      flow: "scrolled",
      gap: "20px",
      margin: "20px",
      maxColumnCount: "1",
      maxInlineSize: "100%",
    });
  });
});
