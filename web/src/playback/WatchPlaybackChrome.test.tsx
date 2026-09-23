// @vitest-environment jsdom

import { Profiler, useEffect, type ReactNode } from "react";
import { act, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import CardPlayOverlay from "@/components/CardPlayOverlay";
import { createWatchRouteRequest } from "@/pages/watchRouteHelpers";
import { WatchPlaybackBar, WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  useWatchPlaybackController,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

// Render counters. A nested React Profiler can miss a render that reaches its
// subtree through lazy context propagation, so each component is counted where
// it runs. The one Profiler, at the root, only counts commits.
const counts = vi.hoisted(() => ({ cardRenders: 0, chromeRenders: 0, barRenders: 0, commits: 0 }));

// The real card overlay, counted on every render.
vi.mock("@/components/CardPlayOverlay", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/components/CardPlayOverlay")>();
  return {
    default: function CountedCardPlayOverlay(
      props: Parameters<typeof actual.default>[0],
    ): ReturnType<typeof actual.default> {
      counts.cardRenders += 1;
      return actual.default(props);
    },
  };
});

// The playback host reads the signed-in user to gate its settings reads.
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: 1 } }),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({
    profile: { id: "profile-1" },
    hasSelectedProfile: true,
    isLoading: false,
  }),
}));

vi.mock("@/hooks/queries/seekPreferences", () => ({
  useSeekPreferences: () => ({ skipBack: 10, skipForward: 10 }),
}));

// WatchPlaybackBar is the only caller in this harness, once per render.
vi.mock("@/hooks/queries/items", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/items")>()),
  useWatchDetail: () => {
    counts.barRenders += 1;
    return { data: { content_id: "movie-1", title: "The Movie", year: 2024 } };
  },
}));

// The Radix slider needs layout APIs jsdom lacks; a range input carries the
// same value, bounds and disabled state.
vi.mock("@/components/ui/slider", () => ({
  Slider: ({ value, max, disabled }: { value: number[]; max: number; disabled?: boolean }) => (
    <input
      type="range"
      aria-label="Playback position"
      value={value[0]}
      max={max}
      disabled={disabled}
      readOnly
    />
  ),
}));

// About one home page of cards, each with its own play overlay.
const CARD_COUNT = 160;
// Ten seconds of playback at the player's 4 Hz time updates.
const TICKS = 40;
const DURATION = 3600;

function renderPlaybackHarness(player?: ReactNode) {
  const controllerRef: { current: WatchPlaybackControllerValue | null } = { current: null };

  // Stands in for Layout, the Host and the other chrome that read the controller.
  // The effect has no dependencies, so it runs once per committed render.
  function ChromeProbe() {
    const controller = useWatchPlaybackController();
    useEffect(() => {
      controllerRef.current = controller;
      counts.chromeRenders += 1;
    });
    return null;
  }

  render(
    <MemoryRouter initialEntries={["/home"]}>
      <Profiler id="app" onRender={() => (counts.commits += 1)}>
        <WatchPlaybackProvider>
          <ChromeProbe />
          {player}
          {Array.from({ length: CARD_COUNT }, (_, index) => (
            <CardPlayOverlay key={index} contentId={`movie-${index}`} title={`Movie ${index}`} />
          ))}
          <WatchPlaybackBar />
        </WatchPlaybackProvider>
      </Profiler>
    </MemoryRouter>,
  );

  const controller = () => {
    if (!controllerRef.current) throw new Error("controller not captured");
    return controllerRef.current;
  };
  const resetCounts = () => {
    counts.cardRenders = 0;
    counts.chromeRenders = 0;
    counts.barRenders = 0;
    counts.commits = 0;
  };
  const tick = (requestKey: string, currentTime: number, playing = true) => {
    act(() => {
      controller().updatePlaybackSnapshot(requestKey, {
        currentTime,
        duration: DURATION,
        playing,
      });
    });
  };

  return { controller, resetCounts, tick };
}

