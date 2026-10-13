import { createAdminSection, updateAdminSection } from "@/api/adminSections";
import {
  queryDefinitionFromSectionConfig,
  type PageSectionConfig,
  type QueryDefinition,
} from "@/api/types";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { buildRowCreateRequest, buildRowUpdateRequest, nextAppendPosition } from "./payloads";
import golden from "./payloads.golden.json";
import { everyPreset, recipeCatalogFixture } from "./recipeCatalogFixture.test-support";
import {
  canSaveDraft,
  draftForPreset,
  draftFromRow,
  findRecipe,
  mergeReloadedDraft,
  previewWaitText,
  savedTitle,
  withCollection,
  withRules,
  withTitle,
  withVariant,
  type RowDraft,
} from "./rowDraft";
import type { HomeRow, PageRef } from "./types";
import { VARIANT_FAMILIES } from "./variants";

const wire = vi.hoisted(() => ({ last: undefined as unknown }));
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(async (route: string, init?: { body?: unknown }) => {
    wire.last = { route, body: init?.body };
    throw new Error("request captured");
  }),
}));

beforeEach(() => {
  wire.last = undefined;
});

async function sentBody(send: () => Promise<unknown>): Promise<Record<string, unknown>> {
  await expect(send()).rejects.toThrow("request captured");
  return JSON.parse(JSON.stringify((wire.last as { body: unknown }).body));
}

const PAGES: Array<[string, PageRef]> = [
  ["home", { kind: "home" }],
  ["library", { kind: "library", libraryId: 7 }],
];

function row(overrides: Partial<HomeRow> = {}): HomeRow {
  return {
    id: "row-1",
    title: "Trending This Week",
    sectionType: "trending_on_server",
    config: { window: "7d" },
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    legacyTrakt: false,
    ...overrides,
  };
}

function stored(draft: RowDraft, overrides: Partial<PageSectionConfig> = {}): PageSectionConfig {
  return {
    id: "row-1",
    scope: "home",
    library_id: null,
    position: 3,
    section_type: draft.sectionType,
    title: draft.title,
    featured: draft.hero,
    item_limit: draft.itemLimit,
    config: draft.config,
    enabled: true,
    created_at: "2026-01-02T03:04:05.000Z",
    updated_at: "2026-01-02T03:04:05.000Z",
    ...overrides,
  };
}

describe("adding a row", () => {
  it("sends the gallery's create body for every preset, plus the bottom position", async () => {
    const adminCreate = golden.adminCreate as Record<string, unknown>;
    for (const { def, preset } of everyPreset()) {
      for (const [pageName, page] of PAGES) {
        const key = `${def.type}/${preset.key}/${pageName}`;
        expect(adminCreate, `missing preset: ${key}`).toHaveProperty(key);
        // Start from the family's first preset and pick this one, as a chip or
        // radio does, so the variant switch is part of what is checked.
        let draft = draftForPreset(def, def.presets[0]);
        if (VARIANT_FAMILIES[def.type]) draft = withVariant(draft, def, preset.key);
        const body = await sentBody(() =>
          createAdminSection(
            buildRowCreateRequest(draft, savedTitle(draft, recipeCatalogFixture), page, 9),
          ),
        );
        const { position, ...rest } = body;
        expect(rest, key).toEqual(adminCreate[key]);
        expect(position).toBe(9);
      }
    }
  });

  it("puts a new row after every row on the page", () => {
    expect(nextAppendPosition([])).toBe(0);
    expect(nextAppendPosition([0, 4, 2])).toBe(5);
  });

  it("names a seasonal row after its preset (#585)", () => {
    const def = findRecipe(recipeCatalogFixture, "seasonal_themed")!;
    expect(draftForPreset(def, def.presets[0]).title).toBe("Seasonal Picks");
    expect(
      withVariant(draftForPreset(def, def.presets[0]), def, "se_family_movie_night").title,
    ).toBe("Family Movie Night");
  });
});

