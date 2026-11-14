// @vitest-environment jsdom

import { useEffect, type ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import WatchRoute from "@/pages/WatchRoute";
import type { WatchPage } from "@/player/components/WatchPage";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

type WatchPageProps = ComponentProps<typeof WatchPage>;

const mocks = vi.hoisted(() => {
  // One movie with two versions, so a start has something to pin. The detail
  // query returns this same object for every read, as the cache would.
  const data = {
    content_id: "movie-1",
    title: "The Movie",
    year: 2024,
    subtitles: [],
    versions: [
      { file_id: 7, file_path: "/release-a.mkv" },
      { file_id: 9, file_path: "/release-b.mkv" },
    ] as unknown[],
  };
  return {
    player: { props: null as WatchPageProps | null },
    detail: { data, error: null },
  };
});

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
  useWatchDetail: () => mocks.detail,
}));
vi.mock("@/player/hooks/useSeriesEpisodes", () => ({
  useSeriesEpisodes: () => ({ episodes: [] }),
}));
// Stands in for the player: the host hands it the props that decide what the
// session asks the server for.
vi.mock("@/player/components/WatchPage", () => ({
  WatchPage: (props: WatchPageProps) => {
    mocks.player.props = props;
    return <div data-testid="player" />;
  },
}));

const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };

function ControllerProbe() {
  const controller = useWatchPlaybackController();
  useEffect(() => {
    controllerRef.current = controller;
  });
  return null;
}

/**
 * Mounts the app shell on the item page and a real `/watch/:id` route, so the
 * start goes through the same navigation the detail page uses. A mocked
 * `navigate` would skip the hand-off that has to carry the viewer's choice.
 */
async function startFromItemPage(
  input: Parameters<WatchPlaybackControllerValue["startPlayback"]>[0],
) {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/item/movie-1"]}>
        <WatchPlaybackProvider>
          <ControllerProbe />
          <Routes>
            <Route path="/item/:id" element={<div>item page</div>} />
            <Route path="/watch/:id" element={<WatchRoute />} />
          </Routes>
          <WatchPlaybackHost />
        </WatchPlaybackProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await waitFor(() => expect(controllerRef.current).not.toBeNull());

  act(() => controllerRef.current!.startPlayback(input, "viewer"));

  await screen.findByTestId("player");
  return () => mocks.player.props!;
}

describe("Playback start version selection", () => {
  beforeEach(() => {
    localStorage.clear();
    mocks.player.props = null;
  });
  afterEach(cleanup);

  it("keeps the viewer's pick explicit through the navigation to the player", async () => {
    const player = await startFromItemPage({
      contentId: "movie-1",
      fileId: 9,
      explicitFileSelection: true,
    });

    // The pick is the row the server must start, and the request has to say
    // the viewer named it. This is the downgrade the detail page's Play showed
    // on the wire: the request arrived as `file_selection: "auto"`.
    expect(player()).toMatchObject({ fileId: 9, explicitFileSelection: true });
  });

  it("starts an Auto Play as the server's choice", async () => {
    const player = await startFromItemPage({ contentId: "movie-1", fileId: 7 });

    expect(player()).toMatchObject({ fileId: 7, explicitFileSelection: false });
  });
});
