import { beforeEach, describe, expect, it, vi } from "vitest";

import getHomeSectionItemsOk from "../../../../contracts/api/v2/fixtures/get_home_section_items_ok.json";
import getLibrarySectionItemsOk from "../../../../contracts/api/v2/fixtures/get_library_section_items_ok.json";

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
  normalizeProfileSectionOverridesResponse,
} from "./sections";

describe("sections query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.fetchWithSession.mockReset();
  });

  it("fetches home section items from the v2 home section and keeps the { section } shape", async () => {
    mocks.fetchWithSession.mockResolvedValue({
      res: new Response(JSON.stringify(getHomeSectionItemsOk), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
      requestProfileId: "p-owner",
      requestProfileToken: null,
    });

    const response = await fetchHomeSectionItems("continue_watching");

    expect(mocks.fetchWithSession.mock.calls[0]?.[0]).toBe(
      "/api/v2/home/sections/continue_watching/items",
    );
    expect(response.section.id).toBe("continue_watching");
    expect(response.section.items[0]?.content_id).toBe("movie:heat-1995");
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

  it("normalizes backend-shaped raw section overrides into frontend override fields", () => {
    const response = normalizeProfileSectionOverridesResponse({
      overrides: [
        {
          ID: "override-1",
          SectionID: "admin-1",
          Position: 2,
          Hidden: true,
          Removed: true,
          SectionType: "recently_added",
          Title: "Recently Added",
          Featured: true,
          ItemLimit: 12,
          Config: '{"media_scope":"movie"}',
        },
      ],
    });

    expect(response).toEqual({
      overrides: [
        {
          id: "override-1",
          section_id: "admin-1",
          position: 2,
          hidden: true,
          removed: true,
          section_type: "recently_added",
          title: "Recently Added",
          featured: true,
          item_limit: 12,
          config: { media_scope: "movie" },
        },
      ],
    });
  });
});
