import { describe, expect, it } from "vitest";

import type { FileVersion } from "@/api/types";
import { DocumentLoader } from "@/reader/readest/libs/document";
import {
  isReaderSupportedFile,
  normalizeReaderSettings,
  parseReaderLocation,
  READER_FONT_STACKS,
  restoreProgressTarget,
  progressFromRelocate,
  readerFileFormat,
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

  it("parses stored reader locations into navigation targets", () => {
    expect(parseReaderLocation("fraction:0.500000")).toEqual({ type: "fraction", fraction: 0.5 });
    expect(parseReaderLocation("fraction:1.5")).toEqual({ type: "fraction", fraction: 1 });
    expect(parseReaderLocation(" epubcfi(/6/4) ")).toEqual({
      type: "location",
      location: "epubcfi(/6/4)",
    });
    expect(parseReaderLocation("fraction:not-a-number")).toBeNull();
    expect(parseReaderLocation("")).toBeNull();
    expect(parseReaderLocation(undefined)).toBeNull();
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

  it("migrates font choices persisted before the generic stacks existed", () => {
    expect(
      normalizeReaderSettings({ fontFamily: "Inter, ui-sans-serif, system-ui, sans-serif" })
        .fontFamily,
    ).toBe(READER_FONT_STACKS.sans);
    expect(normalizeReaderSettings({ fontFamily: "Georgia, serif" }).fontFamily).toBe(
      READER_FONT_STACKS.serif,
    );
    expect(normalizeReaderSettings({ fontFamily: "Merriweather, Georgia, serif" }).fontFamily).toBe(
      READER_FONT_STACKS.serif,
    );
    expect(
      normalizeReaderSettings({ fontFamily: "ui-serif, Georgia, Cambria, serif" }).fontFamily,
    ).toBe(READER_FONT_STACKS.serif);
  });

  it("normalizes persisted reading ruler settings", () => {
    expect(
      normalizeReaderSettings({
        readingRuler: true,
        readingRulerTop: 250,
      }),
    ).toMatchObject({
      readingRuler: true,
      readingRulerTop: 100,
    });
    expect(normalizeReaderSettings({ readingRulerTop: -10 })).toMatchObject({
      readingRuler: false,
      readingRulerTop: 0,
    });
  });
});
