// @vitest-environment jsdom

import { useEffect, type ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createWatchRouteRequest, type WatchPlaybackStartInput } from "@/pages/watchRouteHelpers";
import type { WatchPage } from "@/player/components/WatchPage";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import { readRequestedFileId } from "./requestedVersion";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

type WatchPageProps = ComponentProps<typeof WatchPage>;

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  player: { props: null as WatchPageProps | null },
  // One object for every render, as the query cache would return.
  episode: {
    data: {} as Record<string, unknown>,
    error: null,
  },
}));

const EPISODE = {
  content_id: "episode-1",
  type: "episode",
  title: "Episode 1",
  series_id: "series-1",
  season_number: 1,
  episode_number: 1,
};
// A series-owned extra carries the series' id, as the server sends it.
const SERIES_TRAILER = {
  content_id: "trailer-1",
  type: "extra",
  title: "Trailer",
  series_id: "series-1",
};

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
  // The post-roll screen reads auto_play_next through these, so the mock has
  // to answer them even though this file renders a stand-in for it.
  useSetSettingValue: () => ({ isPending: false, mutateAsync: vi.fn() }),
  useClearSettingValue: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));
vi.mock("@/hooks/queries/progress", () => ({
  useContinueWatching: () => ({ items: [] }),
}));
vi.mock("@/hooks/queries/items", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/items")>()),
  useWatchDetail: () => mocks.episode,
}));
// The item's own props do not matter here, only the host's callbacks.
vi.mock("@/pages/watchRouteHelpers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/pages/watchRouteHelpers")>()),
  buildWatchPageProps: () => ({}),
}));
vi.mock("@/player/hooks/useSeriesEpisodes", () => ({
  useSeriesEpisodes: () => ({ episodes: [] }),
}));
// Stands in for the player: the host hands it the callbacks under test.
vi.mock("@/player/components/WatchPage", () => ({
  WatchPage: (props: WatchPageProps) => {
    mocks.player.props = props;
    return <div data-testid="player" />;
  },
}));
const postRoll = vi.hoisted(() => ({
  close: null as null | (() => void),
  playItem: null as null | ((contentId: string) => void),
}));
vi.mock("@/player/components/PlayingNextScreen", () => ({
  PlayingNextScreen: (props: {
    onClose?: () => void;
    onPlayItem?: (contentId: string) => void;
  }) => {
    postRoll.close = props.onClose ?? null;
    postRoll.playItem = props.onPlayItem ?? null;
    return <div data-testid="post-roll" />;
  },
}));

const DURATION = 2434;

async function renderEpisode(input: WatchPlaybackStartInput) {
  const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };
  function ControllerProbe() {
    const controller = useWatchPlaybackController();
    useEffect(() => {
      controllerRef.current = controller;
    });
    return null;
  }

  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/watch/episode-1"]}>
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
  // The player's time update 20 seconds before the end of the episode.
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

describe("WatchPlaybackHost end of an episode", () => {
  beforeEach(() => {
    localStorage.clear();
    mocks.navigate.mockReset();
    mocks.player.props = null;
    mocks.episode.data = EPISODE;
  });
  afterEach(cleanup);

  it("enters post-roll before the end outside a Watch Together room", async () => {
    const episode = await renderEpisode({ contentId: "episode-1", libraryId: 1 });

    episode.nearEnd();

    expect(episode.controller().state.mode).toBe("post-roll");
  });

  it("remembers a version switched during the episode when it completes into post-roll", async () => {
    const episode = await renderEpisode({ contentId: "episode-1", libraryId: 1 });
    // Post-roll opens before the end, then the real `ended` event reports the
    // switch: row 1 served the bytes, but the viewer asked for row 2.
    episode.nearEnd();
    expect(episode.controller().state.mode).toBe("post-roll");

    act(() =>
      episode.player().onEnded?.({
        positionSeconds: DURATION,
        durationSeconds: DURATION,
        lastFileId: 1,
        selectedFileId: 2,
      }),
    );

    expect(readRequestedFileId("episode-1", "profile-1")).toBe(2);
  });

  it("remembers a version switched during the episode when post-roll closes before the end", async () => {
    const episode = await renderEpisode({ contentId: "episode-1", libraryId: 1 });
    // The version switch is committed before the early post-roll opens; the
    // viewer then leaves through its Close action and `onEnded` never fires.
    act(() => episode.player().onRequestedVersionChange?.(2, true));
    episode.nearEnd();
    expect(episode.controller().state.mode).toBe("post-roll");

    act(() => postRoll.close?.());

    expect(readRequestedFileId("episode-1", "profile-1")).toBe(2);
  });

  it("remembers a version switched during the episode when post-roll advances without ending", async () => {
    const episode = await renderEpisode({ contentId: "episode-1", libraryId: 1 });
    act(() => episode.player().onRequestedVersionChange?.(2, true));
    episode.nearEnd();
    expect(episode.controller().state.mode).toBe("post-roll");

    act(() => postRoll.playItem?.("episode-2"));

    expect(readRequestedFileId("episode-1", "profile-1")).toBe(2);
  });

  it("keeps a room member in the player and returns it to the room when the room leaves playback", async () => {
    const episode = await renderEpisode({
      contentId: "episode-1",
      libraryId: 1,
      roomId: "room-1",
      roomToken: "proof",
    });

    episode.nearEnd();
    expect(episode.controller().state.mode).toBe("foreground");
    expect(screen.queryByTestId("post-roll")).toBeNull();

    // What the player does when the room returns to its lobby.
    await act(async () => {
      await episode.player().onExit();
    });

    expect(mocks.navigate).toHaveBeenCalledWith(
      "/rooms/room-1?room_token=proof",
      expect.objectContaining({ replace: true }),
    );
  });
});

describe("WatchPlaybackHost end of a series extra", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.player.props = null;
    mocks.episode.data = SERIES_TRAILER;
  });
  afterEach(cleanup);

  // A direct watch link has no return location; the extra has no detail page
  // of its own, so it falls back to its series.
  it.each([
    ["where it was started", "/search?q=trailer", "/search?q=trailer"],
    ["its series without a return location", undefined, "/item/series-1"],
  ])("plays to the end without post-roll and returns to %s", async (_, returnHref, expected) => {
    const trailer = await renderEpisode({ contentId: "trailer-1", returnHref });

    trailer.nearEnd();
    expect(trailer.controller().state.mode).toBe("foreground");
    expect(screen.queryByTestId("post-roll")).toBeNull();

    act(() => trailer.player().onEnded?.());

    expect(trailer.controller().state.mode).not.toBe("post-roll");
    expect(mocks.navigate).toHaveBeenCalledWith(expected, { up: true });
  });
});
