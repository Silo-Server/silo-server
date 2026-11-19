import { act, renderHook, waitFor } from "@testing-library/react";
import type { RefObject } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { EnginePlayer } from "../engine/EnginePlayer";
import { attachEngineToElement } from "../engine/facade";
import { EngineTextTrack } from "../engine/textTracks";
import type { PlayerSubtitleInfo } from "../types";
import { useSubtitleTracks } from "./useSubtitleTracks";

// jsdom implements neither addTextTrack nor VTTCue; provide minimal fakes
// that keep the shapes the hook relies on (cues list, add/removeCue,
// cuechange listeners).
class FakeVTTCue {
  constructor(
    public startTime: number,
    public endTime: number,
    public text: string,
  ) {}
}

class FakeTextTrack {
  mode = "hidden";
  cues: FakeVTTCue[] = [];
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  constructor(private video?: HTMLVideoElement) {}
  // jsdom never computes active cues, so resolve them from the element's
  // clock the way a real TextTrack does.
  get activeCues(): FakeVTTCue[] {
    const time = this.video?.currentTime ?? 0;
    return this.cues.filter((cue) => cue.startTime <= time && time < cue.endTime);
  }
  addCue(cue: FakeVTTCue) {
    this.cues.push(cue);
  }
  removeCue(cue: FakeVTTCue) {
    const i = this.cues.indexOf(cue);
    if (i >= 0) this.cues.splice(i, 1);
  }
}

const createdTracks: FakeTextTrack[] = [];

// jsdom media elements always report readyState 0 (nothing ever loads);
// default to HAVE_METADATA so tests exercise the normal "clock is
// trustworthy" path unless a test opts into the unloaded state.
function makeVideoRef(readyState = 1): RefObject<HTMLVideoElement | null> {
  const video = document.createElement("video");
  (video as unknown as { addTextTrack: () => FakeTextTrack }).addTextTrack = () => {
    const track = new FakeTextTrack(video);
    createdTracks.push(track);
    return track;
  };
  Object.defineProperty(video, "readyState", { value: readyState, configurable: true });
  return { current: video };
}

/** One-shot streamed response yielding the whole VTT body in a single chunk. */
function vttResponse(body: string) {
  const encoder = new TextEncoder();
  let sent = false;
  return {
    ok: true,
    body: {
      getReader: () => ({
        read: async () => {
          if (sent) return { done: true, value: undefined };
          sent = true;
          return { done: false, value: encoder.encode(body) };
        },
      }),
    },
  };
}

const srtTrack: PlayerSubtitleInfo = {
  index: 1,
  language: "eng",
  codec: "subrip",
  label: "English",
  source: "embedded",
  url: "/api/v1/stream/x/subtitles/1?token=abc",
};

const fetchMock = vi.fn();

