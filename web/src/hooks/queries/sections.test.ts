import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getHomeLayoutOk from "../../../../contracts/api/v2/fixtures/get_home_layout_ok.json";
import getHomeLayoutIncludeItemsOutOfRange from "../../../../contracts/api/v2/fixtures/get_home_layout_include_items_out_of_range.json";
import getHomeLayoutWithItemsOk from "../../../../contracts/api/v2/fixtures/get_home_layout_with_items_ok.json";

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

import { homeLayoutQueryOptions, HOME_LAYOUT_INCLUDED_ROWS } from "./sections";
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
});
