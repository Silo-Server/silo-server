import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  invalidateMediaSurfaceQueries: vi.fn(),
  removeItemFromHomeSectionCaches: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  useMutation: vi.fn(),
  useQueryClient: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");

  return {
    ...actual,
    useMutation: (...args: unknown[]) => mocks.useMutation(...args),
    useQueryClient: () => mocks.useQueryClient(),
  };
});

vi.mock("@/api/client", () => ({
  api: (...args: unknown[]) => mocks.api(...args),
}));

vi.mock("./mediaSurfaceRefresh", () => ({
  invalidateMediaSurfaceQueries: (...args: unknown[]) =>
    mocks.invalidateMediaSurfaceQueries(...args),
  removeItemFromHomeSectionCaches: (...args: unknown[]) =>
    mocks.removeItemFromHomeSectionCaches(...args),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

import { type DismissHomeItemVariables, useDismissHomeItem } from "./homeDismissals";

type MutationOptions = {
  mutationFn: (variables: DismissHomeItemVariables) => Promise<unknown>;
  onError?: (error: unknown) => void;
  onSuccess?: (data: unknown, variables: DismissHomeItemVariables) => Promise<unknown> | unknown;
};

function latestMutationOptions(index = 1): MutationOptions {
  return mocks.useMutation.mock.calls[index]?.[0] as MutationOptions;
}

describe("home dismissal query hooks", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient();

    mocks.api.mockReset();
    mocks.invalidateMediaSurfaceQueries.mockReset();
    mocks.removeItemFromHomeSectionCaches.mockReset();
    mocks.toastError.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.useMutation.mockReset();
    mocks.useQueryClient.mockReset();

    mocks.useMutation.mockImplementation((options: unknown) => ({
      ...(options as object),
      mutate: vi.fn(),
    }));
    mocks.useQueryClient.mockReturnValue(queryClient);
  });

  it("calls the continue watching dismissal endpoint with progress_updated_at", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    await mutation.mutationFn({
      itemId: "ep-1",
      surface: "continue_watching",
      progressUpdatedAt: "2026-03-22T18:10:00Z",
    });

    expect(mocks.api).toHaveBeenCalledWith("/home/dismissals/continue_watching/ep-1", {
      method: "PUT",
      body: JSON.stringify({
        progress_updated_at: "2026-03-22T18:10:00Z",
      }),
    });
  });

  it("encodes item IDs in the dismissal path", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    await mutation.mutationFn({
      itemId: "ebook 1/isbn:978",
      surface: "continue_watching",
      progressUpdatedAt: "2026-03-22T18:10:00Z",
    });

    expect(mocks.api).toHaveBeenCalledWith(
      "/home/dismissals/continue_watching/ebook%201%2Fisbn%3A978",
      {
        method: "PUT",
        body: JSON.stringify({
          progress_updated_at: "2026-03-22T18:10:00Z",
        }),
      },
    );
  });

  it("calls the next up dismissal endpoint with series_id", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    await mutation.mutationFn({
      itemId: "ep-2",
      surface: "next_up",
      seriesId: "series-1",
    });

    expect(mocks.api).toHaveBeenCalledWith("/home/dismissals/next_up/ep-2", {
      method: "PUT",
      body: JSON.stringify({
        series_id: "series-1",
      }),
    });
  });

  it("invalidates media surfaces and shows an undo toast on success", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    const variables: DismissHomeItemVariables = {
      itemId: "ep-1",
      surface: "continue_watching",
      mediaType: "episode",
      progressUpdatedAt: "2026-03-22T18:10:00Z",
    };

    await mutation.onSuccess?.(undefined, variables);

    expect(mocks.removeItemFromHomeSectionCaches).toHaveBeenCalledWith(
      queryClient,
      "ep-1",
      "continue_watching",
    );
    expect(mocks.invalidateMediaSurfaceQueries).toHaveBeenCalledWith(queryClient, {
      itemId: "ep-1",
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      "Removed from Continue Watching",
      expect.objectContaining({
        action: expect.objectContaining({
          label: "Undo",
        }),
      }),
    );
  });

  it("uses continue listening toast copy for audiobook dismissals", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    const variables: DismissHomeItemVariables = {
      itemId: "book-1",
      surface: "continue_watching",
      mediaType: "audiobook",
      progressUpdatedAt: "2026-03-22T18:10:00Z",
    };

    await mutation.onSuccess?.(undefined, variables);

    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      "Removed from Continue Listening",
      expect.objectContaining({
        action: expect.objectContaining({
          label: "Undo",
        }),
      }),
    );
  });

  it("uses continue reading toast copy for ebook dismissals", async () => {
    useDismissHomeItem();
    const mutation = latestMutationOptions();

    const variables: DismissHomeItemVariables = {
      itemId: "ebook-1",
      surface: "continue_watching",
      mediaType: "ebook",
      progressUpdatedAt: "2026-03-22T18:10:00Z",
    };

    await mutation.onSuccess?.(undefined, variables);

    expect(mocks.toastSuccess).toHaveBeenCalledWith(
      "Removed from Continue Reading",
      expect.objectContaining({
        action: expect.objectContaining({
          label: "Undo",
        }),
      }),
    );
  });
});
