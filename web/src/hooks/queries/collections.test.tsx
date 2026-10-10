import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { Collection, CollectionsListResponse } from "@/api/types";
import {
  useAddItemToCollection,
  useRemoveCollectionItem,
  useReorderCollectionItems,
  useReorderCollections,
  useSetCollectionSortPreference,
} from "./collections";
import { collectionKeys, libraryCollectionKeys } from "./keys";

const apiMock = vi.hoisted(() => vi.fn());
const apiWithProfileRequestContextMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: apiWithProfileRequestContextMock,
}));
const errorToast = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: errorToast, success: vi.fn() } }));
// Both guards are stubbed so these tests exercise the hook's own branching
// rather than the client module's internal auth-generation counter.
const isProfileRequestContextCurrentMock = vi.hoisted(() => vi.fn(() => true));
const isCapturedProfileAuthorityActiveMock = vi.hoisted(() => vi.fn(() => true));

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    api: apiMock,
    apiWithProfileRequestContext: apiWithProfileRequestContextMock,
    isProfileRequestContextCurrent: isProfileRequestContextCurrentMock,
    isCapturedProfileAuthorityActive: isCapturedProfileAuthorityActiveMock,
  };
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function profileAuth(profileId: string): ProfileRequestContextSnapshot {
  return {
    accessToken: "access-token-secret",
    authContextVersion: 1,
    serverOrigin: globalThis.location?.origin ?? "",
    profileId,
    profileToken: "pin-token-secret",
  };
}

function renderSortPreferenceHook() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, ...renderHook(() => useSetCollectionSortPreference(), { wrapper }) };
}

describe("useSetCollectionSortPreference", () => {
  afterEach(() => {
    vi.clearAllMocks();
    isProfileRequestContextCurrentMock.mockReturnValue(true);
    isCapturedProfileAuthorityActiveMock.mockReturnValue(true);
  });

  it("serializes writes so the latest sort choice is persisted last", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    apiWithProfileRequestContextMock
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    const { result } = renderSortPreferenceHook();

    act(() => {
      void result.current({
        collection_kind: "library",
        collection_id: "collection-1",
        field: "year",
        order: "desc",
        profileAuth: profileAuth("profile-1"),
      });
      void result.current({
        collection_kind: "library",
        collection_id: "collection-1",
        field: "title",
        order: "asc",
        profileAuth: profileAuth("profile-1"),
      });
    });

    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(1));

    first.resolve({});
    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(2));
    expect(apiWithProfileRequestContextMock.mock.calls[1]?.[1]?.body).toMatchObject({
      field: "title",
      order: "asc",
    });

    second.resolve({});
  });

  // These preferences are profile-scoped and the writes are queued, so a write
  // must carry the profile that chose the sort rather than whichever household
  // member happens to be active when it finally sends.
  it("sends a queued write under the profile captured at selection time", async () => {
    const first = deferred<unknown>();
    apiWithProfileRequestContextMock.mockReturnValueOnce(first.promise).mockResolvedValue({});

    const { result, queryClient } = renderSortPreferenceHook();
    const writes: Promise<void>[] = [];
    const chooser = profileAuth("profile-1");

    act(() => {
      writes.push(
        result.current({
          collection_kind: "watchlist",
          field: "title",
          order: "asc",
          profileAuth: chooser,
        }),
      );
      writes.push(
        result.current({
          collection_kind: "favorites",
          field: "runtime",
          order: "desc",
          profileAuth: chooser,
        }),
      );
    });

    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(1));
    first.resolve({});
    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(2));

    await act(async () => {
      await Promise.all(writes);
    });
    expect(queryClient.getMutationCache().getAll()).toHaveLength(0);
    const cached = JSON.stringify([
      queryClient.getMutationCache().getAll(),
      queryClient
        .getQueryCache()
        .getAll()
        .map((query) => query.state),
    ]);
    expect(cached).not.toContain("access-token-secret");
    expect(cached).not.toContain("pin-token-secret");

    for (const call of apiWithProfileRequestContextMock.mock.calls) {
      expect(call[0]).toBe("PUT /api/v2/collections/sort-preference");
      expect(call[1].profileContext).toBe(chooser);
      // The snapshot is request authority, not part of the stored preference.
      expect(call[1]?.body).not.toHaveProperty("profileAuth");
    }
  });

  // The snapshot carries the bearer access token and the profile PIN token.
  // Routing these writes through a TanStack mutation would strand both in the
  // mutation cache after the request settles, which api/client.ts forbids.

  it("does not invalidate another profile's catalog after a profile switch", async () => {
    apiWithProfileRequestContextMock.mockResolvedValue({});
    isCapturedProfileAuthorityActiveMock.mockReturnValue(false);

    const { result, queryClient } = renderSortPreferenceHook();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await act(async () => {
      await result.current({
        collection_kind: "watchlist",
        field: "title",
        order: "asc",
        profileAuth: profileAuth("profile-1"),
      });
    });

    expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(1);
    expect(invalidate).not.toHaveBeenCalled();
  });
});

