import type { RefObject } from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { usePGSSubtitles } from "./usePGSSubtitles";
import type { PlayerSubtitleInfo } from "../types";

// Capture every constructed renderer so tests can assert options and
// lifecycle (libpgs fetches subUrl inside its worker — no fetch mocking).
const constructorOpts: Array<Record<string, unknown>> = [];
const disposeSpies: Array<ReturnType<typeof vi.fn>> = [];
const renderers: MockPgsRenderer[] = [];

class MockPgsRenderer {
  timeOffset: number;
  dispose = vi.fn();
  constructor(opts: Record<string, unknown>) {
    constructorOpts.push(opts);
    this.timeOffset = (opts.timeOffset as number) ?? 0;
    disposeSpies.push(this.dispose);
    renderers.push(this);
  }
}

vi.mock("libpgs", () => ({ PgsRenderer: MockPgsRenderer }));
vi.mock("libpgs/dist/libpgs.worker.js?url", () => ({ default: "/assets/libpgs.worker.js" }));

function makeVideoRef(): RefObject<HTMLVideoElement | null> {
  return { current: document.createElement("video") };
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
  constructorOpts.length = 0;
  disposeSpies.length = 0;
  renderers.length = 0;
});

describe("usePGSSubtitles", () => {
  it("creates a renderer wired to the video and .sup URL for a PGS track", async () => {
    const videoRef = makeVideoRef();
    const { result } = renderHook(() => usePGSSubtitles(videoRef, tracks, 3, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    expect(opts.video).toBe(videoRef.current);
    expect(opts.subUrl).toBe(pgsTrack.url);
    expect(opts.workerUrl).toBe("/assets/libpgs.worker.js");
    expect(opts.aspectRatio).toBe("contain");
    expect(result.current.isActive).toBe(true);
  });

  it("does nothing for text and ASS tracks", async () => {
    const { result, rerender } = renderHook(
      ({ index }) => usePGSSubtitles(makeVideoRef(), tracks, index, false, 0, 0),
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
    renderHook(() => usePGSSubtitles(makeVideoRef(), tracks, 3, false, 30, 2000));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    expect(constructorOpts[0]!.timeOffset).toBe(28);
  });

  it("updates the time offset in place without recreating the renderer", async () => {
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ delay }) => usePGSSubtitles(videoRef, tracks, 3, false, 0, delay),
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
      ({ index }) => usePGSSubtitles(videoRef, tracks, index, false, 0, 0),
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
      ({ index }) => usePGSSubtitles(videoRef, tracks, index, false, 0, 0),
      { initialProps: { index: 3 } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ index: 4 });
    await waitFor(() => expect(renderers).toHaveLength(2));
    expect(disposeSpies[0]!).toHaveBeenCalledTimes(1);
    expect(constructorOpts[1]!.subUrl).toBe(otherPgsTrack.url);
  });

  it("disposes the renderer while detached and reports inactive", async () => {
    const videoRef = makeVideoRef();
    const { result, rerender } = renderHook(
      ({ detached }) => usePGSSubtitles(videoRef, tracks, 3, detached, 0, 0),
      { initialProps: { detached: false } },
    );

    await waitFor(() => expect(renderers).toHaveLength(1));

    rerender({ detached: true });
    await waitFor(() => expect(disposeSpies[0]!).toHaveBeenCalledTimes(1));
    expect(result.current.isActive).toBe(false);
  });

  it("disposes the renderer on unmount", async () => {
    const { unmount } = renderHook(() => usePGSSubtitles(makeVideoRef(), tracks, 3, false, 0, 0));

    await waitFor(() => expect(renderers).toHaveLength(1));

    unmount();
    expect(disposeSpies[0]!).toHaveBeenCalled();
  });
});
