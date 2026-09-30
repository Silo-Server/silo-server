import { describe, expect, it } from "vitest";

import type { components } from "@/api/v2/schema";

import {
  buildHomeLayoutFile,
  mergeImportedPage,
  parseHomeLayoutFile,
  planHomeLayoutImport,
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
    expect(parseHomeLayoutFile(" ".repeat(1024 * 1024 + 1))).toEqual({
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
              { section_type: "retired_recipe", title: "Old" },
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
      { page: "Home", title: "Old", reason: "unknown_recipe" },
      { page: "Home", title: "trending_discover", reason: "trakt" },
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
  const page = {
    scope: "home" as const,
    label: "Home",
    overrides: [{ id: "new-1", user_section_type: "hidden_gems" }],
  };
  const existing = [
    stored({ id: "keep", section_id: "admin-1", position: 3, hidden: true }),
    stored({ id: "old-custom", user_section_type: "hidden_gems", is_user_added: true }),
  ];

  it("replaces the whole page on the same server", () => {
    expect(mergeImportedPage(page, existing, true)).toEqual(page.overrides);
  });

  it("keeps this server's section changes on another server", () => {
    expect(mergeImportedPage(page, existing, false)).toEqual([
      { id: "keep", section_id: "admin-1", position: 3, hidden: true },
      { id: "new-1", user_section_type: "hidden_gems" },
    ]);
  });
});