describe("reordering personal collections", () => {
  afterEach(() => vi.clearAllMocks());

  function listed(id: string, creator: string, sortOrder: number) {
    return { id, creator_profile_id: creator, sort_order: sortOrder } as Collection;
  }

  it("sends a flat order and moves only the profile's own collections in the cache", async () => {
    const pending = deferred<unknown>();
    apiWithProfileRequestContextMock.mockReturnValue(pending.promise);
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    queryClient.setQueryData<CollectionsListResponse>(collectionKeys.list(), {
      collections: [
        listed("mine-a", "p-me", 0),
        listed("mine-b", "p-me", 1),
        listed("theirs", "p-parent", 0),
      ],
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useReorderCollections(), { wrapper });

    act(() => {
      result.current.mutate({ orderedIds: ["mine-b", "mine-a"], etag: '"order"' });
    });

    await waitFor(() =>
      expect(
        queryClient
          .getQueryData<CollectionsListResponse>(collectionKeys.list())
          ?.collections.map((c) => [c.id, c.sort_order]),
      ).toEqual([
        ["mine-b", 0],
        ["mine-a", 1],
        ["theirs", 0],
      ]),
    );
    expect(apiWithProfileRequestContextMock).toHaveBeenCalledWith("PUT /api/v2/collections/order", {
      headers: { "If-Match": '"order"' },
      body: { ordered_ids: ["mine-b", "mine-a"] },
    });
    pending.resolve({});
  });
});

describe("personal collection writes refresh library Collections tabs", () => {
  afterEach(() => vi.clearAllMocks());

  // A personal collection shown on the Collections tab is also listed, with its
  // item count and poster, on each library's Collections tab.
  const writes: Array<[string, () => () => Promise<unknown>]> = [
    [
      "adding an item",
      () => {
        const m = useAddItemToCollection();
        return () => m.mutateAsync({ collectionId: "c", mediaItemId: "m" });
      },
    ],
    [
      "removing an item",
      () => {
        const m = useRemoveCollectionItem("c");
        return () => m.mutateAsync("m");
      },
    ],
    [
      "reordering items",
      () => {
        const m = useReorderCollectionItems("c");
        return () => m.mutateAsync({ orderedIds: ["m"], etag: '"items"' });
      },
    ],
    [
      "reordering collections",
      () => {
        const m = useReorderCollections();
        return () => m.mutateAsync({ orderedIds: ["c"], etag: '"order"' });
      },
    ],
  ];

  it.each(writes)("%s marks loaded library Collections tabs stale", async (_name, useWrite) => {
    apiWithProfileRequestContextMock.mockReset().mockResolvedValue({});
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    const libraryTab = [...libraryCollectionKeys.all, 7];
    queryClient.setQueryData(libraryTab, []);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useWrite(), { wrapper });

    await act(async () => {
      await result.current();
    });

    await waitFor(() => expect(queryClient.getQueryState(libraryTab)?.isInvalidated).toBe(true));
  });
});
