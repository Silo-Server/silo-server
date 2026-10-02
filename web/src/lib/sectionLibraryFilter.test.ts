import { describe, expect, it } from "vitest";
import { sectionLibraryFilterIds, withSectionLibraryFilterIds } from "./sectionLibraryFilter";

describe("sectionLibraryFilterIds", () => {
  it("returns no libraries for an empty config", () => {
    expect(sectionLibraryFilterIds({})).toEqual([]);
  });

  it("reads the legacy single-library key used by generated sections", () => {
    expect(
      sectionLibraryFilterIds({ generated_source: "home_library_recent", filter_library_id: 2 }),
    ).toEqual([2]);
  });

  it("unions filter_library_ids with the legacy key", () => {
    expect(
      sectionLibraryFilterIds({ filter_library_ids: [4, 4, 6], filter_library_id: 2 }),
    ).toEqual([4, 6, 2]);
  });

  it("treats a non-empty filter_library_ids as set even without usable IDs, like the backend", () => {
    expect(sectionLibraryFilterIds({ filter_library_ids: [0], library_ids: [3] })).toEqual([]);
  });

  it("falls back to query-definition library_ids only without a flat key", () => {
    expect(sectionLibraryFilterIds({ library_ids: [17], media_scope: "manga" })).toEqual([17]);
    expect(sectionLibraryFilterIds({ library_ids: [17], filter_library_ids: [3] })).toEqual([3]);
  });
});

describe("withSectionLibraryFilterIds", () => {
  it("writes filter_library_ids to an empty config", () => {
    expect(withSectionLibraryFilterIds({}, [4])).toEqual({ filter_library_ids: [4] });
  });

  it("replaces the legacy key instead of widening it", () => {
    const next = withSectionLibraryFilterIds(
      { generated_source: "home_library_recent", filter_library_id: 2 },
      [4, 2],
    );
    expect(next).toEqual({
      generated_source: "home_library_recent",
      generated_library_id: 2,
      filter_library_ids: [4, 2],
    });
    expect(sectionLibraryFilterIds(next)).toEqual([4, 2]);
  });

  it("keeps the legacy owner while its library is deselected in the draft", () => {
    expect(
      withSectionLibraryFilterIds(
        {
          generated_source: "home_library_recent",
          filter_library_id: 2,
          generated_library_id: null,
        },
        [4],
      ),
    ).toEqual({
      generated_source: "home_library_recent",
      generated_library_id: 2,
      filter_library_ids: [4],
    });
  });

  it("keeps the owner in both config shapes when a draft leaves its library out", () => {
    expect(
      withSectionLibraryFilterIds(
        {
          generated_source: "home_library_recent",
          generated_library_id: 2,
          filter_library_ids: [2],
        },
        [4],
      ),
    ).toEqual({
      generated_source: "home_library_recent",
      generated_library_id: 2,
      filter_library_ids: [4],
    });
    expect(
      withSectionLibraryFilterIds(
        {
          generated_source: "home_library_recent",
          generated_library_id: 2,
          library_ids: [2],
          media_scope: "series",
        },
        [4],
      ),
    ).toEqual({
      generated_source: "home_library_recent",
      generated_library_id: 2,
      library_ids: [4],
      media_scope: "series",
    });
  });

  it("keeps a generated row's library when all libraries are selected", () => {
    expect(
      withSectionLibraryFilterIds(
        { generated_source: "home_library_recent", filter_library_id: 2 },
        [],
      ),
    ).toEqual({ generated_source: "home_library_recent", generated_library_id: 2 });
  });

  it("drops a stale library_ids when clearing a flat filter, keeping other fields", () => {
    const next = withSectionLibraryFilterIds(
      { filter_library_ids: [3], library_ids: [17], sort: { field: "added_at" } },
      [],
    );
    expect(next).toEqual({ sort: { field: "added_at" } });
    expect(sectionLibraryFilterIds(next)).toEqual([]);
  });

  it("clears both flat keys when all libraries are selected", () => {
    expect(
      withSectionLibraryFilterIds({ filter_library_id: 2, filter_library_ids: [4] }, []),
    ).toEqual({});
  });

  it("keeps a query-definition config in its own shape so media_scope still applies", () => {
    const config = { library_ids: [17], media_scope: "manga", match: "all" };
    expect(withSectionLibraryFilterIds(config, [17, 18])).toEqual({
      library_ids: [17, 18],
      media_scope: "manga",
      match: "all",
    });
    expect(withSectionLibraryFilterIds(config, [])).toEqual({ media_scope: "manga", match: "all" });
  });

  it("does not mutate its input", () => {
    const config = { filter_library_id: 2 };
    withSectionLibraryFilterIds(config, [4]);
    expect(config).toEqual({ filter_library_id: 2 });
  });
});
