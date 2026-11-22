import { describe, expect, it, vi } from "vitest";

import type { components } from "@/api/v2/schema";
import { v2Problem } from "@/api/v2/problems.test-support";
import { everyRecipe } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import { matchRecipePreset } from "@/lib/recipes";

import {
  buildHomeLayoutFile,
  importPage,
  mergeImportedPage,
  overrideToWrite,
  parseHomeLayoutFile,
  planHomeLayoutImport,
  type HomeLayoutFile,
  type HomeLayoutImportTarget,
  type SectionOverrideWrite,
} from "./homeLayoutTransfer";

type SectionOverrideRead = components["schemas"]["SectionOverride"];

function stored(overrides: Partial<SectionOverrideRead>): SectionOverrideRead {
  return {
    id: "",
    section_id: "",
    position: null,
    hidden: false,
    removed: false,
    section_type: "",
    title: "",
    featured: null,
    item_limit: null,
    is_user_added: false,
    user_section_type: "",
    user_title: "",
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
    ...overrides,
  };
}

function layoutFile(overrides: Partial<HomeLayoutFile>): HomeLayoutFile {
  return {
    format: "silo-home-layout",
    version: 1,
    exported_at: "2026-09-29T00:00:00.000Z",
    server_id: "server-a",
    libraries: [
      { id: 1, name: "Movies", type: "movie" },
      { id: 2, name: "TV Shows", type: "series" },
    ],
    pages: [],
    ...overrides,
  };
}

function target(overrides: Partial<HomeLayoutImportTarget> = {}): HomeLayoutImportTarget {
  return {
    serverId: "server-a",
    libraries: [
      { id: 1, name: "Movies", type: "movie" },
      { id: 2, name: "TV Shows", type: "series" },
    ],
    recipes: new Map([
      ["hidden_gems", { adminOnly: false }],
      ["collection", { adminOnly: false }],
      ["recently_added", { adminOnly: false }],
      ["profile_activity_feed", { adminOnly: false }],
      ["trending_discover", { adminOnly: false }],
      ["custom_filter", { adminOnly: false }],
      ["admin_curated_list", { adminOnly: true }],
    ]),
    allowAdminOnlyRecipes: true,
    personalCollectionIds: new Set(["mine"]),
    profileIds: new Set(["p-sibling"]),
    ...overrides,
  };
}

function sequentialIds() {
  let next = 0;
  return () => `new-${++next}`;
}

describe("buildHomeLayoutFile", () => {
  it("writes every page without IDs or timestamps", () => {
    const libraries = [{ id: 1, name: "Movies", type: "movie", sort_order: 0 }];
    const file = buildHomeLayoutFile({
      serverId: "server-a",
      exportedAt: new Date("2026-09-29T12:00:00.000Z"),
      libraries,
      hideWatchedItems: true,
      pages: [
        {
          scope: "home",
          overrides: [
            stored({ id: "o1", section_id: "admin-1", position: 0, hidden: true }),
            stored({
              id: "o2",
              position: 1,
              is_user_added: true,
              user_section_type: "hidden_gems",
              user_title: "Gems",
              user_config: {},
            }),
          ],
        },
        { scope: "library", libraryId: 1, overrides: [] },
      ],
    });

    expect(file).toEqual({
      format: "silo-home-layout",
      version: 1,
      exported_at: "2026-09-29T12:00:00.000Z",
      server_id: "server-a",
      libraries: [{ id: 1, name: "Movies", type: "movie" }],
      hide_watched_items: true,
      pages: [
        {
          scope: "home",
          overrides: [
            { section_id: "admin-1", position: 0, hidden: true },
            {
              position: 1,
              is_user_added: true,
              user_section_type: "hidden_gems",
              user_title: "Gems",
              user_config: {},
            },
          ],
        },
        { scope: "library", library_id: 1, overrides: [] },
      ],
    });
  });
});

