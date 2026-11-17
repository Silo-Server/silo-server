import { describe, expect, it } from "vitest";
import { readSearchView, splitOverviewResults, withSearchView } from "./searchResultsView";

const items = (count: number) => Array.from({ length: count }, (_, index) => index);

describe("splitOverviewResults", () => {
  it("fills two rows of best matches, then up to three rows of more results", () => {
    const split = splitOverviewResults(items(60), 6, false);
    expect(split.best).toEqual(items(12));
    expect(split.more).toEqual([
      12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
    ]);
    expect(split.hasMoreThanShown).toBe(true);
  });

  it("shows everything without a Show all when the results fit", () => {
    const split = splitOverviewResults(items(8), 6, false);
    expect(split.best).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
    expect(split.more).toEqual([]);
    expect(split.hasMoreThanShown).toBe(false);
  });

  it("offers Show all when the server has another page beyond the loaded results", () => {
    expect(splitOverviewResults(items(20), 4, true).hasMoreThanShown).toBe(true);
  });

  it("treats an unmeasured grid as one column", () => {
    expect(splitOverviewResults(items(10), 0, false).best).toEqual([0, 1]);
  });
});

describe("search results view", () => {
  it.each([
    ["", "overview"],
    ["view=library", "library"],
    ["view=requests", "requests"],
    ["view=nonsense", "overview"],
    ["request_page=2", "requests"],
  ])("reads %j as %s", (query, expected) => {
    expect(readSearchView(new URLSearchParams(query))).toBe(expected);
  });

  it("keeps the overview out of the URL and resets the request pager", () => {
    const params = new URLSearchParams("q=star&view=library&request_page=3");
    expect(withSearchView(params, "overview").toString()).toBe("q=star");
    expect(withSearchView(params, "requests").toString()).toBe("q=star&view=requests");
  });
});
