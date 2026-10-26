// @vitest-environment node

import { describe, expect, it } from "vitest";
import { planNextHomeSectionBatch } from "./homeSectionQueue";

describe("planNextHomeSectionBatch", () => {
  it("prioritizes the featured section first and respects remaining concurrency slots", () => {
    expect(
      planNextHomeSectionBatch({
        layout: ["row-1", "row-2", "row-3", "hero", "row-4", "row-5", "row-6"].map((id) => ({
          id,
          section_type: "recently_added",
          title: id,
          featured: id === "hero",
          item_limit: 5,
          is_custom: false,
          customized: false,
        })),
        loadedIds: new Set(["row-1"]),
        inFlightIds: new Set(["row-2", "already"]),
        maxConcurrentRequests: 5,
      }),
    ).toEqual(["hero", "row-3", "row-4"]);
  });
});
