import { describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";

import type { FileVersion } from "@/api/types";
import {
  cacheEbookReaderProgress,
  ebookProgressPath,
  ebookReaderProgressQueryKey,
  ebookReadPath,
  formatReaderProgress,
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
    expect(readerFileFormat(version({ container: "zip", file_name: "book.fb2.zip" }))).toBe(
      "fbz",
    );
    expect(readerFileFormat(version({ file_path: "/library/book.cbz" }))).toBe("cbz");
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

  it("formats reader progress for compact controls", () => {
    expect(formatReaderProgress(null)).toBeNull();
    expect(formatReaderProgress(Number.NaN)).toBeNull();
    expect(formatReaderProgress(0)).toBe("0%");
    expect(formatReaderProgress(0.421)).toBe("42%");
    expect(formatReaderProgress(1)).toBe("100%");
    expect(formatReaderProgress(1.5)).toBe("100%");
  });

  it("ignores relocate events without a usable location", () => {
    expect(progressFromRelocate({ location: { current: 2, total: 10 } }, 42)).toBeNull();
    expect(progressFromRelocate({ cfi: "epubcfi(/6/4)", location: { total: 0 } }, 42)).toBeNull();
  });
});
