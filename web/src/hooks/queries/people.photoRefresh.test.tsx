import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import itemFixture from "../../../../contracts/api/v2/fixtures/get_catalog_item_ok.json";
import { catalogItemDetailFromV2 } from "@/api/v2/catalog";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useCatalogItemDetail } from "./catalogRead";
import { catalogKeys, itemKeys, personKeys } from "./keys";
import { useRefreshPerson, useUpdatePersonMetadata } from "./people";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

// Person IDs can exceed Number.MAX_SAFE_INTEGER. Match the route's string ID.
const personId = "9007199254740993";
const oldPhoto = "https://images.example.test/old.jpg";
const newPhoto = "https://images.example.test/new.jpg";
const credit = {
  person_id: personId,
  name: "Actor",
  character: "Lead",
  order: 0,
  photo_url: oldPhoto,
};

describe("person refresh and cached item credits", () => {
  const clients: QueryClient[] = [];
  beforeEach(installPolicyStorageMocks);
  afterEach(() => {
    cleanup();
    for (const client of clients.splice(0)) client.clear();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function setup({ fail = false } = {}) {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 120_000 } },
    });
    clients.push(client);
    client.setQueryData(personKeys.detail(personId), {
      id: personId,
      name: "Actor",
      photo_url: oldPhoto,
    });
    const detail = catalogItemDetailFromV2({ ...itemFixture, cast: [credit] });
    const relatedKeys = [
      catalogKeys.itemDetail(detail.content_id),
      catalogKeys.itemDetail(detail.content_id, 12),
      itemKeys.detail(detail.content_id, 12),
    ];
    for (const key of relatedKeys) client.setQueryData(key, detail);
    const unrelatedKey = catalogKeys.itemDetail("unrelated");
    client.setQueryData(unrelatedKey, { ...detail, content_id: "unrelated", cast: [] });
    const crewKey = catalogKeys.itemDetail("crew-item", 12);
    client.setQueryData(crewKey, {
      ...detail,
      content_id: "crew-item",
      cast: [],
      crew: [{ person_id: personId, name: "Actor", job: "Director" }],
    });
    const fetchMock = vi.fn<typeof fetch>(async (input) => {
      const path = String(input);
      if (path.startsWith("/api/v2/catalog/items/")) {
        return jsonResponse({ ...itemFixture, cast: [{ ...credit, photo_url: newPhoto }] });
      }
      if (fail) return jsonResponse({ title: "Refresh failed", status: 500 }, 500);
      if (path.startsWith("/api/v2/admin/people/")) {
        return jsonResponse({ id: personId, name: "Actor", photo_url: newPhoto });
      }
      if (path === `/api/v2/catalog/people/${personId}`) {
        return jsonResponse({ id: personId, name: "Actor", photo_url: oldPhoto });
      }
      return jsonResponse({ status: "queued", person_id: personId });
    });
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    return { client, detail, relatedKeys, unrelatedKey, crewKey, fetchMock, wrapper };
  }

  it.each(["refresh", "edit"] as const)(
    "shows the refreshed cast photo when returning to a cached item after %s",
    async (action) => {
      const { client, detail, relatedKeys, unrelatedKey, crewKey, wrapper } = setup();
      const { result } = renderHook(
        () => ({
          refresh: useRefreshPerson(personId, true),
          edit: useUpdatePersonMetadata(personId),
        }),
        { wrapper },
      );
      await act(async () => {
        if (action === "refresh") await result.current.refresh.mutateAsync();
        else await result.current.edit.mutateAsync({ name: "Actor" });
      });

      for (const key of relatedKeys) expect(client.getQueryState(key)?.isInvalidated).toBe(true);
      expect(client.getQueryState(unrelatedKey)?.isInvalidated).toBe(false);
      expect(client.getQueryState(crewKey)?.isInvalidated).toBe(true);

      const returned = renderHook(() => useCatalogItemDetail(detail.content_id, 12), { wrapper });
      await waitFor(() => expect(returned.result.current.data?.cast[0]?.photo_url).toBe(newPhoto));
    },
  );

  it.each(["queued", "failed"] as const)(
    "keeps cached item data when a refresh is only %s",
    async (mode) => {
      const { client, relatedKeys, wrapper } = setup({ fail: mode === "failed" });
      const { result } = renderHook(() => useRefreshPerson(personId, mode === "failed"), {
        wrapper,
      });
      await act(async () => {
        if (mode === "failed") await expect(result.current.mutateAsync()).rejects.toThrow();
        else await result.current.mutateAsync();
      });
      for (const key of relatedKeys) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
    },
  );

  it.each(["cache reset", "related items removed", "related items expire"] as const)(
    "stops queued refresh observation after %s",
    async (stopReason) => {
      vi.useFakeTimers();
      const { client, relatedKeys, crewKey, fetchMock, wrapper } = setup();
      const { result, unmount } = renderHook(() => useRefreshPerson(personId, false), { wrapper });
      await act(async () => {
        await result.current.mutateAsync();
      });
      unmount();
      await act(() => vi.advanceTimersByTimeAsync(3_000));
      expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith(personId))).toHaveLength(
        2,
      );

      if (stopReason === "cache reset") client.clear();
      else if (stopReason === "related items removed") {
        for (const queryKey of [...relatedKeys, crewKey]) {
          client.removeQueries({ queryKey, exact: true });
        }
      } else await act(() => vi.advanceTimersByTimeAsync(5 * 60_000));
      const requests = fetchMock.mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(30_000));
      expect(fetchMock).toHaveBeenCalledTimes(requests);
      if (stopReason === "cache reset") expect(client.getQueryCache().getAll()).toHaveLength(0);
    },
  );

  it("keeps observing an open person page until it unmounts even without cached items", async () => {
    vi.useFakeTimers();
    const { client, relatedKeys, crewKey, fetchMock, wrapper } = setup();
    const { result, unmount } = renderHook(
      () => {
        useQuery({
          queryKey: personKeys.detail(personId),
          queryFn: async () => ({ id: personId, name: "Actor", photo_url: oldPhoto }),
        });
        return useRefreshPerson(personId, false);
      },
      { wrapper },
    );
    await act(async () => {
      await result.current.mutateAsync();
    });
    for (const queryKey of [...relatedKeys, crewKey]) {
      client.removeQueries({ queryKey, exact: true });
    }
    const requests = fetchMock.mock.calls.length;
    await act(() => vi.advanceTimersByTimeAsync(33_000));
    expect(fetchMock.mock.calls.length).toBeGreaterThan(requests);
    unmount();
    const finalRequests = fetchMock.mock.calls.length;
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetchMock).toHaveBeenCalledTimes(finalRequests);
  });

  it("reuses the observer when the same person is queued again", async () => {
    vi.useFakeTimers();
    const { fetchMock, wrapper } = setup();
    const { result } = renderHook(() => useRefreshPerson(personId, false), { wrapper });
    await act(async () => {
      await result.current.mutateAsync();
      await result.current.mutateAsync();
    });
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith(personId))).toHaveLength(2);
  });

  it.each(["cleared", "expired"] as const)(
    "disposes the paused listener when the person cache is %s",
    async (reason) => {
      vi.useFakeTimers();
      const { client, detail, relatedKeys, crewKey, fetchMock, wrapper } = setup();
      const { result, unmount } = renderHook(() => useRefreshPerson(personId, false), { wrapper });
      await act(async () => {
        await result.current.mutateAsync();
      });
      unmount();
      for (const queryKey of [...relatedKeys, crewKey]) {
        client.removeQueries({ queryKey, exact: true });
      }
      if (reason === "cleared") client.clear();
      else await act(() => vi.advanceTimersByTimeAsync(5 * 60_000));
      expect(client.getQueryState(personKeys.detail(personId))).toBeUndefined();

      const requests = fetchMock.mock.calls.length;
      client.setQueryData(relatedKeys[0]!, detail);
      await act(() => vi.advanceTimersByTimeAsync(30_000));
      expect(fetchMock).toHaveBeenCalledTimes(requests);
      expect(client.getQueryState(personKeys.detail(personId))).toBeUndefined();
    },
  );

  it.each([false, true])(
    "ignores a refresh completed after cache clearing (admin=%s)",
    async (isAdmin) => {
      const { client, fetchMock, wrapper } = setup();
      let finishQueue!: () => void;
      const queued = new Promise<Response>((resolve) => {
        finishQueue = () =>
          resolve(
            jsonResponse(
              isAdmin
                ? { id: personId, name: "Actor", photo_url: newPhoto }
                : { status: "queued", person_id: personId },
            ),
          );
      });
      fetchMock.mockImplementation(async (input) =>
        String(input).endsWith("/refresh")
          ? queued
          : jsonResponse({ id: personId, name: "Actor", photo_url: oldPhoto }),
      );
      const { result } = renderHook(() => useRefreshPerson(personId, isAdmin), { wrapper });
      let request!: Promise<unknown>;
      act(() => {
        request = result.current.mutateAsync();
      });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
      client.clear();
      await act(async () => {
        finishQueue();
        await request;
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(client.getQueryCache().getAll()).toHaveLength(0);
    },
  );
});
