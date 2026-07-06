import type { RefObject } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { usePGSSubtitles } from "./usePGSSubtitles";
import type { PlayerSubtitleInfo } from "../types";
import { DEFAULT_SUBTITLE_APPEARANCE } from "../../lib/subtitleAppearance";

// Capture every constructed renderer so tests can assert options and
// lifecycle (libpgs fetches subUrl inside its worker — no fetch mocking).
const constructorOpts: Array<Record<string, unknown>> = [];
const disposeSpies: Array<ReturnType<typeof vi.fn>> = [];
const renderers: MockPgsRenderer[] = [];

class MockPgsRenderer {
  timeOffset: number;
  dispose = vi.fn();
  loadFromUrl = vi.fn();
  // Mirrors the private libpgs internals the hook observes for cue
  // readiness: the parsed-timestamp array (90kHz ticks) and the callback
  // fired after each worker update replaces it.
  implementation: { updateTimestamps: number[]; onTimestampsUpdated?: () => void } = {
    updateTimestamps: [],
  };
  constructor(opts: Record<string, unknown>) {
    constructorOpts.push(opts);
    this.timeOffset = (opts.timeOffset as number) ?? 0;
    disposeSpies.push(this.dispose);
    renderers.push(this);
  }
}

// Simulates the worker posting a parse-progress update: replaces the
// timestamp array (values in source-time seconds) and fires the callback,
// exactly like PgsRendererImpl.setUpdateTimestamps.
function deliverTimestamps(renderer: MockPgsRenderer, seconds: number[]) {
  renderer.implementation.updateTimestamps = seconds.map((s) => s * 90_000);
  act(() => renderer.implementation.onTimestampsUpdated?.());
}

vi.mock("libpgs", () => ({ PgsRenderer: MockPgsRenderer }));
vi.mock("libpgs/dist/libpgs.worker.js?url", () => ({ default: "/assets/libpgs.worker.js" }));

function makeVideoRef(): RefObject<HTMLVideoElement | null> {
  return { current: document.createElement("video") };
}

// jsdom's HTMLMediaElement has no working play()/pause(); stub them to flip
// `paused` and fire the native events the hook's hold machinery listens to.
function makePlayableVideoRef(): RefObject<HTMLVideoElement> {
  const video = document.createElement("video");
  let paused = true;
  Object.defineProperty(video, "paused", { get: () => paused });
  video.play = vi.fn(() => {
    paused = false;
    video.dispatchEvent(new Event("play"));
    return Promise.resolve();
  });
  video.pause = vi.fn(() => {
    paused = true;
    video.dispatchEvent(new Event("pause"));
  });
  return { current: video };
}

const pgsTrack: PlayerSubtitleInfo = {
  index: 3,
  language: "eng",
  codec: "hdmv_pgs_subtitle",
  label: "English (PGS)",
  source: "embedded",
  url: "/api/v1/stream/x/subtitles/3.sup?token=abc",
};

const otherPgsTrack: PlayerSubtitleInfo = {
  index: 4,
  language: "ger",
  codec: "pgs",
  label: "German (PGS)",
  source: "embedded",
  url: "/api/v1/stream/x/subtitles/4.sup?token=abc",
};

const srtTrack: PlayerSubtitleInfo = {
  index: 1,
  language: "eng",
  codec: "subrip",
  label: "English",
  source: "embedded",
  url: "/api/v1/stream/x/subtitles/1.vtt?token=abc",
};

const tracks = [srtTrack, pgsTrack, otherPgsTrack];

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  constructorOpts.length = 0;
  disposeSpies.length = 0;
  renderers.length = 0;
});

afterEach(() => {
  vi.useRealTimers();
});