beforeEach(() => {
  createdTracks.length = 0;
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("VTTCue", FakeVTTCue);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderTracks(initial: {
  origin: number;
  durationRef: RefObject<number>;
  anchorRef?: RefObject<number>;
  readyState?: number;
  streamGeneration?: number;
  track?: PlayerSubtitleInfo;
  sourceGeneration?: number;
  refreshRevision?: number;
  onLoadState?: (
    state: "idle" | "loading" | "refreshing" | "ready" | "stalled" | "error" | "unavailable",
  ) => void;
  onSourceChanged?: () => void;
}) {
  const videoRef = makeVideoRef(initial.readyState);
  const defaultAnchor = { current: 0 };
  const hook = renderHook(
    ({
      origin,
      durationRef,
      anchorRef,
      streamGeneration,
      track,
      sourceGeneration,
      refreshRevision,
      onLoadState,
      onSourceChanged,
    }) =>
      useSubtitleTracks(
        videoRef,
        [track ?? srtTrack],
        1,
        origin,
        0,
        durationRef,
        anchorRef ?? defaultAnchor,
        undefined,
        null,
        streamGeneration ?? 0,
        onLoadState,
        onSourceChanged,
        sourceGeneration ?? 0,
        0,
        refreshRevision ?? 0,
      ),
    { initialProps: initial },
  );
  return { videoRef, ...hook };
}

describe("useSubtitleTracks", () => {
  it("adds cues rebased by the stream origin", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:01:40.000 --> 00:01:44.000\nhello\n\n"));

    renderTracks({ origin: 60, durationRef: { current: 7200 } });

    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));
    const cue = createdTracks[0]!.cues[0]!;
    expect(cue.startTime).toBe(40);
    expect(cue.endTime).toBe(44);
    expect(cue.text).toBe("hello");
  });

  it("shifts existing cues in place when the stream origin changes, without refetching", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:01:40.000 --> 00:01:44.000\nhello\n\n"));

    const { rerender } = renderTracks({ origin: 0, durationRef: { current: 7200 } });

    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));
    expect(createdTracks[0]!.cues[0]!.startTime).toBe(100);
    const fetchCount = fetchMock.mock.calls.length;

    // Copy-mode session restart at a later position: origin jumps forward.
    rerender({ origin: 60, durationRef: { current: 7200 } });

    await waitFor(() => expect(createdTracks[0]!.cues[0]!.startTime).toBe(40));
    expect(createdTracks[0]!.cues[0]!.endTime).toBe(44);
    // Same track, same cues, no extra network round trip.
    expect(createdTracks).toHaveLength(1);
    expect(fetchMock.mock.calls.length).toBe(fetchCount);
  });

  it("keeps prefetching past a window whose cues stop before the window end", async () => {
    // First window's only cue ends at 12s — far short of the 120s window.
    // That must NOT be treated as end-of-input (the file is 2h long).
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"));

    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    // A position beyond the first window re-fetches from just behind it.
    videoRef.current!.currentTime = 580;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=570");
  });

  it("stops prefetching once the window reaches the media duration", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"));

    // Whole file fits inside the first 120s window.
    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 100 } });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    videoRef.current!.currentTime = 100;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    // Give any (incorrect) follow-up fetch a chance to fire.
    await new Promise((r) => setTimeout(r, 10));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("anchors the initial fetch to the intended start position while no media is loaded", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nresumed\n\n"));

    // Resume at 1400s: the element hasn't loaded media yet (readyState 0,
    // currentTime 0), so the fetch must target the resume position, not 0.
    renderTracks({
      origin: 0,
      durationRef: { current: 2808 },
      anchorRef: { current: 1400 },
      readyState: 0,
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=1380&duration=120");
    expect(String(fetchMock.mock.calls[0]![0])).toContain("token=abc");
  });

  it("snaps a resumed position to the window grid so nearby polls share one request", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nresumed\n\n"));

    // 100.5 s is inside the [90, 210) grid cell: a poll at 101.2 s must ask
    // for the same window instead of re-demuxing 115 of 120 s.
    renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      anchorRef: { current: 100.5 },
      readyState: 0,
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=90&duration=120");
  });

  it("coalesces a burst of identical in-flight window requests", async () => {
    let releaseFetch: ((value: unknown) => void) | undefined;
    fetchMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          releaseFetch = resolve;
        }),
    );

    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    // timeupdate + seeking/seeked bursts can arrive within a few milliseconds.
    for (const time of [1, 2, 3]) {
      videoRef.current!.currentTime = time;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      videoRef.current!.dispatchEvent(new Event("seeking"));
      videoRef.current!.dispatchEvent(new Event("seeked"));
    }
    await new Promise((r) => setTimeout(r, 10));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    releaseFetch?.(vttResponse("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nburst\n\n"));
    await waitFor(() => expect(createdTracks[0]?.cues.map((c) => c.text)).toEqual(["burst"]));
  });

  it("asks for one window per grid cell across the anchor and the element clock", async () => {
    let finish!: (value: unknown) => void;
    fetchMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );

    // Before the element has media, the intended start position is 812.4 s.
    const { videoRef } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      anchorRef: { current: 812.4 },
      readyState: 0,
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=810&duration=120");

    // The element then reports its own clock, 838 s. Both readings sit in the
    // same cell, so this must not ask for a second, shifted window.
    Object.defineProperty(videoRef.current!, "readyState", { value: 1, configurable: true });
    videoRef.current!.currentTime = 838;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]![1].signal.aborted).toBe(false);

    finish(vttResponse("WEBVTT\n\n14:00.000 --> 14:02.000\ncell\n\n"));
    await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["cell"]));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("queues the next window behind an in-flight one instead of aborting it", async () => {
    let finish!: (value: unknown) => void;
    fetchMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );

    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    // A timeupdate far outside the requested range is not a seek: the extract
    // already running must finish and land in the server's window cache.
    videoRef.current!.currentTime = 400;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]![1].signal.aborted).toBe(false);

    finish(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nfirst\n\n"));
    // Once it settles the playhead's own window is requested, aligned to the
    // grid (floor((400 - 2) / 30) * 30).
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=390&duration=120");
  });

  it("advances past an empty first window instead of treating it as a failure", async () => {
    const state = vi.fn();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    // A valid VTT document with no cues: a dialogue gap where the playhead
    // starts, or a track whose first cue begins later.
    fetchMock
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:01:50.000 --> 00:01:52.000\nlater\n\n"));

    renderTracks({ origin: 0, durationRef: { current: 7200 }, onLoadState: state });

    // The next slice is asked for as soon as the empty one commits, without
    // waiting for a timeupdate or for the prefetch lead to come due.
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=0&duration=120");
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=90&duration=120");
    await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["later"]));
    expect(state).not.toHaveBeenCalledWith("error");
    expect(error).not.toHaveBeenCalled();
    error.mockRestore();
  });

  it("builds the track when the media element arrives after the mount", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nlate\n\n"));
    // A stream reload replaces the <video> element, so the rebuilt track lands
    // in the gap where the ref is still empty.
    const videoRef: RefObject<HTMLVideoElement | null> = { current: null };

    renderHook(() =>
      useSubtitleTracks(videoRef, [srtTrack], 1, 0, 0, { current: 7200 }, { current: 0 }),
    );

    await act(async () => {});
    expect(fetchMock).not.toHaveBeenCalled();

    // The replacement element appears and no revision changes: the hook has to
    // notice the element itself, or the track never fetches and never loads.
    videoRef.current = makeVideoRef().current;
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=0&duration=120");
    await waitFor(() => expect(createdTracks.at(-1)!.cues.map((c) => c.text)).toEqual(["late"]));
  });

  it("still aborts the in-flight window when the track changes", async () => {
    let finish!: (value: unknown) => void;
    fetchMock.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nother\n\n"));

    const { rerender } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    rerender({
      origin: 0,
      durationRef: { current: 7200 },
      track: { ...srtTrack, url: "/api/v1/stream/x/subtitles/2?token=abc" },
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[0]![1].signal.aborted).toBe(true);
    expect(String(fetchMock.mock.calls[1]![0])).toContain("subtitles/2");
    finish(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nstale\n\n"));
    // The rebuilt track holds the new track's cues; the aborted window's late
    // bytes are dropped.
    await waitFor(() => expect(createdTracks.at(-1)!.cues.map((c) => c.text)).toEqual(["other"]));
    expect(createdTracks.flatMap((t) => t.cues.map((c) => c.text))).not.toContain("stale");
  });

  it("resets and refetches on a forward seek past the covered window", async () => {
    fetchMock
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nlate\n\n"));

    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    // Jump far past the first window ([0, 600]); the gap in between was
    // never fetched, so coverage must reset around the new position.
    videoRef.current!.currentTime = 1400;
    videoRef.current!.dispatchEvent(new Event("seeked"));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=1380");
    // Reset dropped the old window's cues and loaded the new window's.
    await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["late"]));
  });

  it.each([false, true])(
    "rebuilds loaded cues after stream restart (HLS cleared track: %s)",
    async (hlsCleared) => {
      fetchMock.mockImplementation(() =>
        Promise.resolve(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nhi\n\n")),
      );

      const videoRef = makeVideoRef(1);
      const anchorRef = { current: 0 };
      const durationRef = { current: 7200 };
      const { rerender } = renderHook(
        ({ generation }) =>
          useSubtitleTracks(
            videoRef,
            [srtTrack],
            1,
            0,
            0,
            durationRef,
            anchorRef,
            undefined,
            null,
            generation,
          ),
        { initialProps: { generation: 0 } },
      );

      await waitFor(() => expect(createdTracks).toHaveLength(1));
      await waitFor(() => expect(createdTracks[0]!.cues).toHaveLength(1));

      // A transcode restart (seek, quality/audio switch, burn-in toggle)
      // reloads the <video> element and can orphan the track; a generation bump
      // must rebuild it against the new stream so the text subtitles still
      // render — carrying the loaded cues and coverage over instead of
      // refetching the window.
      if (hlsCleared) {
        // HLS TimelineController clears every native track before the hook's
        // cleanup runs. Coverage and decoded source cues must survive that.
        createdTracks[0]!.cues = [];
      }
      rerender({ generation: 1 });

      await waitFor(() => expect(createdTracks).toHaveLength(2));
      expect(createdTracks[1]!.cues).toHaveLength(1);
      expect(createdTracks[1]!.cues[0]!.text).toBe("hi");
      expect(fetchMock).toHaveBeenCalledTimes(1);
    },
  );

  it("moves a selected track onto the browser engine once its metadata arrives", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nhi\n\n"));
    const videoRef = makeVideoRef(1);
    const { result, rerender } = renderHook(
      ({ generation }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          0,
          0,
          { current: 7200 },
          { current: 0 },
          undefined,
          null,
          generation,
        ),
      { initialProps: { generation: 0 } },
    );
    // The track is built on the native element before the engine has loaded.
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    // The engine takes over the element, then reports loadedmetadata on it,
    // which VideoPlayer turns into a stream generation bump.
    const engineTracks: EngineTextTrack[] = [];
    const engine = {
      readyState: 1,
      currentTime: 0,
      paused: true,
      volume: 1,
      muted: false,
      addTextTrack: (kind: TextTrackKind, label: string, language: string) => {
        const track = new EngineTextTrack(kind, label, language);
        engineTracks.push(track);
        return track;
      },
    };
    const detach = attachEngineToElement(videoRef.current!, engine as unknown as EnginePlayer);
    rerender({ generation: 1 });

    await waitFor(() => expect(engineTracks).toHaveLength(1));
    // The fetched cues carry over, and the engine's clock drives them.
    expect(fetchMock).toHaveBeenCalledTimes(1);
    act(() => engineTracks[0]!.update(11));
    expect(result.current).toEqual([{ text: "hi" }]);
    detach();
  });

  it("refetches retimed cues behind an unchanged URL when the cue revision changes", async () => {
    fetchMock
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nold\n\n"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:12.300 --> 00:00:14.300\nnew\n\n"));

    const videoRef = makeVideoRef(1);
    const anchorRef = { current: 0 };
    const durationRef = { current: 7200 };
    const { rerender } = renderHook(
      ({ revision }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          0,
          0,
          durationRef,
          anchorRef,
          undefined,
          null,
          0,
          undefined,
          undefined,
          0,
          revision,
        ),
      { initialProps: { revision: 0 } },
    );

    await waitFor(() => expect(createdTracks[0]?.cues.map((c) => c.text)).toEqual(["old"]));
    rerender({ revision: 1 });

    await waitFor(() => expect(createdTracks).toHaveLength(2));
    await waitFor(() => expect(createdTracks[1]!.cues.map((c) => c.text)).toEqual(["new"]));
    expect(createdTracks[1]!.cues[0]!.startTime).toBeCloseTo(12.3);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("keeps the current cues on screen while retimed ones load", async () => {
    let answer: (value: unknown) => void = () => {};
    fetchMock
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nold\n\n"))
      .mockReturnValueOnce(
        new Promise((resolve) => {
          answer = resolve;
        }),
      );
    const states: string[] = [];
    const videoRef = makeVideoRef(1);
    const { rerender } = renderHook(
      ({ revision }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          0,
          0,
          { current: 7200 },
          { current: 0 },
          undefined,
          null,
          0,
          (state) => states.push(state),
          undefined,
          0,
          revision,
        ),
      { initialProps: { revision: 0 } },
    );
    await waitFor(() => expect(createdTracks[0]?.cues.map((c) => c.text)).toEqual(["old"]));
    states.length = 0;

    rerender({ revision: 1 });
    await waitFor(() => expect(createdTracks).toHaveLength(2));
    // The retimed track starts with the old cues: nothing blinks, and the
    // reload is not announced as loading.
    expect(createdTracks[1]!.cues.map((c) => c.text)).toEqual(["old"]);
    expect(states).toEqual(["refreshing"]);

    await act(async () => {
      answer(vttResponse("WEBVTT\n\n00:00:12.300 --> 00:00:14.300\nnew\n\n"));
    });
    await waitFor(() => expect(createdTracks[1]!.cues.map((c) => c.text)).toEqual(["new"]));
    expect(states.at(-1)).toBe("ready");
    expect(states).not.toContain("loading");
  });

  it("deduplicates restored cues newly visible after the origin moves backward", async () => {
    fetchMock.mockImplementation(async () =>
      vttResponse(
        "WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n00:02:00.000 --> 00:02:04.000\nlater\n\n",
      ),
    );
    const videoRef = makeVideoRef();
    const durationRef = { current: 7200 };
    const anchorRef = { current: 100 };
    const { rerender } = renderHook(
      ({ origin, generation }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          origin,
          0,
          durationRef,
          anchorRef,
          undefined,
          null,
          generation,
        ),
      { initialProps: { origin: 100, generation: 0 } },
    );
    await waitFor(() => expect(createdTracks[0]!.cues).toHaveLength(1));
    // Keep the same source position while the replacement stream starts at zero.
    videoRef.current!.currentTime = 100;
    createdTracks[0]!.cues = [];
    rerender({ origin: 0, generation: 1 });
    await waitFor(() => expect(createdTracks[1]!.cues).toHaveLength(2));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    // Stored subtitles can return the full track for an overlapping window.
    act(() => {
      videoRef.current!.currentTime = 680;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await act(async () => {});
    expect(createdTracks[1]!.cues.map((cue) => cue.text)).toEqual(["early", "later"]);
  });

  it("retries a failed window fetch after a backoff instead of marking it covered", async () => {
    let now = 1_000_000;
    const nowSpy = vi.spyOn(Date, "now").mockImplementation(() => now);
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      fetchMock
        .mockRejectedValueOnce(new Error("extraction died"))
        .mockResolvedValueOnce(
          vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nrecovered\n\n"),
        );

      const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });

      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

      // Inside the backoff window: no retry yet.
      videoRef.current!.currentTime = 5;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await new Promise((r) => setTimeout(r, 10));
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // Past the backoff: the uncovered range is retried and recovers.
      now += 6_000;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["recovered"]));
    } finally {
      nowSpy.mockRestore();
      errorSpy.mockRestore();
    }
  });

  it("stops scheduling retries after the consecutive-failure ceiling", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockRejectedValue(new Error("extractor unavailable"));
    const { videoRef, unmount } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    try {
      await act(async () => {
        // The remaining backoff ladder the budget allows: initial fetch, then
        // the 5s/10s/20s retries spend it (the 4th failure never schedules a
        // 40s retry). Advance step-by-step, nudging media events between
        // backoff expirations like real playback would.
        await vi.advanceTimersByTimeAsync(0);
      });
      let attempts = 1;
      for (const delay of [5000, 10000, 20000]) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(delay - 1);
          videoRef.current!.dispatchEvent(new Event("timeupdate"));
        });
        // Inside the backoff window the event must not fetch.
        expect(fetchMock).toHaveBeenCalledTimes(attempts);
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1);
        });
        // Past the backoff: the scheduled retry fetches.
        expect(fetchMock).toHaveBeenCalledTimes(++attempts);
      }
      expect(fetchMock).toHaveBeenCalledTimes(4);
      // The budget is spent: no further retry fires, and media events do not
      // revive the fetcher for this mount.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(4);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("does not re-enter the fetching effect when a failed window renders its error state", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockResolvedValue(new Response(null, { status: 503 }));
    const initial = { origin: 0, durationRef: { current: 7200 } };
    const { rerender, unmount } = renderTracks(initial);
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // The failure told the player to render "error"; that render re-runs the
      // hook with the same inputs. It must not start another extraction: the
      // scheduled backoff is the only path forward, or a failing track turns
      // every render into an ffmpeg spawn.
      rerender(initial);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      rerender(initial);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("reports stalled after a repeated retryable refusal and recovers on the retry", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    // The production shape: a 4K text window answered
    // subtitle_window_budget_exceeded with Retry-After, twice. The first refusal
    // promises the retry is served from the recovery extraction; the second one
    // proves it was not, so the overlay must stop claiming a load is in
    // progress while the ladder keeps retrying.
    fetchMock.mockResolvedValue(new Response(null, { status: 503 }));
    const states: string[] = [];
    const { unmount } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      onLoadState: (state) => states.push(state),
    });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(states).toContain("loading");
      expect(states).not.toContain("stalled");

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(states).toContain("stalled");
      expect(states).not.toContain("error");

      // A later attempt is served: the state returns to ready and the stall
      // notice is not repeated.
      fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:01:40.000 --> 00:01:44.000\nok\n\n"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(states).toContain("ready");
      const stalledCount = states.filter((state) => state === "stalled").length;
      expect(stalledCount).toBe(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("retries a retryable 503 in the background without reporting the track failed", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    // The server is recovering the page it refused; Retry-After says when.
    fetchMock.mockResolvedValue(
      new Response(null, { status: 503, headers: { "Retry-After": "30" } }),
    );
    const states: string[] = [];
    const { videoRef, unmount } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      onLoadState: (state) => states.push(state),
    });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      // A refusal the server is already recovering from is not a failed track:
      // playback continues, the track stays selected, and the player keeps
      // saying it is loading rather than offering a manual retry.
      expect(states).toContain("loading");
      expect(states).not.toContain("error");

      // The header is honored: nothing fires before 30 s even though the local
      // ladder would have retried after 5 s.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(25_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(states).not.toContain("error");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("keeps retrying explicitly-retryable 503s past the terminal ceiling", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockResolvedValue(new Response(null, { status: 503 }));
    const { videoRef, unmount } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // Four failures would have spent the terminal budget; retryable statuses
      // ride their own generous budget, so attempts 5 and 6 still fetch on the
      // 40s/60s backoff rungs instead of latching terminal.
      let attempts = 1;
      for (const delay of [5000, 10000, 20000, 40000, 60000]) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(delay - 1);
          videoRef.current!.dispatchEvent(new Event("timeupdate"));
        });
        expect(fetchMock).toHaveBeenCalledTimes(attempts);
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1);
        });
        expect(fetchMock).toHaveBeenCalledTimes(++attempts);
      }
      expect(fetchMock).toHaveBeenCalledTimes(6);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("recovers on a track rebuild after the consecutive-failure ceiling latches terminal", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    // Every fetch fails until the rebuild, then the rebuilt mount succeeds.
    fetchMock.mockRejectedValue(new Error("relay is dead"));
    const { videoRef, rerender, unmount } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
    });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      let attempts = 1;
      // The backoff ladder spends the budget: initial fetch, then 5s/10s/20s
      // retries; the 4th failure never schedules another retry.
      for (const delay of [5000, 10000, 20000]) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(delay - 1);
          videoRef.current!.dispatchEvent(new Event("timeupdate"));
        });
        expect(fetchMock).toHaveBeenCalledTimes(attempts);
        await act(async () => {
          await vi.advanceTimersByTimeAsync(1);
        });
        expect(fetchMock).toHaveBeenCalledTimes(++attempts);
      }
      expect(fetchMock).toHaveBeenCalledTimes(4);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      // Terminal between rebuilds: no backoff retry and no timeupdate revival.
      // (An explicit seek resets the budget too; that path is covered
      // separately.)
      expect(fetchMock).toHaveBeenCalledTimes(4);

      // A stream restart rebuilds the fetcher (streamGeneration bump) and its
      // fresh budget lets the rebuilt mount fetch and recover.
      fetchMock.mockResolvedValue(
        vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nrecovered\n\n"),
      );
      rerender({ origin: 0, durationRef: { current: 7200 }, streamGeneration: 1 });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(5);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(200);
      });
      expect(createdTracks[1]!.cues.map((c) => c.text)).toEqual(["recovered"]);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("does not treat a failed window as covered when prefetching later", async () => {
    let now = 1_000_000;
    const nowSpy = vi.spyOn(Date, "now").mockImplementation(() => now);
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      fetchMock
        .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nfirst\n\n"))
        .mockRejectedValueOnce(new Error("prefetch died"))
        .mockResolvedValueOnce(vttResponse("WEBVTT\n\n10:05.000 --> 10:07.000\nsecond\n\n"));

      const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

      // Enter the prefetch lead of window [0, 120] — this extend fails.
      videoRef.current!.currentTime = 100;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

      // After the backoff the same still-uncovered range is fetched again;
      // before the fix the failed window counted as covered and subtitles
      // silently stopped for its whole span.
      now += 6_000;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
      expect(String(fetchMock.mock.calls[2]![0])).toContain("position=90");
      await waitFor(() =>
        expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["first", "second"]),
      );
    } finally {
      nowSpy.mockRestore();
      errorSpy.mockRestore();
    }
  });

  it("fetches a sidecar track once whole and never refetches on seeks", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nwhole\n\n"));
    const externalTrack: PlayerSubtitleInfo = { ...srtTrack, source: "external" };
    const { videoRef } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      track: externalTrack,
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    // A whole-track fetch asks for the complete artifact: no position/duration
    // window parameters at all.
    expect(String(fetchMock.mock.calls[0]![0])).toBe(externalTrack.url);
    await waitFor(() => expect(createdTracks[0]?.cues.map((c) => c.text)).toEqual(["whole"]));

    // Seeking inside a complete track never needs another request.
    for (const time of [600, 1400, 30]) {
      videoRef.current!.currentTime = time;
      videoRef.current!.dispatchEvent(new Event("seeking"));
      videoRef.current!.dispatchEvent(new Event("seeked"));
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
    }
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["whole"]);
  });

  it("treats a track without a source as embedded and windows it", async () => {
    fetchMock.mockResolvedValue(
      vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nembedded\n\n"),
    );
    const { videoRef } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      track: { ...srtTrack, source: undefined },
    });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const url = String(fetchMock.mock.calls[0]![0]);
    expect(url).toContain("position=");
    expect(url).toContain("duration=120");
    await waitFor(() => expect(createdTracks[0]?.cues.map((c) => c.text)).toEqual(["embedded"]));

    videoRef.current!.currentTime = 600;
    videoRef.current!.dispatchEvent(new Event("seeking"));
    videoRef.current!.dispatchEvent(new Event("seeked"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });

  it("backs off a failed whole-track fetch instead of refetching on every timeupdate", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock
      .mockRejectedValueOnce(new Error("sidecar read died"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nwhole\n\n"));
    const { videoRef, unmount } = renderTracks({
      origin: 0,
      durationRef: { current: 7200 },
      track: { ...srtTrack, source: "external" },
    });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // Inside the backoff a timeupdate must not start a second whole-track
      // read; the scheduled retry is the only path forward.
      await act(async () => {
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["whole"]);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("retries immediately on an out-of-coverage seek during backoff", async () => {
    let now = 1_000_000;
    const nowSpy = vi.spyOn(Date, "now").mockImplementation(() => now);
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      fetchMock
        .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"))
        .mockRejectedValueOnce(new Error("prefetch died"))
        .mockResolvedValueOnce(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nlate\n\n"));

      const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

      // Enter the prefetch lead; this extend fails and starts the backoff.
      videoRef.current!.currentTime = 100;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

      // Still inside the backoff: a seek into unfetched range is a new window
      // request, so it must not wait out a retry for the failed one.
      now += 1_000;
      videoRef.current!.currentTime = 1400;
      videoRef.current!.dispatchEvent(new Event("seeked"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
      expect(String(fetchMock.mock.calls[2]![0])).toContain("position=1380");
      await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["late"]));
    } finally {
      nowSpy.mockRestore();
      errorSpy.mockRestore();
    }
  });

  it("retries on a seek while the failed first window is still backing off", async () => {
    let now = 1_000_000;
    const nowSpy = vi.spyOn(Date, "now").mockImplementation(() => now);
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      fetchMock
        .mockRejectedValueOnce(new Error("first extraction died"))
        .mockResolvedValueOnce(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nlate\n\n"));
      const states: string[] = [];
      const { videoRef } = renderTracks({
        origin: 0,
        durationRef: { current: 7200 },
        onLoadState: (state) => states.push(state),
      });
      // Wait until the initial failure has been counted, not merely requested.
      await waitFor(() => expect(states.at(-1)).toBe("error"));
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // Still inside the 5s backoff and hasFetched is false: no window has
      // ever loaded, so a seek is a fresh request rather than a retry of the
      // failed one. It must not wait out the backoff.
      now += 1_000;
      videoRef.current!.currentTime = 1400;
      videoRef.current!.dispatchEvent(new Event("seeked"));
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      expect(String(fetchMock.mock.calls[1]![0])).toContain("position=1380");
      await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["late"]));
    } finally {
      nowSpy.mockRestore();
      errorSpy.mockRestore();
    }
  });

  it("revives the fetcher on a seek after the terminal failure budget", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"))
      .mockRejectedValueOnce(new Error("extend one dead"))
      .mockRejectedValueOnce(new Error("extend two dead"))
      .mockRejectedValueOnce(new Error("extend three dead"))
      .mockRejectedValueOnce(new Error("extend four dead"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nlate\n\n"));
    const { videoRef, unmount } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // Enter the prefetch lead; the first extend fails and spends one of the
      // four terminal failures.
      await act(async () => {
        videoRef.current!.currentTime = 100;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);

      // The 5s/10s/20s backoff rungs spend the rest of the budget; the fourth
      // failure schedules no fifth attempt.
      for (const delay of [5_000, 10_000, 20_000]) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(delay);
        });
      }
      expect(fetchMock).toHaveBeenCalledTimes(5);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      expect(fetchMock).toHaveBeenCalledTimes(5);

      // A seek into unfetched range resets the spent budget and fetches.
      await act(async () => {
        videoRef.current!.currentTime = 1400;
        videoRef.current!.dispatchEvent(new Event("seeked"));
      });
      expect(fetchMock).toHaveBeenCalledTimes(6);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["late"]);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("populates the display when cues are added while paused", async () => {
    fetchMock.mockResolvedValue(
      vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\npaused cue\n\n"),
    );
    const videoRef = makeVideoRef(1);
    // The playhead sits inside the cue before the first fetch resolves; a real
    // track would leave it active, but adding a cue while paused does not fire
    // `cuechange`.
    videoRef.current!.currentTime = 11;
    const { result } = renderHook(() =>
      useSubtitleTracks(videoRef, [srtTrack], 1, 0, 0, { current: 7200 }, { current: 0 }),
    );

    await waitFor(() => expect(result.current).toEqual([{ text: "paused cue" }]));
    expect(videoRef.current!.paused).toBe(true);

    // The pause event refreshes the display from the track as well.
    act(() => videoRef.current!.dispatchEvent(new Event("pause")));
    expect(result.current).toEqual([{ text: "paused cue" }]);
  });

  it("re-reads the active cues when playback resumes", async () => {
    fetchMock.mockResolvedValue(
      vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nresumed cue\n\n"),
    );
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 11;
    const { result } = renderHook(() =>
      useSubtitleTracks(videoRef, [srtTrack], 1, 0, 0, { current: 7200 }, { current: 0 }),
    );

    await waitFor(() => expect(result.current).toEqual([{ text: "resumed cue" }]));

    // The browser dropped the active cue while paused: no cuechange fires, so
    // the display clears on the pause refresh and stays empty.
    videoRef.current!.currentTime = 0;
    act(() => videoRef.current!.dispatchEvent(new Event("pause")));
    expect(result.current).toEqual([]);

    // Resuming inside the cue re-reads the track instead of waiting for a
    // cuechange that may never come.
    videoRef.current!.currentTime = 11;
    act(() => videoRef.current!.dispatchEvent(new Event("play")));
    expect(result.current).toEqual([{ text: "resumed cue" }]);
  });

  it("carries cue placement through to the display objects", async () => {
    fetchMock.mockResolvedValue(
      vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000 align:right line:50%\nmiddle right\n\n"),
    );
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 11;
    const { result } = renderHook(() =>
      useSubtitleTracks(videoRef, [srtTrack], 1, 0, 0, { current: 7200 }, { current: 0 }),
    );

    await waitFor(() =>
      expect(result.current).toEqual([{ text: "middle right", align: "right", line: "middle" }]),
    );
    // The cue itself also receives the settings when the implementation has
    // them; a fake that only stores text must not throw.
    const cue = createdTracks[0]!.cues[0] as unknown as { align?: string; line?: number };
    expect(cue.align).toBe("right");
    expect(cue.line).toBe(50);
  });

  it("repopulates live cues after a transport rebuild", async () => {
    const liveTrack: PlayerSubtitleInfo = {
      ...srtTrack,
      live: true,
      url: "",
    };
    const liveCues = [{ start: 0, end: 10, text: "live cue" }];
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 1;
    const { rerender } = renderHook(
      ({ transportRevision }: { transportRevision: number }) =>
        useSubtitleTracks(
          videoRef,
          [liveTrack],
          1,
          0,
          0,
          { current: 7200 },
          { current: 0 },
          liveCues,
          "job-1",
          0,
          undefined,
          undefined,
          0,
          0,
          0,
          transportRevision,
          0,
        ),
      { initialProps: { transportRevision: 0 } },
    );

    await waitFor(() =>
      expect(createdTracks[0]!.cues.map((cue) => cue.text)).toEqual(["live cue"]),
    );

    rerender({ transportRevision: 1 });
    await waitFor(() =>
      expect(createdTracks[1]!.cues.map((cue) => cue.text)).toEqual(["live cue"]),
    );
  });

  it("renders decoded cue text and preserves literal angle brackets", async () => {
    fetchMock.mockResolvedValue(
      vttResponse(
        "WEBVTT\n\n00:00:10.000 --> 00:00:12.000\n<i>Fish &lt;3 U &gt; 2</i> &amp; hearts &lt;b&gt;literal&lt;/b&gt;\n\n",
      ),
    );
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 11;
    const { result } = renderHook(() =>
      useSubtitleTracks(videoRef, [srtTrack], 1, 0, 0, { current: 7200 }, { current: 0 }),
    );

    await waitFor(() =>
      expect(result.current).toEqual([{ text: "Fish <3 U > 2 & hearts <b>literal</b>" }]),
    );
  });
});

