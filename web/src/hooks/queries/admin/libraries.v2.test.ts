// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import checkLibraryMountOk from "../../../../../contracts/api/v2/fixtures/check_library_mount_ok.json";
import createLibraryOk from "../../../../../contracts/api/v2/fixtures/create_library_ok.json";
import deleteLibraryAccepted from "../../../../../contracts/api/v2/fixtures/delete_library_accepted.json";
import getLibraryProvidersOk from "../../../../../contracts/api/v2/fixtures/get_library_providers_ok.json";
import getMetadataMatchQueueOk from "../../../../../contracts/api/v2/fixtures/get_metadata_match_queue_ok.json";
import listLibrariesOk from "../../../../../contracts/api/v2/fixtures/list_libraries_ok.json";
import listLibraryRootsOk from "../../../../../contracts/api/v2/fixtures/list_library_roots_ok.json";
import listMetadataMatchQueuesOk from "../../../../../contracts/api/v2/fixtures/list_metadata_match_queues_ok.json";
import listStaleIdsOk from "../../../../../contracts/api/v2/fixtures/list_stale_ids_ok.json";
import listUnmatchedItemsOk from "../../../../../contracts/api/v2/fixtures/list_unmatched_items_ok.json";
import refreshLibraryMetadataAccepted from "../../../../../contracts/api/v2/fixtures/refresh_library_metadata_accepted.json";
import updateLibraryOk from "../../../../../contracts/api/v2/fixtures/update_library_ok.json";
import deleteLibraryConflict from "../../../../../contracts/api/v2/fixtures/delete_library_conflict.json";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { V2ProblemError } from "@/api/v2/request";

import {
  fetchAdminLibraries,
  fetchLibraryMetadataMatchQueuePage,
  fetchUnmatchedLibraryItemsPage,
  useCheckLibraryMount,
  useCreateLibrary,
  useDeleteLibrary,
  useLibraryMetadataMatchQueues,
  useLibraryMetadataMatchQueueDetail,
  useLibraryProviders,
  useLibraryRoots,
  flattenLibraryRoots,
  LIBRARY_ROOTS_PAGE_LIMIT,
  useRefreshLibraryMetadata,
  useSetLibraryProviders,
  useStaleMediaIDs,
  flattenStaleMediaIDs,
  STALE_MEDIA_IDS_PAGE_LIMIT,
  useUpdateLibrary,
} from "./libraries";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

