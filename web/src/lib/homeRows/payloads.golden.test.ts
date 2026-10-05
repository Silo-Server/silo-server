// Characterization tests: they record what each Home row kind saves today,
// so the Home rows redesign can show it changes no stored bytes by accident.
// A golden entry changes only with a one-line reason in that pull request.
// Rewrite the golden with `pnpm exec vitest run -u src/lib/homeRows/payloads.golden.test.ts`.
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  queryDefinitionFromSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
  type SettingsSectionEntry,
} from "@/api/types";
import {
  bulkCreateAdminSections,
  createAdminSection,
  updateAdminSection,
} from "@/api/adminSections";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { buildSectionOverrides } from "@/pages/settings/HomeScreenSettings";

import {
  buildAdminSectionPayload,
  buildBulkCopyPayload,
  buildGalleryAddPayload,
  buildGalleryBulkCreateRequest,
  buildGalleryCreateRequest,
  buildProfileGallerySection,
  buildProfileSectionSaveEntry,
  buildRowCreateRequest,
  collectionIdOf,
  type BuildAdminSectionPayloadInput,
  type BuildProfileSectionSaveEntryInput,
} from "./payloads";
import {
  everyPreset,
  everyRecipe,
  recipeCatalogFixture,
} from "./recipeCatalogFixture.test-support";
import { canCopyToLibraries } from "./bulkCopy";
import { pickerGroups } from "./catalog";
import { draftForPreset } from "./rowDraft";

const uuid = vi.hoisted(() => ({ next: 0 }));
vi.mock("@/lib/uuid", () => ({ randomUUID: () => `uuid-${++uuid.next}` }));

// Captures the request the v2 client would send, then fails the call: these
// tests are about the bytes on the wire, not the response.
const wire = vi.hoisted(() => ({ last: undefined as unknown }));
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(async (route: string, init?: { body?: unknown }) => {
    wire.last = { route, body: init?.body };
    throw new Error("request captured");
  }),
}));

beforeEach(() => {
  uuid.next = 0;
  wire.last = undefined;
});

/** The JSON a send puts on the wire: undefined members are dropped, as fetch drops them. */
async function sent(send: () => Promise<unknown>): Promise<{ route: string; body: unknown }> {
  wire.last = undefined;
  await expect(send()).rejects.toThrow("request captured");
  return JSON.parse(JSON.stringify(wire.last));
}

const LIBRARY_ID = 7;
const PAGES = [
  { name: "home", scope: "home", libraryId: null },
  { name: "library", scope: "library", libraryId: LIBRARY_ID },
] as const;

function adminRow(overrides: Partial<PageSectionConfig>): PageSectionConfig {
  return {
    id: "row-1",
    scope: "home",
    library_id: null,
    position: 4,
    section_type: "recently_added",
    title: "Recently Added",
    featured: false,
    item_limit: 20,
    config: {},
    enabled: true,
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
    ...overrides,
  };
}

function profileRow(overrides: Partial<SettingsSectionEntry>): SettingsSectionEntry {
  return {
    id: "row-1",
    section_type: "recently_added",
    title: "Recently Added",
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: true,
    customized: true,
    position: 2,
    config: {},
    ...overrides,
  };
}

// The drawer's open effect: the draft starts from the row as stored.
function draftOf(row: {
  section_type: string;
  title: string;
  item_limit: number;
  featured: boolean;
  config?: Record<string, unknown>;
}) {
  return {
    sectionType: row.section_type,
    title: row.title,
    itemLimit: row.item_limit,
    featured: row.featured,
    queryDefinition: queryDefinitionFromSectionConfig(row.config),
    selectedCollectionId: collectionIdOf(row.config),
    recipeParams: { ...(row.config ?? {}) },
  };
}

function editAdminRow(
  row: PageSectionConfig,
  changes: Partial<BuildAdminSectionPayloadInput> = {},
): ReturnType<typeof buildAdminSectionPayload> {
  return buildAdminSectionPayload({
    section: row,
    scope: row.scope,
    currentLibraryId: row.library_id,
    enabled: row.enabled,
    ...draftOf(row),
    selectedCollectionId: collectionIdOf(row.config, "admin"),
    ...changes,
  });
}

