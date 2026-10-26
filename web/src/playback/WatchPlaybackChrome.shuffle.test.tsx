// @vitest-environment jsdom

import { useEffect, type ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";
import type { Shuffle } from "@/api/v2/shuffles";
import { shuffleKeys } from "@/hooks/queries/keys";
import { createWatchRouteRequest, type WatchPlaybackStartInput } from "@/pages/watchRouteHelpers";
import type { WatchPage } from "@/player/components/WatchPage";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

type WatchPageProps = ComponentProps<typeof WatchPage>;

function shuffleWith(current: string, next: string): Shuffle {
  const card = (id: string) => ({
    content_id: id,
    type: "movie",
    title: `Title ${id}`,
    runtime: 100,
  });
  return {
    id: "shuffle-1",
    scope: { kind: "library", id: "1", title: "Movies" },
    current: card(current),
    next: card(next),
    created_at: "2026-10-04T00:00:00.000Z",
    updated_at: "2026-10-04T00:00:00.000Z",
  } as unknown as Shuffle;
}

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  player: { props: null as WatchPageProps | null },
  useSeriesEpisodes: vi.fn(() => ({ episodes: [] })),
  // One object per item for every render, as the query cache would return.
  details: {
    "movie-1": { data: { content_id: "movie-1", title: "Movie One" }, error: null },
    "movie-2": { data: { content_id: "movie-2", title: "Movie Two" }, error: null },
    "movie-parts": {
      data: {
        content_id: "movie-parts",
        title: "Two-Part Movie",
        playback_variants: [
          {
            parts: [
              { part_index: 0, default_file_id: 11, versions: [{ file_id: 11 }] },
              { part_index: 1, default_file_id: 12, versions: [{ file_id: 12 }] },
            ],
          },
        ],
      },
      error: null,
    },
    "episode-1": {
      data: {
        content_id: "episode-1",
        title: "Episode 1",
        series_id: "series-1",
        season_number: 1,
        episode_number: 1,
      },
      error: null,
    },
  } as Record<string, { data: unknown; error: null }>,
  getShuffle: vi.fn(),
  advanceShuffle: vi.fn(),
  skipShuffleItem: vi.fn(),
  deleteShuffle: vi.fn(),
}));

vi.mock("@/api/v2/shuffles", () => ({
  getShuffle: mocks.getShuffle,
  advanceShuffle: mocks.advanceShuffle,
  skipShuffleItem: mocks.skipShuffleItem,
  deleteShuffle: mocks.deleteShuffle,
}));
vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.navigate,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: 1 } }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile-1" } }),
}));
vi.mock("@/hooks/queries/seekPreferences", () => ({
  useSeekPreferences: () => ({ skipBack: 10, skipForward: 10 }),
}));
vi.mock("@/hooks/queries/settingValues", () => ({
  settingsCapabilitiesSupportKey: () => false,
  useEffectiveSettings: () => ({ data: undefined }),
  useSettingsCapabilities: () => ({ data: undefined, isSuccess: false }),
  useSetSettingValue: () => ({ isPending: false, mutateAsync: vi.fn() }),
  useClearSettingValue: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));
vi.mock("@/hooks/useDateTimeFormat", () => ({ useDateTimeFormat: () => undefined }));
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));
vi.mock("@/hooks/queries/progress", () => ({
  useContinueWatching: () => ({ items: [] }),
}));
vi.mock("@/hooks/queries/items", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/items")>()),
  useWatchDetail: (contentId: string) =>
    mocks.details[contentId] ?? { data: undefined, error: null },
}));
vi.mock("@/pages/watchRouteHelpers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/pages/watchRouteHelpers")>()),
  buildWatchPageProps: () => ({}),
}));
vi.mock("@/player/hooks/useSeriesEpisodes", () => ({
  useSeriesEpisodes: mocks.useSeriesEpisodes,
}));
vi.mock("@/player/components/WatchPage", () => ({
  WatchPage: (props: WatchPageProps) => {
    mocks.player.props = props;
    return <div data-testid="player" />;
  },
}));

const DURATION = 6000;

