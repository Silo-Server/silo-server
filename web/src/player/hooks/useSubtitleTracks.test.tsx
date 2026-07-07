import type { RefObject } from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSubtitleTracks } from "./useSubtitleTracks";
import type { PlayerSubtitleInfo } from "../types";

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
  activeCues: FakeVTTCue[] = [];
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
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
    const track = new FakeTextTrack();
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
}) {
  const videoRef = makeVideoRef(initial.readyState);
  const defaultAnchor = { current: 0 };
  const hook = renderHook(
    ({ origin, durationRef, anchorRef }) =>
      useSubtitleTracks(
        videoRef,
        [srtTrack],
        1,
        origin,
        0,
        durationRef,
        anchorRef ?? defaultAnchor,
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
    // First window's only cue ends at 12s — far short of the 600s window.
    // That must NOT be treated as end-of-input (the file is 2h long).
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"));

    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 7200 } });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    // Play into the prefetch lead of the first window ([0, 600]).
    videoRef.current!.currentTime = 580;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=595");
  });

  it("stops prefetching once the window reaches the media duration", async () => {
    fetchMock.mockResolvedValue(vttResponse("WEBVTT\n\n00:00:10.000 --> 00:00:12.000\nearly\n\n"));

    // Whole file fits inside the first 600s window.
    const { videoRef } = renderTracks({ origin: 0, durationRef: { current: 500 } });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createdTracks[0]?.cues).toHaveLength(1));

    videoRef.current!.currentTime = 580;
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
    expect(String(fetchMock.mock.calls[0]![0])).toContain("position=1398");
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
    expect(String(fetchMock.mock.calls[1]![0])).toContain("position=1398");
    // Reset dropped the old window's cues and loaded the new window's.
    await waitFor(() => expect(createdTracks[0]!.cues.map((c) => c.text)).toEqual(["late"]));
  });

  it("rebuilds the track when the stream restarts (generation bump)", async () => {
    // Fresh response per call: vttResponse's streamed body is single-use, and
    // the rebuild issues a second fetch.
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

    // A transcode restart (e.g. selecting a text track that turns off PGS
    // burn-in) reloads the <video> element and orphans the track built moments
    // earlier; a generation bump must rebuild it against the new stream so the
    // text subtitles still render.
    rerender({ generation: 1 });

    await waitFor(() => expect(createdTracks).toHaveLength(2));
    await waitFor(() => expect(createdTracks[1]!.cues).toHaveLength(1));
  });
});
