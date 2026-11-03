// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { DiscoverChart } from "@/api/v2/adminRequests";
import {
  BRAND_ROWS,
  BUILT_IN_ROWS,
  fromTMDBChart,
  missingStockRows,
  newRow,
  rowErrors,
  sortForMediaType,
  toTMDBChart,
  withRow,
} from "./discoverLayoutModel";

describe("withRow", () => {
  const rows = BUILT_IN_ROWS.slice(0, 3);

  it("replaces the stored row of the same id where it stands", () => {
    const renamed = { ...rows[1]!, title: "Hot shows" };
    expect(withRow(rows, renamed).map((row) => row.title)).toEqual([
      "Trending Movies",
      "Hot shows",
      "Popular Movies",
    ]);
  });

  it("adds a new row, or a row the layout lost, at the end", () => {
    const added = {
      ...newRow("mdblist"),
      title: "Anime",
      list: { url: "https://mdblist.com/lists/a/b" },
    };
    expect(withRow(rows, added)).toHaveLength(4);
    expect(withRow(rows, BRAND_ROWS[0]!).at(-1)?.id).toBe("studios");
  });
});

describe("chart conversion", () => {
  it.each<DiscoverChart>([
    { preset: "trending", media_type: "series", time_window: "week" },
    { preset: "trending", media_type: "all", time_window: "day" },
    { preset: "popular", media_type: "movie" },
  ])("keeps %o through the collection chart picker", (chart) => {
    expect(fromTMDBChart(toTMDBChart(chart))).toEqual(chart);
  });
});

describe("sortForMediaType", () => {
  it.each([
    ["primary_release_date.asc", "series", "first_air_date.asc"],
    ["first_air_date.desc", "movie", "primary_release_date.desc"],
    ["revenue.desc", "series", "popularity.desc"],
    ["vote_average.desc", "series", "vote_average.desc"],
  ] as const)("%s for %s is %s", (sort, mediaType, want) => {
    expect(sortForMediaType(sort, mediaType)).toBe(want);
  });
});

describe("rowErrors", () => {
  it("picks one row's errors, not a neighbour's with a longer index", () => {
    const fields = {
      "rows[1].list.url": "mdblist url must be an https://mdblist.com/lists/... list",
      "rows[12].title": "a row needs a title",
      rows: "a layout has at most 30 rows",
    };
    expect(rowErrors(fields, 1)).toEqual([
      "list.url: mdblist url must be an https://mdblist.com/lists/... list",
    ]);
  });
});

describe("missingStockRows", () => {
  it("offers the built-in and brand rows the layout dropped", () => {
    const kept = [...BUILT_IN_ROWS.slice(1), ...BRAND_ROWS.slice(0, 2)];
    expect(missingStockRows(kept).map((row) => row.id)).toEqual(["trending_movies", "genres"]);
  });
});
