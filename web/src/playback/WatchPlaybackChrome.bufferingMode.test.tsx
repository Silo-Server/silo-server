// @vitest-environment jsdom

import { useEffect, type ComponentProps } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createWatchRouteRequest } from "@/pages/watchRouteHelpers";
import type { WatchPage } from "@/player/components/WatchPage";
import { storage } from "@/utils/storage";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

type WatchPageProps = ComponentProps<typeof WatchPage>;

const mocks = vi.hoisted(() => ({
  settings: undefined as Record<string, { value: unknown }> | undefined,
  supported: true,
  player: { props: null as WatchPageProps | null },
}));

vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: (operation: string) =>
    operation === "GET /api/v2/watch/{id}"
      ? Promise.resolve({ content_id: "movie-ok", title: "The Movie" })
      : Promise.reject(new Error(`unexpected ${operation}`)),
}));
vi.mock("@/api/v2/watch", async (original) => ({
  ...(await original<typeof import("@/api/v2/watch")>()),
  watchDetailFromV2: (detail: unknown) => detail,
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile-1" } }),
}));
vi.mock("@/hooks/queries/seekPreferences", () => ({
  useSeekPreferences: () => ({ skipBack: 10, skipForward: 10 }),
}));
vi.mock("@/hooks/queries/settingValues", () => ({
  settingsCapabilitiesSupportKey: (_data: unknown, key: string) =>
    mocks.supported && key === "playback.buffering_quality_mode",
  useEffectiveSettings: () => ({ data: mocks.settings }),
  useSettingsCapabilities: () => ({ data: {}, isSuccess: true }),
}));
vi.mock("@/hooks/queries/progress", () => ({ useContinueWatching: () => ({ items: [] }) }));
vi.mock("@/pages/watchRouteHelpers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/pages/watchRouteHelpers")>()),
  buildWatchPageProps: () => ({}),
}));
vi.mock("@/player/hooks/useSeriesEpisodes", () => ({
  useSeriesEpisodes: () => ({ episodes: [] }),
}));
vi.mock("@/player/components/WatchPage", () => ({
  WatchPage: (props: WatchPageProps) => {
    mocks.player.props = props;
    return <div data-testid="player" />;
  },
}));

async function play() {
  const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };
  function ControllerProbe() {
    const controller = useWatchPlaybackController();
    useEffect(() => {
      controllerRef.current = controller;
    });
    return null;
  }
  const view = render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/watch/movie-ok"]}>
        <WatchPlaybackProvider>
          <ControllerProbe />
          <WatchPlaybackHost />
        </WatchPlaybackProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  act(() =>
    controllerRef.current!.syncRouteRequest(
      createWatchRouteRequest({ contentId: "movie-ok", libraryId: 1 }),
    ),
  );
  await screen.findByTestId("player");
  return view;
}

describe("WatchPlaybackHost buffering mode", () => {
  beforeEach(() => {
    storage.remove(storage.KEYS.BUFFERING_MODES);
    mocks.settings = undefined;
    mocks.supported = true;
    mocks.player.props = null;
  });
  afterEach(cleanup);

  it("starts with the mode it saw last for the profile while the setting loads", async () => {
    mocks.settings = { "playback.buffering_quality_mode": { value: "never" } };
    const first = await play();
    expect(mocks.player.props?.bufferingMode).toBe("never");
    first.unmount();

    // A cold start: the settings read has not answered yet.
    mocks.settings = undefined;
    await play();
    expect(mocks.player.props?.bufferingMode).toBe("never");
  });

  it("uses the default for a profile it has never seen, and on a server without the setting", async () => {
    const first = await play();
    expect(mocks.player.props?.bufferingMode).toBe("always");
    first.unmount();
    storage.set(storage.KEYS.BUFFERING_MODES, JSON.stringify({ "profile-1": "never" }));
    mocks.supported = false;
    await play();
    expect(mocks.player.props?.bufferingMode).toBe("always");
  });
});
