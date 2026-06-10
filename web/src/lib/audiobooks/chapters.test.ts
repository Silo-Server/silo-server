import { describe, expect, it } from "vitest";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { buildPlayerChapters, nextChapterStart, prevChapterStart } from "./chapters";

function file(id: number, durationSeconds: number, chapterStarts: number[]): AudiobookFile {
  return {
    id,
    duration_seconds: durationSeconds,
    chapters: chapterStarts.map((start, index) => ({
      index,
      title: `File ${id} Chapter ${index + 1}`,
      start_seconds: start,
      end_seconds: chapterStarts[index + 1] ?? durationSeconds,
      source: "embedded",
    })),
  } as AudiobookFile;
}

const files = [file(1, 600, [0, 300]), file(2, 400, [0, 200])];
const chapters = buildPlayerChapters(files);

describe("buildPlayerChapters", () => {
  it("flattens chapters across files with absolute offsets", () => {
    expect(chapters.map((c) => c.start_seconds)).toEqual([0, 300, 600, 800]);
    expect(chapters.map((c) => c.index)).toEqual([0, 1, 2, 3]);
    expect(chapters[2]?.end_seconds).toBe(800);
  });
});

describe("nextChapterStart", () => {
  it("returns the following chapter's absolute start", () => {
    expect(nextChapterStart(chapters, chapters[0]!)).toBe(300);
    expect(nextChapterStart(chapters, chapters[1]!)).toBe(600);
  });

  it("returns null at the last chapter and without a current chapter", () => {
    expect(nextChapterStart(chapters, chapters[3]!)).toBeNull();
    expect(nextChapterStart(chapters, null)).toBeNull();
  });
});

describe("prevChapterStart", () => {
  it("restarts the current chapter when more than the threshold in", () => {
    expect(prevChapterStart(chapters, chapters[1]!, 310)).toBe(300);
  });

  it("jumps to the previous chapter near the chapter start", () => {
    expect(prevChapterStart(chapters, chapters[1]!, 302)).toBe(0);
    expect(prevChapterStart(chapters, chapters[2]!, 601)).toBe(300);
  });

  it("stays at the start of the first chapter", () => {
    expect(prevChapterStart(chapters, chapters[0]!, 2)).toBe(0);
  });

  it("returns null without a current chapter", () => {
    expect(prevChapterStart(chapters, null, 100)).toBeNull();
  });
});
