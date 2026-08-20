import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useSetCollectionSortPreference } from "./collections";

const apiMock = vi.hoisted(() => vi.fn());

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock };
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("useSetCollectionSortPreference", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("serializes writes so the latest sort choice is persisted last", async () => {
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    apiMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useSetCollectionSortPreference(), { wrapper });

    act(() => {
      result.current.mutate({
        collection_kind: "library",
        collection_id: "collection-1",
        field: "year",
        order: "desc",
      });
      result.current.mutate({
        collection_kind: "library",
        collection_id: "collection-1",
        field: "title",
        order: "asc",
      });
    });

    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(1));

    first.resolve({});
    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(apiMock.mock.calls[1]?.[1]?.body as string)).toMatchObject({
      field: "title",
      order: "asc",
    });

    second.resolve({});
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });
});