describe("parseHomeLayoutFile", () => {
  it("round-trips an export", () => {
    const file = layoutFile({
      hide_watched_items: false,
      pages: [
        { scope: "home", overrides: [{ section_id: "admin-1", position: 2 }] },
        {
          scope: "library",
          library_id: 1,
          overrides: [{ section_type: "recently_added", config: { library_ids: [1] } }],
        },
      ],
    });

    expect(parseHomeLayoutFile(JSON.stringify(file))).toEqual({ ok: true, file });
  });

  it("keeps only the override members the write contract accepts", () => {
    const result = parseHomeLayoutFile(
      JSON.stringify({
        format: "silo-home-layout",
        version: 1,
        server_id: "server-a",
        libraries: [{ id: "1", name: "Movies", type: "movie" }],
        pages: [
          {
            scope: "home",
            overrides: [
              {
                section_id: "admin-1",
                position: -1,
                hidden: "yes",
                item_limit: 0,
                featured: false,
                profile_id: "someone",
                config: [],
              },
              "not an override",
            ],
          },
          { scope: "home", overrides: [{ section_id: "duplicate-page" }] },
          // Empty after dropping its damaged entries, so not a page to reset.
          { scope: "library", library_id: 2, overrides: [null, 7] },
          { scope: "library", overrides: [] },
          { scope: "admin", overrides: [] },
        ],
      }),
    );

    expect(result).toEqual({
      ok: true,
      file: expect.objectContaining({
        libraries: [],
        pages: [{ scope: "home", overrides: [{ section_id: "admin-1", featured: false }] }],
      }),
    });
  });
});

describe("planHomeLayoutImport on the same server", () => {
  it("keeps every reference and names each profile-built section anew", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        hide_watched_items: true,
        pages: [
          {
            scope: "home",
            overrides: [
              { section_id: "admin-1", position: 0, hidden: true },
              { section_id: "admin-2", removed: true },
              {
                position: 1,
                user_section_type: "collection",
                user_title: "Mine",
                user_config: { user_collection_id: "mine" },
              },
              {
                position: 2,
                section_type: "recently_added",
                title: "Movies",
                config: { filter_library_ids: [1] },
              },
            ],
          },
          { scope: "library", library_id: 2, overrides: [{ section_id: "admin-3", position: 0 }] },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan).toEqual({
      sameServer: true,
      hideWatchedItems: true,
      unappliedPages: [],
      skippedPages: [],
      skippedSections: [],
      skippedServerSectionChanges: 0,
      pages: [
        {
          scope: "home",
          label: "Home",
          overrides: [
            { section_id: "admin-1", position: 0, hidden: true },
            { section_id: "admin-2", removed: true },
            {
              id: "new-1",
              position: 1,
              user_section_type: "collection",
              user_title: "Mine",
              user_config: { user_collection_id: "mine" },
            },
            {
              id: "new-2",
              position: 2,
              section_type: "recently_added",
              title: "Movies",
              config: { filter_library_ids: [1] },
            },
          ],
        },
        {
          scope: "library",
          libraryId: 2,
          label: "TV Shows",
          overrides: [{ section_id: "admin-3", position: 0 }],
        },
      ],
    });
  });

  it("checks pinned profiles against the importing account and keeps library collections", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              {
                user_section_type: "collection",
                user_title: "Seen",
                user_config: { library_collection_id: "shared-lc" },
              },
              {
                user_section_type: "collection",
                user_title: "Unseen",
                user_config: { library_collection_id: "hidden-lc" },
              },
              {
                user_section_type: "profile_activity_feed",
                user_title: "Sibling",
                user_config: { profile_id: "p-sibling" },
              },
              {
                user_section_type: "profile_activity_feed",
                user_title: "Stranger",
                user_config: { profile_id: "p-other-account" },
              },
              {
                user_section_type: "profile_activity_feed",
                user_title: "Household",
                user_config: { profile_id: "" },
              },
            ],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.pages[0]?.overrides.map((override) => override.user_title)).toEqual([
      "Seen",
      "Unseen",
      "Sibling",
      "Household",
    ]);
    expect(plan.skippedSections).toEqual([{ page: "Home", title: "Stranger", reason: "profile" }]);
  });

  it("drops an admin section's config limited to libraries this profile can't open", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              {
                section_id: "admin-1",
                position: 3,
                hidden: true,
                config: { filter_library_id: 9 },
              },
              { section_id: "admin-2", position: 4, config: { filter_library_ids: [1, 9] } },
            ],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.pages[0]?.overrides).toEqual([
      { section_id: "admin-1", position: 3, hidden: true },
      { section_id: "admin-2", position: 4, config: { filter_library_ids: [1, 9] } },
    ]);
    expect(plan.skippedSections).toEqual([]);
  });

  it("drops admin-section configs with personal collections or profiles this profile can't use", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              { section_id: "union", config: { filter_library_id: 9, filter_library_ids: [1] } },
              { section_id: "lc-seen", config: { library_collection_id: "shared-lc" } },
              {
                section_id: "lc-unseen",
                position: 1,
                config: { library_collection_id: "hidden-lc" },
              },
              { section_id: "uc-unseen", position: 2, config: { user_collection_id: "theirs" } },
              { section_id: "feed-sibling", config: { profile_id: "p-sibling" } },
              {
                section_id: "feed-stranger",
                position: 3,
                config: { profile_id: "p-other-account" },
              },
            ],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.pages[0]?.overrides).toEqual([
      { section_id: "union", config: { filter_library_id: 9, filter_library_ids: [1] } },
      { section_id: "lc-seen", config: { library_collection_id: "shared-lc" } },
      { section_id: "lc-unseen", position: 1, config: { library_collection_id: "hidden-lc" } },
      { section_id: "uc-unseen", position: 2 },
      { section_id: "feed-sibling", config: { profile_id: "p-sibling" } },
      { section_id: "feed-stranger", position: 3 },
    ]);
  });

  it("trusts a saved section type the gallery doesn't list", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [{ user_section_type: "award_winners", user_title: "Awards" }],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.skippedSections).toEqual([]);
    expect(plan.pages[0]?.overrides).toEqual([
      { id: "new-1", user_section_type: "award_winners", user_title: "Awards" },
    ]);
  });

  it("skips sections limited to libraries this profile can't open", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              {
                user_section_type: "hidden_gems",
                user_title: "Some",
                user_config: { library_ids: [1, 9] },
              },
              {
                user_section_type: "hidden_gems",
                user_title: "None",
                user_config: { library_ids: [9] },
              },
              { section_type: "recently_added", title: "Single", config: { filter_library_id: 9 } },
              {
                section_type: "recently_added",
                title: "Union",
                config: { filter_library_id: 9, filter_library_ids: [1] },
              },
            ],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.pages[0]?.overrides).toEqual([
      {
        id: "new-1",
        user_section_type: "hidden_gems",
        user_title: "Some",
        user_config: { library_ids: [1, 9] },
      },
      {
        id: "new-2",
        section_type: "recently_added",
        title: "Union",
        config: { filter_library_id: 9, filter_library_ids: [1] },
      },
    ]);
    expect(plan.skippedSections).toEqual([
      { page: "Home", title: "None", reason: "library" },
      { page: "Home", title: "Single", reason: "library" },
    ]);
  });
});

