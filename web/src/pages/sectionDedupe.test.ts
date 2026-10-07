import { describe, expect, it } from "vitest";

import type { ResolvedSection, ResolvedSectionLayout } from "@/api/types";
import { dropCrossSectionDuplicates } from "./sectionDedupe";

const AVOID = new Set(["recommended_for_you", "taste_match", "similar_users_liked"]);

function layoutEntry(id: string, sectionType: string): ResolvedSectionLayout {
  return {
    id,
    section_type: sectionType,
    title: id,
    featured: false,
    item_limit: 20,
    is_custom: false,
    customized: false,
  };
}

function section(entry: ResolvedSectionLayout, contentIDs: string[]): ResolvedSection {
  return {
    ...entry,
    total_count: contentIDs.length,
    items: contentIDs.map((contentID) => ({
      content_id: contentID,
      type: "movie",
      title: contentID,
    })) as ResolvedSection["items"],
  };
}

function itemIDs(sections: Map<string, ResolvedSection>): Record<string, string[]> {
  return Object.fromEntries(
    Array.from(sections, ([id, s]) => [id, s.items.map((item) => item.content_id)]),
  );
}

const layout = [
  layoutEntry("featured", "recently_added"),
  layoutEntry("for-you", "recommended_for_you"),
  layoutEntry("top-picks", "taste_match"),
  layoutEntry("recent", "recently_added"),
  layoutEntry("fans", "similar_users_liked"),
];

const served: Record<string, string[]> = {
  featured: ["a"],
  "for-you": ["a", "b", "c"],
  "top-picks": ["b", "d"],
  recent: ["c", "e"],
  fans: ["d", "e", "f"],
};

function loaded(ids: string[]): Map<string, ResolvedSection> {
  return new Map(
    ids.map((id) => [id, section(layout.find((entry) => entry.id === id)!, served[id]!)]),
  );
}

describe("dropCrossSectionDuplicates", () => {
  it("drops from avoid-duplicates sections what an earlier section shows, in layout order", () => {
    const result = dropCrossSectionDuplicates(layout, loaded(Object.keys(served)), AVOID);

    expect(itemIDs(result)).toEqual({
      featured: ["a"],
      "for-you": ["b", "c"],
      "top-picks": ["d"],
      // Sections that allow duplicates keep theirs but still mark items seen.
      recent: ["c", "e"],
      fans: ["f"],
    });
    expect(result.get("fans")?.total_count).toBe(1);
  });

  it("depends only on which rows have loaded, not the order they arrived in", () => {
    const ids = Object.keys(served);
    const expected = itemIDs(dropCrossSectionDuplicates(layout, loaded(ids), AVOID));
    for (const arrival of [
      [...ids].reverse(),
      ["recent", "fans", "featured", "top-picks", "for-you"],
    ]) {
      expect(itemIDs(dropCrossSectionDuplicates(layout, loaded(arrival), AVOID))).toEqual(expected);
    }
  });

  it("lets a later row show an item until the earlier row that claims it loads", () => {
    const before = dropCrossSectionDuplicates(layout, loaded(["top-picks", "fans"]), AVOID);
    expect(itemIDs(before)).toEqual({ "top-picks": ["b", "d"], fans: ["e", "f"] });

    const after = dropCrossSectionDuplicates(
      layout,
      loaded(["top-picks", "fans", "for-you"]),
      AVOID,
    );
    expect(itemIDs(after)).toEqual({
      "top-picks": ["d"],
      fans: ["e", "f"],
      "for-you": ["a", "b", "c"],
    });
  });

  it("keeps unchanged sections as they are and changes nothing without avoid types", () => {
    const input = loaded(Object.keys(served));
    const result = dropCrossSectionDuplicates(layout, input, AVOID);
    expect(result.get("featured")).toBe(input.get("featured"));
    expect(result.get("recent")).toBe(input.get("recent"));

    const untouched = dropCrossSectionDuplicates(layout, input, new Set());
    for (const [id, s] of input) {
      expect(untouched.get(id)).toBe(s);
    }
  });

  it("keeps items without a content ID", () => {
    const entries = [layoutEntry("one", "taste_match"), layoutEntry("two", "taste_match")];
    const input = new Map([
      ["one", section(entries[0]!, ["", "x"])],
      ["two", section(entries[1]!, ["", "x", "y"])],
    ]);
    expect(itemIDs(dropCrossSectionDuplicates(entries, input, AVOID))).toEqual({
      one: ["", "x"],
      two: ["", "y"],
    });
  });
});