function editProfileRow(
  row: SettingsSectionEntry,
  changes: Partial<BuildProfileSectionSaveEntryInput> = {},
): SettingsSectionEntry {
  return buildProfileSectionSaveEntry({ section: row, ...draftOf(row), ...changes });
}

/** The fields the gallery drawer starts from for a preset, before the user changes anything. */
function galleryFields(sectionType: string, title: string, config: Record<string, unknown>) {
  return {
    sectionType,
    title,
    itemLimit: 20,
    featured: false,
    enabled: true,
    config: { ...config },
  };
}

function drawerCreate(
  page: (typeof PAGES)[number],
  sectionType: string,
  title: string,
  recipeParams: Record<string, unknown>,
  extra: Partial<BuildAdminSectionPayloadInput> = {},
) {
  return buildAdminSectionPayload({
    section: null,
    scope: page.scope,
    currentLibraryId: page.libraryId,
    sectionType,
    title,
    itemLimit: 20,
    featured: false,
    enabled: true,
    queryDefinition: queryDefinitionFromSectionConfig(),
    selectedCollectionId: "",
    recipeParams: { ...recipeParams },
    ...extra,
  });
}

function profileDrawerCreate(
  sectionType: string,
  title: string,
  recipeParams: Record<string, unknown>,
  extra: Partial<BuildProfileSectionSaveEntryInput> = {},
) {
  return buildProfileSectionSaveEntry({
    section: null,
    sectionType,
    title,
    itemLimit: 20,
    featured: false,
    queryDefinition: queryDefinitionFromSectionConfig(),
    selectedCollectionId: "",
    recipeParams: { ...recipeParams },
    ...extra,
  });
}

const MULTI_GROUP_RULES: QueryDefinition = {
  library_ids: [1],
  media_scope: "movie",
  match: "any",
  groups: [
    {
      match: "all",
      rules: [
        { field: "genre", op: "contains", value: "Horror" },
        { field: "year", op: "between", value: [1990, 1999] },
      ],
    },
    { match: "any", rules: [{ field: "rating_imdb", op: "gte", value: 7.5 }] },
  ],
  sort: { field: "rating_imdb", order: "desc" },
};

const COLLECTION_OPTIONS: CollectionOption[] = [
  { id: "user-1", title: "Mine", source: "user", group: "My Collections" },
  { id: "lib-1", title: "Studio Ghibli", source: "library", group: "Movies" },
];

// Rows whose stored config the redesign's variant rules must read correctly.
const VARIANT_EDGE_ROWS = {
  era_1990s: {
    section_type: "editorial_spotlight",
    config: { subject_type: "era", subject: "1990s" },
  },
  director_monthly: {
    section_type: "editorial_spotlight",
    config: { subject_type: "director", auto_rotate: true, rotation_cadence: "monthly" },
  },
  legacy_family_theme: { section_type: "seasonal_themed", config: { theme: "family_movie_night" } },
  legacy_christmas_theme: { section_type: "seasonal_themed", config: { theme: "christmas" } },
  legacy_trakt_trending: {
    section_type: "trending_discover",
    config: { source: "trakt", window: "week" },
  },
} as const;

function galleryCreate(
  page: (typeof PAGES)[number],
  sectionType: string,
  title: string,
  config: Record<string, unknown>,
) {
  return buildGalleryCreateRequest(
    buildGalleryAddPayload(galleryFields(sectionType, title, config)),
    page.scope,
    page.libraryId,
  );
}

/** A profile row as the page saves it, without its generated ID. */
function savedOverride(entry: SettingsSectionEntry) {
  const [override] = buildSectionOverrides([entry]);
  const { id: _id, ...rest } = override!;
  return rest;
}

