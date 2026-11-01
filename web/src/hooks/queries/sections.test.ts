import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getHomeLayoutOk from "../../../../contracts/api/v2/fixtures/get_home_layout_ok.json";
import getHomeLayoutIncludeItemsOutOfRange from "../../../../contracts/api/v2/fixtures/get_home_layout_include_items_out_of_range.json";
import getHomeLayoutWithItemsOk from "../../../../contracts/api/v2/fixtures/get_home_layout_with_items_ok.json";
import getHomeSectionItemsOk from "../../../../contracts/api/v2/fixtures/get_home_section_items_ok.json";
import getHomeSectionItemsCappedOk from "../../../../contracts/api/v2/fixtures/get_home_section_items_capped_ok.json";
import getHomeSectionItemsItemLimitOutOfRange from "../../../../contracts/api/v2/fixtures/get_home_section_items_item_limit_out_of_range.json";
import getHomeLayoutItemLimitOutOfRange from "../../../../contracts/api/v2/fixtures/get_home_layout_item_limit_out_of_range.json";

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
  homeLayoutQueryOptions,
  HOME_LAYOUT_INCLUDED_ROWS,
} from "./sections";
import { completeHomeRow, HOME_ROW_ITEM_LIMIT } from "./homeRowLimit";
import { sectionKeys } from "./keys";
import type { ResolvedSection } from "@/api/types";

type QueryOptions = {
  queryKey: unknown;
  queryFn: (ctx: { signal?: AbortSignal }) => Promise<unknown>;
};

function jsonResponse(body: unknown, status = 200) {
  return {
    res: new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": status === 200 ? "application/json" : "application/problem+json" },
    }),
    requestProfileId: "p-owner",
    requestProfileToken: null,
  };
}

function row(ids: string[], fields: Partial<ResolvedSection> = {}): ResolvedSection {
  return {
    id: "row",
    section_type: "recently_added",
    title: "Row",
    featured: false,
    item_limit: 20,
    total_count: ids.length,
    is_custom: false,
    customized: false,
    items: ids.map((content_id) => ({ content_id }) as ResolvedSection["items"][number]),
    ...fields,
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
    const rejected = () => ({
      ...jsonResponse(getHomeLayoutIncludeItemsOutOfRange),
      res: new Response(JSON.stringify(getHomeLayoutIncludeItemsOutOfRange), {
        status: 422,
        headers: { "Content-Type": "application/problem+json" },
      }),
    });
    mocks.fetchWithSession
      .mockResolvedValueOnce(rejected())
      .mockResolvedValueOnce(rejected())
      .mockResolvedValueOnce(jsonResponse({ sections: getHomeLayoutOk.sections }));

    const options = homeLayoutQueryOptions(new QueryClient()) as unknown as QueryOptions;
    const response = await options.queryFn({});

    expect(mocks.fetchWithSession.mock.calls.map((call) => call[0])).toEqual([
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}&item_limit=${HOME_ROW_ITEM_LIMIT}`,
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}`,
      "/api/v2/home/layout",
    ]);
    expect(response).toEqual({ sections: getHomeLayoutOk.sections });
  });

  it("keeps included rows whole when the server rejects only item_limit", async () => {
    // A whole row longer than the cap, as a server without item_limit sends it.
    const row = getHomeLayoutWithItemsOk.included_sections[0]!;
    const items = Array.from({ length: HOME_ROW_ITEM_LIMIT + 5 }, (_, i) => ({
      ...row.items[0]!,
      content_id: `${row.items[0]!.content_id}-${i}`,
    }));
    const whole = {
      ...getHomeLayoutWithItemsOk,
      included_sections: [{ ...row, item_limit: 100, total_count: items.length, items }],
    };
    mocks.fetchWithSession
      .mockResolvedValueOnce(jsonResponse(getHomeLayoutItemLimitOutOfRange, 422))
      .mockResolvedValueOnce(jsonResponse(whole));
    const queryClient = new QueryClient();

    const options = homeLayoutQueryOptions(queryClient) as unknown as QueryOptions;
    await options.queryFn({});

    expect(mocks.fetchWithSession.mock.calls.map((call) => call[0])).toEqual([
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}&item_limit=${HOME_ROW_ITEM_LIMIT}`,
      `/api/v2/home/layout?include_items=${HOME_LAYOUT_INCLUDED_ROWS}`,
    ]);
    const cached = queryClient.getQueryData<{ partial?: boolean; section: { items: unknown[] } }>(
      sectionKeys.homeItems(row.id),
    );
    expect(cached?.section.items).toHaveLength(items.length);
    expect(cached?.partial).toBeUndefined();
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

  it.each([
    {
      name: "a capped row the server says holds more as partial",
      responses: () => [jsonResponse(getHomeSectionItemsCappedOk)],
      urls: ["/api/v2/home/sections/continue_watching/items?item_limit=20"],
      partial: true,
    },
    {
      name: "a capped row the server answered in full as whole",
      responses: () => [jsonResponse(getHomeSectionItemsOk)],
      urls: ["/api/v2/home/sections/continue_watching/items?item_limit=20"],
      partial: undefined,
    },
    {
      name: "a row whole when the server rejects item_limit",
      responses: () => [
        jsonResponse(getHomeSectionItemsItemLimitOutOfRange, 422),
        jsonResponse(getHomeSectionItemsOk),
      ],
      urls: [
        "/api/v2/home/sections/continue_watching/items?item_limit=20",
        "/api/v2/home/sections/continue_watching/items",
      ],
      partial: undefined,
    },
  ])("reads $name", async ({ responses, urls, partial }) => {
    for (const response of responses()) mocks.fetchWithSession.mockResolvedValueOnce(response);

    const row = await fetchHomeSectionItems("continue_watching", { itemLimit: 20 });

    expect(mocks.fetchWithSession.mock.calls.map((call) => call[0])).toEqual(urls);
    expect(row.partial).toBe(partial);
  });
});

describe("home row limit", () => {
  it("completes a row with the shown cards in place and the rest after them", () => {
    const shown = { section: row(["a", "b", "c"]), partial: true };
    const whole = { section: row(["a", "x", "b", "y"], { item_limit: 100, total_count: 4 }) };

    const completed = completeHomeRow(shown, whole);

    expect(completed.partial).toBeUndefined();
    expect(completed.shown).toBe(3);
    expect(completed.section.item_limit).toBe(100);
    expect(completed.section.items.map((item) => item.content_id)).toEqual([
      "a",
      "b",
      "c",
      "x",
      "y",
    ]);
  });

  it("orders rows completed within the same millisecond", () => {
    vi.spyOn(Date, "now").mockReturnValue(1_000_000_000_000);
    const shown = { section: row(["a"]), partial: true };
    const whole = { section: row(["a", "b"], { item_limit: 100, total_count: 2 }) };

    const first = completeHomeRow(shown, whole);
    const second = completeHomeRow(shown, whole);

    expect(second.shownAt).toBeGreaterThan(first.shownAt ?? Infinity);
    vi.restoreAllMocks();
  });
});