describe("home layout round trip on the same profile", () => {
  const libraries = [
    { id: 1, name: "Movies", type: "movie" },
    { id: 2, name: "TV Shows", type: "series" },
  ];
  const customHome = [
    stored({ id: "o1", section_id: "admin-1", hidden: true }),
    stored({ id: "o2", section_id: "admin-2", position: 0, title: "Renamed" }),
    stored({
      id: "o3",
      position: 1,
      is_user_added: true,
      user_section_type: "hidden_gems",
      user_title: "Gems",
      user_config: {},
    }),
  ];
  const customMovies = [stored({ id: "o4", section_id: "admin-9", removed: true })];
  // Changes made after the export, which the import must undo.
  const changedSince = {
    home: [
      stored({ id: "c1", section_id: "admin-1", position: 4 }),
      stored({ id: "c2", is_user_added: true, user_section_type: "trending_discover" }),
    ],
    library: [stored({ id: "c3", section_id: "admin-8", hidden: true })],
  };

  // Exports the given saved pages, then imports the file over `changedSince`
  // and returns what each page saves.
  async function roundTrip(saved: { home: SectionOverrideRead[]; movies: SectionOverrideRead[] }) {
    const exported = buildHomeLayoutFile({
      serverId: "server-a",
      exportedAt: new Date("2026-09-29T12:00:00.000Z"),
      libraries,
      pages: [
        { scope: "home", overrides: saved.home },
        { scope: "library", libraryId: 1, overrides: saved.movies },
        { scope: "library", libraryId: 2, overrides: [] },
      ],
    });
    const parsed = parseHomeLayoutFile(JSON.stringify(exported));
    if (!parsed.ok) throw new Error(parsed.error);
    const plan = planHomeLayoutImport(parsed.file, target(), sequentialIds());
    const saves = new Map<string, unknown>();
    for (const page of plan.pages) {
      await importPage(
        page,
        plan.sameServer,
        {
          listSaved: async () => changedSince[page.scope],
          save: async (overrides) => saves.set(page.label, overrides),
        },
        sequentialIds(),
      );
    }
    return { plan, saves };
  }

  it("restores the server's rows from an export of the default layout", async () => {
    const { plan, saves } = await roundTrip({ home: [], movies: [] });

    expect(plan.pages.map((page) => page.label)).toEqual(["Home", "Movies", "TV Shows"]);
    expect(plan.skippedPages).toEqual([]);
    expect(plan.skippedSections).toEqual([]);
    expect(Object.fromEntries(saves)).toEqual({ Home: [], Movies: [], "TV Shows": [] });
  });

  it("restores a custom layout, including pages left on the server's rows", async () => {
    const { plan, saves } = await roundTrip({ home: customHome, movies: customMovies });

    expect(plan.pages.map((page) => page.label)).toEqual(["Home", "Movies", "TV Shows"]);
    // IDs are new or reused from the profile's saved rows; the rows match.
    const withoutIds = (overrides: unknown) =>
      (overrides as SectionOverrideWrite[]).map(({ id: _id, ...rest }) => rest);
    expect(withoutIds(saves.get("Home"))).toEqual(customHome.map(overrideToWrite));
    expect(withoutIds(saves.get("Movies"))).toEqual(customMovies.map(overrideToWrite));
    expect(saves.get("TV Shows")).toEqual([]);
  });
});