describe("row names", () => {
  const def = findRecipe(recipeCatalogFixture, "trending_on_server")!;

  it("follows the variant until the user types a name", () => {
    let draft = draftForPreset(def, def.presets[1]);
    expect(draft.title).toBe("Trending This Week");
    draft = withVariant(draft, def, "tr_24h");
    expect(draft.title).toBe("Trending Now (24h)");
    draft = withVariant(withTitle(draft, "Hot here"), def, "tr_30d");
    expect(draft.title).toBe("Hot here");
  });

  it("follows the variant on an existing row only while its name is the preset's", () => {
    expect(draftFromRow(row(), recipeCatalogFixture).titleFollowsVariant).toBe(true);
    expect(
      draftFromRow(row({ title: "What's hot" }), recipeCatalogFixture).titleFollowsVariant,
    ).toBe(false);
  });

  it("falls back to a plain name for a blank title, never a raw type", () => {
    const blank = { ...draftFromRow(row(), recipeCatalogFixture), title: "  " };
    expect(savedTitle(blank, recipeCatalogFixture)).toBe("Trending This Week");
    expect(savedTitle({ ...blank, config: { window: "90d" } }, recipeCatalogFixture)).toBe(
      "Trending on this server",
    );
    expect(savedTitle({ ...blank, sectionType: "next_up", config: {} }, recipeCatalogFixture)).toBe(
      "On Deck",
    );
    expect(savedTitle({ ...blank, sectionType: "next_up", config: {} }, undefined)).toBe("On deck");
  });
});

describe("editing a row", () => {
  it("never changes the config of a row that is only renamed", async () => {
    const configs = [
      { theme: "christmas", mode: "" },
      { subject_type: "era", subject: "1990s" },
      { continue_type: "reading" },
      { window: "7d", filter_library_ids: [3], generated_source: "home" },
    ];
    for (const config of configs) {
      const draft = withTitle(draftFromRow(row({ config }), recipeCatalogFixture), "New name");
      const body = await sentBody(() =>
        updateAdminSection({
          ...buildRowUpdateRequest(stored(draft), draft, draft.title),
          id: "row-1",
          etag: '"1"',
        }),
      );
      expect(body.config).toEqual(config);
      expect(body.title).toBe("New name");
    }
  });

  it("drops keys a variant change removed instead of merging the stored config back", () => {
    const def = findRecipe(recipeCatalogFixture, "editorial_spotlight");
    const original = draftFromRow(
      row({
        sectionType: "editorial_spotlight",
        config: { subject_type: "era", subject: "1990s" },
      }),
      recipeCatalogFixture,
    );
    const draft = withVariant(original, def, "es_director_auto");
    const request = buildRowUpdateRequest(stored(original), draft, "Directors");
    expect(request.config).toEqual({
      subject_type: "director",
      auto_rotate: true,
      rotation_cadence: "weekly",
    });
  });

  it("saves over the stored on/off state", () => {
    const draft = draftFromRow(row(), recipeCatalogFixture);
    expect(buildRowUpdateRequest(stored(draft, { enabled: false }), draft, "x").enabled).toBe(
      false,
    );
  });
});

describe("reloading after a conflict", () => {
  const original = draftFromRow(row(), recipeCatalogFixture);

  it("keeps the user's changes, takes the rest from the server and names what moved", () => {
    const edited = { ...withTitle(original, "My draft"), hero: true };
    const upstream = { ...original, title: "Remote title", itemLimit: 40 };
    const { draft, changedUpstream } = mergeReloadedDraft(original, edited, upstream);
    expect(draft).toMatchObject({ title: "My draft", hero: true, itemLimit: 40 });
    expect(changedUpstream).toEqual(["title", "itemLimit"]);
  });

  it("takes an upstream variant change when the user left the variant alone", () => {
    const upstream = { ...original, config: { window: "30d" } };
    const { draft, changedUpstream } = mergeReloadedDraft(original, original, upstream);
    expect(draft.config).toEqual({ window: "30d" });
    expect(changedUpstream).toEqual(["shows"]);
  });
});

describe("saving a draft", () => {
  const seasonal = (config: Record<string, unknown>): RowDraft => ({
    sectionType: "seasonal_themed",
    title: "Seasonal Picks",
    titleFollowsVariant: true,
    config,
    itemLimit: 20,
    hero: false,
  });

  it("refuses a seasonal row with no holiday, which the server would reject", () => {
    expect(canSaveDraft(seasonal({ enabled_themes: [] }))).toBe(false);
    expect(canSaveDraft(seasonal({ enabled_themes: ["christmas"] }))).toBe(true);
    expect(canSaveDraft(seasonal({ theme: "christmas" }))).toBe(true);
  });
});

