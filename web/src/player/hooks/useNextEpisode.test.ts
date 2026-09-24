// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SeriesContext } from "../types";
import { useNextEpisode } from "./useNextEpisode";

const seriesContext: SeriesContext = {
  seriesId: "series-1",
  currentSeason: 1,
  currentEpisode: 1,
  episodes: [
    { contentId: "ep-1", seasonNumber: 1, episodeNumber: 1, title: "One", runtime: 1800 },
    { contentId: "ep-2", seasonNumber: 1, episodeNumber: 2, title: "Two", runtime: 1800 },
  ],
};
const credits = { start: 1700, end: 1800 };

describe("useNextEpisode", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("starts the next episode as an automatic start when the countdown runs out", () => {
    const onNavigate = vi.fn();
    renderHook(() => useNextEpisode(credits, seriesContext, 1710, onNavigate));

    act(() => vi.advanceTimersByTime(10_000));

    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "automatic");
  });

  it("starts the next episode as the viewer's start when they skip the countdown", () => {
    const onNavigate = vi.fn();
    const { result } = renderHook(() => useNextEpisode(credits, seriesContext, 1710, onNavigate));

    act(() => result.current.skipToNext());
    act(() => vi.advanceTimersByTime(10_000));

    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "viewer");
  });
});
