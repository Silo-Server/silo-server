import { describe, expect, it } from "vitest";

import { queryDefinitionFromSectionConfig } from "@/api/types";

import { buildSectionSaveEntry } from "./SectionDrawer";
import { buildAdminSectionPayload } from "./sections/SectionEditorDrawer";

describe("SectionDrawer helpers", () => {
  it("builds a custom section entry with a user-selected featured flag", () => {
    const entry = buildSectionSaveEntry({
      section: null,
      sectionType: "recently_added",
      title: "Hero Picks",
      itemLimit: 12,
      featured: true,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
    });

    expect(entry).toMatchObject({
      title: "Hero Picks",
      featured: true,
      is_custom: true,
      item_limit: 12,
    });
  });

  it("preserves generated metadata when editing an existing generated section", () => {
    const entry = buildSectionSaveEntry({
      section: {
        id: "home-generated",
        section_type: "recently_added",
        title: "Recently Added in Movies",
        featured: false,
        item_limit: 20,
        hidden: false,
        is_custom: false,
        customized: false,
        position: 1,
        config: {
          generated_source: "home_library_recent",
          filter_library_id: 7,
          filter_library_ids: [7],
        },
      },
      sectionType: "recently_added",
      title: "Recently Added in Movies",
      itemLimit: 24,
      featured: false,
      queryDefinition: queryDefinitionFromSectionConfig({ filter_library_ids: [7] }),
      selectedCollectionId: "",
    });

    expect(entry.config).toMatchObject({
      generated_source: "home_library_recent",
      filter_library_id: 7,
    });
  });

  it("builds admin section payloads with enabled state and scope", () => {
    const payload = buildAdminSectionPayload({
      section: null,
      scope: "library",
      currentLibraryId: 42,
      sectionType: "genre",
      title: "Movie Night",
      itemLimit: 16,
      featured: false,
      enabled: false,
      queryDefinition: queryDefinitionFromSectionConfig({ media_scope: "movie" }),
      selectedCollectionId: "",
    });

    expect(payload).toMatchObject({
      scope: "library",
      library_id: 42,
      section_type: "genre",
      title: "Movie Night",
      item_limit: 16,
      enabled: false,
      config: expect.objectContaining({ media_scope: "movie" }),
    });
  });

  it("uses the section type label when an admin title is left blank", () => {
    const payload = buildAdminSectionPayload({
      section: null,
      scope: "home",
      currentLibraryId: null,
      sectionType: "recently_added",
      title: "   ",
      itemLimit: 20,
      featured: false,
      enabled: true,
      queryDefinition: queryDefinitionFromSectionConfig(),
      selectedCollectionId: "",
    });

    expect(payload.title).toBe("Recently Added");
  });
});
