import { describe, expect, it } from "vitest";

import type { Library, PageSectionConfig } from "@/api/types";
import { templateResultFromV2 } from "@/api/adminCollections";
import {
  coreApplied,
  coreAppliedAllFailed,
  coreDryRun,
  coreDryRunWithHeroes,
  starterPackBundles,
  starterPackLibraries,
} from "@/test/fixtures/starterPacks";
import {
  KEEP_CURRENT,
  currentHero,
  defaultPackLibraryIds,
  effectiveHeroes,
  failureReason,
  featuredRequest,
  heroTemplates,
  packAdded,
  packLibraries,
  packPlan,
  packResultHeading,
  packResultSummary,
  starterPacksOf,
  templateFitsLibrary,
} from "./starterPacks";

const packs = starterPacksOf(starterPackBundles.bundles);
const pack = (id: string) => packs.find((entry) => entry.id === id)!;
const [movies, shows, audiobooks] = starterPackLibraries as [Library, Library, Library];

describe("starter packs", () => {
  it("lists Everything last and leaves out templates that need setup", () => {
    expect(packs.map((entry) => [entry.id, entry.templates.length, entry.everything])).toEqual([
      ["core_defaults", 5, false],
      ["popular_genres", 2, false],
      ["franchise_collections", 1, false],
      ["all_defaults", 8, true],
    ]);
    expect(pack("franchise_collections").templates.map((entry) => entry.title)).toEqual([
      "Star Wars",
    ]);
  });

  it("drops a pack whose only templates need setup", () => {
    const placeholderOnly = {
      ...starterPackBundles.bundles[3]!,
      id: "placeholders",
      templates: starterPackBundles.bundles[3]!.templates.slice(1),
    };
    expect(starterPacksOf([placeholderOnly])).toEqual([]);
  });

  it.each([
    ["movie", "movies", true],
    ["movie", "series", false],
    ["tv", "series", true],
    ["tv", "TVShows", true],
    ["mixed", "series", true],
    ["movie", "mixed", true],
    ["movie", "", true],
    ["movie", "audiobooks", false],
    ["movie", "ebooks", false],
  ])("a %s template fits a %j library: %s", (kind, type, fits) => {
    expect(templateFitsLibrary(kind, type)).toBe(fits);
  });

  it("offers the enabled libraries that can take at least one of the pack's lists", () => {
    const disabled = { ...shows, id: 4, enabled: false };
    expect(
      packLibraries(pack("core_defaults"), [...starterPackLibraries, disabled]).map(
        (library) => library.name,
      ),
    ).toEqual(["Movies", "TV Shows"]);
    expect(packLibraries(pack("franchise_collections"), starterPackLibraries)).toEqual([movies]);
  });

  it("starts with the page's library when it fits, otherwise every library that fits", () => {
    const core = pack("core_defaults");
    expect(defaultPackLibraryIds(core, starterPackLibraries, 2)).toEqual([2]);
    expect(defaultPackLibraryIds(core, starterPackLibraries, 3)).toEqual([1, 2]);
    expect(defaultPackLibraryIds(core, starterPackLibraries, null)).toEqual([1, 2]);
    expect(defaultPackLibraryIds(core, [audiobooks], null)).toEqual([]);
  });

  it("reads one table row per library from a dry run", () => {
    const plan = packPlan(templateResultFromV2(coreDryRun), pack("core_defaults"), [1, 2]);
    expect(plan).toEqual([
      {
        libraryId: 1,
        libraryName: "Movies",
        added: [
          {
            templateId: "tmdb_trending_movies_week",
            title: "Trending Movies This Week",
            pinned: true,
          },
          { templateId: "tmdb_popular_movies", title: "Popular Movies", pinned: false },
        ],
        existing: [
          { templateId: "tmdb_top_rated_movies", title: "Top Rated Movies", pinned: false },
        ],
        failed: [],
        notForLibrary: "2 TV lists",
      },
      {
        libraryId: 2,
        libraryName: "TV Shows",
        added: [
          { templateId: "tmdb_trending_tv_week", title: "Trending TV This Week", pinned: true },
          { templateId: "tmdb_popular_tv", title: "Popular TV", pinned: false },
        ],
        existing: [],
        failed: [],
        notForLibrary: "3 movie lists",
      },
    ]);
  });

  it("keeps a failed list and its reason, and hides templates that need setup", () => {
    const franchise = pack("franchise_collections");
    const result = templateResultFromV2({
      ...coreDryRun,
      bundle_id: "franchise_collections",
      created: [
        {
          template_id: "tmdb_franchise_placeholder",
          template_title: "TMDB Franchise",
          library_id: "1",
          library_name: "Movies",
          reason: "would_create",
        },
      ],
      skipped: [],
      failed: [
        {
          template_id: "tmdb_franchise_star_wars",
          template_title: "Star Wars",
          library_id: "1",
          library_name: "Movies",
          reason: "operation_failed",
        },
      ],
    });
    expect(packPlan(result, franchise, [1])).toEqual([
      {
        libraryId: 1,
        libraryName: "Movies",
        added: [],
        existing: [],
        failed: [
          {
            templateId: "tmdb_franchise_star_wars",
            title: "Star Wars",
            pinned: false,
            reason: "something went wrong",
          },
        ],
        notForLibrary: "",
      },
    ]);
  });

  it("reads a finished apply the same way", () => {
    const plan = packPlan(templateResultFromV2(coreApplied), pack("core_defaults"), [1, 2]);
    expect(plan.map((row) => [row.libraryName, row.added.length, row.failed.length])).toEqual([
      ["Movies", 2, 0],
      ["TV Shows", 1, 1],
    ]);
  });

  it("counts a finished apply as added unless nothing new landed and something failed", () => {
    const core = pack("core_defaults");
    expect(packAdded(templateResultFromV2(coreApplied), core)).toBe(true);
    expect(packAdded(templateResultFromV2(coreAppliedAllFailed), core)).toBe(false);
    // Only heroes set: the pack's lists were all there already.
    expect(packAdded(templateResultFromV2({ ...coreAppliedAllFailed, failed: [] }), core)).toBe(
      true,
    );
    expect(
      packAdded(
        templateResultFromV2({
          ...coreAppliedAllFailed,
          failed: [],
          featured_failed: [
            {
              surface: "home",
              library_id: "1",
              library_name: "Movies",
              template_id: "tmdb_trending_movies_week",
              template_title: "Trending Movies This Week",
              reason: "row limit reached",
            },
          ],
        }),
        core,
      ),
    ).toBe(false);
  });

  it.each([
    ["collection_not_available", "its list wasn't added to that library"],
    ["ineligible_library", "that library can't take this list"],
    ["template_not_in_bundle", "that list isn't in this pack"],
    ["library_not_selected", "that library isn't one you picked"],
    ["template_not_found", "this server doesn't have that list anymore"],
    ["operation_failed", "something went wrong"],
    [undefined, "something went wrong"],
  ])("says why %s failed in words", (code, words) => {
    expect(failureReason(code)).toBe(words);
  });

  it("sums up a finished apply from the lists the pack shows", () => {
    const core = pack("core_defaults");
    const applied = templateResultFromV2({
      ...coreApplied,
      // The franchise placeholder isn't in the pack's list, so it isn't counted.
      created: [
        ...coreApplied.created,
        {
          template_id: "tmdb_franchise_placeholder",
          template_title: "TMDB Franchise",
          library_id: "1",
          library_name: "Movies",
        },
      ],
      featured: coreDryRunWithHeroes.featured.slice(0, 2),
    });
    expect(packResultSummary(applied, core)).toBe(
      "Added 3 lists. 1 list couldn't be added. Set 2 hero banners.",
    );
    expect(packResultSummary(templateResultFromV2(coreAppliedAllFailed), core)).toBe(
      "Nothing new was added. 2 lists couldn't be added.",
    );
    expect(packResultHeading("Core Defaults", true, true)).toBe("Core Defaults added");
    expect(packResultHeading("Core Defaults", true, false)).toBe(
      "Core Defaults finished with problems",
    );
    expect(packResultHeading("Core Defaults", false, false)).toBe("Core Defaults wasn't added");
  });

  it("calls a mixed set of left-out lists just lists", () => {
    const result = templateResultFromV2({
      ...coreDryRun,
      created: [],
      skipped: [
        {
          template_id: "tmdb_trending_tv_week",
          template_title: "Trending TV This Week",
          library_id: "3",
          library_name: "Audiobooks",
          reason: "ineligible_library",
        },
        {
          template_id: "tmdb_popular_movies",
          template_title: "Popular Movies",
          library_id: "3",
          library_name: "Audiobooks",
          reason: "ineligible_library",
        },
      ],
    });
    expect(packPlan(result, pack("core_defaults"), [3])[0]!.notForLibrary).toBe("2 lists");
  });
});

