// @vitest-environment jsdom

import { useEffect, type ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createWatchRouteRequest, type WatchPlaybackStartInput } from "@/pages/watchRouteHelpers";
import type { WatchPage } from "@/player/components/WatchPage";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
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
    data: {
      content_id: "episode-1",
      title: "Episode 1",
      series_id: "series-1",
      season_number: 1,
      episode_number: 1,
    },
    error: null,
  },
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
vi.mock("@/player/components/PlayingNextScreen", () => ({
  PlayingNextScreen: () => <div data-testid="post-roll" />,
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
    mocks.navigate.mockReset();
    mocks.player.props = null;
  });
  afterEach(cleanup);

  it("enters post-roll before the end outside a Watch Together room", async () => {
    const episode = await renderEpisode({ contentId: "episode-1", libraryId: 1 });

    episode.nearEnd();

    expect(episode.controller().state.mode).toBe("post-roll");
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
