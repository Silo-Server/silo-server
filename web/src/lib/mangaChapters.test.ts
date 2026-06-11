// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { MangaChapter } from "@/api/types";
import { buildMangaList, prettifyVolumeLabel } from "./mangaChapters";

function chapter(partial: Partial<MangaChapter>): MangaChapter {
  return {
    content_id: partial.content_id ?? "c",
    title: partial.title ?? "Chapter",
    chapter_index: partial.chapter_index,
    volume: partial.volume,
  };
}

describe("buildMangaList", () => {
  it("renders a pure-volume series as flat volume units (no nested chapter)", () => {
    const entries = buildMangaList([
      chapter({ content_id: "v1", chapter_index: 1, volume: "v01" }),
      chapter({ content_id: "v2", chapter_index: 2, volume: "v02" }),
    ]);

    expect(entries).toEqual([
      { kind: "volume", chapter: expect.objectContaining({ content_id: "v1" }), label: "Volume 1" },
      { kind: "volume", chapter: expect.objectContaining({ content_id: "v2" }), label: "Volume 2" },
    ]);
  });

  it("renders a pure-chapter series as flat loose chapters ordered by index", () => {
    const entries = buildMangaList([
      chapter({ content_id: "c178", chapter_index: 178, volume: "" }),
      chapter({ content_id: "c179", chapter_index: 179 }),
    ]);

    expect(entries).toEqual([
      {
        kind: "chapter",
        chapter: expect.objectContaining({ content_id: "c178" }),
        label: "Chapter 178",
      },
      {
        kind: "chapter",
        chapter: expect.objectContaining({ content_id: "c179" }),
        label: "Chapter 179",
      },
    ]);
  });

  it("nests only when a single volume holds multiple chapters", () => {
    const entries = buildMangaList([
      chapter({ content_id: "v1-c2", chapter_index: 2, volume: "v01" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v01" }),
    ]);

    expect(entries).toHaveLength(1);
    const entry = entries[0];
    expect(entry?.kind).toBe("section");
    if (entry?.kind === "section") {
      expect(entry.label).toBe("Volume 1");
      expect(entry.chapters.map((c) => c.content_id)).toEqual(["v1-c1", "v1-c2"]);
    }
  });

  it("orders all top-level entries by representative index, loose chapters not forced last", () => {
    const entries = buildMangaList([
      chapter({ content_id: "loose-5", chapter_index: 5 }),
      chapter({ content_id: "v-c10", chapter_index: 10, volume: "v02" }),
      chapter({ content_id: "v-c1", chapter_index: 1, volume: "v01" }),
      chapter({ content_id: "loose-3", chapter_index: 3, volume: "" }),
    ]);

    expect(entries.map((e) => e.label)).toEqual(["Volume 1", "Chapter 3", "Chapter 5", "Volume 2"]);
  });

  it("orders a section by its minimum chapter index relative to other entries", () => {
    const entries = buildMangaList([
      chapter({ content_id: "v2-c9", chapter_index: 9, volume: "v02" }),
      chapter({ content_id: "v2-c10", chapter_index: 10, volume: "v02" }),
      chapter({ content_id: "v1", chapter_index: 1, volume: "v01" }),
    ]);

    expect(entries.map((e) => e.label)).toEqual(["Volume 1", "Volume 2"]);
    expect(entries[1]?.kind).toBe("section");
  });

  it("labels a loose chapter without an index by its trimmed title", () => {
    const entries = buildMangaList([chapter({ content_id: "bonus", title: "  Bonus  " })]);

    expect(entries).toEqual([
      {
        kind: "chapter",
        chapter: expect.objectContaining({ content_id: "bonus" }),
        label: "Bonus",
      },
    ]);
  });

  it("places chapters with a null index last within a section", () => {
    const entries = buildMangaList([
      chapter({ content_id: "v1-cNull", volume: "v01" }),
      chapter({ content_id: "v1-c1", chapter_index: 1, volume: "v01" }),
      chapter({ content_id: "v1-c2", chapter_index: 2, volume: "v01" }),
    ]);

    expect(entries[0]?.kind).toBe("section");
    if (entries[0]?.kind === "section") {
      expect(entries[0].chapters.map((c) => c.content_id)).toEqual(["v1-c1", "v1-c2", "v1-cNull"]);
    }
  });

  it("returns an empty array for no chapters", () => {
    expect(buildMangaList([])).toEqual([]);
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

describe("volume token normalization", () => {
  it("buckets 'v01' and '1' into the same volume", () => {
    const entries = buildMangaList([
      { content_id: "a", title: "Series v01", chapter_index: 1, volume: "v01" },
      { content_id: "b", title: "Series 1 extras", chapter_index: 2, volume: "1" },
    ]);

    // One section labeled "Volume 1" holding both chapters — not two
    // duplicate top-level entries.
    expect(entries).toHaveLength(1);
    const [entry] = entries;
    if (!entry || entry.kind !== "section") {
      throw new Error(`expected a section entry, got ${JSON.stringify(entry)}`);
    }
    expect(entry.label).toBe("Volume 1");
    expect(entry.chapters.map((c) => c.content_id)).toEqual(["a", "b"]);
  });

  it("keeps non-numeric tokens distinct", () => {
    const entries = buildMangaList([
      { content_id: "a", title: "Omnibus", chapter_index: 1, volume: "Omnibus" },
      { content_id: "b", title: "v2", chapter_index: 2, volume: "v2" },
    ]);
    expect(entries).toHaveLength(2);
  });
});
