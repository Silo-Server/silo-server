import { describe, expect, it } from "vitest";
import { createWatchRouteRequest } from "@/pages/watchRouteHelpers";
import {
  createEmptyPlaybackState,
  watchPlaybackReducer,
  type WatchPlaybackHostState,
} from "./watchPlaybackReducer";

function makeRequest(overrides: Partial<ReturnType<typeof createWatchRouteRequest>> = {}) {
  return {
    ...createWatchRouteRequest({
      contentId: "movie-1",
      libraryId: 7,
      returnHref: "/item/movie-1?libraryId=7",
    }),
    ...overrides,
  };
}

function makeState(overrides: Partial<WatchPlaybackHostState> = {}): WatchPlaybackHostState {
  return {
    ...createEmptyPlaybackState(),
    request: makeRequest(),
    mode: "foreground",
    ...overrides,
  };
}

describe("watchPlaybackReducer", () => {
  it("clears the active playback state on explicit exit", () => {
    const next = watchPlaybackReducer(
      makeState({
        pictureInPictureActive: true,
        snapshot: { currentTime: 120, duration: 3600, playing: true },
        transport: {
          playPause: () => {},
          seekBy: () => {},
          skipBack: () => {},
          skipForward: () => {},
          seekTo: () => {},
          togglePictureInPicture: () => {},
        },
      }),
      { type: "EXIT_PLAYBACK" },
    );

    expect(next).toEqual(createEmptyPlaybackState());
  });

  it("keeps playback alive and marks route bypass on minimize", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(makeState({ request }), {
      type: "MINIMIZE_PLAYBACK",
      requestKey: request.requestKey,
    });

    expect(next.request).toEqual(request);
    expect(next.mode).toBe("background-bar");
    expect(next.routeExitBypassRequestKey).toBe(request.requestKey);
  });

  it("ignores the first route leave after an explicit minimize", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(
      makeState({
        request,
        mode: "background-bar",
        routeExitBypassRequestKey: request.requestKey,
      }),
      { type: "ROUTE_LEFT", requestKey: request.requestKey },
    );

    expect(next.request).toEqual(request);
    expect(next.routeExitBypassRequestKey).toBeNull();
  });

  it("treats route leave as exit while playback is still foreground", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(makeState({ request }), {
      type: "ROUTE_LEFT",
      requestKey: request.requestKey,
    });

    expect(next).toEqual(createEmptyPlaybackState());
  });

  it("returns detached playback to the background bar when PiP exits and playback continues", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(
      makeState({
        request,
        mode: "picture-in-picture",
        pictureInPictureActive: true,
      }),
      {
        type: "LEAVE_PIP",
        requestKey: request.requestKey,
        playbackContinues: true,
      },
    );

    expect(next.mode).toBe("background-bar");
    expect(next.pictureInPictureActive).toBe(false);
    expect(next.shouldReturnToWatchPage).toBe(true);
  });

  it("stops detached playback when PiP exits and media is no longer playing", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(
      makeState({
        request,
        mode: "picture-in-picture",
        pictureInPictureActive: true,
      }),
      {
        type: "LEAVE_PIP",
        requestKey: request.requestKey,
        playbackContinues: false,
      },
    );

    expect(next).toEqual(createEmptyPlaybackState());
  });

  it("only enters post-roll for the active foreground request", () => {
    const request = makeRequest();
    const next = watchPlaybackReducer(makeState({ request }), {
      type: "ENTER_POSTROLL",
      requestKey: request.requestKey,
    });

    expect(next.mode).toBe("post-roll");
    expect(next.request).toEqual(request);
  });
});