describe("usePGSSubtitles", () => {
  it("creates a renderer wired to the video and .sup URL for a PGS track", async () => {
    const videoRef = makeVideoRef();
    const { result } = renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    expect(opts.video).toBe(videoRef.current);
    // The fetch is windowed: the server seeks near the playback position
    // (0 here) instead of demuxing the whole file from byte 0.
    expect(opts.subUrl).toBe(`${pgsTrack.url}&windowed=1&position=0&duration=3600`);
    expect(opts.workerUrl).toBe("/assets/libpgs.worker.js");
    // Decode in the worker, draw on the main thread: the source canvas must
    // stay readable for region detection.
    expect(opts.mode).toBe("workerWithoutOffscreenCanvas");
    expect(opts.canvas).toBeInstanceOf(HTMLCanvasElement);
    expect(result.current.isActive).toBe(true);
  });

  it("does nothing for text and ASS tracks", async () => {
    const { result, rerender } = renderHook(
      ({ index }) =>
        usePGSSubtitles(makeVideoRef(), tracks, index, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      { initialProps: { index: 1 as number | null } },
    );

    expect(result.current.isActive).toBe(false);

    rerender({ index: null });
    await Promise.resolve();
    expect(constructorOpts).toHaveLength(0);
    expect(result.current.isActive).toBe(false);
  });

  it("applies stream origin and subtracts user delay from the time offset", async () => {
    // Positive delay shows subtitles later; libpgs looks up the display set
    // at currentTime + timeOffset, so later means a smaller offset.
    renderHook(() =>
      usePGSSubtitles(makeVideoRef(), tracks, 3, false, 30, 2000, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    expect(constructorOpts[0]!.timeOffset).toBe(28);
  });

  it("updates the time offset in place without recreating the renderer", async () => {
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ delay }) =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, delay, DEFAULT_SUBTITLE_APPEARANCE),
      { initialProps: { delay: 0 } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ delay: 1500 });
    await waitFor(() => expect(renderers[0]!.timeOffset).toBe(-1.5));
    expect(renderers).toHaveLength(1);
    expect(disposeSpies[0]!).not.toHaveBeenCalled();
  });

  it("disposes the renderer when switching to a non-PGS track", async () => {
    const videoRef = makeVideoRef();
    const { result, rerender } = renderHook(
      ({ index }) =>
        usePGSSubtitles(videoRef, tracks, index, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      { initialProps: { index: 3 } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ index: 1 });
    await waitFor(() => expect(disposeSpies[0]!).toHaveBeenCalledTimes(1));
    expect(result.current.isActive).toBe(false);
  });

  it("recreates the renderer when switching between PGS tracks", async () => {
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ index }) =>
        usePGSSubtitles(videoRef, tracks, index, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      { initialProps: { index: 3 } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ index: 4 });
    await waitFor(() => expect(renderers).toHaveLength(2));
    expect(disposeSpies[0]!).toHaveBeenCalledTimes(1);
    expect(constructorOpts[1]!.subUrl).toBe(
      `${otherPgsTrack.url}&windowed=1&position=0&duration=3600`,
    );
  });

  it("anchors the initial window just before the playback position", async () => {
    const videoRef = makeVideoRef();
    videoRef.current!.currentTime = 1200;
    renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    // 10s backoff: PGS epochs can straddle the seek point.
    expect(constructorOpts[0]!.subUrl).toBe(
      `${pgsTrack.url}&windowed=1&position=1190&duration=3600`,
    );
  });

  it("re-points the renderer at a new window on a seek outside coverage", async () => {
    const videoRef = makeVideoRef();
    renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    // Initial window covers [0, 3600); seek far past it.
    videoRef.current!.currentTime = 5000;
    videoRef.current!.dispatchEvent(new Event("seeked"));

    expect(renderers[0]!.loadFromUrl).toHaveBeenCalledTimes(1);
    expect(renderers[0]!.loadFromUrl).toHaveBeenCalledWith(
      `${pgsTrack.url}&windowed=1&position=4990&duration=3600`,
    );
    expect(disposeSpies[0]!).not.toHaveBeenCalled();
  });

  it("does not reload the window for a seek inside coverage", async () => {
    const videoRef = makeVideoRef();
    renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    videoRef.current!.currentTime = 500;
    videoRef.current!.dispatchEvent(new Event("seeked"));

    expect(renderers[0]!.loadFromUrl).not.toHaveBeenCalled();
  });

  it("prefetches the next window as playback approaches the coverage end", async () => {
    const videoRef = makeVideoRef();
    renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    // Inside the 60s prefetch lead of the [0, 3600) window.
    videoRef.current!.currentTime = 3550;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    expect(renderers[0]!.loadFromUrl).toHaveBeenCalledWith(
      `${pgsTrack.url}&windowed=1&position=3540&duration=3600`,
    );

    // The reload updated coverage; the next tick must not refetch.
    renderers[0]!.loadFromUrl.mockClear();
    videoRef.current!.currentTime = 3555;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    expect(renderers[0]!.loadFromUrl).not.toHaveBeenCalled();
  });

  it("computes window positions in source time using the stream origin", async () => {
    const videoRef = makeVideoRef();
    // Copy-mode session: player timeline is rebased so currentTime 0 maps to
    // source time 600.
    renderHook(() =>
      usePGSSubtitles(videoRef, tracks, 3, false, 600, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    expect(constructorOpts[0]!.subUrl).toBe(
      `${pgsTrack.url}&windowed=1&position=590&duration=3600`,
    );

    // Stream-relative 3100 is source 3700 — comfortably inside the
    // [590, 4190) window (and clear of its 60s prefetch lead), so no reload.
    videoRef.current!.currentTime = 3100;
    videoRef.current!.dispatchEvent(new Event("seeked"));
    expect(renderers[0]!.loadFromUrl).not.toHaveBeenCalled();

    // Stream-relative 3600 is source 4200, past the window end (4190).
    videoRef.current!.currentTime = 3600;
    videoRef.current!.dispatchEvent(new Event("seeked"));
    expect(renderers[0]!.loadFromUrl).toHaveBeenCalledWith(
      `${pgsTrack.url}&windowed=1&position=4190&duration=3600`,
    );
  });

  it("disposes the renderer while detached and reports inactive", async () => {
    const videoRef = makeVideoRef();
    const { result, rerender } = renderHook(
      ({ detached }) =>
        usePGSSubtitles(videoRef, tracks, 3, detached, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      { initialProps: { detached: false } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ detached: true });
    await waitFor(() => expect(disposeSpies[0]!).toHaveBeenCalledTimes(1));
    expect(result.current.isActive).toBe(false);
  });

  it("disposes the renderer on unmount", async () => {
    const { unmount } = renderHook(() =>
      usePGSSubtitles(makeVideoRef(), tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    unmount();
    expect(disposeSpies[0]!).toHaveBeenCalled();
  });

  describe("cue-gating hold", () => {
    it("pauses a playing video on the initial load and resumes once cues cover the playhead", async () => {
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));

      // The initial window fetch started with nothing to show: hold.
      expect(videoRef.current.pause).toHaveBeenCalledTimes(1);
      expect(videoRef.current.paused).toBe(true);

      // Parsed watermark passes the playhead (0s) → auto-resume.
      deliverTimestamps(renderers[0]!, [5, 30]);
      expect(videoRef.current.paused).toBe(false);
      expect(result.current.isLoadingCues).toBe(false);
    });

    it("does not resume a video that was already paused when the track was enabled", async () => {
      const videoRef = makePlayableVideoRef();
      renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));

      expect(videoRef.current.pause).not.toHaveBeenCalled();
      deliverTimestamps(renderers[0]!, [5, 30]);
      // The user's deliberate pause is respected: readiness must not play.
      expect(videoRef.current.play).not.toHaveBeenCalled();
      expect(videoRef.current.paused).toBe(true);
    });

    it("pauses on a seek outside coverage and ignores the previous window's stale watermark", async () => {
      const videoRef = makePlayableVideoRef();
      videoRef.current.currentTime = 5000;
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));
      // Resolve the initial hold: window [4990, 8590) parsed up to 8000s.
      deliverTimestamps(renderers[0]!, [5010, 8000]);
      expect(videoRef.current.paused).toBe(false);

      // Seek far outside coverage.
      videoRef.current.currentTime = 100;
      videoRef.current.dispatchEvent(new Event("seeked"));

      expect(renderers[0]!.loadFromUrl).toHaveBeenCalledWith(
        `${pgsTrack.url}&windowed=1&position=90&duration=3600`,
      );
      // Still held: the old watermark (8000s > 100s) must not count while no
      // update has arrived for the new load yet.
      expect(videoRef.current.paused).toBe(true);
      deliverTimestamps(renderers[0]!, [120, 400]);
      expect(videoRef.current.paused).toBe(false);
      expect(result.current.isLoadingCues).toBe(false);
    });

    it("does not pause for the prefetch reload inside coverage", async () => {
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));
      deliverTimestamps(renderers[0]!, [5, 3599]);
      expect(videoRef.current.paused).toBe(false);
      const pauseCalls = (videoRef.current.pause as ReturnType<typeof vi.fn>).mock.calls.length;

      // Inside the 60s prefetch lead of the [0, 3600) window: reload without
      // gating — the current window's cues still render.
      videoRef.current.currentTime = 3550;
      videoRef.current.dispatchEvent(new Event("timeupdate"));

      expect(renderers[0]!.loadFromUrl).toHaveBeenCalledWith(
        `${pgsTrack.url}&windowed=1&position=3540&duration=3600`,
      );
      expect(videoRef.current.pause).toHaveBeenCalledTimes(pauseCalls);
      expect(videoRef.current.paused).toBe(false);
    });

    it("respects a user play during the hold and does not fight it on readiness", async () => {
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));
      expect(videoRef.current.paused).toBe(true);

      // User presses play mid-hold: playback continues, cues pop in late.
      await videoRef.current.play();
      expect(videoRef.current.paused).toBe(false);

      const playCalls = (videoRef.current.play as ReturnType<typeof vi.fn>).mock.calls.length;
      deliverTimestamps(renderers[0]!, [5, 30]);
      // Readiness must not issue another play (or pause) of its own.
      expect(videoRef.current.play).toHaveBeenCalledTimes(playCalls);
      expect(videoRef.current.pause).toHaveBeenCalledTimes(1);
      expect(result.current.isLoadingCues).toBe(false);
    });

    it("does not auto-resume when the user pauses after playing through a hold", async () => {
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await waitFor(() => expect(renderers).toHaveLength(1));

      // User plays through the hold, then deliberately pauses.
      await videoRef.current.play();
      videoRef.current.pause();
      expect(videoRef.current.paused).toBe(true);

      deliverTimestamps(renderers[0]!, [5, 30]);
      expect(videoRef.current.paused).toBe(true);
    });

    it("cancels the hold and resumes when subtitles are disabled", async () => {
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result, rerender } = renderHook(
        ({ index }) =>
          usePGSSubtitles(videoRef, tracks, index, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
        { initialProps: { index: 3 as number | null } },
      );
      await waitFor(() => expect(renderers).toHaveLength(1));
      expect(videoRef.current.paused).toBe(true);

      rerender({ index: null });
      await waitFor(() => expect(disposeSpies[0]!).toHaveBeenCalled());
      expect(videoRef.current.paused).toBe(false);
      expect(result.current.isLoadingCues).toBe(false);
    });

    it("shows the loading indicator only after the grace period", async () => {
      vi.useFakeTimers();
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      // Flush the mocked dynamic import (a microtask; timers stay frozen).
      await act(async () => {});
      expect(renderers).toHaveLength(1);
      expect(videoRef.current.paused).toBe(true);
      expect(result.current.isLoadingCues).toBe(false);

      act(() => vi.advanceTimersByTime(499));
      expect(result.current.isLoadingCues).toBe(false);
      act(() => vi.advanceTimersByTime(1));
      expect(result.current.isLoadingCues).toBe(true);

      deliverTimestamps(renderers[0]!, [5, 30]);
      expect(result.current.isLoadingCues).toBe(false);
      expect(videoRef.current.paused).toBe(false);
    });

    it("never flashes the indicator when cues arrive within the grace period", async () => {
      vi.useFakeTimers();
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await act(async () => {});
      expect(renderers).toHaveLength(1);

      act(() => vi.advanceTimersByTime(300));
      deliverTimestamps(renderers[0]!, [5, 30]);
      act(() => vi.advanceTimersByTime(1000));
      expect(result.current.isLoadingCues).toBe(false);
      expect(videoRef.current.paused).toBe(false);
    });

    it("resumes after the safety timeout and keeps the indicator until data arrives", async () => {
      vi.useFakeTimers();
      const videoRef = makePlayableVideoRef();
      await videoRef.current.play();

      const { result } = renderHook(() =>
        usePGSSubtitles(videoRef, tracks, 3, false, 0, 0, DEFAULT_SUBTITLE_APPEARANCE),
      );
      await act(async () => {});
      expect(renderers).toHaveLength(1);
      expect(videoRef.current.paused).toBe(true);

      act(() => vi.advanceTimersByTime(20_000));
      // Playback must not stay stranded on a broken extract...
      expect(videoRef.current.paused).toBe(false);
      // ...but the indicator persists until cue data really lands.
      expect(result.current.isLoadingCues).toBe(true);

      deliverTimestamps(renderers[0]!, [5, 30]);
      expect(result.current.isLoadingCues).toBe(false);
    });
  });
});
