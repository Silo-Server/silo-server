import { describe, expect, it } from "vitest";

import type { FileVersion } from "@/api/types";
import { ebookReadPath, readerFileFormat, readerMimeType } from "./FoliateBookReader";

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

  it("detects reader file formats from container or filename", () => {
    expect(readerFileFormat(version({ container: ".AZW3", file_name: "ignored.epub" }))).toBe(
      "azw3",
    );
    expect(readerFileFormat(version({ file_name: "book.fb2.zip" }))).toBe("fbz");
    expect(readerFileFormat(version({ file_path: "/library/book.cbz" }))).toBe("cbz");
  });

  it("maps readest formats to MIME types", () => {
    expect(readerMimeType("epub")).toBe("application/epub+zip");
    expect(readerMimeType("azw3")).toBe("application/vnd.amazon.mobi8-ebook");
    expect(readerMimeType("fbz")).toBe("application/x-zip-compressed-fb2");
    expect(readerMimeType("md")).toBe("text/markdown");
  });
});
