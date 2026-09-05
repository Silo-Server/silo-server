import { describe, expect, it, vi } from "vitest";

import getLibrarySectionItemsOk from "../../../../contracts/api/v2/fixtures/get_library_section_items_ok.json";
import listProfileSectionOverridesOk from "../../../../contracts/api/v2/fixtures/list_profile_section_overrides_ok.json";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  fetchWithSession: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
  fetchWithSession: mocks.fetchWithSession,
  reportProfileUnverified: vi.fn(),
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

  it("fetches library section items from the v2 library section and keeps the { section } shape", async () => {
    mocks.fetchWithSession.mockResolvedValue({
      res: new Response(JSON.stringify(getLibrarySectionItemsOk), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
      requestProfileId: "p-owner",
      requestProfileToken: null,
    });

    const response = await fetchLibrarySectionItems(1, "continue_watching");

    expect(mocks.fetchWithSession.mock.calls[0]?.[0]).toBe(
      "/api/v2/library/1/sections/continue_watching/items",
    );
    expect(response.section.id).toBe("continue_watching");
    expect(response.section.items[0]?.content_id).toBe("movie:heat-1995");
    // The card components read "" for absent art and null for an absent rating.
    expect(response.section.items[0]?.logo_url).toBe("");
    expect(response.section.items[0]?.rating_imdb).toBe(8.3);
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
