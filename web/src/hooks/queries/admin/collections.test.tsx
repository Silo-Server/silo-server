import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { v2Problem } from "@/api/v2/problems.test-support";
import { useDeleteAdminCollections } from "./collections";

const apiMock = vi.hoisted(() => vi.fn());
const invalidateAdminCollectionQueriesMock = vi.hoisted(() => vi.fn());
const toastSuccessMock = vi.hoisted(() => vi.fn());
const toastWarningMock = vi.hoisted(() => vi.fn());
const toastErrorMock = vi.hoisted(() => vi.fn());

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: apiMock };
});

vi.mock("../collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: invalidateAdminCollectionQueriesMock,
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    warning: toastWarningMock,
    error: toastErrorMock,
  },
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function renderDeleteCollectionsHook() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return renderHook(() => useDeleteAdminCollections(), { wrapper });
}

describe("useDeleteAdminCollections", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("deduplicates collection IDs and limits each request batch to four", async () => {
    const firstBatch = deferred<void>();
    apiMock
      .mockReturnValueOnce(firstBatch.promise)
      .mockReturnValueOnce(firstBatch.promise)
      .mockReturnValueOnce(firstBatch.promise)
      .mockReturnValueOnce(firstBatch.promise)
      .mockResolvedValue(undefined);
    invalidateAdminCollectionQueriesMock.mockResolvedValue(undefined);
    const { result } = renderDeleteCollectionsHook();

    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync(
        [
          "collection-1",
          "collection-2",
          "collection-3",
          "collection-4",
          "collection-5",
          "collection-6",
          "collection-1",
        ].map((id) => ({ id, etag: '"captured"' })),
      );
    });

    await waitFor(() => expect(apiMock).toHaveBeenCalledTimes(4));
    expect(result.current.progress).toEqual({ completed: 0, total: 6 });
    await act(async () => {
      firstBatch.resolve();
      await mutation;
    });

    expect(apiMock.mock.calls).toEqual([
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-1" }, headers: { "If-Match": '"captured"' } },
      ],
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-2" }, headers: { "If-Match": '"captured"' } },
      ],
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-3" }, headers: { "If-Match": '"captured"' } },
      ],
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-4" }, headers: { "If-Match": '"captured"' } },
      ],
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-5" }, headers: { "If-Match": '"captured"' } },
      ],
      [
        "DELETE /api/v2/admin/collections/{id}",
        { path: { id: "collection-6" }, headers: { "If-Match": '"captured"' } },
      ],
    ]);
    expect(result.current.progress).toBeNull();
    expect(toastSuccessMock).toHaveBeenCalledWith("Deleted 6 collections");
    expect(invalidateAdminCollectionQueriesMock).toHaveBeenCalledTimes(1);
  });

  it("continues after an unexpected failure and reports the partial result", async () => {
    apiMock
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("Collection is used by one or more sections"))
      .mockResolvedValueOnce(undefined);
    invalidateAdminCollectionQueriesMock.mockResolvedValue(undefined);
    const { result } = renderDeleteCollectionsHook();

    let response: unknown;
    await act(async () => {
      response = await result.current.mutateAsync(
        ["collection-1", "collection-2", "collection-3"].map((id) => ({ id, etag: '"captured"' })),
      );
    });

    expect(response).toEqual({
      requested: 3,
      deleted: 2,
      kept: 0,
      failed: 1,
      firstError: "Collection is used by one or more sections",
    });
    expect(apiMock).toHaveBeenCalledTimes(3);
    expect(toastWarningMock).toHaveBeenCalledWith("Deleted 2 of 3 collections", {
      description: "Collection is used by one or more sections",
    });
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(invalidateAdminCollectionQueriesMock).toHaveBeenCalledTimes(1);
  });

  it("treats missing collections as cleared and reports protected collections as kept", async () => {
    apiMock
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(v2Problem(404, "not_found", "Collection not found"))
      .mockRejectedValueOnce(
        v2Problem(409, "collection_in_use", "Collection is used by one or more sections"),
      );
    invalidateAdminCollectionQueriesMock.mockResolvedValue(undefined);
    const { result } = renderDeleteCollectionsHook();

    let response: unknown;
    await act(async () => {
      response = await result.current.mutateAsync(
        ["collection-1", "collection/2", "collection-3"].map((id) => ({ id, etag: '"captured"' })),
      );
    });

    expect(response).toEqual({
      requested: 3,
      deleted: 2,
      kept: 1,
      failed: 0,
      firstError: undefined,
    });
    expect(apiMock).toHaveBeenNthCalledWith(2, "DELETE /api/v2/admin/collections/{id}", {
      path: { id: "collection/2" },
      headers: { "If-Match": '"captured"' },
    });
    expect(toastWarningMock).toHaveBeenCalledWith("Deleted 2 collections", {
      description: "Kept 1 collection in use by home or library sections",
    });
    expect(toastErrorMock).not.toHaveBeenCalled();
  });
});
