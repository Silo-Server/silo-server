import { describe, expect, it, vi } from "vitest";

import listProfileSectionOverridesOk from "../../../../contracts/api/v2/fixtures/list_profile_section_overrides_ok.json";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
}));

import {
  fetchHomeSectionItems,
  fetchLibrarySectionItems,
  useLibraryLayout,
  profileSectionOverridesFromV2,
} from "./sections";

describe("sections query helpers", () => {
  it("fetches home section items from the home section items endpoint", async () => {
    const options = { cache: "no-store" } satisfies RequestInit;
    mocks.api.mockResolvedValue({ section: { items: [] } });

    await fetchHomeSectionItems("section-1", options);

    expect(mocks.api).toHaveBeenCalledWith("/home/sections/section-1/items", options);
  });

  it("fetches library section items from the library section items endpoint", async () => {
    const options = { cache: "no-store" } satisfies RequestInit;
    mocks.api.mockResolvedValue({ section: { items: [] } });

    await fetchLibrarySectionItems(1, "section-1", options);

    expect(mocks.api).toHaveBeenCalledWith("/library/1/sections/section-1/items", options);
  });

  it("exports the library layout hook", () => {
    expect(useLibraryLayout).toBeTypeOf("function");
  });

  it("projects stored v2 section overrides onto the editor's override fields", () => {
    const overrides = profileSectionOverridesFromV2(listProfileSectionOverridesOk.items);

    expect(overrides[0]).toEqual({
      id: "o-1",
      section_id: "s-continue",
      position: 2,
      hidden: true,
      removed: false,
      section_type: undefined,
      title: "Keep watching",
      featured: false,
      item_limit: 10,
      config: undefined,
    });
    expect(overrides[1]).toMatchObject({
      section_id: undefined,
      position: undefined,
      featured: undefined,
      item_limit: undefined,
    });
  });
});