async function buildGolden() {
  // The gallery's bytes; the drawer sends the same ones (asserted below).
  const adminCreate: Record<string, unknown> = {};
  for (const { def, preset } of everyPreset()) {
    for (const page of PAGES) {
      adminCreate[`${def.type}/${preset.key}/${page.name}`] = (
        await sent(() =>
          createAdminSection(
            galleryCreate(page, def.type, preset.display_name, preset.default_params),
          ),
        )
      ).body;
    }
  }
  for (const page of PAGES) {
    adminCreate[`collection/picked/${page.name}`] = (
      await sent(() =>
        createAdminSection(
          galleryCreate(page, "collection", "Studio Ghibli", { library_collection_id: "lib-1" }),
        ),
      )
    ).body;
    // custom_filter has no preset, so only the drawer creates it.
    adminCreate[`custom_filter/rules/${page.name}`] = (
      await sent(() =>
        createAdminSection(
          drawerCreate(
            page,
            "custom_filter",
            "90s Horror",
            {},
            { queryDefinition: MULTI_GROUP_RULES },
          ),
        ),
      )
    ).body;
  }

  // The gallery's saved override; the drawer saves the same one (asserted below).
  const profileCreate: Record<string, unknown> = {};
  for (const { def, preset } of everyPreset()) {
    profileCreate[`${def.type}/${preset.key}`] = savedOverride(
      buildProfileGallerySection(
        buildGalleryAddPayload(galleryFields(def.type, preset.display_name, preset.default_params)),
        3,
      ),
    );
  }
  for (const [key, extra] of [
    ["collection/user", { selectedCollectionId: "user-1", collections: COLLECTION_OPTIONS }],
    ["collection/library", { selectedCollectionId: "lib-1", collections: COLLECTION_OPTIONS }],
    ["custom_filter/rules", { queryDefinition: MULTI_GROUP_RULES }],
  ] as const) {
    profileCreate[key] = savedOverride(profileDrawerCreate(key.split("/")[0]!, "Row", {}, extra));
  }

  const variantEdges: Record<string, unknown> = {};
  for (const [key, row] of Object.entries(VARIANT_EDGE_ROWS)) {
    variantEdges[key] = {
      adminTitleEdit: (
        await sent(() =>
          updateAdminSection({
            ...editAdminRow(adminRow({ ...row, config: { ...row.config } }), { title: "Renamed" }),
            id: "row-1",
            etag: 'W/"1"',
          }),
        )
      ).body,
      profileTitleEdit: savedOverride(
        editProfileRow(profileRow({ ...row, config: { ...row.config } }), { title: "Renamed" }),
      ),
    };
  }

  return { adminCreate, profileCreate, variantEdges };
}

describe("Home row payload golden", () => {
  it("matches what every row kind saves today", async () => {
    const golden = await buildGolden();
    await expect(`${JSON.stringify(golden, null, 2)}\n`).toMatchFileSnapshot(
      "./payloads.golden.json",
    );
  });

  it("covers every preset in the recipe catalog", () => {
    // A new preset without a golden entry would slip past the redesign's checks.
    expect(everyPreset().length).toBeGreaterThan(30);
    expect(
      everyRecipe()
        .filter((def) => def.presets.length === 0)
        .map((def) => def.type),
    ).toEqual(["custom_filter"]);
  });
});

describe("admin create: gallery vs drawer (architecture 7.4)", () => {
  it("sends the same bytes from the gallery and the drawer for every preset", async () => {
    for (const { def, preset } of everyPreset()) {
      for (const page of PAGES) {
        const gallery = await sent(() =>
          createAdminSection(
            galleryCreate(page, def.type, preset.display_name, preset.default_params),
          ),
        );
        const drawer = await sent(() =>
          createAdminSection(
            drawerCreate(page, def.type, preset.display_name, preset.default_params),
          ),
        );
        expect(drawer, `${def.type}/${preset.key}/${page.name}`).toEqual(gallery);
      }
    }
  });

  it("sends the page's library only on a library page", async () => {
    const fields = galleryFields("recently_added", "Recently Added", {});
    const home = await sent(() =>
      createAdminSection(
        buildGalleryCreateRequest(buildGalleryAddPayload(fields), "home", LIBRARY_ID),
      ),
    );
    const library = await sent(() =>
      createAdminSection(
        buildGalleryCreateRequest(buildGalleryAddPayload(fields), "library", LIBRARY_ID),
      ),
    );
    expect(home).toEqual({
      route: "POST /api/v2/admin/sections",
      body: {
        scope: "home",
        section_type: "recently_added",
        title: "Recently Added",
        featured: false,
        item_limit: 20,
        config: {},
        enabled: true,
      },
    });
    expect(library.body).toMatchObject({ scope: "library", library_id: String(LIBRARY_ID) });
  });
});

