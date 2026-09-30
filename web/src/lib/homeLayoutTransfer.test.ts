import { describe, expect, it, vi } from "vitest";

import type { components } from "@/api/v2/schema";
import { v2Problem } from "@/api/v2/problems.test-support";

import {
  buildHomeLayoutFile,
  importPage,
  legacyTraktSectionIds,
  mergeImportedPage,
  parseHomeLayoutFile,
  planHomeLayoutImport,
  fileReferences,
  HOME_LAYOUT_MAX_LENGTH,
  type HomeLayoutFile,
  type HomeLayoutImportTarget,
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
      ["custom_filter", { adminOnly: true }],
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
  it("writes each customized page without IDs or timestamps", () => {
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

  it("explains text that is not a usable export", () => {
    expect(parseHomeLayoutFile("{")).toEqual({ ok: false, error: "This isn't valid JSON." });
    expect(parseHomeLayoutFile('{"format":"other"}')).toEqual({
      ok: false,
      error: "This isn't a Silo home layout export.",
    });
    expect(parseHomeLayoutFile('{"format":"silo-home-layout","version":2}')).toEqual({
      ok: false,
      error: "This export comes from a newer version of Silo.",
    });
    expect(parseHomeLayoutFile('{"format":"silo-home-layout","version":1}')).toEqual({
      ok: false,
      error: "This home layout export is incomplete or damaged.",
    });
    expect(parseHomeLayoutFile(" ".repeat(HOME_LAYOUT_MAX_LENGTH + 1))).toEqual({
      ok: false,
      error: "This file is too large to be a home layout export.",
    });
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

  it("skips what this profile or server can't use", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              { user_section_type: "collection", user_config: { user_collection_id: "theirs" } },
              { section_type: "custom_filter", title: "Filtered" },
              { section_type: "genre", title: "Legacy genre" },
              { user_section_type: "trending_discover", user_config: { source: "trakt" } },
              { user_section_type: "hidden_gems", removed: true },
            ],
          },
          { scope: "library", library_id: 9, overrides: [{ section_id: "admin-9" }] },
        ],
      }),
      target({ allowAdminOnlyRecipes: false }),
      sequentialIds(),
    );

    expect(plan.pages).toEqual([]);
    expect(plan.skippedPages).toEqual(["Library 9"]);
    expect(plan.skippedSections).toEqual([
      { page: "Home", title: "collection", reason: "collection" },
      { page: "Home", title: "Filtered", reason: "custom_disabled" },
      { page: "Home", title: "Legacy genre", reason: "custom_disabled" },
      { page: "Home", title: "trending_discover", reason: "trakt" },
    ]);
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

  it("checks only the config the server uses for a section", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [
          {
            scope: "home",
            overrides: [
              {
                user_section_type: "hidden_gems",
                user_title: "Current",
                config: { filter_library_id: 9 },
                user_config: { library_ids: [1] },
              },
            ],
          },
        ],
      }),
      target(),
      sequentialIds(),
    );

    expect(plan.skippedSections).toEqual([]);
    expect(plan.pages[0]?.overrides).toHaveLength(1);
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

  it("skips a section type this server doesn't list", () => {
    const plan = planHomeLayoutImport(
      layoutFile({
        pages: [{ scope: "home", overrides: [{ section_type: "retired_recipe", title: "Old" }] }],
      }),
      otherServer,
      sequentialIds(),
    );

    expect(plan.skippedSections).toEqual([
      { page: "Home", title: "Old", reason: "unknown_recipe" },
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
  const noTrakt = new Set<string>();

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

    expect(mergeImportedPage(page, existing, true, noTrakt, sequentialIds())).toEqual([
      { id: "saved-1", section_id: "admin-1", position: 0, hidden: true },
      { id: "new-1", section_id: "admin-2", position: 1 },
      { id: "new-custom", user_section_type: "hidden_gems", position: 2 },
    ]);
  });

  it("leaves legacy Trakt admin sections as the profile saved them", () => {
    const trakt = new Set([
      "trakt-saved",
      "trakt-unsaved-hidden",
      "trakt-unsaved-shown",
      "trakt-omitted",
    ]);
    const page = {
      scope: "home" as const,
      label: "Home",
      overrides: [
        { section_id: "trakt-saved", position: 0 },
        { section_id: "trakt-unsaved-hidden", hidden: true },
        { section_id: "trakt-unsaved-shown", position: 3 },
      ],
    };
    const existing = [
      stored({ id: "saved-a", section_id: "trakt-saved", hidden: true }),
      stored({ id: "saved-b", section_id: "trakt-omitted", removed: true }),
    ];

    expect(mergeImportedPage(page, existing, true, trakt, sequentialIds())).toEqual([
      { id: "saved-a", section_id: "trakt-saved", hidden: true },
      { id: "new-1", section_id: "trakt-unsaved-hidden", hidden: true },
      { id: "saved-b", section_id: "trakt-omitted", removed: true },
    ]);
  });

  it("keeps saved profile-built Trakt sections on either server", () => {
    const page = {
      scope: "home" as const,
      label: "Home",
      overrides: [{ id: "new-1", user_section_type: "hidden_gems" }],
    };
    const existing = [
      stored({
        id: "trakt-row",
        user_section_type: "trending_discover",
        user_config: { source: "trakt" },
      }),
      stored({ id: "plain-row", user_section_type: "hidden_gems", user_config: {} }),
    ];
    const kept = {
      id: "trakt-row",
      user_section_type: "trending_discover",
      user_config: { source: "trakt" },
    };

    expect(mergeImportedPage(page, existing, true, noTrakt, sequentialIds())).toEqual([
      { id: "new-1", user_section_type: "hidden_gems" },
      kept,
    ]);
    expect(mergeImportedPage(page, existing, false, noTrakt, sequentialIds())).toEqual([
      kept,
      { id: "new-1", user_section_type: "hidden_gems" },
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

    expect(mergeImportedPage(page, existing, false, noTrakt, sequentialIds())).toEqual([
      { id: "keep", section_id: "admin-1", position: 3, hidden: true },
      { id: "new-1", user_section_type: "hidden_gems" },
    ]);
  });
});

describe("fileReferences", () => {
  it("reports which kinds of references the sections make, in either config", () => {
    const file = (override: HomeLayoutFile["pages"][number]["overrides"][number]) =>
      layoutFile({ pages: [{ scope: "home", overrides: [override] }] });

    expect(fileReferences(file({ user_config: { library_collection_id: "lc" } }))).toEqual({
      personalCollections: false,
      profiles: false,
    });
    expect(
      fileReferences(
        file({ section_id: "a", config: { user_collection_id: "mine", profile_id: "p" } }),
      ),
    ).toEqual({ personalCollections: true, profiles: true });
    expect(fileReferences(file({ user_config: { profile_id: "" } }))).toEqual({
      personalCollections: false,
      profiles: false,
    });
  });
});

describe("legacyTraktSectionIds", () => {
  it("finds Trakt-sourced admin sections in the page view and saved overrides", () => {
    const settings = [
      { id: "trending", is_custom: false, config: { source: "trakt", window: "week" } },
      { id: "list", is_custom: false, config: { source_provider: "trakt" } },
      { id: "tmdb", is_custom: false, config: { source: "tmdb" } },
      { id: "custom", is_custom: true, config: { source: "trakt" } },
    ];
    const existing = [
      stored({ id: "o1", section_id: "removed-trakt", config: { source: "trakt" } }),
    ];

    expect(legacyTraktSectionIds(settings, existing)).toEqual(
      new Set(["trending", "list", "removed-trakt"]),
    );
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
    return {
      listSaved: vi.fn(async () => [removedUnlisted]),
      listView: vi.fn(async () => [{ id: "shown", is_custom: false }]),
      save,
    };
  }

  it("saves the merged page once when the server accepts it", async () => {
    const api = fakeApi([undefined]);

    await expect(importPage(page, true, api, sequentialIds())).resolves.toEqual({
      keptSavedChanges: false,
    });
    expect(api.save).toHaveBeenCalledTimes(1);
    expect(api.save).toHaveBeenCalledWith([{ id: "new-custom", user_section_type: "hidden_gems" }]);
  });

  it("retries a refused same-server save with every saved admin-section change kept", async () => {
    const api = fakeApi([
      v2Problem(422, "validation_failed", "The request did not pass validation; see errors."),
      undefined,
    ]);

    await expect(importPage(page, true, api, sequentialIds())).resolves.toEqual({
      keptSavedChanges: true,
    });
    expect(api.save).toHaveBeenCalledTimes(2);
    expect(api.save).toHaveBeenLastCalledWith([
      { id: "new-custom", user_section_type: "hidden_gems" },
      { id: "saved-gone", section_id: "gone", removed: true },
    ]);
  });

  it("keeps a saved override whose config masks a legacy Trakt source", async () => {
    const masking = stored({ id: "saved-mask", section_id: "masked", config: { source: "tmdb" } });
    const api = {
      listSaved: vi.fn(async () => [masking]),
      listView: vi.fn(async () => [{ id: "masked", is_custom: false, config: { source: "tmdb" } }]),
      save: vi
        .fn()
        .mockRejectedValueOnce(v2Problem(422, "validation_failed", "invalid"))
        .mockResolvedValueOnce(undefined),
    };

    await expect(importPage(page, true, api, sequentialIds())).resolves.toEqual({
      keptSavedChanges: true,
    });
    expect(api.save).toHaveBeenNthCalledWith(1, [
      { id: "new-custom", user_section_type: "hidden_gems" },
    ]);
    expect(api.save).toHaveBeenLastCalledWith([
      { id: "new-custom", user_section_type: "hidden_gems" },
      { id: "saved-mask", section_id: "masked", config: { source: "tmdb" } },
    ]);
  });

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

  it("doesn't retry other failures", async () => {
    const refused = v2Problem(422, "validation_failed", "invalid");
    const unavailable = v2Problem(503, "dependency_unavailable", "down");

    const other = fakeApi([unavailable]);
    await expect(importPage(page, true, other, sequentialIds())).rejects.toBe(unavailable);
    expect(other.save).toHaveBeenCalledTimes(1);

    const otherServer = fakeApi([refused]);
    await expect(importPage(page, false, otherServer, sequentialIds())).rejects.toBe(refused);
    expect(otherServer.save).toHaveBeenCalledTimes(1);
    expect(otherServer.listView).not.toHaveBeenCalled();
  });
});
