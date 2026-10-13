// @vitest-environment node

import { describe, expect, it } from "vitest";
import { buildHomeSectionViewModel } from "./homeSectionState";

describe("buildHomeSectionViewModel", () => {
  it("marks empty sections as empty once a section payload is available", () => {
    const vm = buildHomeSectionViewModel({
      layout: [
        {
          id: "row-1",
          section_type: "recently_added",
          title: "Row 1",
          featured: false,
          item_limit: 5,
          is_custom: false,
          customized: false,
        },
      ],
      loadedSections: new Map([
        [
          "row-1",
          {
            id: "row-1",
            section_type: "recently_added",
            title: "Row 1",
            featured: false,
            item_limit: 5,
            total_count: 0,
            is_custom: false,
            customized: false,
            items: [],
          },
        ],
      ]),
      failedIds: new Set(),
    });

    expect(vm.rows[0]?.state).toBe("empty");
  });

  it("keeps cached rows visible when a background refresh fails", () => {
    const vm = buildHomeSectionViewModel({
      layout: [
        {
          id: "row-1",
          section_type: "recently_added",
          title: "Row 1",
          featured: false,
          item_limit: 5,
          is_custom: false,
          customized: false,
        },
      ],
      loadedSections: new Map([
        [
          "row-1",
          {
            id: "row-1",
            section_type: "recently_added",
            title: "Row 1",
            featured: false,
            item_limit: 5,
            total_count: 0,
            is_custom: false,
            customized: false,
            items: [
              {
                content_id: "item-1",
                type: "movie",
                title: "Item One",
                year: 2024,
                genres: [],
                status: "matched",
                rating_imdb: null,
                overview: "",
                poster_url: "",
                poster_thumbhash: "",
                backdrop_url: "",
                backdrop_thumbhash: "",
                logo_url: "",
              },
            ],
          },
        ],
      ]),
      failedIds: new Set(["row-1"]),
    });

    expect(vm.rows[0]?.state).toBe("ready");
  });
});
