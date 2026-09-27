import { beforeEach, describe, expect, it, vi } from "vitest";

import getHomeLayoutOk from "../../../../contracts/api/v2/fixtures/get_home_layout_ok.json";
import getHomeSectionItemsOk from "../../../../contracts/api/v2/fixtures/get_home_section_items_ok.json";
import getLibrarySectionItemsOk from "../../../../contracts/api/v2/fixtures/get_library_section_items_ok.json";
import listHomeSectionsOk from "../../../../contracts/api/v2/fixtures/list_home_sections_ok.json";
import listProfileSectionOverridesOk from "../../../../contracts/api/v2/fixtures/list_profile_section_overrides_ok.json";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  fetchWithSession: vi.fn(),
  useQuery: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return { ...actual, useQuery: (...args: unknown[]) => mocks.useQuery(...args) };
});

vi.mock("@/api/client", () => ({
  api: mocks.api,
  fetchWithSession: mocks.fetchWithSession,
  reportProfileUnverified: vi.fn(),
}));

import {
  fetchHomeSectionItems,
  fetchLibrarySectionItems,
  useHomeLayout,
  useHomeSections,
  useLibraryLayout,
  profileSectionOverridesFromV2,
} from "./sections";

type QueryOptions = {
  queryKey: unknown;
  queryFn: (ctx: { signal?: AbortSignal }) => Promise<unknown>;
};

function jsonResponse(body: unknown) {
  return {
    res: new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
    requestProfileId: "p-owner",
    requestProfileToken: null,
  };
}

describe("sections query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.fetchWithSession.mockReset();
    mocks.useQuery.mockReset();
    mocks.useQuery.mockImplementation((options: unknown) => options);
  });

  it("lists home sections from v2 and keeps the { sections } shape", async () => {
    mocks.fetchWithSession.mockResolvedValue(jsonResponse(listHomeSectionsOk));

    const options = useHomeSections() as unknown as QueryOptions;
    const response = (await options.queryFn({})) as {
      sections: { id: string; items: unknown[] }[];
    };

    expect(options.queryKey).toEqual(["sections", "home"]);
    expect(mocks.fetchWithSession.mock.calls[0]?.[0]).toBe("/api/v2/home/sections");
    expect(response.sections.map((section) => section.id)).toEqual(
      listHomeSectionsOk.sections.map((section) => section.id),
    );
    expect(response.sections[0]?.items).toHaveLength(
      listHomeSectionsOk.sections[0]?.items.length ?? -1,
    );
  });

  it("fetches the home layout from v2 and keeps the { sections } shape", async () => {
    mocks.fetchWithSession.mockResolvedValue(jsonResponse(getHomeLayoutOk));

    const options = useHomeLayout() as unknown as QueryOptions;
    const response = await options.queryFn({});

    expect(options.queryKey).toEqual(["sections", "home", "layout"]);
    expect(mocks.fetchWithSession.mock.calls[0]?.[0]).toBe("/api/v2/home/layout");
    expect(response).toEqual({ sections: getHomeLayoutOk.sections });
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