describe("planHomeLayoutImport on another server", () => {
  const otherServer = target({
    serverId: "server-b",
    libraries: [
      { id: 7, name: "movies ", type: "movie" },
      { id: 8, name: "TV Shows", type: "series" },
      { id: 9, name: "TV Shows", type: "series" },
    ],
  });

  it("maps libraries by name and type and drops the other server's own sections", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              { section_id: "admin-1", position: 0, hidden: true },
              {
                section_type: "recently_added",
                title: "New movies",
                config: {
                  filter_library_ids: [1],
                  filter_library_id: 1,
                  generated_library_id: null,
                },
              },
              { user_section_type: "hidden_gems", user_config: { library_ids: [1, 2] } },
              { user_section_type: "profile_activity_feed", user_config: { profile_id: "" } },
            ],
          },
          { scope: "library", library_id: 1, overrides: [{ user_section_type: "hidden_gems" }] },
          { scope: "library", library_id: 2, overrides: [{ user_section_type: "hidden_gems" }] },
        ],
      }),
      otherServer,
      sequentialIds(),
    );

    expect(plan.sameServer).toBe(false);
    expect(plan.skippedServerSectionChanges).toBe(1);
    expect(plan.skippedPages).toEqual(["TV Shows"]);
    expect(plan.skippedSections).toEqual([
      { page: "Home", title: "hidden_gems", reason: "library" },
    ]);
    expect(plan.pages).toEqual([
      {
        scope: "home",
        label: "Home",
        overrides: [
          {
            id: "new-1",
            section_type: "recently_added",
            title: "New movies",
            config: { filter_library_ids: [7], filter_library_id: 7, generated_library_id: null },
          },
          {
            id: "new-2",
            user_section_type: "profile_activity_feed",
            user_config: { profile_id: "" },
          },
        ],
      },
      {
        scope: "library",
        libraryId: 7,
        label: "movies ",
        overrides: [{ id: "new-3", user_section_type: "hidden_gems" }],
      },
    ]);
  });

  it("drops an unused config that can't be mapped", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              {
                user_section_type: "hidden_gems",
                config: { filter_library_id: 2 },
                user_config: { library_ids: [1] },
              },
            ],
          },
        ],
      }),
      otherServer,
      sequentialIds(),
    );

    expect(plan.pages[0]?.overrides).toEqual([
      { id: "new-1", user_section_type: "hidden_gems", user_config: { library_ids: [7] } },
    ]);
  });

  it("skips collections and profiles that belong to the other server", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              { user_section_type: "collection", user_config: { library_collection_id: "c1" } },
              { user_section_type: "collection", user_config: { user_collection_id: "mine" } },
              {
                user_section_type: "profile_activity_feed",
                user_title: "What Sam watched",
                user_config: { profile_id: "p1" },
              },
            ],
          },
        ],
      }),
      otherServer,
      sequentialIds(),
    );

    expect(plan.pages).toEqual([]);
    expect(plan.skippedSections.map((section) => section.reason)).toEqual([
      "collection",
      "collection",
      "profile",
    ]);
  });

  it("maps a library only when the name and type match one library on each side", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        libraries: [
          { id: 1, name: "Movies", type: "movie" },
          { id: 3, name: "movies", type: "movie" },
        ],
        pages: [
          { scope: "library", library_id: 1, overrides: [{ user_section_type: "hidden_gems" }] },
          { scope: "library", library_id: 3, overrides: [{ user_section_type: "hidden_gems" }] },
        ],
      }),
      target({ serverId: "server-b", libraries: [{ id: 7, name: "Movies", type: "movie" }] }),
      sequentialIds(),
    );

    expect(plan.pages).toEqual([]);
    expect(plan.skippedPages).toEqual(["Movies", "movies"]);
  });

  it("treats an export without a server identity as another server's", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        server_id: "",
        pages: [{ scope: "home", overrides: [{ section_id: "admin-1" }] }],
      }),
      target({ serverId: "" }),
      sequentialIds(),
    );

    expect(plan.sameServer).toBe(false);
    expect(plan.skippedServerSectionChanges).toBe(1);
  });
});

