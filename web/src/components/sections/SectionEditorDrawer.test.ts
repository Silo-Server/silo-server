import { describe, expect, it } from "vitest";
import { buildAdminSectionPayload, buildProfileSectionSaveEntry } from "./SectionEditorDrawer";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import { withSectionLibraryFilterIds } from "@/lib/sectionLibraryFilter";
import type { RecipeDefinition } from "@/lib/recipes";
import { fallbackSectionTypes, filterRecipeCatalog } from "@/lib/sectionTypes";

describe("SectionEditorDrawer payload builders", () => {
  describe.each(["recently_added", "recently_released"])("%s ownership", (sectionType) => {
    it.each([
      { name: "the original library is reselected", ids: [2], owner: 2 },
      { name: "all libraries are selected", ids: [], owner: 2 },
      { name: "the original library stays excluded", ids: [4], owner: null },
    ])("uses the final selection when $name", ({ ids, owner }) => {
      for (const filter of [
        { filter_library_id: 2 },
        { library_ids: [2], media_scope: "series" },
      ]) {
        const config = {
          ...filter,
          generated_source: "home_library_recent",
          generated_library_id: 2,
        };
        const draft = withSectionLibraryFilterIds(config, [4]);
        const recipeParams = withSectionLibraryFilterIds(draft, ids);
        const input = {
          section: {
            id: "1",
            scope: "home",
            library_id: null,
            section_type: sectionType,
            title: "Recent in TV",
            item_limit: 20,
            featured: false,
            enabled: true,
            hidden: false,
            is_custom: false,
            customized: false,
            position: 0,
            created_at: "",
            updated_at: "",
            config,
          },
          scope: "home",
          currentLibraryId: null,
          sectionType,
          title: "Recent in TV",
          itemLimit: 20,
          featured: false,
          enabled: true,
          queryDefinition: queryDefinitionFromSectionConfig(),
          selectedCollectionId: "",
          recipeParams,
        };
        const expectedConfig = { ...recipeParams, generated_library_id: owner };
        expect(buildAdminSectionPayload(input).config).toEqual(expectedConfig);
        expect(buildProfileSectionSaveEntry(input).config).toEqual(expectedConfig);
      }
    });
  });

  it("preserves continue listening config for admin sections", () => {
    const payload = buildAdminSectionPayload({
      section: null,
      scope: "home",
      currentLibraryId: null,
      sectionType: "continue_watching",
      title: "Continue Listening",
      itemLimit: 20,
      featured: false,
      enabled: true,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { continue_type: "listening" },
    });

    expect(payload).toMatchObject({
      section_type: "continue_watching",
      title: "Continue Listening",
      config: { continue_type: "listening" },
    });
  });

  it("preserves continue listening config for profile sections", () => {
    const entry = buildProfileSectionSaveEntry({
      section: null,
      sectionType: "continue_watching",
      title: "Continue Listening",
      itemLimit: 20,
      featured: false,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { continue_type: "listening" },
    });

    expect(entry).toMatchObject({
      section_type: "continue_watching",
      title: "Continue Listening",
      is_custom: true,
      config: { continue_type: "listening" },
    });
  });

  it("replaces a generated row's legacy library filter in admin sections", () => {
    const section = {
      id: "1",
      scope: "home",
      title: "Recently Added in TV",
      section_type: "recently_added",
      item_limit: 20,
      featured: false,
      enabled: true,
      position: 0,
      config: { filter_library_id: 4, generated_source: "library" },
    };
    const payload = buildAdminSectionPayload({
      section: section as unknown as Parameters<typeof buildAdminSectionPayload>[0]["section"],
      scope: "home",
      currentLibraryId: null,
      sectionType: "recently_added",
      title: "Recently Added in TV",
      itemLimit: 20,
      featured: false,
      enabled: true,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { generated_source: "library", filter_library_ids: [6] },
    });

    expect(payload.config).toEqual({ generated_source: "library", filter_library_ids: [6] });
  });

  it("replaces a generated row's legacy library filter in profile sections", () => {
    const entry = buildProfileSectionSaveEntry({
      section: {
        id: "s1",
        section_type: "recently_added",
        title: "Recently Added in TV",
        featured: false,
        item_limit: 20,
        hidden: false,
        is_custom: false,
        customized: false,
        position: 0,
        config: { filter_library_id: 4 },
      },
      sectionType: "recently_added",
      title: "Recently Added in TV",
      itemLimit: 20,
      featured: false,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
      recipeParams: { filter_library_ids: [6] },
    });

    expect(entry.config).toEqual({ filter_library_ids: [6] });
  });
});

describe("SectionEditorDrawer recipe choices", () => {
  const recipe = (type: string, admin_only: boolean): RecipeDefinition => ({
    type,
    category: "custom",
    presets: [],
    avoid_duplicates: false,
    supports_rotation: false,
    admin_only,
  });
  const catalog = {
    categories: {
      library_staples: [recipe("recently_added", false)],
      custom: [recipe("custom_filter", true), recipe("admin_curated_list", true)],
    },
  };

  it("drops admin-only recipes when the profile may not add them", () => {
    const filtered = filterRecipeCatalog(catalog, false);

    expect(filtered?.categories.library_staples?.map((r) => r.type)).toEqual(["recently_added"]);
    expect(filtered?.categories.custom).toEqual([]);
    expect(catalog.categories.custom).toHaveLength(2);
  });

  it("keeps the whole catalog when the profile may add admin-only recipes", () => {
    expect(filterRecipeCatalog(catalog, true)).toBe(catalog);
    expect(filterRecipeCatalog(undefined, false)).toBeUndefined();
  });

  it("drops custom_filter from the static fallback types only when restricted", () => {
    const values = (allow: boolean) => fallbackSectionTypes(allow).map((type) => type.value);

    expect(values(false)).not.toContain("custom_filter");
    expect(values(false)).toContain("recently_added");
    expect(values(true)).toContain("custom_filter");
  });
});
