import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getCatalogItemOk from "../../../../contracts/api/v2/fixtures/get_catalog_item_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { fetchCatalogItemDetail, useCatalogItemDetail } from "./catalogRead";

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(body: unknown): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(body));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestedUrl(fetchMock: FetchMock, index = 0): string {
  return String(fetchMock.mock.calls[index]?.[0]);
}

describe("catalog item reads on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the item detail and keeps the v1 detail shape for the page", async () => {
    const fetchMock = stubFetch(getCatalogItemOk);

    const detail = await fetchCatalogItemDetail("movie:heat-1995", 12);

    expect(requestedUrl(fetchMock)).toBe("/api/v2/catalog/items/movie%3Aheat-1995?library_id=12");
    expect((fetchMock.mock.calls[0]?.[1]?.headers as Record<string, string>)["X-Profile-Id"]).toBe(
      "p-owner",
    );
    expect(detail.content_id).toBe("movie:heat-1995");
    expect(detail.title).toBe("Heat");
    expect(detail.year).toBe(1995);
    // v2 omits absent strings; the page still reads v1's "" / null spellings.
    expect(detail.overview).toBe("");
    expect(detail.poster_url).toBe("");
    expect(detail.rating_imdb).toBeNull();
    expect(detail.release_date).toBeNull();
    expect(detail.intro).toBeNull();
    expect(detail.user_state).toEqual({
      played: true,
      is_favorite: true,
      in_watchlist: false,
      not_interested: false,
    });
    expect(detail.status).toBeUndefined();
  });

  it("aborts the detail fetch when its last observer unmounts", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let requestSignal: AbortSignal | null | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (_url, options) => {
        requestSignal = options?.signal;
        return new Promise<Response>((_resolve, reject) => {
          requestSignal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      }),
    );
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client: queryClient }, children);
    const { unmount } = renderHook(() => useCatalogItemDetail("movie:heat-1995", 12), { wrapper });

    await waitFor(() => expect(requestSignal).toBeDefined());
    expect(requestSignal?.aborted).toBe(false);
    unmount();
    expect(requestSignal?.aborted).toBe(true);
    queryClient.clear();
  });
});
