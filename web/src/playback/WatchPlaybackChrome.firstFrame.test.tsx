// @vitest-environment jsdom

import { cleanup, renderHook } from "@testing-library/react";
import { useContext } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createWatchRouteRequest } from "@/pages/watchRouteHelpers";
import { takePlaybackIntent } from "@/player/first-frame";
import { WatchPlaybackProvider } from "./WatchPlaybackChrome";
import {
  WatchPlaybackControllerContext,
  type WatchPlaybackControllerValue,
} from "./watchPlaybackContext";

const mocks = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.navigate,
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile-1" } }),
}));

function renderController(): WatchPlaybackControllerValue {
  const { result } = renderHook(() => useContext(WatchPlaybackControllerContext), {
    wrapper: WatchPlaybackProvider,
  });
  if (!result.current) throw new Error("controller not provided");
  return result.current;
}

describe("WatchPlaybackProvider first-frame timing", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    // The intent mark is module state; start each test without one.
    takePlaybackIntent("");
  });
  afterEach(cleanup);

  it("times a start the viewer asked for", () => {
    const controller = renderController();
    const input = { contentId: "movie-1", libraryId: 3 };

    controller.startPlayback(input, "viewer");

    expect(mocks.navigate).toHaveBeenCalledOnce();
    expect(takePlaybackIntent(createWatchRouteRequest(input).requestKey)).not.toBeNull();
  });

  it("does not time a watch-party start the viewer did not ask for", () => {
    const controller = renderController();
    // What WatchPage does when another member changes the room's selection.
    const input = {
      contentId: "movie-2",
      fileId: 8,
      libraryId: 3,
      roomId: "room-1",
      roomToken: "proof",
      restart: true,
    };

    controller.startPlayback(input, "automatic");

    // The start still happens; its first_frame goes out without a duration.
    expect(mocks.navigate).toHaveBeenCalledOnce();
    expect(takePlaybackIntent(createWatchRouteRequest(input).requestKey)).toBeNull();
  });
});
