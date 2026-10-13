// @vitest-environment jsdom

import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  createShuffle: vi.fn(),
  getShuffle: vi.fn(),
  startPlayback: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/api/v2/shuffles", () => ({
  createShuffle: mocks.createShuffle,
  getShuffle: mocks.getShuffle,
}));
vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: mocks.startPlayback }),
}));
vi.mock("sonner", () => ({ toast: { error: mocks.toastError } }));

import { V2ProblemError } from "@/api/v2/request";
import { shuffleKeys } from "./keys";
import { useShuffle, useStartShuffle } from "./shuffles";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>;
}

describe("useStartShuffle", () => {
  it("plays the first pick from the beginning and returns to the starting page", async () => {
    window.history.replaceState(null, "", "/library/7?tab=library");
    mocks.createShuffle.mockResolvedValue({ id: "shuffle-1", current: { content_id: "movie-1" } });
    const { result } = renderHook(() => useStartShuffle(), { wrapper });

    act(() => result.current.startShuffle({ kind: "library", id: "7" }));

    await waitFor(() => expect(mocks.startPlayback).toHaveBeenCalled());
    expect(mocks.createShuffle).toHaveBeenCalledWith({ kind: "library", id: "7" });
    expect(mocks.startPlayback).toHaveBeenCalledWith(
      {
        contentId: "movie-1",
        shuffleId: "shuffle-1",
        restart: true,
        returnHref: "/library/7?tab=library",
      },
      "viewer",
    );
  });

  it("says when nothing in the scope can play", async () => {
    mocks.createShuffle.mockRejectedValue(
      new V2ProblemError("createShuffle", {
        type: "https://siloserver.org/docs/api/v2/problems/conflict",
        title: "Conflict",
        status: 409,
        detail: "Nothing here can be played.",
        instance: "urn:silo:request:1",
      }),
    );
    const { result } = renderHook(() => useStartShuffle(), { wrapper });

    act(() => result.current.startShuffle({ kind: "season", id: "season-1" }));

    await waitFor(() =>
      expect(mocks.toastError).toHaveBeenCalledWith("Nothing here can be played."),
    );
  });

  it("fetches the shuffle again when the post-roll opens, even with a cached copy", async () => {
    const client = new QueryClient();
    client.setQueryData(shuffleKeys.detail("shuffle-1"), {
      id: "shuffle-1",
      next: { content_id: "gone" },
    });
    mocks.getShuffle.mockResolvedValue({ id: "shuffle-1", next: { content_id: "movie-2" } });

    const { result } = renderHook(() => useShuffle("shuffle-1"), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    });

    await waitFor(() => expect(result.current.data?.next.content_id).toBe("movie-2"));
    expect(mocks.getShuffle).toHaveBeenCalledOnce();
  });
});