describe("mergeImportedPage", () => {
  it("replaces the whole page on the same server, reusing the saved override IDs the server resolves", () => {
    const page = {
      scope: "home" as const,
      label: "Home",
      overrides: [
        { section_id: "admin-1", position: 0, hidden: true },
        { section_id: "admin-2", position: 1 },
        { section_id: "admin-2", position: 5 },
        { id: "new-custom", user_section_type: "hidden_gems", position: 2 },
      ],
    };
    const existing = [
      stored({ id: "stale-1", section_id: "admin-1", position: 7 }),
      stored({ id: "saved-1", section_id: "admin-1", position: 4 }),
      stored({ id: "saved-3", section_id: "admin-3", removed: true }),
      stored({ id: "old-custom", user_section_type: "hidden_gems", is_user_added: true }),
    ];

    expect(mergeImportedPage(page, existing, true, sequentialIds())).toEqual([
      { id: "saved-1", section_id: "admin-1", position: 0, hidden: true },
      { id: "new-1", section_id: "admin-2", position: 1 },
      { id: "new-custom", user_section_type: "hidden_gems", position: 2 },
    ]);
  });

  it("keeps this server's section changes on another server", () => {
    const page = {
      scope: "home" as const,
      label: "Home",
      overrides: [{ id: "new-1", user_section_type: "hidden_gems" }],
    };
    const existing = [
      stored({ id: "keep", section_id: "admin-1", position: 3, hidden: true }),
      stored({ id: "old-custom", user_section_type: "hidden_gems", is_user_added: true }),
    ];

    expect(mergeImportedPage(page, existing, false, sequentialIds())).toEqual([
      { id: "keep", section_id: "admin-1", position: 3, hidden: true },
      { id: "new-1", user_section_type: "hidden_gems" },
    ]);
  });
});

