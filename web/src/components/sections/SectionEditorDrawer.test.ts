import { describe, expect, it } from "vitest";
import { buildAdminSectionPayload, buildProfileSectionSaveEntry } from "./SectionEditorDrawer";
import { queryDefinitionFromSectionConfig } from "@/api/types";
import type { RecipeDefinition } from "@/lib/recipes";
import { fallbackSectionTypes, filterRecipeCatalog } from "@/lib/sectionTypes";

describe("SectionEditorDrawer payload builders", () => {
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
