import { describe, expect, it } from "vitest";
import { canCopyToLibraries, copyTargetPages, libraryCopyIds } from "./bulkCopy";
import { recipeCatalogFixture } from "./recipeCatalogFixture.test-support";

const LIBRARY = { kind: "library", libraryId: 7 } as const;

function readyMadeTypes(): string[] {
  return Object.values(recipeCatalogFixture.categories ?? {})
    .flat()
    .map((def) => def!.type)
    .filter((type) => !["collection", "custom_filter", "admin_curated_list"].includes(type));
}

describe("canCopyToLibraries", () => {
  it("allows every ready-made kind whose settings name no library", () => {
    const types = readyMadeTypes();
    expect(types.length).toBeGreaterThan(20);
    for (const sectionType of types) {
      expect(canCopyToLibraries({ sectionType, config: {} }), sectionType).toBe(true);
    }
  });

  it.each(["collection", "custom_filter", "admin_curated_list", "genre", "award_winners"])(
    "refuses %s rows, whose settings belong to one page",
    (sectionType) => {
      expect(canCopyToLibraries({ sectionType, config: {} })).toBe(false);
    },
  );

  it.each([
    ["filter_library_ids", { filter_library_ids: [7] }],
    ["filter_library_id", { filter_library_id: 7 }],
    ["library_ids", { library_ids: [7] }],
    ["generated_library_id", { generated_source: "home_library", generated_library_id: 7 }],
    ["library_id", { subject_type: "director", library_id: 7 }],
  ])("refuses a row whose settings name a library through %s", (_key, config) => {
    expect(canCopyToLibraries({ sectionType: "recently_added", config })).toBe(false);
  });

  it("refuses a legacy Trakt row, which the server won't create again", () => {
    expect(
      canCopyToLibraries({ sectionType: "trending_discover", config: { source: "trakt" } }),
    ).toBe(false);
  });
});

const PAGES = [
  { id: 7, label: "Movies", libraryType: "movies" },
  { id: 8, label: "TV Shows", libraryType: "series" },
  { id: 9, label: "Kids", libraryType: " Movies " },
  { id: 10, label: "Unknown" },
];

const recentlyAdded = (config: Record<string, unknown>) => ({
  sectionType: "recently_added",
  config,
});

describe("copyTargetPages", () => {
  it("offers every page for a row that isn't set to one kind of title", () => {
    expect(copyTargetPages(recentlyAdded({ window: "7d" }), PAGES, 7)).toEqual(PAGES);
    expect(copyTargetPages(recentlyAdded({ media_scope: "" }), PAGES, 7)).toEqual(PAGES);
  });

  it("offers a row set to one kind of title only pages of this page's library type", () => {
    expect(
      copyTargetPages(recentlyAdded({ media_scope: "movie" }), PAGES, 7).map((page) => page.id),
    ).toEqual([7, 9]);
    expect(
      copyTargetPages(recentlyAdded({ media_scope: "series" }), PAGES, 8).map((page) => page.id),
    ).toEqual([8]);
    // A page whose library type is unknown matches nothing but itself.
    expect(
      copyTargetPages(recentlyAdded({ media_scope: "movie" }), PAGES, 10).map((page) => page.id),
    ).toEqual([10]);
  });
});

describe("copyTargetPages for rows that show one kind of library", () => {
  const BOOK_PAGES = [
    { id: 20, label: "Audiobooks", libraryType: "audiobooks" },
    { id: 21, label: "More audiobooks", libraryType: "Audiobooks" },
    { id: 22, label: "Ebooks", libraryType: "ebooks" },
    ...PAGES,
  ];
  const ids = (row: { sectionType: string; config: Record<string, unknown> }, current = 20) =>
    copyTargetPages(row, BOOK_PAGES, current).map((page) => page.id);

  it.each([
    [
      "Continue Listening",
      { sectionType: "continue_watching", config: { continue_type: "listening" } },
    ],
    [
      "a legacy audiobook filter",
      { sectionType: "continue_watching", config: { filter_type: "audiobook" } },
    ],
    ["Next in Series", { sectionType: "next_in_series", config: {} }],
  ])("offers %s only audiobook pages", (_name, row) => {
    expect(ids(row)).toEqual([20, 21]);
  });

  it("offers Continue Reading only ebook pages", () => {
    const row = { sectionType: "continue_watching", config: { continue_type: "Reading" } };
    expect(ids(row, 22)).toEqual([22]);
  });

  it.each([
    [
      "Continue Listening",
      { sectionType: "continue_watching", config: { continue_type: "listening" } },
      [20, 21, 7],
    ],
    [
      "Continue Reading",
      { sectionType: "continue_watching", config: { continue_type: "reading" } },
      [22, 7],
    ],
    [
      "a legacy ebook filter",
      { sectionType: "continue_watching", config: { filter_type: "ebook" } },
      [22, 7],
    ],
    ["Next in Series", { sectionType: "next_in_series", config: {} }, [20, 21, 7]],
  ])(
    "offers %s on another kind of page the pages of the row's own library type",
    (_name, row, want) => {
      expect(ids(row, 7)).toEqual(want);
    },
  );

  it("matches library types by kind, not by spelling", () => {
    const pages = [
      { id: 30, label: "Audiobooks", libraryType: "audiobook" },
      { id: 31, label: "More audiobooks", libraryType: "Audiobooks" },
      { id: 32, label: "TV", libraryType: "tv" },
      { id: 33, label: "Shows", libraryType: "series" },
    ];
    expect(
      copyTargetPages({ sectionType: "next_in_series", config: {} }, pages, 32).map((p) => p.id),
    ).toEqual([30, 31, 32]);
    expect(
      copyTargetPages(recentlyAdded({ media_scope: "series" }), pages, 32).map((p) => p.id),
    ).toEqual([32, 33]);
  });

  it("offers Continue Watching and other rows every page", () => {
    expect(
      ids({ sectionType: "continue_watching", config: { continue_type: "watching" } }, 7),
    ).toEqual(BOOK_PAGES.map((page) => page.id));
    expect(ids({ sectionType: "continue_watching", config: {} }, 7)).toHaveLength(
      BOOK_PAGES.length,
    );
  });
});

describe("libraryCopyIds", () => {
  const draft = { sectionType: "trending_on_server", config: { window: "7d" }, hero: false };

  it("returns the other pages picked, without the current one or repeats", () => {
    expect(libraryCopyIds({ ...draft, extraLibraryIds: [8, 7, 9, 8] }, LIBRARY, PAGES)).toEqual([
      8, 9,
    ]);
  });

  it("drops pages of another library type for a row set to one kind of title", () => {
    expect(
      libraryCopyIds(
        { ...draft, config: { media_scope: "movie" }, extraLibraryIds: [8, 9] },
        LIBRARY,
        PAGES,
      ),
    ).toEqual([9]);
  });

  it("returns nothing on Home, for a hero row, or for a kind that can't be copied", () => {
    expect(libraryCopyIds({ ...draft, extraLibraryIds: [8] }, { kind: "home" }, PAGES)).toEqual([]);
    expect(libraryCopyIds({ ...draft, hero: true, extraLibraryIds: [8] }, LIBRARY, PAGES)).toEqual(
      [],
    );
    expect(
      libraryCopyIds(
        { sectionType: "collection", config: {}, hero: false, extraLibraryIds: [8] },
        LIBRARY,
        PAGES,
      ),
    ).toEqual([]);
    expect(libraryCopyIds(draft, LIBRARY, PAGES)).toEqual([]);
  });
});
