// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import createLibraryOk from "../../../../../contracts/api/v2/fixtures/create_library_ok.json";
import getLibraryProvidersOk from "../../../../../contracts/api/v2/fixtures/get_library_providers_ok.json";
import listLibraryRootsOk from "../../../../../contracts/api/v2/fixtures/list_library_roots_ok.json";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  useSetUnmatchedItemIgnored,
  useCreateLibrary,
  useLibraryProviders,
  useLibraryRoots,
  flattenLibraryRoots,
  LIBRARY_ROOTS_PAGE_LIMIT,
  useSetLibraryProviders,
} from "./libraries";

const toastMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast: toastMocks }));

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function requestsOf(fetchMock: FetchMock) {
  return fetchMock.mock.calls.map(([input, init]) => ({
    url: new URL(String(input), "http://localhost"),
    method: init?.method ?? "GET",
    body: typeof init?.body === "string" ? (JSON.parse(init.body) as unknown) : init?.body,
  }));
}

function stubFetch(handler: (url: URL, init: RequestInit | undefined) => Response) {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) =>
    handler(new URL(String(input), "http://localhost"), init),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("library admin hooks on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a library on POST /api/v2/libraries and sends only the declared members", async () => {
    const fetchMock = stubFetch(() => jsonResponse(createLibraryOk, 201));

    const { result } = renderHook(() => useCreateLibrary(), { wrapper: createWrapper() });
    const created = await result.current.mutateAsync({
      paths: ["/media/movies"],
      type: "movies",
      name: "Movies",
      enabled: true,
      auto_translate_metadata: false,
      metadata_language: "en",
      trailer_kinds: ["trailer"],
    });

    const [request] = requestsOf(fetchMock);
    expect(request?.method).toBe("POST");
    expect(request?.url.pathname).toBe("/api/v2/libraries");
    expect(request?.body).toEqual({
      paths: ["/media/movies"],
      type: "movies",
      name: "Movies",
      metadata_language: "en",
      trailer_kinds: ["trailer"],
    });
    expect(created.id).toBe(Number(createLibraryOk.id));
  });

  it("pages the library roots listing on demand", async () => {
    const fetchMock = stubFetch((url) => {
      expect(url.pathname).toBe("/api/v2/libraries/roots");
      expect(url.searchParams.get("library_id")).toBe("1");
      expect(url.searchParams.get("limit")).toBe(String(LIBRARY_ROOTS_PAGE_LIMIT));
      if (url.searchParams.get("cursor") === null) return jsonResponse(listLibraryRootsOk);
      expect(url.searchParams.get("cursor")).toBe(listLibraryRootsOk.page.next_cursor);
      return jsonResponse({
        items: [{ ...listLibraryRootsOk.items[0], root_path: "/media/movies/Heat" }],
        page: { has_more: false },
        total: 3,
      });
    });

    const { result } = renderHook(() => useLibraryRoots(1, "ambiguous", { search: " Heat " }), {
      wrapper: createWrapper(),
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
      expect(result.current.data).toBeDefined();
    });

    // Only the first page loads up front, with the trimmed search sent to the server.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestsOf(fetchMock)[0]?.url.searchParams.get("state")).toBe("ambiguous");
    expect(requestsOf(fetchMock)[0]?.url.searchParams.get("q")).toBe("Heat");
    expect(result.current.hasNextPage).toBe(true);
    expect(result.current.data?.pages[0]?.total).toBe(3);
    expect(flattenLibraryRoots(result.current.data).map((r) => r.root_path)).toEqual([
      "/media/movies/Alien",
      "/media/movies/Blade Runner",
    ]);
    expect(flattenLibraryRoots(result.current.data)[0]?.library_id).toBe(1);

    await result.current.fetchNextPage();
    await waitFor(() => expect(result.current.hasNextPage).toBe(false));

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(flattenLibraryRoots(result.current.data).map((r) => r.root_path)).toEqual([
      "/media/movies/Alien",
      "/media/movies/Blade Runner",
      "/media/movies/Heat",
    ]);
  });

  it("ignores an unmatched item and undoes it from the toast", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }));
    toastMocks.success.mockClear();

    const { result } = renderHook(() => useSetUnmatchedItemIgnored(), {
      wrapper: createWrapper(),
    });
    await result.current.mutateAsync({ contentId: "movie:a", title: "Alpha", ignored: true });

    expect(
      requestsOf(fetchMock).map((r) => [r.method, decodeURIComponent(r.url.pathname)]),
    ).toEqual([["PUT", "/api/v2/libraries/unmatched-items/movie:a/ignore"]]);
    expect(toastMocks.success).toHaveBeenCalledWith(
      "Ignored Alpha",
      expect.objectContaining({ action: expect.objectContaining({ label: "Undo" }) }),
    );

    const [, options] = toastMocks.success.mock.calls[0] as [
      string,
      { action: { onClick: () => void } },
    ];
    options.action.onClick();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(requestsOf(fetchMock)[1]?.method).toBe("DELETE");
    expect(decodeURIComponent(requestsOf(fetchMock)[1]?.url.pathname ?? "")).toBe(
      "/api/v2/libraries/unmatched-items/movie:a/ignore",
    );
  });

  it("maps the provider chain both ways between the level list and the form map", async () => {
    const fetchMock = stubFetch((url) =>
      url.pathname.endsWith("/providers") && fetchMock.mock.calls.length === 1
        ? jsonResponse(getLibraryProvidersOk)
        : new Response(null, { status: 204 }),
    );

    const providers = renderHook(() => useLibraryProviders(1), { wrapper: createWrapper() });
    await waitFor(() => expect(providers.result.current.isSuccess).toBe(true));
    expect(providers.result.current.data).toEqual({
      levels: {
        movie: [
          {
            plugin_installation_id: 3,
            capability_id: "tmdb",
            provider_slug: "tmdb",
            priority: 0,
            enabled: true,
            local: false,
          },
        ],
      },
    });

    const set = renderHook(() => useSetLibraryProviders(), { wrapper: createWrapper() });
    await set.result.current.mutateAsync({
      id: 1,
      body: {
        levels: {
          movie: [{ plugin_installation_id: 3, capability_id: "tmdb", priority: 0, enabled: true }],
        },
      },
    });

    const request = requestsOf(fetchMock)[1];
    expect(request?.method).toBe("PUT");
    expect(request?.url.pathname).toBe("/api/v2/libraries/1/providers");
    expect(request?.body).toEqual({
      levels: [
        {
          content_level: "movie",
          entries: [
            { plugin_installation_id: "3", capability_id: "tmdb", priority: 0, enabled: true },
          ],
        },
      ],
    });
  });
});
