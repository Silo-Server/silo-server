// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getLibraryCollectionsOk from "../../../../contracts/api/v2/fixtures/get_library_collections_ok.json";
import getLibraryLayoutOk from "../../../../contracts/api/v2/fixtures/get_library_layout_ok.json";
import listLibrarySectionsOk from "../../../../contracts/api/v2/fixtures/list_library_sections_ok.json";
import listLibraryUserCollectionsOk from "../../../../contracts/api/v2/fixtures/list_library_user_collections_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  flattenLibraryCollections,
  getLibraryCollectionList,
  useLibraryCollectionItems,
  useLibraryCollections,
  useLibraryUserCollections,
} from "./libraryCollections";
import { useLibraryLayout, useLibrarySections } from "./sections";

function createWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(handler: (url: URL) => Response): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async (input) =>
    handler(new URL(String(input), "http://localhost")),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function headersOf(fetchMock: FetchMock, index = 0): Record<string, string> {
  return (fetchMock.mock.calls[index]?.[1]?.headers ?? {}) as Record<string, string>;
}

describe("library viewer reads", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("loads the library collections tab and keeps the v1 tab shape for the page", async () => {
    const fetchMock = stubFetch(() => jsonResponse(getLibraryCollectionsOk));

    const { result } = renderHook(() => useLibraryCollections(1), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/library/1/collections");
    expect(headersOf(fetchMock)["X-Profile-Id"]).toBe("p-owner");

    const tab = result.current.data;
    expect(tab?.library_id).toBe(1);
    expect(getLibraryCollectionList(tab).map((c) => [c.id, c.library_id, c.library_ids])).toEqual(
      getLibraryCollectionsOk.collections.map((c) => [
        c.id,
        Number(c.library_id),
        c.library_ids.map(Number),
      ]),
    );
    expect(flattenLibraryCollections(tab).map((c) => c.id)).toEqual([
      ...getLibraryCollectionsOk.groups.flatMap((g) => g.collections.map((c) => c.id)),
      ...(getLibraryCollectionsOk.ungrouped?.collections.map((c) => c.id) ?? []),
    ]);
  });

  it("lists a library's user collections from the items envelope", async () => {
    stubFetch(() => jsonResponse(listLibraryUserCollectionsOk));

    const { result } = renderHook(() => useLibraryUserCollections(1), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.map((c) => [c.id, c.name, c.creator_profile_id])).toEqual(
      listLibraryUserCollectionsOk.items.map((c) => [c.id, c.name, c.creator_profile_id]),
    );
  });

  it("reads a bounded v2 collection teaser with the active profile", async () => {
    const response = {
      items: [{ content_id: "movie:heat-1995", title: "Heat" }],
      page: { has_more: false, next_cursor: null },
    };
    const fetchMock = stubFetch(() => jsonResponse(response));
    const { result } = renderHook(() => useLibraryCollectionItems(1, "c1"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v2/library/1/collections/c1/items?limit=50",
    );
    expect(headersOf(fetchMock)["X-Profile-Id"]).toBe("p-owner");
    expect(result.current.data?.items[0]?.content_id).toBe("movie:heat-1995");
    expect(result.current.data?.has_more).toBe(false);
  });

  it("loads the library layout and sections", async () => {
    const fetchMock = stubFetch((url) =>
      url.pathname.endsWith("/layout")
        ? jsonResponse(getLibraryLayoutOk)
        : jsonResponse(listLibrarySectionsOk),
    );

    const layout = renderHook(() => useLibraryLayout(1), { wrapper: createWrapper() });
    const sections = renderHook(() => useLibrarySections(1), { wrapper: createWrapper() });
    await waitFor(() => expect(layout.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(sections.result.current.isSuccess).toBe(true));

    const requested = fetchMock.mock.calls.map((call) => String(call[0])).sort();
    expect(requested).toEqual(["/api/v2/library/1/layout", "/api/v2/library/1/sections"]);
    expect(layout.result.current.data?.sections.map((s) => s.id)).toEqual(
      getLibraryLayoutOk.sections.map((s) => s.id),
    );
    const [section] = sections.result.current.data?.sections ?? [];
    expect(section?.id).toBe("continue_watching");
    expect(section?.total_count).toBe(1);
    expect(section?.items[0]?.title).toBe("Heat");
    expect(section?.items[0]?.position_seconds).toBe(1200.5);
  });
});
