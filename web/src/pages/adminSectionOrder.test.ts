// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { PageSectionConfig } from "@/api/types";
import { buildSectionReorderEntries, moveSectionBeforeTarget } from "./adminSectionOrder";

function makeSection(id: string, position: number): PageSectionConfig {
  return {
    id,
    scope: "home",
    library_id: null,
    position,
    section_type: "recently_added",
    title: id,
    featured: false,
    item_limit: 20,
    config: {},
    enabled: true,
    created_at: "2026-03-08T00:00:00Z",
    updated_at: "2026-03-08T00:00:00Z",
  };
}

describe("adminSectionOrder", () => {
  it("moves a dragged section before the drop target and rebuilds positions", () => {
    const sections = [
      makeSection("featured", 0),
      makeSection("recent", 1),
      makeSection("top-rated", 2),
    ];

    const reordered = moveSectionBeforeTarget(sections, "top-rated", "recent");

    expect(reordered.map((section) => section.id)).toEqual(["featured", "top-rated", "recent"]);
    expect(buildSectionReorderEntries(reordered)).toEqual([
      { id: "featured", position: 0 },
      { id: "top-rated", position: 1 },
      { id: "recent", position: 2 },
    ]);
  });

  it("keeps before-target semantics when moving a section downward", () => {
    const sections = [
      makeSection("featured", 0),
      makeSection("recent", 1),
      makeSection("top-rated", 2),
      makeSection("random", 3),
    ];

    const reordered = moveSectionBeforeTarget(sections, "featured", "random");

    expect(reordered.map((section) => section.id)).toEqual([
      "recent",
      "top-rated",
      "featured",
      "random",
    ]);
  });
});
