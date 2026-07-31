import { describe, expect, it } from "vitest";
import type { PlayerSubtitleInfo } from "../types";
import { resolvePlayableSubtitles } from "./playableSubtitles";

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