describe("importPage", () => {
  const page = {
    scope: "home" as const,
    label: "Home",
    overrides: [{ id: "new-custom", user_section_type: "hidden_gems" }],
  };
  const removedUnlisted = stored({ id: "saved-gone", section_id: "gone", removed: true });

  function fakeApi(saveResults: unknown[]) {
    const save = vi.fn(async () => {
      const result = saveResults.shift();
      if (result instanceof Error) throw result;
      return result;
    });
    return { listSaved: vi.fn(async () => [removedUnlisted]), save };
  }

  it("refuses a page over the save endpoint's override limit", async () => {
    const big = {
      scope: "home" as const,
      label: "Home",
      overrides: Array.from({ length: 501 }, (_, index) => ({
        id: `row-${index}`,
        user_section_type: "hidden_gems",
      })),
    };
    const api = fakeApi([]);

    await expect(importPage(big, false, api, sequentialIds())).rejects.toThrow(
      "more than 500 saved section changes",
    );
    expect(api.save).not.toHaveBeenCalled();
  });

  it("passes a refused save through without retrying", async () => {
    const refused = v2Problem(422, "validation_failed", "invalid");
    const api = fakeApi([refused]);

    await expect(importPage(page, true, api, sequentialIds())).rejects.toBe(refused);
    expect(api.save).toHaveBeenCalledTimes(1);
  });

  it("synthesizes hidden overrides for checked rows still present on a fresh baseline and counts them toward the 500 limit", async () => {
    const api = {
      ...fakeApi([{}]),
      getBaseline: vi.fn(async () => ({
        section_tokens: ["t1", "t2"],
        visible_sections: [
          {
            id: "s-collections",
            section_type: "collection_posters",
            title: "Collections",
            token: "t2",
          },
        ],
      })),
    };

    // s-gone was deleted between preview and apply; only s-collections survives.
    await importPage(page, true, api, sequentialIds(), ["s-collections", "s-gone"]);
    expect(api.save).toHaveBeenCalledWith([
      { id: "new-custom", user_section_type: "hidden_gems" },
      { id: "new-1", section_id: "s-collections", hidden: true },
    ]);

    const atLimit = {
      scope: "home" as const,
      label: "Home",
      overrides: Array.from({ length: 500 }, (_, index) => ({
        id: `row-${index}`,
        user_section_type: "hidden_gems",
      })),
    };
    const apiAtLimit = fakeApi([]);
    await expect(
      importPage(atLimit, true, apiAtLimit, sequentialIds(), ["s-collections"]),
    ).rejects.toThrow("more than 500 saved section changes");
    expect(apiAtLimit.save).not.toHaveBeenCalled();
  });

  it("saves the page with the preview's hides when the fresh baseline read fails", async () => {
    const api = {
      ...fakeApi([{}]),
      getBaseline: vi.fn(async () => {
        throw new Error("unavailable");
      }),
    };

    await importPage(page, true, api, sequentialIds(), ["s-collections"]);
    expect(api.save).toHaveBeenCalledWith([
      { id: "new-custom", user_section_type: "hidden_gems" },
      { id: "new-1", section_id: "s-collections", hidden: true },
    ]);
  });

  it("skips the save on a hide-only page when every checked row is gone", async () => {
    const api = {
      ...fakeApi([]),
      getBaseline: vi.fn(async () => ({
        section_tokens: [],
        visible_sections: [],
      })),
    };

    await importPage(
      { scope: "home", label: "Home", overrides: [], replaceExisting: false },
      true,
      api,
      sequentialIds(),
      ["s-gone"],
    );
    expect(api.save).not.toHaveBeenCalled();
  });

  it("preserves existing destination overrides when hiding rows on a page whose file overrides were all skipped", () => {
    const existing = [
      stored({ id: "saved-1", section_id: "admin-1", position: 0 }),
      stored({
        id: "saved-col",
        section_id: "s-collections",
        title: "My Collections",
        position: 2,
      }),
      stored({ id: "saved-gone", section_id: "s-removed", removed: true }),
      stored({ id: "saved-custom", user_section_type: "hidden_gems", is_user_added: true }),
    ];

    expect(
      mergeImportedPage(
        { scope: "home", label: "Home", overrides: [], replaceExisting: false },
        existing,
        true,
        sequentialIds(),
        ["s-collections", "s-removed", "s-new"],
      ),
    ).toEqual([
      { id: "saved-1", section_id: "admin-1", position: 0 },
      {
        id: "saved-col",
        section_id: "s-collections",
        title: "My Collections",
        position: 2,
        hidden: true,
      },
      { id: "saved-gone", section_id: "s-removed", removed: true },
      { id: "saved-custom", user_section_type: "hidden_gems", is_user_added: true },
      { id: "new-1", section_id: "s-new", hidden: true },
    ]);
  });

  it("round-trips server_section_tokens and degrades malformed or truncated lists to []", () => {
    const exported = buildHomeLayoutFile({
      serverId: "server-a",
      exportedAt: new Date("2026-11-20T12:00:00.000Z"),
      libraries: [],
      pages: [{ scope: "home", overrides: [], serverSectionTokens: ["tok-1", "tok-2"] }],
    });
    const parsed = parseHomeLayoutFile(JSON.stringify(exported));
    expect(parsed.ok && parsed.file.pages[0]?.server_section_tokens).toEqual(["tok-1", "tok-2"]);

    const withTokens = (count: number) => ({
      ...exported,
      pages: [
        {
          scope: "home",
          overrides: [],
          server_section_tokens: Array.from({ length: count }, (_, i) => `tok-${i}`),
        },
      ],
    });
    // A page can have more server rows than one save takes overrides.
    const parsedLarge = parseHomeLayoutFile(JSON.stringify(withTokens(501)));
    expect(parsedLarge.ok && parsedLarge.file.pages[0]?.server_section_tokens).toHaveLength(501);
    const parsedTruncated = parseHomeLayoutFile(JSON.stringify(withTokens(10_001)));
    expect(parsedTruncated.ok && parsedTruncated.file.pages[0]?.server_section_tokens).toEqual([]);
  });
});