describe("subtitle loading recovery", () => {
  it("aborts a pending window immediately on seek and ignores its late cues", async () => {
    let finishOld!: (value: { done: boolean; value: Uint8Array }) => void;
    fetchMock
      .mockResolvedValueOnce({
        ok: true,
        body: {
          getReader: () => ({
            read: () =>
              new Promise((resolve) => {
                finishOld = resolve;
              }),
          }),
        },
      })
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n23:20.000 --> 23:22.000\nnew\n\n"));
    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });
    await waitFor(() => expect(finishOld).toBeDefined());
    act(() => {
      videoRef.current!.currentTime = 1400;
      videoRef.current!.dispatchEvent(new Event("seeking"));
    });
    expect(fetchMock.mock.calls[0]![1].signal.aborted).toBe(true);
    await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["new"]));
    await act(async () => {
      finishOld({
        done: false,
        value: new TextEncoder().encode("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nold\n\n"),
      });
    });
    expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["new"]);
  });

  it("retries while paused without media events and reports recovery", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    const videoRef = makeVideoRef();
    fetchMock
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nrecovered\n\n"));
    const { unmount } = renderHook(() =>
      useSubtitleTracks(
        videoRef,
        [srtTrack],
        1,
        0,
        0,
        { current: 7200 },
        { current: 0 },
        undefined,
        null,
        0,
        state,
      ),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(state).toHaveBeenLastCalledWith("error");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("subtitle source changed", () => {
  function errorResponse(status: number, body?: unknown) {
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }

  function renderWithSourceChanged() {
    const videoRef = makeVideoRef();
    const onSourceChanged = vi.fn();
    const hook = renderHook(() =>
      useSubtitleTracks(
        videoRef,
        [srtTrack],
        1,
        0,
        0,
        { current: 7200 },
        { current: 0 },
        undefined,
        null,
        0,
        undefined,
        onSourceChanged,
      ),
    );
    return { videoRef, onSourceChanged, ...hook };
  }

  it("signals onSourceChanged once on 409 subtitle_source_changed and schedules no retry", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(409, { error: "subtitle_source_changed" })),
    );
    const { onSourceChanged, unmount } = renderWithSourceChanged();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).toHaveBeenCalledTimes(1);
      // Retrying the same stale URL can never succeed; even past the max
      // backoff no retry may fire.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("retries the same URL on a refresh revision after a 409 and renders the cues", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const states: string[] = [];
    const onSourceChanged = vi.fn();
    fetchMock
      .mockResolvedValueOnce(errorResponse(409, { error: "subtitle_source_changed" }))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nrecovered\n\n"));
    const videoRef = makeVideoRef();
    const durationRef = { current: 7200 };
    const { rerender, unmount } = renderHook(
      ({ refreshRevision }: { refreshRevision: number }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          0,
          0,
          durationRef,
          { current: 0 },
          undefined,
          null,
          0,
          (state) => states.push(state),
          onSourceChanged,
          0,
          0,
          refreshRevision,
        ),
      { initialProps: { refreshRevision: 0 } },
    );
    try {
      await waitFor(() => expect(onSourceChanged).toHaveBeenCalledTimes(1));
      // The stale URL cannot deliver cues: the overlay must stop claiming to
      // load and report the error until the refreshed URL arrives.
      expect(states.at(-1)).toBe("error");
      const staleUrl = fetchMock.mock.calls[0]![0];

      // The refresh replan lands even when it re-mints the same URL: the
      // rebuild retries it once instead of staying parked on the 409.
      rerender({ refreshRevision: 1 });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      expect(fetchMock.mock.calls[1]![0]).toBe(staleUrl);
      await waitFor(() => expect(createdTracks[1]!.cues.map((c) => c.text)).toEqual(["recovered"]));
    } finally {
      unmount();
      error.mockRestore();
    }
  });

  it("does not signal onSourceChanged on a generic 500 and still backs off", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() => Promise.resolve(errorResponse(500, { error: "internal" })));
    const { onSourceChanged, unmount } = renderWithSourceChanged();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).not.toHaveBeenCalled();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("parks the track on a plain 404 instead of backing off", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() => Promise.resolve(errorResponse(404, { error: "missing" })));
    const { onSourceChanged, unmount } = renderWithSourceChanged();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).not.toHaveBeenCalled();
      // The track does not exist for this file: the identical URL can never
      // start working, so no backoff retry may fire.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("signals the session gone once on a 410 subtitle fetch and stops retrying", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(410, { error: "playback_session_ended" })),
    );
    const videoRef = makeVideoRef();
    const onSessionGone = vi.fn();
    const { unmount } = renderHook(() =>
      useSubtitleTracks(
        videoRef,
        [srtTrack],
        1,
        0,
        0,
        { current: 7200 },
        { current: 0 },
        undefined,
        null,
        0,
        undefined,
        undefined,
        0,
        0,
        0,
        0,
        0,
        onSessionGone,
      ),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSessionGone).toHaveBeenCalledTimes(1);
      // The dead session cannot deliver this track. No backoff retries.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(onSessionGone).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("signals the session gone on a 404 that names the playback session", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(404, { error: "playback_session_not_found" })),
    );
    const videoRef = makeVideoRef();
    const onSessionGone = vi.fn();
    const { unmount } = renderHook(() =>
      useSubtitleTracks(
        videoRef,
        [srtTrack],
        1,
        0,
        0,
        { current: 7200 },
        { current: 0 },
        undefined,
        null,
        0,
        undefined,
        undefined,
        0,
        0,
        0,
        0,
        0,
        onSessionGone,
      ),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSessionGone).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("does not signal onSourceChanged again when a rebuilt track retries the same source generation", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(409, { error: "subtitle_source_changed" })),
    );
    const videoRef = makeVideoRef();
    const onSourceChanged = vi.fn();
    const { rerender, unmount } = renderHook(
      ({
        streamGeneration,
        sourceGeneration,
      }: {
        streamGeneration: number;
        sourceGeneration: number;
      }) =>
        useSubtitleTracks(
          videoRef,
          [srtTrack],
          1,
          0,
          0,
          { current: 7200 },
          { current: 0 },
          undefined,
          null,
          streamGeneration,
          undefined,
          onSourceChanged,
          sourceGeneration,
        ),
      { initialProps: { streamGeneration: 0, sourceGeneration: 0 } },
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).toHaveBeenCalledTimes(1);

      // A stream reload rebuilds the fetcher (a new effect instance) but the
      // source generation is unchanged: the 409 must not restart the cycle.
      rerender({ streamGeneration: 1, sourceGeneration: 0 });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).toHaveBeenCalledTimes(1);

      // A genuine source change re-arms the signal exactly once.
      rerender({ streamGeneration: 1, sourceGeneration: 1 });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).toHaveBeenCalledTimes(2);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("subtitle track missing (404) parking", () => {
  const missingTrack: PlayerSubtitleInfo = {
    index: 1,
    language: "eng",
    codec: "subrip",
    label: "English",
    source: "embedded",
    url: "/api/v2/stream/session-1/subtitles/1.vtt?file_id=7&embedded_stream_index=0",
  };
  const otherTrack: PlayerSubtitleInfo = {
    index: 2,
    language: "fra",
    codec: "subrip",
    label: "French",
    source: "embedded",
    url: "/api/v2/stream/session-1/subtitles/2.vtt?file_id=7&embedded_stream_index=1",
  };

  function errorResponse(status: number, body?: unknown) {
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  }

  function renderParking() {
    const videoRef = makeVideoRef(1);
    const states: string[] = [];
    const hook = renderHook(
      ({ activeIndex, streamGeneration }: { activeIndex: number; streamGeneration: number }) =>
        useSubtitleTracks(
          videoRef,
          [missingTrack, otherTrack],
          activeIndex,
          0,
          0,
          { current: 7200 },
          { current: 0 },
          undefined,
          null,
          streamGeneration,
          (state) => states.push(state),
        ),
      { initialProps: { activeIndex: missingTrack.index, streamGeneration: 0 } },
    );
    return { videoRef, states, ...hook };
  }

  it("stops fetching after the first 404 and stays parked through seeks", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(() => Promise.resolve(errorResponse(404, { error: "not_found" })));
    const { videoRef, states, rerender, unmount } = renderParking();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      // A missing track reports unavailable so the viewer sees the failure and
      // can turn it off or switch, without claiming to be ready or idle.
      expect(states.at(-1)).toBe("unavailable");

      // Backoff windows, window advances, and seeks are all the same parked
      // identity: none may re-request it.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      act(() => {
        videoRef.current!.currentTime = 600;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        videoRef.current!.dispatchEvent(new Event("seeking"));
        videoRef.current!.dispatchEvent(new Event("seeked"));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);

      // A stream reload rebuilds the fetcher for the same URL: the track is
      // still missing for this file, so the rebuild stays parked.
      rerender({ activeIndex: missingTrack.index, streamGeneration: 1 });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("unparks a track when the viewer switches away and back", async () => {
    fetchMock
      .mockResolvedValueOnce(errorResponse(404, { error: "not_found" }))
      .mockResolvedValueOnce(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nbonjour\n\n"))
      .mockResolvedValueOnce(errorResponse(404, { error: "not_found" }));
    const { rerender, unmount } = renderParking();
    try {
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

      rerender({ activeIndex: otherTrack.index, streamGeneration: 0 });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
      expect(fetchMock.mock.calls[1]![0]).toContain(otherTrack.url);
      await waitFor(() =>
        expect(createdTracks.at(-1)!.cues.map((cue) => cue.text)).toEqual(["bonjour"]),
      );

      // Re-selecting the parked track is a deliberate attempt, not a retry of
      // the parked window: the switch away cleared the verdict.
      rerender({ activeIndex: missingTrack.index, streamGeneration: 0 });
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
      expect(String(fetchMock.mock.calls[2]![0])).toContain(missingTrack.url);
    } finally {
      unmount();
    }
  });

  it("keeps the transient-failure retry for 5xx responses", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockImplementation(() =>
      Promise.resolve(errorResponse(503, { error: "unavailable" })),
    );
    const { unmount } = renderParking();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(3);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("keeps the retry for a network error with no response", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    fetchMock.mockRejectedValue(new TypeError("network down"));
    const { unmount } = renderParking();
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});