describe("WatchPlaybackBar time updates", () => {
  it("re-renders only the bar while playback time advances in the background", () => {
    const harness = renderPlaybackHarness();
    const request = createWatchRouteRequest({ contentId: "movie-1", libraryId: 1 });
    act(() => harness.controller().syncRouteRequest(request));
    act(() => harness.controller().minimizePlayback());
    harness.tick(request.requestKey, 60);
    harness.resetCounts();

    for (let index = 1; index <= TICKS; index += 1) {
      harness.tick(request.requestKey, 60 + index * 0.25);
    }

    expect(counts).toEqual({
      cardRenders: 0,
      chromeRenders: 0,
      barRenders: TICKS,
      commits: TICKS,
    });
    expect(screen.getByText("1:10")).toBeInTheDocument();
    expect(screen.getByText("1:00:00")).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Playback position" })).toHaveValue("70");
    expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();

    // An identical snapshot changes nothing on screen, so it renders nothing.
    harness.tick(request.requestKey, 70);
    expect(counts.barRenders).toBe(TICKS);

    harness.tick(request.requestKey, 70, false);
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();
  });

  it("renders nothing for time updates while the player is in the foreground", () => {
    const harness = renderPlaybackHarness();
    const request = createWatchRouteRequest({ contentId: "movie-1", libraryId: 1 });
    act(() => harness.controller().syncRouteRequest(request));
    harness.resetCounts();

    for (let index = 1; index <= TICKS; index += 1) {
      harness.tick(request.requestKey, 60 + index * 0.25);
    }

    expect(counts).toEqual({ cardRenders: 0, chromeRenders: 0, barRenders: 0, commits: 0 });

    // The position recorded while the bar was hidden is there once it shows.
    act(() => harness.controller().minimizePlayback());
    expect(screen.getByText("1:10")).toBeInTheDocument();
  });

  it("does not show the replaced request's position", () => {
    const harness = renderPlaybackHarness();
    const first = createWatchRouteRequest({ contentId: "movie-1", libraryId: 1 });
    act(() => harness.controller().syncRouteRequest(first));
    act(() => harness.controller().minimizePlayback());
    harness.tick(first.requestKey, 120);
    expect(screen.getByText("2:00")).toBeInTheDocument();

    // Starting another title from the bar keeps the old player running until
    // the new one is ready; its time updates must not reach the bar.
    act(() => harness.controller().startPlayback({ contentId: "movie-2", libraryId: 1 }, "viewer"));
    harness.tick(first.requestKey, 121);
    expect(screen.queryByText("2:01")).not.toBeInTheDocument();
    expect(screen.getAllByText("0:00")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();

    const second = createWatchRouteRequest({ contentId: "movie-2", libraryId: 1 });
    harness.tick(second.requestKey, 5);
    expect(screen.getByText("0:05")).toBeInTheDocument();
    expect(screen.getByText("1:00:00")).toBeInTheDocument();
  });

  it("starts a replayed title without the previous run's position", () => {
    const harness = renderPlaybackHarness();
    const request = createWatchRouteRequest({ contentId: "movie-1", libraryId: 1 });
    act(() => harness.controller().syncRouteRequest(request));
    act(() => harness.controller().minimizePlayback());
    harness.tick(request.requestKey, 120);
    expect(screen.getByText("2:00")).toBeInTheDocument();

    // Playing the same title again builds a new request with the same key.
    act(() => harness.controller().startPlayback({ contentId: "movie-1", libraryId: 1 }, "viewer"));
    expect(harness.controller().state.request).not.toBe(request);
    expect(harness.controller().state.request?.requestKey).toBe(request.requestKey);
    expect(screen.queryByText("2:00")).not.toBeInTheDocument();
    expect(screen.getAllByText("0:00")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Play" })).toBeInTheDocument();

    harness.tick(request.requestKey, 3);
    expect(screen.getByText("0:03")).toBeInTheDocument();
  });

  it("keeps the report a player sends in the commit that starts its request", () => {
    // The host mounts the player of an already loaded title in the same commit
    // that starts its request. The player reports from its first effect, which
    // runs before the provider's own effects.
    function MountReportingPlayer() {
      const { state, updatePlaybackSnapshot } = useWatchPlaybackController();
      const requestKey = state.request?.requestKey;
      useEffect(() => {
        if (!requestKey) return;
        updatePlaybackSnapshot(requestKey, { currentTime: 0, duration: 5400, playing: false });
      }, [requestKey, updatePlaybackSnapshot]);
      return null;
    }

    const harness = renderPlaybackHarness(<MountReportingPlayer />);
    const first = createWatchRouteRequest({ contentId: "movie-1", libraryId: 1 });
    act(() => harness.controller().syncRouteRequest(first));
    act(() => harness.controller().minimizePlayback());
    harness.tick(first.requestKey, 120);
    expect(screen.getByText("2:00")).toBeInTheDocument();

    act(() => harness.controller().startPlayback({ contentId: "movie-2", libraryId: 1 }, "viewer"));
    expect(screen.getByText("0:00")).toBeInTheDocument();
    expect(screen.getByText("1:30:00")).toBeInTheDocument();
  });
});
