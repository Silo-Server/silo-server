import { describe, expect, it } from "vitest";
import type { PlayerSubtitleInfo } from "../types";
import { pendingServerSubtitleSelection, resolvePlayableSubtitles } from "./playableSubtitles";

function makeSubtitle(overrides: Partial<PlayerSubtitleInfo> = {}): PlayerSubtitleInfo {
  return {
    index: 0,
    language: "eng",
    label: "English",
    url: "",
    ...overrides,
  };
}

describe("resolvePlayableSubtitles", () => {
  it("prefers playback-session subtitle tracks when they include stream urls", () => {
    const sessionTrack = makeSubtitle({
      index: 2,
      source: "embedded",
      url: "/stream/session/subtitles/2",
    });
    const detailTrack = makeSubtitle({
      index: 0,
      source: "embedded",
      url: "",
    });

    expect(resolvePlayableSubtitles([sessionTrack], [detailTrack])).toEqual([sessionTrack]);
  });

  it("drops watch-detail subtitle tracks that have no playable url", () => {
    const detailTrack = makeSubtitle({
      index: 0,
      source: "embedded",
      codec: "hdmv_pgs_subtitle",
      url: "",
    });

    expect(resolvePlayableSubtitles([], [detailTrack])).toEqual([]);
  });

  it("keeps burn-in-only session tracks even though they have no url", () => {
    const burnInTrack = makeSubtitle({
      index: 3,
      source: "embedded",
      codec: "hdmv_pgs_subtitle",
      burn_in_only: true,
      url: "",
    });
    const sidecarTrack = makeSubtitle({
      index: 4,
      source: "embedded",
      url: "/stream/session/subtitles/4",
    });

    expect(resolvePlayableSubtitles([burnInTrack, sidecarTrack], [])).toEqual([
      burnInTrack,
      sidecarTrack,
    ]);
  });

  it("keeps fallback tracks that already have playable urls", () => {
    const fallbackTrack = makeSubtitle({
      index: 1,
      source: "downloaded",
      url: "/stream/fallback/subtitles/1",
    });

    expect(resolvePlayableSubtitles([], [fallbackTrack])).toEqual([fallbackTrack]);
  });
});

describe("pendingServerSubtitleSelection", () => {
  it("settles an already-selected burn-in plan without another replan", () => {
    expect(pendingServerSubtitleSelection("burn_in", 2, 2, true)).toBeUndefined();
  });

  it("preserves a sidecar selection while replacing burn-in", () => {
    expect(pendingServerSubtitleSelection("burn_in", 2, 0, false)).toBe(0);
  });

  it("turns burn-in off explicitly rather than looping", () => {
    expect(pendingServerSubtitleSelection("burn_in", 2, null, false)).toBeNull();
  });

  it("requests a burn-in track from a sidecar plan", () => {
    expect(pendingServerSubtitleSelection("render", 0, 2, true)).toBe(2);
  });

  it("persists a newly selected sidecar track", () => {
    expect(pendingServerSubtitleSelection("off", null, 0, false)).toBe(0);
  });

  it("persists a switch between sidecar tracks", () => {
    expect(pendingServerSubtitleSelection("render", 0, 1, false)).toBe(1);
  });

  it("persists turning a sidecar track off", () => {
    expect(pendingServerSubtitleSelection("render", 0, null, false)).toBeNull();
  });
});
