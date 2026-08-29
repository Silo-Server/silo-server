import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import { useSetCollectionSortPreference } from "./collections";

const apiMock = vi.hoisted(() => vi.fn());
const apiWithProfileRequestContextMock = vi.hoisted(() => vi.fn());
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
    expect(
      JSON.parse(apiWithProfileRequestContextMock.mock.calls[1]?.[2]?.body as string),
    ).toMatchObject({ field: "title", order: "asc" });

    second.resolve({});
  });

  // These preferences are profile-scoped and the writes are queued, so a write
  // must carry the profile that chose the sort rather than whichever household
  // member happens to be active when it finally sends.
  it("sends a queued write under the profile captured at selection time", async () => {
    const first = deferred<unknown>();
    apiWithProfileRequestContextMock.mockReturnValueOnce(first.promise).mockResolvedValue({});

    const { result } = renderSortPreferenceHook();
    const chooser = profileAuth("profile-1");

    act(() => {
      void result.current({
        collection_kind: "watchlist",
        field: "title",
        order: "asc",
        profileAuth: chooser,
      });
      void result.current({
        collection_kind: "favorites",
        field: "runtime",
        order: "desc",
        profileAuth: chooser,
      });
    });

    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(1));
    first.resolve({});
    await waitFor(() => expect(apiWithProfileRequestContextMock).toHaveBeenCalledTimes(2));

    for (const call of apiWithProfileRequestContextMock.mock.calls) {
      expect(call[0]).toBe("/collections/sort-preference");
      expect(call[1]).toBe(chooser);
      // The snapshot is request authority, not part of the stored preference.
      expect(JSON.parse(call[2]?.body as string)).not.toHaveProperty("profileAuth");
    }
  });

  // The snapshot carries the bearer access token and the profile PIN token.
  // Routing these writes through a TanStack mutation would strand both in the
  // mutation cache after the request settles, which api/client.ts forbids.
  it("keeps the profile snapshot out of cached client state", async () => {
    apiWithProfileRequestContextMock.mockResolvedValue({});
    const { result, queryClient } = renderSortPreferenceHook();

    await act(async () => {
      await result.current({
        collection_kind: "favorites",
        field: "title",
        order: "asc",
        profileAuth: profileAuth("profile-1"),
      });
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
  });

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
