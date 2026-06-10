// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { MangaChapter } from "@/api/types";
import { groupMangaChapters, prettifyVolumeLabel } from "./mangaChapters";

function chapter(partial: Partial<MangaChapter>): MangaChapter {
  return {
    content_id: partial.content_id ?? "c",
    title: partial.title ?? "Chapter",
    chapter_index: partial.chapter_index,
    volume: partial.volume,
  };
}

describe("groupMangaChapters", () => {
  it("groups chapters by volume and orders within a group by chapter_index", () => {
    const groups = groupMangaChapters([
      chapter({ content_id: "v1-c2", chapter_index: 2, volume: "v1" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v1" }),
      chapter({ content_id: "v2-c3", chapter_index: 3, volume: "v2" }),
    ]);

    expect(groups.map((g) => g.volume)).toEqual(["v1", "v2"]);
    expect(groups[0]?.chapters.map((c) => c.content_id)).toEqual(["v1-c1", "v1-c2"]);
    expect(groups[1]?.chapters.map((c) => c.content_id)).toEqual(["v2-c3"]);
  });

  it("orders groups by their minimum chapter_index", () => {
    const groups = groupMangaChapters([
      chapter({ content_id: "v2-c10", chapter_index: 10, volume: "v2" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v1" }),
    ]);

    expect(groups.map((g) => g.volume)).toEqual(["v1", "v2"]);
  });

  it("collects loose chapters (no volume) into a single trailing 'Chapters' group", () => {
    const groups = groupMangaChapters([
      chapter({ content_id: "loose-b", chapter_index: 6, volume: "" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v1" }),
      chapter({ content_id: "loose-a", chapter_index: 5 }),
    ]);

    expect(groups.map((g) => g.volume)).toEqual(["v1", null]);
    const loose = groups.find((g) => g.volume === null);
    expect(loose?.chapters.map((c) => c.content_id)).toEqual(["loose-a", "loose-b"]);
  });

  it("places chapters with a null chapter_index last within their group", () => {
    const groups = groupMangaChapters([
      chapter({ content_id: "v1-cNull", volume: "v1" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v1" }),
    ]);

    expect(groups[0]?.chapters.map((c) => c.content_id)).toEqual(["v1-c1", "v1-cNull"]);
  });

  it("returns an empty array for no chapters", () => {
    expect(groupMangaChapters([])).toEqual([]);
  });
});

describe("prettifyVolumeLabel", () => {
  it("expands a v-prefixed token to a Volume label", () => {
    expect(prettifyVolumeLabel("v13")).toBe("Volume 13");
    expect(prettifyVolumeLabel("V2")).toBe("Volume 2");
  });

  it("expands a bare numeric token to a Volume label", () => {
    expect(prettifyVolumeLabel("7")).toBe("Volume 7");
  });

  it("passes through non-numeric tokens unchanged", () => {
    expect(prettifyVolumeLabel("Omnibus")).toBe("Omnibus");
  });
});