describe("admin bulk create", () => {
  it("sends the single library-page create payload apart from the libraries", async () => {
    for (const { def, preset } of everyPreset()) {
      const fields = galleryFields(def.type, preset.display_name, preset.default_params);
      const single = await sent(() =>
        createAdminSection(
          buildGalleryCreateRequest(buildGalleryAddPayload(fields), "library", LIBRARY_ID),
        ),
      );
      const bulk = await sent(() =>
        bulkCreateAdminSections(
          buildGalleryBulkCreateRequest(buildGalleryAddPayload(fields), [3, 5]),
        ),
      );
      const { library_id: singleLibrary, ...singleRest } = single.body as Record<string, unknown>;
      const { library_ids: bulkLibraries, ...bulkRest } = bulk.body as Record<string, unknown>;
      expect(bulk.route).toBe("POST /api/v2/admin/sections/bulk");
      expect(singleLibrary).toBe(String(LIBRARY_ID));
      expect(bulkLibraries).toEqual(["3", "5"]);
      expect(bulkRest, `${def.type}/${preset.key}`).toEqual(singleRest);
    }
  });

  it("sends an Add row draft's library copies with the single create body apart from the libraries", async () => {
    for (const { def, preset } of everyPreset()) {
      const draft = draftForPreset(def, preset);
      if (!canCopyToLibraries(draft)) continue;
      const page = { kind: "library", libraryId: LIBRARY_ID } as const;
      const single = await sent(() =>
        createAdminSection(buildRowCreateRequest(draft, draft.title, page, 3)),
      );
      const bulk = await sent(() =>
        bulkCreateAdminSections(buildBulkCopyPayload({ ...draft, enabled: true }, [LIBRARY_ID, 8])),
      );
      const {
        library_id: _library,
        position: _position,
        ...singleRest
      } = single.body as Record<string, unknown>;
      const { library_ids: bulkLibraries, ...bulkRest } = bulk.body as Record<string, unknown>;
      expect(bulk.route).toBe("POST /api/v2/admin/sections/bulk");
      expect(bulkLibraries).toEqual([String(LIBRARY_ID), "8"]);
      expect(bulkRest, `${def.type}/${preset.key}`).toEqual(singleRest);
    }
  });

  it("copies an existing row as it is, never as a hero banner", async () => {
    const copy = await sent(() =>
      bulkCreateAdminSections(
        buildBulkCopyPayload(
          {
            sectionType: "trending_on_server",
            title: "Trending This Week",
            itemLimit: 30,
            config: { window: "7d" },
            enabled: false,
          },
          [8, 9],
        ),
      ),
    );
    expect(copy).toEqual({
      route: "POST /api/v2/admin/sections/bulk",
      body: {
        scope: "library",
        library_ids: ["8", "9"],
        section_type: "trending_on_server",
        title: "Trending This Week",
        item_limit: 30,
        featured: false,
        enabled: false,
        config: { window: "7d" },
      },
    });
  });
});

