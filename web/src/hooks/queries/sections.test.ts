import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getHomeLayoutOk from "../../../../contracts/api/v2/fixtures/get_home_layout_ok.json";
import getHomeLayoutIncludeItemsOutOfRange from "../../../../contracts/api/v2/fixtures/get_home_layout_include_items_out_of_range.json";
import getHomeLayoutWithItemsOk from "../../../../contracts/api/v2/fixtures/get_home_layout_with_items_ok.json";
import getHomeSectionItemsOk from "../../../../contracts/api/v2/fixtures/get_home_section_items_ok.json";
import getLibrarySectionItemsOk from "../../../../contracts/api/v2/fixtures/get_library_section_items_ok.json";
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
  homeLayoutQueryOptions,
  HOME_LAYOUT_INCLUDED_ROWS,
  HOME_SECTION_GC_TIME,
  profileSectionOverridesFromV2,
} from "./sections";
import { sectionKeys } from "./keys";

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

  it("fetches the home layout from v2 and keeps the { sections } shape", async () => {
    mocks.fetchWithSession.mockResolvedValue(jsonResponse(getHomeLayoutOk));

    const options = homeLayoutQueryOptions(new QueryClient()) as unknown as QueryOptions;
    const response = await options.queryFn({});

    expect(options.queryKey).toEqual(["sections", "home", "layout"]);
    expect(mocks.fetchWithSession.mock.calls[0]?.[0]).toBe(
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}`,
    );
    expect(response).toEqual({ sections: getHomeLayoutOk.sections });
  });

  it("reads the layout alone when the server rejects include_items", async () => {
    mocks.fetchWithSession
      .mockResolvedValueOnce({
        ...jsonResponse(getHomeLayoutIncludeItemsOutOfRange),
        res: new Response(JSON.stringify(getHomeLayoutIncludeItemsOutOfRange), {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        }),
      })
      .mockResolvedValueOnce(jsonResponse({ sections: getHomeLayoutOk.sections }));

    const options = homeLayoutQueryOptions(new QueryClient()) as unknown as QueryOptions;
    const response = await options.queryFn({});

    expect(mocks.fetchWithSession.mock.calls.map((call) => call[0])).toEqual([
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}`,
      "/api/v2/home/layout",
    ]);
    expect(response).toEqual({ sections: getHomeLayoutOk.sections });
  });

  it("seeds each included row's item cache before the layout resolves", async () => {
    mocks.fetchWithSession.mockResolvedValue(jsonResponse(getHomeLayoutWithItemsOk));
    const queryClient = new QueryClient();

    const options = homeLayoutQueryOptions(queryClient) as unknown as QueryOptions;
    await options.queryFn({});

    const included = getHomeLayoutWithItemsOk.included_sections[0]!;
    const cached = queryClient.getQueryData<{ section: { id: string; items: unknown[] } }>(
      sectionKeys.homeItems(included.id),
    );
    expect(cached?.section.id).toBe(included.id);
    expect(cached?.section.items).toHaveLength(included.items.length);
  });

  it("keeps seeded rows for the home section gcTime, not the client default", async () => {
    mocks.fetchWithSession.mockResolvedValue(jsonResponse(getHomeLayoutWithItemsOk));
    const queryClient = new QueryClient({ defaultOptions: { queries: { gcTime: 10 * 60_000 } } });

    const options = homeLayoutQueryOptions(queryClient) as unknown as QueryOptions;
    await options.queryFn({});

    for (const included of getHomeLayoutWithItemsOk.included_sections) {
      const query = queryClient.getQueryCache().find({
        queryKey: sectionKeys.homeItems(included.id),
        exact: true,
      });
      expect(query?.gcTime).toBe(HOME_SECTION_GC_TIME);
    }
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