// Characterization for the Home rows redesign (#1705): one profile-built row
// of each kind, stored as the server echoes what the editor saves, survives
// export and import unchanged. The redesign must not change these.
describe("home layout round trip for every row kind", () => {
  const presetRows = everyRecipe().flatMap((def) =>
    def.presets.slice(0, 1).map((preset) => ({
      section_type: def.type,
      title: preset.display_name,
      config: { ...preset.default_params },
    })),
  );
  const ownRows = [
    ...presetRows,
    { section_type: "custom_filter", title: "90s Horror", config: { match: "any", groups: [] } },
    { section_type: "genre", title: "Horror", config: { filter_type: "movie", groups: [] } },
    { section_type: "collection", title: "Mine", config: { user_collection_id: "mine" } },
    { section_type: "collection", title: "Ghibli", config: { library_collection_id: "lib-1" } },
  ];
  const exported = buildHomeLayoutFile({
    serverId: "server-a",
    exportedAt: new Date("2026-09-29T12:00:00.000Z"),
    libraries: [{ id: 1, name: "Movies", type: "movie" }],
    pages: [
      {
        scope: "home",
        overrides: [
          stored({
            id: "o-admin",
            section_id: "admin-1",
            position: 0,
            hidden: true,
            title: "Renamed",
          }),
          ...ownRows.map((row, index) =>
            stored({
              ...row,
              id: `o-${index}`,
              position: index + 1,
              is_user_added: true,
              featured: index === 0,
              item_limit: 20,
            }),
          ),
        ],
      },
    ],
  });

  function importAs(allowAdminOnlyRecipes: boolean) {
    const parsed = parseHomeLayoutFile(JSON.stringify(exported));
    if (!parsed.ok) throw new Error(parsed.error);
    const recipes = new Map(
      everyRecipe().map((def) => [def.type, { adminOnly: def.admin_only }] as const),
    );
    return planHomeLayoutImport(
      parsed.file,
      target({ recipes, allowAdminOnlyRecipes }),
      sequentialIds(),
    );
  }

  it("imports every kind back unchanged into an admin account", () => {
    const plan = importAs(true);
    const written = exported.pages[0]!.overrides;

    expect(plan.skippedSections).toEqual([]);
    expect(plan.pages[0]!.overrides).toEqual(
      written.map((override, index) =>
        override.section_id ? override : { ...override, id: `new-${index}` },
      ),
    );
    // Rows saved from a preset still name that preset after the trip.
    for (const override of plan.pages[0]!.overrides.slice(1, presetRows.length + 1)) {
      const def = everyRecipe().find((d) => d.type === override.section_type)!;
      expect(matchRecipePreset(def, override.config)?.display_name).toBe(override.title);
    }
  });

  it("imports rule rows but not Editor's picks into an account that is not an admin", () => {
    const plan = importAs(false);

    expect(plan.skippedSections).toEqual([
      { page: "Home", title: "Editor's Picks", reason: "custom_disabled" },
    ]);
    expect(plan.pages[0]!.overrides).toHaveLength(exported.pages[0]!.overrides.length - 1);
    expect(plan.pages[0]!.overrides.map((override) => override.title)).toEqual(
      expect.arrayContaining(["90s Horror", "Horror"]),
    );
  });
});