function problemResponse(body: unknown, status: number) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });
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

  it("lists libraries with numeric ids from the listLibraries fixture", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listLibrariesOk));

    const libraries = await fetchAdminLibraries();

    expect(requestsOf(fetchMock)[0]?.url.pathname).toBe("/api/v2/libraries");
    expect(libraries.map((l) => [l.id, l.name])).toEqual([
      [1, "Movies"],
      [2, "Busy"],
    ]);
    expect(libraries[0]?.last_scanned_at).toBe("2026-01-02T03:04:05.678Z");
    expect(libraries[0]?.scan_warning_code).toBe("empty_root");
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

  it("updates a library with PATCH and returns the updated row", async () => {
    const fetchMock = stubFetch(() => jsonResponse(updateLibraryOk));

    const { result } = renderHook(() => useUpdateLibrary(), { wrapper: createWrapper() });
    const updated = await result.current.mutateAsync({ id: 1, body: { name: "Films" } });

    const [request] = requestsOf(fetchMock);
    expect(request?.method).toBe("PATCH");
    expect(request?.url.pathname).toBe("/api/v2/libraries/1");
    expect(request?.body).toEqual({ name: "Films" });
    expect(updated.name).toBe(updateLibraryOk.name);
  });

  it("deletes a library and returns the accepted admin job", async () => {
    stubFetch(() => jsonResponse(deleteLibraryAccepted, 202));

    const { result } = renderHook(() => useDeleteLibrary(), { wrapper: createWrapper() });
    const job = await result.current.mutateAsync(1);

    expect(job.id).toBe(deleteLibraryAccepted.id);
    expect(job.job_type).toBe("delete_library");
    expect(job.created_by_user_id).toBe(Number(deleteLibraryAccepted.created_by_user_id));
  });

  it("surfaces the 409 conflict problem when a deletion is already running", async () => {
    stubFetch(() => problemResponse(deleteLibraryConflict, 409));

    const { result } = renderHook(() => useDeleteLibrary(), { wrapper: createWrapper() });
    await expect(result.current.mutateAsync(2)).rejects.toMatchObject({
      status: 409,
      problemType: "conflict",
    });
  });

  it("queues a metadata refresh through the v2 202 answer", async () => {
    const fetchMock = stubFetch(() => jsonResponse(refreshLibraryMetadataAccepted, 202));

    const { result } = renderHook(() => useRefreshLibraryMetadata(), {
      wrapper: createWrapper(),
    });
    const job = await result.current.mutateAsync(1);

    const [request] = requestsOf(fetchMock);
    expect(request?.method).toBe("POST");
    expect(request?.url.pathname).toBe("/api/v2/libraries/1/refresh-metadata");
    expect(job.job_type).toBe("library_refresh");
  });

  it("runs a mount check and keeps the numeric library id the page keys on", async () => {
    stubFetch(() => jsonResponse(checkLibraryMountOk));

    const { result } = renderHook(() => useCheckLibraryMount(), { wrapper: createWrapper() });
    const check = await result.current.mutateAsync(1);

    expect(check.library_id).toBe(1);
    expect(check.healthy).toBe(checkLibraryMountOk.healthy);
    expect(check.roots.map((r) => r.path)).toEqual(checkLibraryMountOk.roots.map((r) => r.path));
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

    const { result } = renderHook(() => useLibraryRoots(1, "ambiguous"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    // Only the first page loads up front.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestsOf(fetchMock)[0]?.url.searchParams.get("state")).toBe("ambiguous");
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

  it("does not load library roots until the caller enables the query", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listLibraryRootsOk));

    const { result, rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) => useLibraryRoots(1, "ambiguous", { enabled }),
      { wrapper: createWrapper(), initialProps: { enabled: false } },
    );

    expect(result.current.fetchStatus).toBe("idle");
    expect(fetchMock).not.toHaveBeenCalled();

    rerender({ enabled: true });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("resolves a numeric unmatched-items page by following cursors", async () => {
    const fetchMock = stubFetch((url) => {
      expect(url.pathname).toBe("/api/v2/libraries/unmatched-items");
      expect(url.searchParams.get("q")).toBe("a");
      if (url.searchParams.get("cursor") === null) return jsonResponse(listUnmatchedItemsOk);
      return jsonResponse({
        items: [{ ...listUnmatchedItemsOk.items[0], content_id: "movie:b", title: "Beta" }],
        page: { has_more: false },
        total: listUnmatchedItemsOk.total,
      });
    });

    const first = await fetchUnmatchedLibraryItemsPage(0, "a");
    expect(first.items.map((i) => i.title)).toEqual(["Alpha"]);
    expect(first.total).toBe(listUnmatchedItemsOk.total);
    expect(first.items[0]?.library_id).toBe(1);

    const second = await fetchUnmatchedLibraryItemsPage(1, "a");
    expect(second.items.map((i) => i.title)).toEqual(["Beta"]);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("decodes the matcher backlog detail with the page fields the section reads", async () => {
    const fetchMock = stubFetch(() => jsonResponse(getMetadataMatchQueueOk));

    const page = await fetchLibraryMetadataMatchQueuePage(1);

    expect(page.library_id).toBe(1);
    expect(page.limit).toBe(10);
    expect(page.has_more).toBe(true);
    expect(page.nextCursor).toBe(getMetadataMatchQueueOk.page.next_cursor);
    expect(page.movies[0]?.failure_kind).toBe("no_match");
    expect(page.movies[0]?.failure_detail).toEqual({ candidates: 0 });

    // A later page is requested with its cursor directly, not by replaying earlier pages.
    await fetchLibraryMetadataMatchQueuePage(1, page.nextCursor);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(requestsOf(fetchMock)[1]?.url.searchParams.get("cursor")).toBe(
      getMetadataMatchQueueOk.page.next_cursor,
    );
  });

  it("retains the matcher backlog cursor chain across refetches", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.searchParams.get("cursor") === null) return jsonResponse(getMetadataMatchQueueOk);
      return jsonResponse({
        ...getMetadataMatchQueueOk,
        movies: [{ ...getMetadataMatchQueueOk.movies[0], media_file_id: "121" }],
        page: { has_more: false },
      });
    });

    const { result } = renderHook(() => useLibraryMetadataMatchQueueDetail(1), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.pages).toHaveLength(1);
    await result.current.fetchNextPage();
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
    expect(fetchMock).toHaveBeenCalledTimes(2);

    await result.current.refetch();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4));
    // The refetch replays exactly the loaded pages with their stored cursors.
    expect(requestsOf(fetchMock).map((r) => r.url.searchParams.get("cursor"))).toEqual([
      null,
      getMetadataMatchQueueOk.page.next_cursor,
      null,
      getMetadataMatchQueueOk.page.next_cursor,
    ]);
    expect(result.current.data?.pages[1]?.movies[0]?.media_file_id).toBe("121");
  });

  it("lists matcher queue statuses with numeric library ids", async () => {
    stubFetch(() => jsonResponse(listMetadataMatchQueuesOk));

    const queues = renderHook(() => useLibraryMetadataMatchQueues(), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(queues.result.current.isSuccess).toBe(true));

    expect(queues.result.current.data?.map((q) => q.library_id)).toEqual(
      listMetadataMatchQueuesOk.items.map((q) => Number(q.library_id)),
    );
  });

  it("pages the stale ids listing on demand with numeric library ids", async () => {
    const fetchMock = stubFetch((url) => {
      expect(url.pathname).toBe("/api/v2/libraries/stale-ids");
      expect(url.searchParams.get("limit")).toBe(String(STALE_MEDIA_IDS_PAGE_LIMIT));
      if (url.searchParams.get("cursor") === null) return jsonResponse(listStaleIdsOk);
      expect(url.searchParams.get("cursor")).toBe(listStaleIdsOk.page.next_cursor);
      return jsonResponse({
        items: [{ ...listStaleIdsOk.items[0], content_id: "series:lost-2004", library_id: "0" }],
        page: { has_more: false },
      });
    });

    const { result, rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) => useStaleMediaIDs({ enabled }),
      { wrapper: createWrapper(), initialProps: { enabled: false } },
    );

    // Nothing loads until the diagnostics section opens.
    expect(result.current.fetchStatus).toBe("idle");
    expect(fetchMock).not.toHaveBeenCalled();

    rerender({ enabled: true });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result.current.hasNextPage).toBe(true);
    expect(flattenStaleMediaIDs(result.current.data).map((s) => s.library_id)).toEqual(
      listStaleIdsOk.items.map((s) => Number(s.library_id)),
    );

    await result.current.fetchNextPage();
    await waitFor(() => expect(result.current.hasNextPage).toBe(false));
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(flattenStaleMediaIDs(result.current.data).map((s) => s.content_id)).toEqual([
      ...listStaleIdsOk.items.map((s) => s.content_id),
      "series:lost-2004",
    ]);
    expect(flattenStaleMediaIDs(result.current.data)[2]?.library_id).toBe(0);
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

  it("throws V2ProblemError instances so callers can branch on the problem type", async () => {
    stubFetch(() => problemResponse(deleteLibraryConflict, 409));
    const { result } = renderHook(() => useDeleteLibrary(), { wrapper: createWrapper() });
    await expect(result.current.mutateAsync(2)).rejects.toBeInstanceOf(V2ProblemError);
  });
});