describe("collection rows", () => {
  const def = findRecipe(recipeCatalogFixture, "collection")!;
  const ghibli = { id: "lib-1", title: "Studio Ghibli", source: "library" as const };
  const pixar = { id: "lib-2", title: "Pixar", source: "library" as const };

  it("adds a picked collection with the gallery's create body, plus the bottom position", async () => {
    const adminCreate = golden.adminCreate as Record<string, unknown>;
    for (const [pageName, page] of PAGES) {
      const draft = withCollection(draftForPreset(def, def.presets[0]), ghibli);
      const body = await sentBody(() =>
        createAdminSection(
          buildRowCreateRequest(draft, savedTitle(draft, recipeCatalogFixture), page, 9),
        ),
      );
      expect(body).toEqual({
        ...(adminCreate[`collection/picked/${pageName}`] as object),
        position: 9,
      });
    }
  });

  it("can't be saved before a collection is picked", () => {
    const draft = draftForPreset(def, def.presets[0]);
    expect(canSaveDraft(draft)).toBe(false);
    expect(canSaveDraft(withCollection(draft, ghibli))).toBe(true);
  });

  it("counts a legacy personal collection as no selection on the admin surface", () => {
    const draft = {
      ...draftForPreset(def, def.presets[0]),
      config: { user_collection_id: "u-9", sort_by: "title" },
    };
    expect(canSaveDraft(draft)).toBe(true);
    expect(canSaveDraft(draft, "admin")).toBe(false);
    expect(previewWaitText(draft, "admin")).toBe("Pick a collection to see its titles here.");
    const picked = withCollection(draft, ghibli);
    expect(canSaveDraft(picked, "admin")).toBe(true);
    expect(picked.config).toEqual({ library_collection_id: "lib-1", sort_by: "title" });
  });

  it("starts with the picked collection's name until the user types one", () => {
    let draft = withCollection(draftForPreset(def, def.presets[0]), ghibli);
    expect(draft.title).toBe("Studio Ghibli");
    draft = withCollection(draft, pixar, ghibli.title);
    expect(draft.title).toBe("Pixar");
    draft = withCollection(withTitle(draft, "Animated"), ghibli, pixar.title);
    expect(draft.title).toBe("Animated");
  });

  it("follows a new pick on an existing row only while its name is the old collection's", () => {
    const named = draftFromRow(
      row({
        title: "Pixar",
        sectionType: "collection",
        config: { library_collection_id: "lib-2" },
      }),
      recipeCatalogFixture,
    );
    expect(withCollection(named, ghibli, "Pixar").title).toBe("Studio Ghibli");
    expect(withCollection({ ...named, title: "Kids" }, ghibli, "Pixar").title).toBe("Kids");
  });

  it("falls back to the collection's name when the name is left blank", () => {
    const draft = { ...withCollection(draftForPreset(def, def.presets[0]), ghibli), title: " " };
    expect(savedTitle(draft, recipeCatalogFixture, "Studio Ghibli")).toBe("Studio Ghibli");
  });

  it("keeps the stored config byte for byte when the collection is not picked again", async () => {
    // A personal collection that is not in the admin's list, with metadata the form never edits.
    const config = {
      user_collection_id: "u-9",
      generated_source: "collection_auto",
      generated_library_id: 2,
    };
    const original = draftFromRow(
      row({ sectionType: "collection", title: "Shared", config }),
      recipeCatalogFixture,
    );
    for (const draft of [withTitle(original, "Renamed"), { ...original, hero: true }]) {
      const body = await sentBody(() =>
        updateAdminSection({
          ...buildRowUpdateRequest(stored(original), draft, draft.title),
          id: "row-1",
          etag: '"1"',
        }),
      );
      expect(body.config).toEqual(config);
      expect(body.section_type).toBe("collection");
    }
  });

  it("writes the picked option's key and drops the old id key, keeping every other key", () => {
    const original = draftFromRow(
      row({
        sectionType: "collection",
        config: { user_collection_id: "u-1", generated_source: "collection_auto" },
      }),
      recipeCatalogFixture,
    );
    const library = withCollection(original, pixar);
    expect(library.config).toEqual({
      generated_source: "collection_auto",
      library_collection_id: "lib-2",
    });
    expect(withCollection(library, { id: "u-2", title: "Mine", source: "user" }).config).toEqual({
      generated_source: "collection_auto",
      user_collection_id: "u-2",
    });
    // Picking the collection it already shows changes nothing.
    expect(withCollection(original, { id: "u-1", title: "Mine", source: "user" }).config).toBe(
      original.config,
    );
  });
});

