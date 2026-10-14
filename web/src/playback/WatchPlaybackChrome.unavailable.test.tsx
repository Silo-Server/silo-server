// @vitest-environment jsdom

import { useEffect } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";
import { createWatchRouteRequest } from "@/pages/watchRouteHelpers";
import { WatchPlaybackHost, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  watchDetail: vi.fn<(id: string) => Promise<unknown>>(),
}));

// The watch-detail read goes through the real query hook; only the transport
// is replaced, so the failure reaches the host the way the server's 404 does.
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: (operation: string, options: { path?: { id?: string } }) =>
    operation === "GET /api/v2/watch/{id}"
      ? mocks.watchDetail(options.path?.id ?? "")
      : Promise.reject(new Error(`unexpected ${operation}`)),
}));
vi.mock("@/api/v2/watch", async (original) => ({
  ...(await original<typeof import("@/api/v2/watch")>()),
  watchDetailFromV2: (detail: unknown) => detail,
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
vi.mock("@/pages/watchRouteHelpers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/pages/watchRouteHelpers")>()),
  buildWatchPageProps: () => ({}),
}));
vi.mock("@/player/hooks/useSeriesEpisodes", () => ({
  useSeriesEpisodes: () => ({ episodes: [] }),
}));
vi.mock("@/player/components/WatchPage", () => ({
  WatchPage: () => <div data-testid="player" />,
}));

function notFound() {
  return new V2ProblemError("getWatchDetail", {
    type: "https://siloserver.org/docs/api/v2/problems/not_found",
    title: "Not Found",
    status: 404,
    detail: "media item not found",
  } as never);
}

function renderHost() {
  const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };
  function ControllerProbe() {
    const controller = useWatchPlaybackController();
    useEffect(() => {
      controllerRef.current = controller;
    });
    return null;
  }

  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/watch/movie-tmdb-99999999"]}>
        <WatchPlaybackProvider>
          <ControllerProbe />
          <WatchPlaybackHost />
        </WatchPlaybackProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return () => controllerRef.current!;
}

describe("WatchPlaybackHost when the title cannot be loaded", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.watchDetail.mockReset();
  });
  afterEach(cleanup);

  it("replaces the preparing screen with the unavailable screen on a 404", async () => {
    mocks.watchDetail.mockRejectedValue(notFound());
    const controller = renderHost();

    act(() =>
      controller().syncRouteRequest(
        createWatchRouteRequest({ contentId: "movie-tmdb-99999999", libraryId: 1 }),
      ),
    );
    expect(screen.getByText("Preparing playback")).toBeInTheDocument();

    expect(await screen.findByText("Playback unavailable")).toBeInTheDocument();
    expect(screen.queryByText("Preparing playback")).not.toBeInTheDocument();
    expect(screen.queryByText("media item not found")).not.toBeInTheDocument();
    expect(screen.getByText(/This title isn't available\./)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Go Back" }));
    expect(mocks.navigate).toHaveBeenCalledWith(-1);
  });

  it("does not carry the failure over to the next title", async () => {
    mocks.watchDetail.mockImplementation((id) =>
      id === "movie-ok"
        ? Promise.resolve({ content_id: "movie-ok", title: "The Movie" })
        : Promise.reject(notFound()),
    );
    const controller = renderHost();

    act(() =>
      controller().syncRouteRequest(
        createWatchRouteRequest({ contentId: "movie-missing", libraryId: 1 }),
      ),
    );
    await screen.findByText("Playback unavailable");

    act(() =>
      controller().syncRouteRequest(
        createWatchRouteRequest({ contentId: "movie-ok", libraryId: 1 }),
      ),
    );
    expect(screen.queryByText("Playback unavailable")).not.toBeInTheDocument();
    expect(await screen.findByTestId("player")).toBeInTheDocument();
    expect(screen.queryByText("Playback unavailable")).not.toBeInTheDocument();
  });
});