describe("admin edit payloads", () => {
  async function patched(row: PageSectionConfig, changes: Partial<BuildAdminSectionPayloadInput>) {
    return sent(() =>
      updateAdminSection({ ...editAdminRow(row, changes), id: row.id, etag: 'W/"1"' }),
    );
  }

  it("keeps a collection row's metadata on a title-only edit", async () => {
    const config = {
      library_collection_id: "lib-1",
      generated_source: "collection_auto",
      generated_library_id: 2,
    };
    const request = await patched(adminRow({ section_type: "collection", config }), {
      title: "Ghibli",
    });
    expect(request).toEqual({
      route: "PATCH /api/v2/admin/sections/{id}",
      body: {
        title: "Ghibli",
        section_type: "collection",
        featured: false,
        item_limit: 20,
        config,
        enabled: true,
      },
    });
  });

  it("round-trips multi-group custom_filter rules unchanged", async () => {
    const config = {
      library_ids: [1],
      media_scope: "movie",
      match: "any",
      groups: MULTI_GROUP_RULES.groups,
      sort: { field: "rating_imdb", order: "desc" },
      limit: 40,
    };
    const request = await patched(adminRow({ section_type: "custom_filter", config }), {
      title: "Horror",
    });
    expect((request.body as { config: unknown }).config).toEqual(config);
  });

  // A no-op save rewrites a legacy genre row's stored config: filter_type
  // becomes media_scope, library_ids is added and sort/order fold into one
  // sort object. The redesign starts from this, not from "saves as-is".
  it("rewrites a legacy genre row into today's filter shape on a no-op save", async () => {
    const groups = [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Horror" }] }];
    const config = { filter_type: "movie", match: "all", groups, sort: "added_at", order: "desc" };
    const request = await patched(adminRow({ section_type: "genre", config }), {});
    expect(request.body).toEqual({
      title: "Recently Added",
      section_type: "genre",
      featured: false,
      item_limit: 20,
      config: {
        match: "all",
        groups,
        sort: { field: "added_at", order: "desc" },
        library_ids: [],
        media_scope: "movie",
      },
      enabled: true,
    });
  });

  // The server refuses config changes to either Trakt-backed form.
  it.each([
    [
      "collection",
      { library_collection_id: "lib-trakt", source_provider: "trakt", source_preset: "trending" },
    ],
    ["trending_discover", { source: "trakt", window: "week" }],
  ] as const)(
    "sends a legacy Trakt %s row's config unchanged on a title-only edit",
    async (sectionType, config) => {
      const request = await patched(
        adminRow({ section_type: sectionType, config: { ...config } }),
        {
          title: "Trending",
        },
      );
      expect((request.body as { config: unknown }).config).toEqual(config);
    },
  );

  it("keeps an admin_curated_list row's items", async () => {
    const config = { item_ids: ["movie:a", "movie:b"] };
    const request = await patched(adminRow({ section_type: "admin_curated_list", config }), {
      featured: true,
    });
    expect(request.body).toEqual({
      title: "Recently Added",
      section_type: "admin_curated_list",
      featured: true,
      item_limit: 20,
      config,
      enabled: true,
    });
  });

  it("keeps a hidden award_winners row's award_type", async () => {
    const config = { award_type: "oscar" };
    const request = await patched(adminRow({ section_type: "award_winners", config }), {
      title: "Oscar Winners",
    });
    expect((request.body as { config: unknown }).config).toEqual(config);
  });
});

describe("profile entries", () => {
  it("keys a picked collection by its option's source", () => {
    const user = profileDrawerCreate(
      "collection",
      "Mine",
      {},
      {
        selectedCollectionId: "user-1",
        collections: COLLECTION_OPTIONS,
      },
    );
    const library = profileDrawerCreate(
      "collection",
      "Ghibli",
      {},
      {
        selectedCollectionId: "lib-1",
        collections: COLLECTION_OPTIONS,
      },
    );
    expect(user.config).toEqual({ user_collection_id: "user-1" });
    expect(library.config).toEqual({ library_collection_id: "lib-1" });
  });

  it("saves the same override from the gallery and the drawer for every preset", () => {
    for (const { def, preset } of everyPreset()) {
      const gallery = buildProfileGallerySection(
        buildGalleryAddPayload(galleryFields(def.type, preset.display_name, preset.default_params)),
        3,
      );
      const drawer = profileDrawerCreate(def.type, preset.display_name, preset.default_params);
      expect(savedOverride(drawer), `${def.type}/${preset.key}`).toEqual(savedOverride(gallery));
    }
  });

  // The one difference (architecture 7.4) stays in memory: overrides carry no `customized`.
  it("marks gallery adds customized and drawer adds not", () => {
    const fields = galleryFields("hidden_gems", "Hidden Gems", { max_play_count: 2 });
    const gallery = buildProfileGallerySection(buildGalleryAddPayload(fields), 0);
    const drawer = profileDrawerCreate("hidden_gems", "Hidden Gems", { max_play_count: 2 });
    expect(gallery.customized).toBe(true);
    expect(drawer.customized).toBe(false);
    expect(buildSectionOverrides([gallery])[0]).not.toHaveProperty("customized");
  });

  // An unchanged collection selection keeps the row's stored config, whatever
  // the picker list holds; only a picked collection writes a new id key.
  describe("collection id key", () => {
    it("keeps a personal collection row's key when its id is not in the options", () => {
      const row = profileRow({
        section_type: "collection",
        config: { user_collection_id: "gone" },
      });
      const saved = editProfileRow(row, { title: "Renamed", collections: COLLECTION_OPTIONS });
      expect(saved.config).toEqual({ user_collection_id: "gone" });
    });

    it("keeps a collection row's other keys while the options are still loading", () => {
      const config = { library_collection_id: "lib-1", generated_source: "collection_auto" };
      const row = profileRow({ section_type: "collection", config });
      const saved = editProfileRow(row, { title: "Renamed", collections: [] });
      expect(saved.config).toEqual(config);
    });

    it("keeps a collection row's other keys on a hero toggle", () => {
      const config = { user_collection_id: "user-1", sort_by: "release_date" };
      const row = profileRow({ section_type: "collection", config });
      const saved = editProfileRow(row, { featured: true, collections: COLLECTION_OPTIONS });
      expect(saved.config).toEqual(config);
    });

    it("writes the picked collection's key and drops the old one", () => {
      const row = profileRow({
        section_type: "collection",
        config: { user_collection_id: "user-1", sort_by: "release_date" },
      });
      const toLibrary = editProfileRow(row, {
        selectedCollectionId: "lib-1",
        collections: COLLECTION_OPTIONS,
      });
      expect(toLibrary.config).toEqual({ library_collection_id: "lib-1", sort_by: "release_date" });

      const libraryRow = profileRow({
        section_type: "collection",
        config: { library_collection_id: "lib-1" },
      });
      const toUser = editProfileRow(libraryRow, {
        selectedCollectionId: "user-1",
        collections: COLLECTION_OPTIONS,
      });
      expect(toUser.config).toEqual({ user_collection_id: "user-1" });
    });

    it("starts a row that becomes a collection row from the picked key alone", () => {
      const row = profileRow({ section_type: "recently_added", config: { filter_library_id: 2 } });
      const saved = editProfileRow(row, {
        sectionType: "collection",
        selectedCollectionId: "user-1",
        collections: COLLECTION_OPTIONS,
      });
      expect(saved.config).toEqual({ user_collection_id: "user-1" });
    });

    it("keeps an admin row's stored config when the selection is unchanged", () => {
      const config = { library_collection_id: "lib-gone", sort_by: "title" };
      const row = adminRow({ section_type: "collection", config });
      expect(editAdminRow(row, { title: "Renamed" }).config).toEqual(config);
      expect(editAdminRow(row, { featured: true, collections: [] }).config).toEqual(config);
    });

    // The admin endpoint rejects a collection row without library_collection_id,
    // so a legacy user_collection_id is no selection there.
    it("never sends a legacy personal collection key from an admin row", () => {
      const row = adminRow({
        section_type: "collection",
        config: { user_collection_id: "legacy", sort_by: "title" },
      });
      expect(collectionIdOf(row.config, "admin")).toBe("");
      expect(editAdminRow(row, { title: "Renamed" }).config).not.toHaveProperty(
        "user_collection_id",
      );
      expect(editAdminRow(row, { selectedCollectionId: "legacy" }).config).toEqual({
        library_collection_id: "legacy",
        sort_by: "title",
      });
    });

    it("writes a library key when an admin picks another collection", () => {
      const row = adminRow({
        section_type: "collection",
        config: { user_collection_id: "legacy", sort_by: "title" },
      });
      expect(editAdminRow(row, { selectedCollectionId: "lib-1" }).config).toEqual({
        library_collection_id: "lib-1",
        sort_by: "title",
      });
    });
  });
});

// Both pickers share one list: the server lets every profile add rule rows
// and refuses only new rows of an admin-only kind from a profile.
describe("rule rows for every profile", () => {
  it("offers rule rows and no admin-only kind", () => {
    const offered = pickerGroups(recipeCatalogFixture).flatMap((group) =>
      group.cards.map((card) => card.type),
    );
    expect(offered).toContain("custom_filter");
    const adminOnly = everyRecipe()
      .filter((def) => def.admin_only)
      .map((def) => def.type);
    expect(adminOnly).toEqual(["admin_curated_list"]);
    expect(offered.some((type) => adminOnly.includes(type))).toBe(false);
  });
});