describe("rule rows", () => {
  const def = findRecipe(recipeCatalogFixture, "custom_filter")!;
  const rules: QueryDefinition = {
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

  it("adds a rule row with the older editor's create body, plus the bottom position", async () => {
    const adminCreate = golden.adminCreate as Record<string, unknown>;
    for (const [pageName, page] of PAGES) {
      const draft = withTitle(withRules(draftForPreset(def, undefined), rules), "90s Horror");
      const body = await sentBody(() =>
        createAdminSection(buildRowCreateRequest(draft, draft.title, page, 9)),
      );
      expect(body).toEqual({
        ...(adminCreate[`custom_filter/rules/${pageName}`] as object),
        position: 9,
      });
    }
  });

  it("starts a new rule row with no rules, in the older editor's shape", () => {
    const draft = draftForPreset(def, undefined);
    expect(queryDefinitionFromSectionConfig(draft.config).groups).toEqual([]);
    expect(draft.config).toMatchObject({ library_ids: [], match: "all", groups: [] });
  });

  it("preserves rule and legacy row config on a rename", async () => {
    const configs: Array<[string, Record<string, unknown>]> = [
      [
        "genre",
        {
          filter_type: "movie",
          match: "all",
          groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Horror" }] }],
          sort: "added_at",
          order: "desc",
        },
      ],
      ["custom_filter", { ...rules, limit: 40, generated_source: "home" }],
      [
        "collection",
        { library_collection_id: "lib-trakt", source_provider: "trakt", source_preset: "trending" },
      ],
      ["trending_discover", { source: "trakt", window: "week" }],
      ["award_winners", { award_type: "oscar" }],
      ["admin_curated_list", { item_ids: ["movie:a", "movie:b"] }],
      [
        "custom_filter",
        {
          library_ids: [],
          match: "all",
          groups: [
            { match: "all", rules: [{ field: "cast", op: "contains", value: "Tom Hanks" }] },
          ],
          sort: { field: "added_at", order: "desc" },
        },
      ],
    ];
    for (const [sectionType, config] of configs) {
      const original = draftFromRow(row({ sectionType, config }), recipeCatalogFixture);
      const draft = withTitle(original, "Renamed");
      const body = await sentBody(() =>
        updateAdminSection({
          ...buildRowUpdateRequest(stored(original), draft, draft.title),
          id: "row-1",
          etag: '"1"',
        }),
      );
      expect(JSON.stringify(body.config), sectionType).toBe(JSON.stringify(config));
    }
  });

  it("rewrites only the query keys when the rules change", () => {
    const original = draftFromRow(
      row({
        sectionType: "genre",
        config: {
          filter_type: "movie",
          match: "all",
          groups: [],
          sort: "added_at",
          order: "desc",
          generated_source: "home",
        },
      }),
      recipeCatalogFixture,
    );
    const query = queryDefinitionFromSectionConfig(original.config);
    const draft = withRules(original, {
      ...query,
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Drama" }] }],
    });
    expect(draft.config).toEqual({
      generated_source: "home",
      library_ids: [],
      media_scope: "movie",
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Drama" }] }],
      sort: { field: "added_at", order: "desc" },
    });
  });

  it("keeps a movies-and-shows scope through an edit to the rules", () => {
    const original = draftFromRow(
      row({ sectionType: "custom_filter", config: { media_scope: "video", groups: [] } }),
      recipeCatalogFixture,
    );
    const query = queryDefinitionFromSectionConfig(original.config);
    expect(query.media_scope).toBe("video");
    const draft = withRules(original, { ...query, match: "any" });
    expect(draft.config.media_scope).toBe("video");
  });
});

describe("Editor's Picks rows", () => {
  it("can't be saved with no titles, which the server refuses", () => {
    const draft = (config: Record<string, unknown>): RowDraft => ({
      sectionType: "admin_curated_list",
      title: "Staff picks",
      titleFollowsVariant: false,
      config,
      itemLimit: 20,
      hero: false,
    });
    expect(canSaveDraft(draft({ item_ids: [] }))).toBe(false);
    expect(canSaveDraft(draft({}))).toBe(false);
    expect(canSaveDraft(draft({ item_ids: ["m1"] }))).toBe(true);
  });
});
