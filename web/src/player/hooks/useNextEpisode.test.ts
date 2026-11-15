// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SeriesContext } from "../types";
import { recordAutoAdvance, resetStillWatchingStreak } from "../stillWatching";
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
const playingWithAutoPlay = { autoPlay: true, playing: true };

describe("useNextEpisode", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    resetStillWatchingStreak();
  });

  it("starts the next episode as an automatic start when the countdown runs out", () => {
    const onNavigate = vi.fn();
    renderHook(() => useNextEpisode(credits, seriesContext, 1710, onNavigate, playingWithAutoPlay));

    act(() => vi.advanceTimersByTime(10_000));

    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "automatic");
  });

  it("starts the next episode as the viewer's start when they skip the countdown", () => {
    const onNavigate = vi.fn();
    const { result } = renderHook(() =>
      useNextEpisode(credits, seriesContext, 1710, onNavigate, playingWithAutoPlay),
    );

    act(() => result.current.skipToNext());
    act(() => vi.advanceTimersByTime(10_000));

    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "viewer");
  });

  it("waits for the viewer once the automatic streak reaches the limit", () => {
    for (let i = 0; i < 3; i++) recordAutoAdvance();
    const onNavigate = vi.fn();
    const { result } = renderHook(() =>
      useNextEpisode(credits, seriesContext, 1710, onNavigate, playingWithAutoPlay),
    );

    act(() => vi.advanceTimersByTime(20_000));

    expect(result.current.showCountdown).toBe(false);
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("shows the card without a countdown when auto-play is off", () => {
    const onNavigate = vi.fn();
    const { result } = renderHook(() =>
      useNextEpisode(credits, seriesContext, 1710, onNavigate, { autoPlay: false, playing: true }),
    );

    act(() => vi.advanceTimersByTime(20_000));

    expect(result.current.showCountdown).toBe(true);
    expect(result.current.secondsRemaining).toBeNull();
    expect(onNavigate).not.toHaveBeenCalled();

    act(() => result.current.skipToNext());
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "viewer");
  });

  it("holds the countdown while paused and resumes it on play", () => {
    const onNavigate = vi.fn();
    const { result, rerender } = renderHook(
      ({ playing }) =>
        useNextEpisode(credits, seriesContext, 1710, onNavigate, { autoPlay: true, playing }),
      { initialProps: { playing: true } },
    );

    act(() => vi.advanceTimersByTime(3_000));
    expect(result.current.secondsRemaining).toBe(7);

    rerender({ playing: false });
    act(() => vi.advanceTimersByTime(20_000));
    expect(result.current.secondsRemaining).toBe(7);
    expect(onNavigate).not.toHaveBeenCalled();

    rerender({ playing: true });
    act(() => vi.advanceTimersByTime(6_000));
    expect(onNavigate).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1_000));
    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "automatic");
  });

  it("starts only one episode when the next one's context arrives on the same mount", () => {
    const onNavigate = vi.fn();
    const threeEpisodes: SeriesContext = {
      ...seriesContext,
      episodes: [
        ...seriesContext.episodes,
        { contentId: "ep-3", seasonNumber: 1, episodeNumber: 3, title: "Three", runtime: 1800 },
      ],
    };
    const { rerender } = renderHook(
      ({ context }) => useNextEpisode(credits, context, 1710, onNavigate, playingWithAutoPlay),
      { initialProps: { context: threeEpisodes } },
    );

    act(() => vi.advanceTimersByTime(10_000));
    rerender({ context: { ...threeEpisodes, currentEpisode: 2 } });
    act(() => vi.advanceTimersByTime(1_000));

    expect(onNavigate).toHaveBeenCalledOnce();
    expect(onNavigate).toHaveBeenCalledWith("ep-2", "automatic");
  });
});