async function renderPlayback(input: WatchPlaybackStartInput, client = new QueryClient()) {
  const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };
  function ControllerProbe() {
    const controller = useWatchPlaybackController();
    useEffect(() => {
      controllerRef.current = controller;
    });
    return null;
  }

  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/watch/${input.contentId}`]}>
        <WatchPlaybackProvider>
          <ControllerProbe />
          <WatchPlaybackHost />
        </WatchPlaybackProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  act(() => controllerRef.current!.syncRouteRequest(createWatchRouteRequest(input)));
  await screen.findByTestId("player");

  const player = () => mocks.player.props!;
  const nearEnd = () =>
    act(() =>
      player().onPlaybackStateChange?.({
        currentTime: DURATION - 20,
        duration: DURATION,
        playing: true,
      }),
    );
  return { controller: () => controllerRef.current!, player, nearEnd };
}

describe("WatchPlaybackHost shuffle", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.player.props = null;
    mocks.useSeriesEpisodes.mockClear();
    mocks.getShuffle.mockReset().mockResolvedValue(shuffleWith("movie-1", "movie-2"));
    mocks.advanceShuffle.mockReset().mockResolvedValue(shuffleWith("movie-2", "movie-3"));
    mocks.skipShuffleItem.mockReset().mockResolvedValue(shuffleWith("movie-1", "movie-4"));
    mocks.deleteShuffle.mockReset().mockResolvedValue(undefined);
  });
  afterEach(cleanup);

  it("shows the shuffle's next pick when a shuffled movie nears its end", async () => {
    const movie = await renderPlayback({ contentId: "movie-1", shuffleId: "shuffle-1" });

    movie.nearEnd();
    expect(movie.controller().state.mode).toBe("post-roll");
    expect(await screen.findByText("Shuffling Movies")).toBeTruthy();
    expect(screen.getByText("Up Next at Random")).toBeTruthy();
    expect(screen.getByText("Title movie-2")).toBeTruthy();
    expect(screen.queryByText(/S0:E0/)).toBeNull();
  });

  it("advances the shuffle and plays its pick from the start in the same shuffle", async () => {
    const movie = await renderPlayback({
      contentId: "movie-1",
      shuffleId: "shuffle-1",
      returnHref: "/library/1",
    });
    movie.nearEnd();

    fireEvent.click(await screen.findByRole("button", { name: "Play Now" }));

    await waitFor(() => expect(movie.controller().state.request?.contentId).toBe("movie-2"));
    expect(mocks.advanceShuffle).toHaveBeenCalledWith("shuffle-1", "movie-1");
    expect(movie.controller().state.request).toMatchObject({
      shuffleId: "shuffle-1",
      restart: true,
      returnHref: "/library/1",
    });
    expect(mocks.navigate).toHaveBeenCalledWith(
      "/watch/movie-2?shuffle=shuffle-1&restart=1",
      expect.anything(),
    );
  });

  it("replaces the next pick when the viewer picks another", async () => {
    const movie = await renderPlayback({ contentId: "movie-1", shuffleId: "shuffle-1" });
    movie.nearEnd();

    fireEvent.click(await screen.findByRole("button", { name: "Pick Another" }));

    expect(await screen.findByText("Title movie-4")).toBeTruthy();
    expect(mocks.skipShuffleItem).toHaveBeenCalledWith("shuffle-1", "movie-2");
  });

  it("stops the shuffle and returns to where it started", async () => {
    const movie = await renderPlayback({
      contentId: "movie-1",
      shuffleId: "shuffle-1",
      returnHref: "/library/1",
    });
    movie.nearEnd();

    fireEvent.click(await screen.findByRole("button", { name: "Stop shuffling" }));

    expect(mocks.deleteShuffle).toHaveBeenCalledWith("shuffle-1");
    expect(movie.controller().state.request).toBeNull();
    expect(mocks.navigate).toHaveBeenCalledWith(
      "/library/1",
      expect.objectContaining({ up: true }),
    );
  });

  it("does not restart playback when the shuffle stops while Play Now is in flight", async () => {
    let finishAdvance: (value: Shuffle) => void = () => {};
    mocks.advanceShuffle.mockReturnValue(
      new Promise<Shuffle>((resolve) => {
        finishAdvance = resolve;
      }),
    );
    const movie = await renderPlayback({
      contentId: "movie-1",
      shuffleId: "shuffle-1",
      returnHref: "/library/1",
    });
    movie.nearEnd();

    fireEvent.click(await screen.findByRole("button", { name: "Play Now" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop shuffling" }));
    await act(async () => {
      finishAdvance(shuffleWith("movie-2", "movie-3"));
    });

    expect(movie.controller().state.request).toBeNull();
    expect(mocks.navigate).not.toHaveBeenCalledWith(
      expect.stringContaining("/watch/movie-2"),
      expect.anything(),
    );
  });

  it("shows Finished, not the cached pick, when nothing in the scope can play any more", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(shuffleKeys.detail("shuffle-1"), shuffleWith("movie-1", "movie-2"));
    mocks.getShuffle.mockRejectedValue(
      new V2ProblemError("getShuffle", {
        type: "https://siloserver.org/docs/api/v2/problems/conflict",
        title: "Conflict",
        status: 409,
        detail: "Nothing here can be played.",
        instance: "urn:silo:request:1",
      }),
    );
    const movie = await renderPlayback({ contentId: "movie-1", shuffleId: "shuffle-1" }, client);

    movie.nearEnd();

    expect(await screen.findByText("Finished")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Play Now" })).toBeNull();
    expect(screen.queryByText("Title movie-2")).toBeNull();
  });

  it("keeps the last pick when reading the shuffle fails for another reason", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(shuffleKeys.detail("shuffle-1"), shuffleWith("movie-1", "movie-2"));
    mocks.getShuffle.mockRejectedValue(new Error("network down"));
    const movie = await renderPlayback({ contentId: "movie-1", shuffleId: "shuffle-1" }, client);

    movie.nearEnd();

    expect(await screen.findByText("Title movie-2")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Play Now" })).toBeTruthy();
    await waitFor(() => expect(mocks.getShuffle).toHaveBeenCalled());
    expect(screen.queryByText("Finished")).toBeNull();
  });

  it("shows Finished rather than replaying the only playable item", async () => {
    mocks.getShuffle.mockResolvedValue(shuffleWith("movie-1", "movie-1"));
    const movie = await renderPlayback({ contentId: "movie-1", shuffleId: "shuffle-1" });

    movie.nearEnd();

    expect(await screen.findByText("Shuffling Movies")).toBeTruthy();
    expect(screen.getByText("Finished")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Play Now" })).toBeNull();
    fireEvent.keyDown(document, { key: "Enter" });
    expect(mocks.advanceShuffle).not.toHaveBeenCalled();
  });

  it("plays a later part of a shuffled movie before moving on", async () => {
    const movie = await renderPlayback({ contentId: "movie-parts", shuffleId: "shuffle-1" });

    movie.nearEnd();
    expect(movie.controller().state.mode).toBe("foreground");

    act(() =>
      movie.player().onEnded?.({ positionSeconds: 0, durationSeconds: DURATION, lastFileId: 11 }),
    );

    // Part two starts inside the same shuffle; only its end moves the shuffle on.
    expect(mocks.navigate).toHaveBeenCalledWith(
      "/watch/movie-parts?fileId=12&shuffle=shuffle-1",
      expect.anything(),
    );
    expect(mocks.advanceShuffle).not.toHaveBeenCalled();
  });

  it("drops the series order for a shuffled episode", async () => {
    await renderPlayback({ contentId: "episode-1", shuffleId: "shuffle-1" });

    expect(mocks.useSeriesEpisodes).toHaveBeenCalled();
    for (const [seriesId] of mocks.useSeriesEpisodes.mock.calls as unknown as [
      string | undefined,
    ][]) {
      expect(seriesId).toBeUndefined();
    }
  });

  it("still ends an unshuffled movie on its detail page", async () => {
    const movie = await renderPlayback({ contentId: "movie-1" });

    movie.nearEnd();
    expect(movie.controller().state.mode).toBe("foreground");
    act(() => movie.player().onEnded?.());

    expect(screen.queryByText("Up Next at Random")).toBeNull();
    expect(mocks.navigate).toHaveBeenCalledWith("/item/movie-1", { up: true });
    expect(mocks.getShuffle).not.toHaveBeenCalled();
  });
});