describe("starter pack hero banners", () => {
  const core = pack("core_defaults");

  it("offers each page the pack's lists that fit it", () => {
    expect(heroTemplates(core, shows).map((entry) => entry.id)).toEqual([
      "tmdb_trending_tv_week",
      "tmdb_popular_tv",
    ]);
  });

  it("seeds Home and each library page with today's defaults", () => {
    expect(effectiveHeroes(core, [movies, shows], {})).toEqual({
      home: "1:tmdb_trending_movies_week",
      libraries: { 1: "tmdb_trending_movies_week", 2: "tmdb_trending_tv_week" },
    });
    expect(effectiveHeroes(core, [shows], {})).toEqual({
      home: "2:tmdb_trending_tv_week",
      libraries: { 2: "tmdb_trending_tv_week" },
    });
  });

  it("keeps a pick while it still fits and drops it when its library is unticked", () => {
    const picked = { home: "2:tmdb_popular_tv", libraries: { 1: KEEP_CURRENT } };
    expect(effectiveHeroes(core, [movies, shows], picked)).toEqual({
      home: "2:tmdb_popular_tv",
      libraries: { 1: KEEP_CURRENT, 2: "tmdb_trending_tv_week" },
    });
    expect(effectiveHeroes(core, [movies], picked)).toEqual({
      home: "1:tmdb_trending_movies_week",
      libraries: { 1: KEEP_CURRENT },
    });
  });

  it("asks only for the pages that don't keep their current hero", () => {
    expect(
      featuredRequest({
        home: "1:tmdb_trending_movies_week",
        libraries: { 1: KEEP_CURRENT, 2: "tmdb_trending_tv_week" },
      }),
    ).toEqual({
      home: { library_id: 1, template_id: "tmdb_trending_movies_week" },
      libraries: { 2: "tmdb_trending_tv_week" },
    });
    expect(featuredRequest({ home: KEEP_CURRENT, libraries: { 1: KEEP_CURRENT } })).toBeUndefined();
  });

  it("names a page's current hero: the first shown row marked as one", () => {
    const row = (title: string, featured: boolean, enabled: boolean) =>
      ({ id: title, title, featured, enabled }) as PageSectionConfig;
    expect(
      currentHero([
        row("Hidden hero", true, false),
        row("Continue Watching", false, true),
        row("Recently Added", true, true),
        row("Another hero", true, true),
      ]),
    ).toBe("Recently Added");
    expect(currentHero([row("Continue Watching", false, true)])).toBeNull();
  });
});
