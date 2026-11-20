import { ReadableStream } from "node:stream/web";

import { act, renderHook, waitFor } from "@testing-library/react";
import type { RefObject } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PlayerSubtitleInfo, VideoFitMode } from "../types";
import { useASSSubtitles } from "./useASSSubtitles";

// Capture the options every JASSUB instance is constructed with, plus the
// instances themselves so tests can observe later timeOffset updates.
const constructorOpts: Array<Record<string, unknown>> = [];
const instances: Array<{
  timeOffset: number;
  resize: ReturnType<typeof vi.fn>;
  destroy: ReturnType<typeof vi.fn>;
  renderer: {
    setTrack: ReturnType<typeof vi.fn>;
    setTrackByUrl: ReturnType<typeof vi.fn>;
    addFonts: ReturnType<typeof vi.fn>;
  };
  prescaleFactor: number;
  prescaleHeightLimit: number;
  _canvas: HTMLCanvasElement;
}> = [];
let rendererReady: Promise<void> = Promise.resolve();

vi.mock("jassub", () => {
  class MockJASSUB {
    timeOffset = 0;
    ready = rendererReady;
    renderer = {
      setTrack: vi.fn().mockResolvedValue(undefined),
      setTrackByUrl: vi.fn().mockResolvedValue(undefined),
      addFonts: vi.fn().mockResolvedValue(true),
    };
    prescaleFactor = 1;
    prescaleHeightLimit = 1080;
    _canvas = document.createElement("canvas");
    _video: HTMLVideoElement | undefined;
    constructor(opts: Record<string, unknown>) {
      constructorOpts.push(opts);
      this._video = opts.video as HTMLVideoElement | undefined;
      this.timeOffset = (opts.timeOffset as number) ?? 0;
      instances.push(this);
    }
    resize = vi.fn().mockResolvedValue(undefined);
    destroy = vi.fn();
  }
  return { default: MockJASSUB };
});

// A video showing a 1920×1080 stream; `setFrameSize(video, 0, 0)` empties it
// the way a stream reload does.
function setFrameSize(video: HTMLVideoElement, width: number, height: number) {
  Object.defineProperty(video, "videoWidth", { configurable: true, value: width });
  Object.defineProperty(video, "videoHeight", { configurable: true, value: height });
}

function makeVideoRef(readyState = 0): RefObject<HTMLVideoElement | null> {
  const video = document.createElement("video");
  Object.defineProperty(video, "readyState", { value: readyState, configurable: true });
  setFrameSize(video, 1920, 1080);
  return { current: video };
}

const arabicTrack: PlayerSubtitleInfo = {
  index: 5,
  language: "ara",
  codec: "ass",
  label: "Arabic",
  source: "embedded",
  url: "/api/v1/playback/x/subtitles/5.ass",
};

const thaiTrack: PlayerSubtitleInfo = {
  index: 7,
  language: "",
  codec: "ass",
  label: "Thai",
  source: "embedded",
  url: "/api/v1/playback/x/subtitles/7.ass",
};

const germanTrack: PlayerSubtitleInfo = {
  index: 6,
  language: "ger",
  codec: "ass",
  label: "German",
  source: "embedded",
  url: "/api/v1/playback/x/subtitles/6.ass",
};

const attachedFontTrack: PlayerSubtitleInfo = {
  ...germanTrack,
  index: 8,
  language: "eng",
  url: "/api/v1/playback/x/subtitles/8.ass",
  font_bundle_url: "/api/v1/stream/x/subtitles/8/fonts",
};

function mockFetchResponse(text: string): Response {
  return new Response(text, { status: 200 });
}

/**
 * A bounded window response: the server advertises the source-time range it
 * covers and marks the body as partial. A complete body carries neither header.
 */
function mockWindowedResponse(text: string, coverage = "0.000-600.000"): Response {
  return new Response(text, {
    status: 200,
    headers: { "X-Subtitle-Windowed": "true", "X-Subtitle-Coverage": coverage },
  });
}

function mockFontBundleResponse(bytes: string): Response {
  return new Response(JSON.stringify({ items: [{ name: "Attached.ttf", data: btoa(bytes) }] }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/** A pending (in-flight) font bundle: empty body plus the no-store marker. */
function pendingFontBundleResponse(): Response {
  return new Response(JSON.stringify([]), {
    status: 200,
    headers: { "Cache-Control": "no-store", "Content-Type": "application/json" },
  });
}

/** A definitive font-less bundle: empty body but cacheable, no pending marker. */
function definitiveEmptyFontBundleResponse(): Response {
  return new Response(JSON.stringify([]), {
    status: 200,
    headers: { "Cache-Control": "private, max-age=600", "Content-Type": "application/json" },
  });
}

/** A pending bundle identified by the explicit marker header. */
function pendingHeaderFontBundleResponse(): Response {
  return new Response(JSON.stringify([]), {
    status: 200,
    headers: { "X-Vio-Font-Bundle-Pending": "true", "Content-Type": "application/json" },
  });
}

/** A response that never resolves until its request signal aborts. */
function hangingResponse(signal: AbortSignal): Promise<Response> {
  return new Promise((_resolve, reject) => {
    const onAbort = () => reject(new DOMException("Aborted", "AbortError"));
    if (signal.aborted) {
      onAbort();
      return;
    }
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

/**
 * A response that settles like the real fetch: aborting its request before the
 * body is delivered rejects with an AbortError rather than resolving into a
 * response the caller already abandoned.
 */
function abortAwareResponse(text: string, init?: RequestInit): Promise<Response> {
  const signal = init?.signal;
  return new Promise<Response>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const onAbort = () => reject(new DOMException("Aborted", "AbortError"));
    signal?.addEventListener("abort", onAbort, { once: true });
    Promise.resolve().then(() => {
      signal?.removeEventListener("abort", onAbort);
      if (signal?.aborted) reject(new DOMException("Aborted", "AbortError"));
      else resolve(mockFetchResponse(text));
    });
  });
}

/**
 * A request the test settles by hand. Its promise rejects on abort exactly like
 * a real fetch, so an abandoned window is distinguishable from one that landed.
 */
function deferredResponse(init?: RequestInit) {
  const signal = init?.signal;
  let settled = false;
  let resolveResponse!: (response: Response) => void;
  let rejectResponse!: (reason: unknown) => void;
  const promise = new Promise<Response>((resolve, reject) => {
    resolveResponse = resolve;
    rejectResponse = reject;
  });
  const onAbort = () => {
    if (settled) return;
    settled = true;
    rejectResponse(new DOMException("Aborted", "AbortError"));
  };
  if (signal?.aborted) onAbort();
  else signal?.addEventListener("abort", onAbort, { once: true });
  return {
    promise,
    get aborted() {
      return signal?.aborted ?? false;
    },
    settle(text: string) {
      if (settled) return;
      settled = true;
      resolveResponse(mockFetchResponse(text));
    },
  };
}

beforeEach(() => {
  constructorOpts.length = 0;
  instances.length = 0;
  rendererReady = Promise.resolve();
  // Abort-aware by default: a request whose signal fires rejects instead of
  // resolving, so tests cannot mistake an abandoned window for a loaded one.
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((_input, init) => abortAwareResponse("", init)),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useASSSubtitles font fallback", () => {
  it("uses an Arabic-capable defaultFont for an Arabic ASS track", async () => {
    renderHook(() => useASSSubtitles(makeVideoRef(), [arabicTrack], 5, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    // libass only renders missing glyphs with the default font, so Arabic
    // coverage depends on defaultFont pointing at an Arabic font.
    expect(opts.defaultFont).toBe("noto sans arabic");
    expect(opts.fonts).toEqual(expect.arrayContaining([expect.any(Uint8Array)]));
  });

  it("uses a Thai-capable defaultFont for a Thai ASS track", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      mockFetchResponse(
        [
          "[V4+ Styles]",
          "Format: Name, Fontname, Fontsize",
          "Style: Default,Trebuchet MS,48",
          "[Events]",
          "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\fnTrebuchet MS}สวัสดี!",
        ].join("\n"),
      ),
    );

    renderHook(() => useASSSubtitles(makeVideoRef(), [thaiTrack], 7, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    expect(opts.defaultFont).toBe("noto sans thai");
    expect(opts.fonts).toEqual(expect.arrayContaining([expect.any(Uint8Array)]));
    expect(opts.subContent).toContain("Style: Default,noto sans thai,48");
    expect(opts.subContent).toContain("{\\fnnoto sans thai}สวัสดี!");
    expect(opts.subContent).not.toContain("Trebuchet MS");
  });

  it("keeps the Liberation Sans default for a Latin (German) ASS track", async () => {
    renderHook(() => useASSSubtitles(makeVideoRef(), [germanTrack], 6, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    expect(opts.defaultFont).toBeUndefined();
    expect(opts.fonts).toBeUndefined();
    // jassub >= 2.5.4 no longer ships its built-in default font file, so the
    // hook must always supply Liberation Sans itself or Latin tracks render
    // nothing (queryFonts is disabled).
    expect(opts.availableFonts).toEqual({ "liberation sans": expect.any(String) });
  });

  it("preloads embedded ASS font bundle bytes when the track advertises them", async () => {
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url.endsWith("/fonts")) {
        return Promise.resolve(mockFontBundleResponse("font-data"));
      }
      return Promise.resolve(
        mockFetchResponse("[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello"),
      );
    });

    renderHook(() => useASSSubtitles(makeVideoRef(), [attachedFontTrack], 8, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const opts = constructorOpts[0]!;
    expect(opts.defaultFont).toBeUndefined();
    expect(opts.fonts).toEqual([expect.any(Uint8Array)]);
  });
});

describe("useASSSubtitles time offset", () => {
  // JASSUB renders the ASS event matching `video.currentTime + timeOffset`,
  // so an event at source time S appears at video time S - timeOffset.
  // Positive user delay means "show subtitles later" (VTTCue semantics in
  // useSubtitleTracks shifts cues by `start - origin + delay`), which for
  // JASSUB requires SUBTRACTING the delay from the stream origin.

  it("subtracts a positive user delay from the constructed timeOffset", async () => {
    renderHook(() => useASSSubtitles(makeVideoRef(), [germanTrack], 6, false, 30, 2000));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    // origin 30s, +2000ms delay → event at source time S renders at video
    // time S - 28 = (S - 30) + 2, i.e. 2s later than the undelayed position.
    expect(constructorOpts[0]!.timeOffset).toBe(28);
  });

  it("waits for the renderer before repainting a changed subtitle offset", async () => {
    let ready!: () => void;
    rendererReady = new Promise((resolve) => {
      ready = resolve;
    });
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ delay }) => useASSSubtitles(videoRef, [germanTrack], 6, false, 30, delay),
      { initialProps: { delay: 0 } },
    );
    await waitFor(() => expect(instances).toHaveLength(1));
    rerender({ delay: 2000 });
    expect(instances[0]!.timeOffset).toBe(28);
    expect(instances[0]!.resize).not.toHaveBeenCalled();
    await act(async () => {
      ready();
    });
    expect(instances[0]!.resize).toHaveBeenCalledWith(true);
  });

  it("does not repaint a destroyed instance when its renderer finishes loading", async () => {
    let ready!: () => void;
    rendererReady = new Promise((resolve) => {
      ready = resolve;
    });
    const videoRef = makeVideoRef();
    const { rerender, unmount } = renderHook(
      ({ delay }) => useASSSubtitles(videoRef, [germanTrack], 6, false, 30, delay),
      { initialProps: { delay: 0 } },
    );
    await waitFor(() => expect(instances).toHaveLength(1));
    rerender({ delay: 2000 });
    unmount();
    await act(async () => {
      ready();
    });
    expect(instances[0]!.resize).not.toHaveBeenCalled();
  });
});

describe("useASSSubtitles stream reload", () => {
  // A seek reanchor reloads the stream behind the same <video> and moves the
  // timeline offset. JASSUB resized while the element is empty draws nothing
  // until its frame size changes, which a same-resolution stream never does.
  async function renderReloadHook() {
    const videoRef = makeVideoRef();
    const hook = renderHook(
      ({ origin }) => useASSSubtitles(videoRef, [germanTrack], 6, false, origin, 0),
      { initialProps: { origin: 900 } },
    );
    await waitFor(() => expect(instances).toHaveLength(1));
    await waitFor(() => expect(instances[0]!.resize).toHaveBeenCalled());
    instances[0]!.resize.mockClear();
    return { video: videoRef.current!, instance: instances[0]!, ...hook };
  }

  it("waits for the next stream's frame size before resizing", async () => {
    const { video, instance, rerender } = await renderReloadHook();

    setFrameSize(video, 0, 0);
    await act(async () => {
      rerender({ origin: 0 });
    });
    expect(instance.timeOffset).toBe(0);
    expect(instance.resize).not.toHaveBeenCalled();

    setFrameSize(video, 1920, 1080);
    await act(async () => {
      video.dispatchEvent(new Event("loadedmetadata"));
    });
    expect(instance.resize).toHaveBeenCalledWith(true);
  });

  it("resizes when the frame size arrives after loadedmetadata", async () => {
    const { video, instance } = await renderReloadHook();

    setFrameSize(video, 0, 0);
    await act(async () => {
      video.dispatchEvent(new Event("loadedmetadata"));
    });
    expect(instance.resize).not.toHaveBeenCalled();

    setFrameSize(video, 1920, 1080);
    await act(async () => {
      video.dispatchEvent(new Event("resize"));
    });
    expect(instance.resize).toHaveBeenCalledWith(true);
  });

  it("removes its video listeners when the track is torn down", async () => {
    const videoRef = makeVideoRef();
    const video = videoRef.current!;
    const add = vi.spyOn(video, "addEventListener");
    const remove = vi.spyOn(video, "removeEventListener");
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(instances).toHaveLength(1));
    const added = add.mock.calls.filter(([type]) => type === "loadedmetadata" || type === "resize");
    expect(added.map(([type]) => type).sort()).toEqual(["loadedmetadata", "resize"]);

    unmount();

    for (const [type, listener] of added) {
      expect(remove).toHaveBeenCalledWith(type, listener);
    }
  });

  it("removes a replaced renderer's resize listeners", async () => {
    const videoRef = makeVideoRef(1);
    const video = videoRef.current!;
    const add = vi.spyOn(video, "addEventListener");
    const remove = vi.spyOn(video, "removeEventListener");
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(instances).toHaveLength(1));
    const first = add.mock.calls.filter(([type]) => type === "loadedmetadata" || type === "resize");
    expect(first).toHaveLength(2);

    await act(async () => {
      video.currentTime = 590;
      video.dispatchEvent(new Event("timeupdate"));
    });
    await waitFor(() => expect(instances).toHaveLength(2));

    for (const [type, listener] of first) {
      expect(remove).toHaveBeenCalledWith(type, listener);
    }
    unmount();
  });
});

describe("useASSSubtitles video fit", () => {
  const script = [
    "[Script Info]",
    "PlayResX: 1920",
    "PlayResY: 1080",
    "",
    "[V4+ Styles]",
    "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding",
    "Style: Default,Arial,64,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,3,0,2,40,40,40,1",
    "",
    "[Events]",
    "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text",
    "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello",
  ].join("\n");
  // A 16:9 frame filling a 2.39:1 player hides 12.8% of its height per edge,
  // which is 138 rows of this script's 1080-row PlayRes.
  const scopeCrop = { x: 0, y: 0.128 };
  const insetStyle = "0,2,40,40,178,1";

  type Props = { videoFit: VideoFitMode; coverCrop: { x: number; y: number } };
  function renderFitHook(initialProps: Props) {
    const videoRef = makeVideoRef();
    return renderHook(
      ({ videoFit, coverCrop }: Props) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          undefined,
          undefined,
          0,
          videoFit,
          coverCrop,
        ),
      { initialProps },
    );
  }

  beforeEach(() => {
    vi.mocked(fetch).mockResolvedValue(mockFetchResponse(script));
  });

  it("applies a fit change made before JASSUB finishes initializing", async () => {
    let resolveFetch!: (response: Response) => void;
    vi.mocked(fetch).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveFetch = resolve;
      }),
    );
    let resolveReady!: () => void;
    rendererReady = new Promise((resolve) => {
      resolveReady = resolve;
    });
    const { rerender } = renderFitHook({ videoFit: "contain", coverCrop: { x: 0, y: 0 } });
    await waitFor(() => expect(fetch).toHaveBeenCalledOnce());

    await act(async () => {
      resolveFetch(mockFetchResponse(script));
    });
    await waitFor(() => expect(instances).toHaveLength(1));
    expect(constructorOpts[0]!.subContent).toBe(script);

    rerender({ videoFit: "cover", coverCrop: scopeCrop });
    await act(async () => {
      resolveReady();
    });

    const instance = instances[0]!;
    await waitFor(() => expect(instance.renderer.setTrack).toHaveBeenCalled());
    expect(instance._canvas).toHaveClass("player-ass-fill");
    expect(instance.renderer.setTrack).toHaveBeenLastCalledWith(
      expect.stringContaining(insetStyle),
    );
    expect(instance.resize).toHaveBeenCalledWith(true);
  });

  it("starts with Fill margins when the player is already cropped", async () => {
    renderFitHook({ videoFit: "cover", coverCrop: scopeCrop });

    await waitFor(() => expect(instances).toHaveLength(1));
    expect(constructorOpts[0]!.subContent).toContain(insetStyle);
    expect(instances[0]!._canvas).toHaveClass("player-ass-fill");
    await waitFor(() => expect(instances[0]!.resize).toHaveBeenCalledWith(true));
    expect(instances[0]!.renderer.setTrack).not.toHaveBeenCalled();
  });

  it("moves regular events into view in Fill and restores them in Fit", async () => {
    const { rerender } = renderFitHook({ videoFit: "contain", coverCrop: { x: 0, y: 0 } });
    await waitFor(() => expect(instances).toHaveLength(1));
    const instance = instances[0]!;
    expect(instance._canvas).not.toHaveClass("player-ass-fill");

    rerender({ videoFit: "cover", coverCrop: scopeCrop });
    expect(instance._canvas).toHaveClass("player-ass-fill");
    await waitFor(() =>
      expect(instance.renderer.setTrack).toHaveBeenLastCalledWith(
        expect.stringContaining(insetStyle),
      ),
    );
    expect(instance.resize).toHaveBeenCalledWith(true);
    // Render at the zoomed size so the cropped bitmap is not upscaled.
    expect(instance.prescaleFactor).toBeCloseTo(1 / (1 - 2 * scopeCrop.y));
    expect(instance.prescaleHeightLimit).toBe(Number.POSITIVE_INFINITY);

    rerender({ videoFit: "contain", coverCrop: { x: 0, y: 0 } });
    expect(instance._canvas).not.toHaveClass("player-ass-fill");
    await waitFor(() => expect(instance.renderer.setTrack).toHaveBeenLastCalledWith(script));
    expect(instance.prescaleFactor).toBe(1);
    expect(instance.prescaleHeightLimit).toBe(1080);
  });

  it("reloads the track once after the player stops resizing", async () => {
    const { rerender } = renderFitHook({ videoFit: "cover", coverCrop: { x: 0, y: 0 } });
    await waitFor(() => expect(instances).toHaveLength(1));
    const instance = instances[0]!;
    await waitFor(() => expect(instance.resize).toHaveBeenCalled());

    rerender({ videoFit: "cover", coverCrop: { x: 0, y: 0.05 } });
    rerender({ videoFit: "cover", coverCrop: { x: 0, y: 0.1 } });
    rerender({ videoFit: "cover", coverCrop: scopeCrop });

    await waitFor(() => expect(instance.renderer.setTrack).toHaveBeenCalled());
    expect(instance.renderer.setTrack).toHaveBeenCalledOnce();
    expect(instance.renderer.setTrack).toHaveBeenCalledWith(expect.stringContaining(insetStyle));
  });
});

describe("ASS subtitle loading recovery", () => {
  it("reports a failed fetch and retries without a track change", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    vi.mocked(fetch)
      .mockRejectedValueOnce(new Error("temporary failure"))
      .mockResolvedValue(mockFetchResponse("[Script Info]"));
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(state).toHaveBeenLastCalledWith("error");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(constructorOpts).toHaveLength(1);
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("starts the font request only after subtitle text succeeds and fetches fresh fonts on retry", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/retry-after-failure" };
    let fontRequests = 0;
    let subtitleRequests = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      if (String(input) === track.font_bundle_url) {
        fontRequests += 1;
        return Promise.resolve(mockFontBundleResponse("fresh-font"));
      }
      if (++subtitleRequests === 1) return Promise.reject(new Error("extraction failed"));
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // The first attempt fails on the subtitle TEXT, before any font request
      // exists: the text drives the pipeline, so there is no stale in-flight
      // font fetch to abort into the retry.
      expect(fontRequests).toBe(0);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      // Retry: text succeeds, then a fresh font bundle is fetched and attached.
      expect(fontRequests).toBe(1);
      expect(constructorOpts).toHaveLength(1);
      expect(constructorOpts[0]!.fonts).toEqual([expect.any(Uint8Array)]);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("discards an old track response after subtitles are switched off", async () => {
    let resolve!: (response: Response) => void;
    vi.mocked(fetch).mockReturnValueOnce(
      new Promise((r) => {
        resolve = r;
      }),
    );
    const state = vi.fn();
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ index }: { index: number | null }) =>
        useASSSubtitles(videoRef, [germanTrack], index, false, 0, 0, state),
      { initialProps: { index: 6 as number | null } },
    );
    rerender({ index: null });
    await act(async () => {
      resolve(mockFetchResponse("[Script Info]"));
    });
    expect(constructorOpts).toHaveLength(0);
    expect(state).toHaveBeenLastCalledWith("idle");
  });
});

it("keeps a slowly progressing ASS extraction alive beyond 30 seconds", async () => {
  vi.useFakeTimers();
  const state = vi.fn();
  let reads = 0;
  vi.mocked(fetch).mockResolvedValue(
    new Response(
      new ReadableStream<Uint8Array>({
        pull(controller) {
          return new Promise((resolve) => {
            setTimeout(() => {
              ++reads;
              if (reads <= 3) {
                controller.enqueue(new TextEncoder().encode("[Script Info]\n"));
              } else {
                controller.close();
              }
              resolve();
            }, 15_000);
          });
        },
      }),
    ),
  );
  const videoRef = makeVideoRef();
  const { unmount } = renderHook(() =>
    useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
  );
  try {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(constructorOpts).toHaveLength(1);
    expect(state).toHaveBeenLastCalledWith("ready");
    expect(state).not.toHaveBeenCalledWith("error");
  } finally {
    unmount();
    vi.useRealTimers();
  }
});

describe("useASSSubtitles font budget", () => {
  it("constructs JASSUB with fallback fonts when the font bundle exceeds the budget", async () => {
    vi.useFakeTimers();
    const state = vi.fn();
    const track = { ...arabicTrack, font_bundle_url: "/fonts/budget-exceeded" };
    vi.mocked(fetch).mockImplementation((input) => {
      if (String(input) === track.font_bundle_url) return new Promise(() => {});
      return Promise.resolve(
        mockFetchResponse("[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,مرحبا"),
      );
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // The subtitle text resolved but the font bundle is still in flight:
      // JASSUB must not be constructed yet, and must not hang on the font.
      expect(constructorOpts).toHaveLength(0);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      // Budget expired: JASSUB was constructed with the fallback font only
      // (the attached bundle never resolved — NOTO_SANS_ARABIC is exactly 3
      // font files, so a length of 3 proves no attached bytes snuck in) and
      // reached ready without error.
      expect(constructorOpts).toHaveLength(1);
      expect(constructorOpts[0]!.defaultFont).toBe("noto sans arabic");
      expect(constructorOpts[0]!.fonts).toEqual(expect.arrayContaining([expect.any(Uint8Array)]));
      expect(constructorOpts[0]!.fonts).toHaveLength(3);
      expect(state).toHaveBeenLastCalledWith("ready");
      expect(state).not.toHaveBeenCalledWith("error");
      // The slow font fetch is still in flight and harmless.
      expect(fetch).toHaveBeenCalledWith(
        track.font_bundle_url,
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });
});

describe("useASSSubtitles subtitle source changed", () => {
  it("signals onSourceChanged once on 409 subtitle_source_changed and schedules no retry", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const onSourceChanged = vi.fn();
    vi.mocked(fetch).mockResolvedValue(
      new Response(JSON.stringify({ error: "subtitle_source_changed" }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(
        videoRef,
        [arabicTrack],
        arabicTrack.index,
        false,
        0,
        0,
        undefined,
        onSourceChanged,
      ),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).toHaveBeenCalledTimes(1);
      // The source-change signal bypasses the outer retry: the stale URL must
      // not be fetched again.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(constructorOpts).toHaveLength(0);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("keeps the 5s retry for a generic 500 subtitle fetch", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const onSourceChanged = vi.fn();
    const state = vi.fn();
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 500 }))
      .mockResolvedValue(mockFetchResponse("[Script Info]"));
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(
        videoRef,
        [arabicTrack],
        arabicTrack.index,
        false,
        0,
        0,
        state,
        onSourceChanged,
      ),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(onSourceChanged).not.toHaveBeenCalled();
      expect(state).toHaveBeenLastCalledWith("error");
      // The 5s retry is unchanged for generic failures and recovers.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fetch).toHaveBeenCalledTimes(2);
      expect(constructorOpts).toHaveLength(1);
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("useASSSubtitles windowed ASS extraction", () => {
  it("requests a bounded ASS window positioned before the playhead", async () => {
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 100;

    // origin 30s, so source time = player 100 + origin 30. A 20s lead starts
    // the window at 110 and the 120s duration bounds the extraction.
    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 30, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    const url = String(vi.mocked(fetch).mock.calls[0]![0]);
    expect(url).toContain("position=110&duration=120");
    expect(url.startsWith(germanTrack.url)).toBe(true);
  });

  it("fetches the next window as playback nears the end and swaps it in", async () => {
    const videoRef = makeVideoRef(1);

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("position=0&duration=120");

    // Playback has left [0, 120], so the refresh re-anchors 20s behind it.
    videoRef.current!.currentTime = 590;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=570&duration=120");
    // Atomic swap: the outgoing instance is destroyed only after the new
    // window has been constructed (and readied), so the old script keeps
    // rendering through the gap instead of flashing empty.
    expect(instances).toHaveLength(2);
    expect(instances[0]!.destroy).toHaveBeenCalledTimes(1);
    expect(instances[1]!.destroy).not.toHaveBeenCalled();
  });

  it("measures timeupdate against the refreshed window and only prefetches at its end", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("position=0&duration=120");

    // Playback has left [0, 120], so the refresh re-anchors 20s behind it.
    videoRef.current!.currentTime = 590;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=570&duration=120");
    // windowStart/windowEnd are committed only after the renderer is ready, and
    // the refresh moved the load state through "loading". Wait for it to flip
    // back to "ready" so the checks below measure against [570, 690], not the
    // outgoing [0, 120].
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));

    // Playback inside the refreshed window must not schedule another fetch.
    for (const t of [600, 620]) {
      videoRef.current!.currentTime = t;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
    }
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(constructorOpts).toHaveLength(2);
    expect(instances).toHaveLength(2);

    // Entering the 60s prefetch lead of [570, 690] fetches exactly one next
    // window and swaps in exactly one renderer.
    videoRef.current!.currentTime = 640;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));

    await waitFor(() => expect(constructorOpts).toHaveLength(3));
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(String(vi.mocked(fetch).mock.calls[2]![0])).toContain("position=620&duration=120");
    expect(instances).toHaveLength(3);
  });

  it("keeps the 60 s prefetch lead at 1x", async () => {
    const videoRef = makeVideoRef(1);

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    // Inside [0, 120] but more than 60 s from its end: no prefetch yet.
    videoRef.current!.currentTime = 59;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(1);

    // Crossing 120 - 60 fetches the next window, exactly as before.
    videoRef.current!.currentTime = 61;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=41&duration=120");
  });

  it.each([2, 4])(
    "prefetches the next window earlier at %ix so the lead keeps its wall clock",
    async (rate) => {
      const videoRef = makeVideoRef(1);
      videoRef.current!.playbackRate = rate;

      renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
      await waitFor(() => expect(constructorOpts).toHaveLength(1));

      // The raw scaled lead is 120 s at 2x and 240 s at 4x; both cap at 90 s,
      // so the trigger for [0, 120] is at 30 source seconds instead of 60.
      videoRef.current!.currentTime = 29;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await act(async () => {});
      expect(fetch).toHaveBeenCalledTimes(1);

      videoRef.current!.currentTime = 31;
      videoRef.current!.dispatchEvent(new Event("timeupdate"));
      await waitFor(() => expect(constructorOpts).toHaveLength(2));
      expect(fetch).toHaveBeenCalledTimes(2);
      expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=11&duration=120");
    },
  );

  it("scales the prefetch lead linearly below the cap", async () => {
    const videoRef = makeVideoRef(1);
    // 60 s x 1.25 is a 75 s lead, well under the 90 s cap, so the trigger for
    // [0, 120] is at 45 rather than the 30 a capped lead would give.
    videoRef.current!.playbackRate = 1.25;

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    videoRef.current!.currentTime = 45;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(1);

    videoRef.current!.currentTime = 46;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=26&duration=120");
  });

  it("recomputes the prefetch trigger when the rate changes mid-playback", async () => {
    const videoRef = makeVideoRef(1);

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    // At 1x, source 50 is still 70 s from the window end: outside the lead.
    videoRef.current!.currentTime = 50;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(1);

    // Doubling the rate shrinks that margin to 35 s of wall clock, inside the
    // scaled lead, so the ratechange itself starts the prefetch.
    videoRef.current!.playbackRate = 2;
    videoRef.current!.dispatchEvent(new Event("ratechange"));
    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=30&duration=120");
  });

  it("restores the 1x prefetch trigger when the rate returns to 1", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    videoRef.current!.playbackRate = 4;

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    // Past the capped 4x trigger: commit a window that starts at 11.
    videoRef.current!.currentTime = 31;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));

    // Back at 1x the lead is 60 s, so [11, 131] must not prefetch until 71.
    videoRef.current!.playbackRate = 1;
    videoRef.current!.currentTime = 70;
    videoRef.current!.dispatchEvent(new Event("ratechange"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(2);

    videoRef.current!.currentTime = 72;
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await waitFor(() => expect(constructorOpts).toHaveLength(3));
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("does not fetch the next window twice for a burst of rate events", async () => {
    const videoRef = makeVideoRef(1);
    videoRef.current!.playbackRate = 2;

    renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    // Hold the prefetch on the wire so the in-flight guard is observable.
    let refresh: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementationOnce((_input, init) => {
      refresh = deferredResponse(init);
      return refresh.promise;
    });

    videoRef.current!.currentTime = 31;
    videoRef.current!.dispatchEvent(new Event("ratechange"));
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    videoRef.current!.dispatchEvent(new Event("ratechange"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(2);

    await act(async () => {
      refresh!.settle("[Script Info]");
    });
    await waitFor(() => expect(constructorOpts).toHaveLength(2));
    // The committed window covers the playhead, so a later tick is a no-op.
    videoRef.current!.dispatchEvent(new Event("timeupdate"));
    await act(async () => {});
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("falls back to the whole-track URL after bounded windowed retries", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    vi.mocked(fetch).mockRejectedValue(new Error("extraction failed"));
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      for (let i = 0; i < 3; i += 1) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(5_000);
        });
      }

      const urls = vi.mocked(fetch).mock.calls.map((call) => String(call[0]));
      const windowed = `${germanTrack.url}?position=0&duration=120`;
      // Bounded retries all target the same window URL...
      expect(urls.slice(0, 3)).toEqual([windowed, windowed, windowed]);
      // ...then the param-less whole-track URL is tried instead of looping on
      // the window that keeps failing.
      expect(urls[3]).toBe(germanTrack.url);
      expect(urls.filter((url) => url === windowed)).toHaveLength(3);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("useASSSubtitles artifact-aware whole-track upgrade", () => {
  const windowScript = "[Script Info]\nComment: bounded window";
  const wholeScript = "[Script Info]\nComment: whole track";

  it("keeps the refresh loop armed when the param-less fallback is windowed", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      // Three bounded attempts fail, then the param-less fallback is answered
      // with the server's implicit 600 s window instead of a whole track.
      if (requested.length <= 3) return Promise.reject(new Error("extraction failed"));
      if (url === germanTrack.url) return Promise.resolve(mockWindowedResponse(windowScript));
      return abortAwareResponse("[Script Info]", init);
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      for (let i = 0; i < 4; i += 1) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(5_000);
        });
      }
      expect(requested[3]).toBe(germanTrack.url);
      expect(constructorOpts).toHaveLength(1);
      expect(constructorOpts[0]!.subContent).toBe(windowScript);

      // The committed script covers only [0, 600]: leaving it must request a
      // real window instead of latching on a "whole track" that ends at 600.
      await act(async () => {
        videoRef.current!.currentTime = 700;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(5);
      expect(requested[4]).toContain("position=680&duration=120");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("swaps a complete artifact into the running renderer after the first probe", async () => {
    vi.useFakeTimers();
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      // The windowed first paint is unchanged; the param-less probe finds the
      // full-track artifact the warm behind that window committed.
      if (url === germanTrack.url) return Promise.resolve(mockFetchResponse(wholeScript));
      return abortAwareResponse(windowScript, init);
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(constructorOpts).toHaveLength(1);
      expect(constructorOpts[0]!.subContent).toBe(windowScript);
      expect(requested).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested).toHaveLength(2);
      expect(requested[1]).toBe(germanTrack.url);
      // The script is swapped through the existing setTrack path: the renderer
      // is neither rebuilt nor destroyed.
      const instance = instances[0]!;
      expect(instances).toHaveLength(1);
      expect(instance.destroy).not.toHaveBeenCalled();
      expect(instance.renderer.setTrack).toHaveBeenCalledTimes(1);
      expect(instance.renderer.setTrack).toHaveBeenCalledWith(wholeScript);

      // A seek after the upgrade needs no network at all.
      await act(async () => {
        videoRef.current!.currentTime = 3000;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("does not run the upgrade probe while a seek-driven attempt is in flight", async () => {
    vi.useFakeTimers();
    const requested: string[] = [];
    let windowFetches = 0;
    let seekFetch: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      if (url === germanTrack.url) return Promise.resolve(mockFetchResponse(wholeScript));
      windowFetches += 1;
      if (windowFetches === 1) return Promise.resolve(mockWindowedResponse(windowScript));
      seekFetch = deferredResponse(init);
      return seekFetch.promise;
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(1);

      // Schedule the first probe for 30 s, then seek one second before it is
      // due: the replacement window owns the pipeline and must cancel it.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(29_000);
      });
      videoRef.current!.currentTime = 200;
      videoRef.current!.dispatchEvent(new Event("seeking"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);
      expect(seekFetch).not.toBeNull();

      // Past the original probe deadline: no param-less GET while the seek
      // window fetch is still in flight.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(requested.filter((url) => url === germanTrack.url)).toHaveLength(0);

      // Committing the seek window re-arms the probe from its own commit.
      await act(async () => {
        seekFetch!.settle(windowScript);
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested.filter((url) => url === germanTrack.url)).toHaveLength(1);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("does not re-anchor a near-identical seek while the replacement window is in flight", async () => {
    vi.useFakeTimers();
    const requested: string[] = [];
    let windowFetches = 0;
    let seekFetch: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      if (url === germanTrack.url) return Promise.resolve(mockFetchResponse(wholeScript));
      windowFetches += 1;
      if (windowFetches === 1) return Promise.resolve(mockWindowedResponse(windowScript));
      seekFetch = deferredResponse(init);
      return seekFetch.promise;
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(1);

      // Seek outside the committed [0,120] window: the replacement starts the
      // usual 20 s behind the playhead and is left pending.
      videoRef.current!.currentTime = 200;
      videoRef.current!.dispatchEvent(new Event("seeking"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);
      expect(requested[1]).toContain("position=180&duration=120");
      expect(seekFetch).not.toBeNull();

      // The playhead moves a fraction of a second during the same gesture, so
      // the paired seeked event computes 180.6. Restarting the extraction for
      // a near-identical window would throw away seconds of demux.
      videoRef.current!.currentTime = 200.6;
      videoRef.current!.dispatchEvent(new Event("seeked"));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);
      expect(seekFetch!.aborted).toBe(false);

      // The retained request still commits and owns the pipeline.
      await act(async () => {
        seekFetch!.settle(windowScript);
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("keeps windowing a non-abutting still-windowed probe answer at the rate limit", async () => {
    vi.useFakeTimers();
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      // A window nowhere near the loaded [0, 120]: nothing to adopt.
      if (url === germanTrack.url) {
        return Promise.resolve(mockWindowedResponse(windowScript, "900.000-1500.000"));
      }
      return abortAwareResponse(windowScript, init);
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(1);

      // First probe after 30 s; it finds no artifact yet.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested).toHaveLength(2);
      expect(requested[1]).toBe(germanTrack.url);

      // At most one probe per 60 s: the next one is not due yet.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested).toHaveLength(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested).toHaveLength(3);
      expect(requested[2]).toBe(germanTrack.url);
      // A windowed answer never replaces the loaded script.
      expect(instances).toHaveLength(1);
      expect(instances[0]!.renderer.setTrack).not.toHaveBeenCalled();
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("adopts an abutting windowed probe answer as the next window", async () => {
    vi.useFakeTimers();
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      // The implicit 600 s window starts where the loaded [0, 120] does and
      // reaches past it, so its body is worth keeping.
      if (url === germanTrack.url) {
        return Promise.resolve(mockWindowedResponse(wholeScript, "0.000-600.000"));
      }
      return abortAwareResponse(windowScript, init);
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() => useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0));
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(requested).toHaveLength(2);
      const instance = instances[0]!;
      expect(instances).toHaveLength(1);
      expect(instance.renderer.setTrack).toHaveBeenCalledWith(wholeScript);

      // The adopted coverage extends to 600, so playback inside it fetches
      // nothing.
      await act(async () => {
        videoRef.current!.currentTime = 400;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(2);

      // The normal 120 s prefetch resumes near the adopted window's end.
      await act(async () => {
        videoRef.current!.currentTime = 560;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(requested).toHaveLength(3);
      expect(requested[2]).toContain("position=540&duration=120");
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("rebuilds instead of swapping a truncated retimed script", async () => {
    const state = vi.fn();
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      requested.push(url);
      // The retime fetch is param-less; with no committed artifact the server
      // answers with a bounded window, which must never become the script.
      if (url === germanTrack.url) return Promise.resolve(mockWindowedResponse("[Script Info]"));
      return abortAwareResponse(windowScript, init);
    });
    const videoRef = makeVideoRef(1);
    const { rerender } = renderHook(
      ({ revision }: { revision: number }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          0,
          "contain",
          { x: 0, y: 0 },
          revision,
        ),
      { initialProps: { revision: 0 } },
    );
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(requested).toHaveLength(1);

    await act(async () => {
      rerender({ revision: 1 });
    });
    await waitFor(() => expect(requested).toHaveLength(3));
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    // The truncated window was discarded and the windowed pipeline rebuilt
    // the renderer, so the running script was never replaced by it.
    expect(requested[1]).toBe(germanTrack.url);
    expect(requested[2]).toContain("position=0&duration=120");
    expect(instances).toHaveLength(2);
    expect(instances[0]!.renderer.setTrack).not.toHaveBeenCalled();
    expect(constructorOpts[1]!.subContent).toBe(windowScript);
  });

  it("still swaps a complete retimed script in place", async () => {
    const state = vi.fn();
    vi.mocked(fetch).mockImplementation((input, init) => {
      const url = String(input);
      if (url === germanTrack.url) return Promise.resolve(mockFetchResponse(wholeScript));
      return abortAwareResponse(windowScript, init);
    });
    const videoRef = makeVideoRef(1);
    const { rerender } = renderHook(
      ({ revision }: { revision: number }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          0,
          "contain",
          { x: 0, y: 0 },
          revision,
        ),
      { initialProps: { revision: 0 } },
    );
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));

    await act(async () => {
      rerender({ revision: 1 });
    });
    await waitFor(() => expect(instances[0]!.renderer.setTrack).toHaveBeenCalledWith(wholeScript));
    expect(instances).toHaveLength(1);
    expect(instances[0]!.destroy).not.toHaveBeenCalled();
  });
});

describe("useASSSubtitles bounded retry/watchdog policy", () => {
  it("abandons a pre-header hang at the stall timeout and retries", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    let calls = 0;
    vi.mocked(fetch).mockImplementation((_input, init) => {
      calls += 1;
      if (calls === 1) return hangingResponse(init?.signal as AbortSignal);
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // No response headers ever arrive, so only the pre-armed watchdog can
      // break the hang.
      expect(calls).toBe(1);
      expect(state).toHaveBeenLastCalledWith("loading");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(state).toHaveBeenLastCalledWith("error");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(calls).toBe(2);
      expect(constructorOpts).toHaveLength(1);
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("stops after the whole-track fallback fails (terminal, no loop)", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    vi.mocked(fetch).mockRejectedValue(new Error("extraction failed"));
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // Three windowed attempts, then the single whole-track fallback.
      for (let i = 0; i < 4; i += 1) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(5_000);
        });
      }
      expect(fetch).toHaveBeenCalledTimes(4);
      expect(String(vi.mocked(fetch).mock.calls[3]![0])).toBe(germanTrack.url);
      expect(state).toHaveBeenLastCalledWith("error");
      // Terminal: no further attempts, even much later.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      expect(fetch).toHaveBeenCalledTimes(4);
      expect(constructorOpts).toHaveLength(0);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("caps a persistently 500ing text fetch at the shared attempt ceiling", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 500 }));
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      // A 500 is retried with backoff, but the shared ceiling (3 windowed + 1
      // whole-track) stops the loop instead of re-fetching the same failing URL
      // for the life of the mount.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      expect(state).toHaveBeenLastCalledWith("error");
      expect(fetch).toHaveBeenCalledTimes(4);
      // Terminal: later timeupdate/seek events do not revive the loop.
      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      expect(fetch).toHaveBeenCalledTimes(4);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("recovers after retryable 503s past the shared attempt ceiling", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    // Five 503s would have spent the 4-deep terminal ceiling; retryable
    // statuses ride their own generous budget, so the 6th attempt still
    // fetches and the track recovers instead of latching terminal.
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValueOnce(new Response(null, { status: 503 }))
      .mockResolvedValue(mockFetchResponse("[Script Info]"));
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("backs off a retryable 503 by its Retry-After without reporting the track failed", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(null, { status: 503, headers: { "Retry-After": "20" } }))
      .mockResolvedValue(mockFetchResponse("[Script Info]"));
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fetch).toHaveBeenCalledTimes(1);
      // The server is recovering the page it refused. The script already on
      // screen keeps rendering, the track stays selected, and the player is not
      // told the track failed.
      expect(state).not.toHaveBeenCalledWith("error");

      // The header is honored: the local 5 s ladder does not fire, and the
      // retry happens once the server asked for it.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(19_999);
      });
      expect(fetch).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1);
      });
      expect(fetch).toHaveBeenCalledTimes(2);
      expect(state).not.toHaveBeenCalledWith("error");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("stops at a definitive 4xx instead of trying the whole-track fallback", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    vi.mocked(fetch).mockResolvedValue(new Response(null, { status: 415 }));
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10 * 60_000);
      });
      // The URL can never satisfy this request, so neither another window nor
      // the param-less whole-track URL is attempted.
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(state).toHaveBeenLastCalledWith("error");
      expect(constructorOpts).toHaveLength(0);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("bounds a stalled window refresh at the stall timeout and recovers", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    let calls = 0;
    vi.mocked(fetch).mockImplementation((_input, init) => {
      calls += 1;
      if (calls === 1) return Promise.resolve(mockFetchResponse("[Script Info]"));
      if (calls === 2) return hangingResponse(init?.signal as AbortSignal);
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(constructorOpts).toHaveLength(1);
      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      // The refresh fetch hangs before any headers. Without the pre-armed
      // watchdog this would block every later refresh.
      expect(calls).toBe(2);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(60_000);
      });
      expect(state).toHaveBeenLastCalledWith("error");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(calls).toBe(3);
      expect(constructorOpts).toHaveLength(2);
      expect(state).toHaveBeenLastCalledWith("ready");
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });

  it("does not let timeupdate cancel an in-flight window refresh", async () => {
    vi.useFakeTimers();
    const state = vi.fn();
    let calls = 0;
    let refreshSignal: AbortSignal | undefined;
    let refresh: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementation((_input, init) => {
      calls += 1;
      if (calls === 1) return abortAwareResponse("[Script Info]", init);
      if (calls === 2) {
        refreshSignal = init?.signal as AbortSignal;
        refresh = deferredResponse(init);
        return refresh.promise;
      }
      return abortAwareResponse("[Script Info]", init);
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(constructorOpts).toHaveLength(1);

      // Start the replacement window at 590 and let timeupdate keep firing
      // outside the old [0, 120] window while extraction is still pending.
      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(calls).toBe(2);
      expect(refreshSignal?.aborted).toBe(false);
      await act(async () => {
        for (const time of [595, 600, 605]) {
          videoRef.current!.currentTime = time;
          videoRef.current!.dispatchEvent(new Event("timeupdate"));
        }
        await vi.advanceTimersByTimeAsync(0);
      });
      // A normal playback tick must not supersede the request it is waiting on.
      expect(refreshSignal?.aborted).toBe(false);
      expect(calls).toBe(2);

      await act(async () => {
        refresh!.settle("[Script Info]");
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(constructorOpts).toHaveLength(2);
      expect(state).toHaveBeenLastCalledWith("ready");
      // The playhead ticked to 605, inside the committed [570, 690] window, so
      // the completion backstop does not request another window.
      expect(calls).toBe(2);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("reuses the last window across a transport rebuild", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 1021;
    const { rerender } = renderHook(
      ({ transportRevision }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          0,
          "contain",
          { x: 0, y: 0 },
          0,
          transportRevision,
        ),
      { initialProps: { transportRevision: 0 } },
    );

    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("position=1001&duration=120");

    // The replacement stream attaches at time 0; the remembered window must
    // keep the request at the real playhead instead of restarting at 0.
    videoRef.current!.currentTime = 0;
    await act(async () => {
      rerender({ transportRevision: 1 });
    });

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2));
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=1001&duration=120");
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    // The commit backstop must not read the same ambiguous 0 and undo the
    // window the rebuild just restored.
    await act(async () => {});
    expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2);
  });

  it("starts from the playhead when a replay bump accompanies a transport rebuild", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      requested.push(String(input));
      return abortAwareResponse("[Script Info]", init);
    });
    videoRef.current!.currentTime = 1021;
    const { rerender } = renderHook(
      ({ transportRevision, replayRevision }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          0,
          "contain",
          { x: 0, y: 0 },
          0,
          transportRevision,
          replayRevision,
        ),
      { initialProps: { transportRevision: 0, replayRevision: 0 } },
    );

    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(requested[0]).toContain("position=1001&duration=120");

    // The replay keeps the URL and source generation but starts at zero. The
    // transport rebuild alone would reuse the old window; the explicit replay
    // epoch must win.
    videoRef.current!.currentTime = 0;
    await act(async () => {
      rerender({ transportRevision: 1, replayRevision: 1 });
    });

    await waitFor(() => expect(requested).toHaveLength(2));
    expect(requested[1]).toContain("position=0&duration=120");
  });

  it("reconciles a resumed window once the playhead becomes authoritative", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    const requested: string[] = [];
    let resumed: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementation((input, init) => {
      requested.push(String(input));
      if (requested.length === 2) {
        resumed = deferredResponse(init);
        return resumed.promise;
      }
      return abortAwareResponse("[Script Info]", init);
    });
    videoRef.current!.currentTime = 1021;
    const { rerender } = renderHook(
      ({ transportRevision }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          0,
          "contain",
          { x: 0, y: 0 },
          0,
          transportRevision,
        ),
      { initialProps: { transportRevision: 0 } },
    );

    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(requested[0]).toContain("position=1001&duration=120");

    // The rebuilt transport briefly reports zero, so the committed window is
    // resumed and the completion backstop is suppressed while the position is
    // ambiguous.
    videoRef.current!.currentTime = 0;
    await act(async () => {
      rerender({ transportRevision: 1 });
    });
    await waitFor(() => expect(requested).toHaveLength(2));
    expect(requested[1]).toContain("position=1001&duration=120");

    // The playhead becomes real while the resumed extraction is still pending.
    // Completion must reconcile instead of leaving the old window in place.
    videoRef.current!.currentTime = 750;
    await act(async () => {
      resumed!.settle("[Script Info]");
    });

    await waitFor(() => expect(requested).toHaveLength(3));
    expect(requested[2]).toContain("position=730&duration=120");
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
  });

  it("starts from zero when a track is re-enabled after a seek to the start", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    const video = videoRef.current!;
    video.currentTime = 1021;
    const { rerender } = renderHook(
      ({ index }: { index: number | null }) =>
        useASSSubtitles(videoRef, [germanTrack], index, false, 0, 0, state),
      { initialProps: { index: 6 as number | null } },
    );

    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("position=1001&duration=120");

    // Subtitles off, seek to the start while paused, then on again. The window
    // loaded at 1021 is not a transport continuation of this position.
    await act(async () => {
      rerender({ index: null });
    });
    video.currentTime = 0;
    await act(async () => {
      rerender({ index: 6 });
    });

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2));
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=0&duration=120");
    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(constructorOpts).toHaveLength(2);
  });

  it("starts a fresh window when the subtitle source identity changes at zero", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 1021;
    const { rerender } = renderHook(
      ({ sourceGeneration }: { sourceGeneration: number }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          sourceGeneration,
        ),
      { initialProps: { sourceGeneration: 0 } },
    );

    await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
    expect(String(vi.mocked(fetch).mock.calls[0]![0])).toContain("position=1001&duration=120");

    // A restart from the beginning can arrive as a new source identity without
    // changing the URL: the old position must not pin the request.
    videoRef.current!.currentTime = 0;
    await act(async () => {
      rerender({ sourceGeneration: 1 });
    });

    await waitFor(() => expect(vi.mocked(fetch)).toHaveBeenCalledTimes(2));
    expect(String(vi.mocked(fetch).mock.calls[1]![0])).toContain("position=0&duration=120");
  });

  it("starts from the playhead when a same-identity rebuild follows a failed fresh run", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    const requested: string[] = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      requested.push(String(input));
      // Generation 0 commits [1001, 1121].
      if (requested.length === 1) return abortAwareResponse("[Script Info]", init);
      // The first run of generation 1 starts at the real playhead (0) and fails
      // before it can commit anything.
      if (requested.length === 2) {
        return Promise.resolve(new Response(null, { status: 500 }));
      }
      return abortAwareResponse("[Script Info]", init);
    });
    const videoRef = makeVideoRef(1);
    videoRef.current!.currentTime = 1021;
    const { rerender } = renderHook(
      ({
        sourceGeneration,
        transportRevision,
      }: {
        sourceGeneration: number;
        transportRevision: number;
      }) =>
        useASSSubtitles(
          videoRef,
          [germanTrack],
          6,
          false,
          0,
          0,
          state,
          undefined,
          sourceGeneration,
          "contain",
          { x: 0, y: 0 },
          0,
          transportRevision,
        ),
      { initialProps: { sourceGeneration: 0, transportRevision: 0 } },
    );

    try {
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
      expect(requested).toHaveLength(1);
      expect(requested[0]).toContain("position=1001&duration=120");

      // The source identity changes at the start of the timeline. The fresh
      // run correctly requests the zero window and fails before committing, so
      // generation 1 owns no window.
      videoRef.current!.currentTime = 0;
      await act(async () => {
        rerender({ sourceGeneration: 1, transportRevision: 0 });
      });
      await waitFor(() => expect(requested).toHaveLength(2));
      expect(requested[1]).toContain("position=0&duration=120");
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("error"));

      // A transport rebuild of the same URL and generation must not certify
      // generation 0's committed window as reusable: the run that started here
      // never committed, so the request stays at the playhead instead of
      // jumping to the stale 1001 window and disabling completion reconciliation.
      await act(async () => {
        rerender({ sourceGeneration: 1, transportRevision: 1 });
      });
      await waitFor(() => expect(requested).toHaveLength(3));
      expect(requested[2]).toContain("position=0&duration=120");
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
      expect(constructorOpts).toHaveLength(2);
    } finally {
      error.mockRestore();
    }
  });

  it("rechecks the playhead when a window lands after the position left it", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    const requested: Array<{ url: string; signal?: AbortSignal }> = [];
    let refresh: ReturnType<typeof deferredResponse> | null = null;
    vi.mocked(fetch).mockImplementation((input, init) => {
      requested.push({ url: String(input), signal: init?.signal ?? undefined });
      if (requested.length === 2) {
        refresh = deferredResponse(init);
        return refresh.promise;
      }
      return abortAwareResponse("[Script Info]", init);
    });
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
      expect(requested).toHaveLength(1);

      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      await waitFor(() => expect(requested).toHaveLength(2));
      expect(requested[1]!.url).toContain("position=570&duration=120");

      // The playhead leaves the replacement while it is still extracting, then
      // playback pauses: no later event will ask for the right window, so
      // completion must recheck rather than stop at "ready".
      videoRef.current!.currentTime = 750;
      await act(async () => {
        refresh!.settle("[Script Info]");
      });

      await waitFor(() => expect(requested).toHaveLength(3));
      expect(requested[2]!.url).toContain("position=730&duration=120");
      // The tick that moved the playhead never aborted the in-flight window.
      expect(requested[1]!.signal?.aborted).toBe(false);
      expect(constructorOpts).toHaveLength(3);
    } finally {
      unmount();
    }
  });

  it("does not restart the same replacement for paired seeking/seeked events", async () => {
    const state = vi.fn();
    const videoRef = makeVideoRef(1);
    const requested: Array<{ url: string; signal?: AbortSignal }> = [];
    const pendingRequests: Array<ReturnType<typeof deferredResponse>> = [];
    vi.mocked(fetch).mockImplementation((input, init) => {
      requested.push({ url: String(input), signal: init?.signal ?? undefined });
      if (requested.length === 1) return abortAwareResponse("[Script Info]", init);
      const request = deferredResponse(init);
      pendingRequests.push(request);
      return request.promise;
    });
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));

      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
      });
      await waitFor(() => expect(requested).toHaveLength(2));

      // A seek to 3000 supersedes the in-flight window; its replacement starts
      // once the aborted request settles.
      videoRef.current!.currentTime = 3000;
      await act(async () => {
        videoRef.current!.dispatchEvent(new Event("seeking"));
      });
      await waitFor(() => expect(requested).toHaveLength(3));
      expect(requested[2]!.url).toContain("position=2980&duration=120");

      // `seeked` reports the same position for the same seek: it must not
      // abort and restart the replacement that is already extracting.
      await act(async () => {
        videoRef.current!.dispatchEvent(new Event("seeked"));
      });
      expect(requested).toHaveLength(3);
      expect(requested[2]!.signal?.aborted).toBe(false);

      await act(async () => {
        pendingRequests[1]!.settle("[Script Info]");
      });
      await waitFor(() => expect(state).toHaveBeenLastCalledWith("ready"));
      expect(requested).toHaveLength(3);
      expect(constructorOpts).toHaveLength(2);
    } finally {
      unmount();
    }
  });

  it("cancels an in-flight refresh on seek and restarts the attempt budget", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const state = vi.fn();
    let calls = 0;
    const refreshSignals: AbortSignal[] = [];
    vi.mocked(fetch).mockImplementation((_input, init) => {
      calls += 1;
      if (calls === 1) return Promise.resolve(mockFetchResponse("[Script Info]"));
      if (calls === 2) {
        const signal = init?.signal as AbortSignal;
        refreshSignals.push(signal);
        return hangingResponse(signal);
      }
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [germanTrack], 6, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(calls).toBe(2);
      expect(refreshSignals[0]!.aborted).toBe(false);
      // Seek far away while the refresh is in flight: it must be cancelled and
      // replaced by an attempt at the new position.
      await act(async () => {
        videoRef.current!.currentTime = 3000;
        videoRef.current!.dispatchEvent(new Event("seeking"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(refreshSignals[0]!.aborted).toBe(true);
      expect(calls).toBe(3);
      expect(String(vi.mocked(fetch).mock.calls[2]![0])).toContain("position=2980&duration=120");
      expect(constructorOpts).toHaveLength(2);
      expect(state).toHaveBeenLastCalledWith("ready");
      // Continued playback inside the replacement window [2980, 3100] must not
      // kick off another fetch.
      await act(async () => {
        videoRef.current!.currentTime = 3010;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        videoRef.current!.currentTime = 3030;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(calls).toBe(3);
      expect(constructorOpts).toHaveLength(2);
    } finally {
      unmount();
      error.mockRestore();
      vi.useRealTimers();
    }
  });
});

describe("useASSSubtitles pending font bundles", () => {
  it("adopts completed fonts from a pending bundle without reloading", async () => {
    vi.useFakeTimers();
    const state = vi.fn();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/pending-adopt" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        return Promise.resolve(
          fontCalls === 1 ? pendingFontBundleResponse() : mockFontBundleResponse("completed"),
        );
      }
      return Promise.resolve(
        mockFetchResponse("[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello"),
      );
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0, state),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // Pending: rendered immediately with fallback/default fonts only.
      expect(constructorOpts).toHaveLength(1);
      expect(constructorOpts[0]!.fonts).toBeUndefined();
      expect(state).toHaveBeenLastCalledWith("ready");
      // The bounded background refresh fetches the completed bundle and
      // hot-swaps it into the live instance.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fontCalls).toBe(2);
      expect(instances[0]!.renderer.addFonts).toHaveBeenCalledWith([expect.any(Uint8Array)]);
      expect(instances).toHaveLength(1);
      expect(instances[0]!.destroy).not.toHaveBeenCalled();

      // A later window reuses the adopted fonts without another font fetch.
      await act(async () => {
        videoRef.current!.currentTime = 590;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fontCalls).toBe(2);
      expect(constructorOpts).toHaveLength(2);
      expect(constructorOpts[1]!.fonts).toEqual([expect.any(Uint8Array)]);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("honors the explicit pending marker header", async () => {
    vi.useFakeTimers();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/pending-marker" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        return Promise.resolve(
          fontCalls === 1 ? pendingHeaderFontBundleResponse() : mockFontBundleResponse("done"),
        );
      }
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fontCalls).toBe(2);
      expect(instances[0]!.renderer.addFonts).toHaveBeenCalledTimes(1);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("backs off consecutive pending font polls with a cap", async () => {
    vi.useFakeTimers();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/pending-backoff" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        return Promise.resolve(pendingFontBundleResponse());
      }
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      // Initial load, then a fast first retry at 5 s.
      expect(fontCalls).toBe(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fontCalls).toBe(2);

      // Consecutive pending answers widen the interval: the next retry is due
      // 15 s after the previous one, not another 5 s.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(fontCalls).toBe(2);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(fontCalls).toBe(3);

      // Third interval is capped at 30 s: still nothing at 50 s, the poll fires
      // at 50 s, and every following poll keeps the 30 s interval. The budget
      // spans the server's 4-minute dump ceiling (12 attempts reach ~5m20s),
      // so it only stops after that.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(fontCalls).toBe(3);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(fontCalls).toBe(4);

      // Eight more capped intervals (50 s → 290 s) spend attempts 4-11.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(240_000);
      });
      expect(fontCalls).toBe(12);

      // The last attempt fires at 320 s, past the 4-minute dump ceiling.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fontCalls).toBe(13);

      // Budget exhausted: no further poll without a new committed window.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fontCalls).toBe(13);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("re-arms a pending font refresh after a window advance once the budget is spent", async () => {
    vi.useFakeTimers();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/pending-rearm" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        // Pending through the whole first budget; the poll armed by the next
        // committed window finds the bundle that landed in the meantime.
        return Promise.resolve(
          fontCalls <= 13 ? pendingFontBundleResponse() : mockFontBundleResponse("late-font"),
        );
      }
      // No advertised coverage: the upgrade probe must drop this window and
      // keep the 120 s pipeline, so the timeupdate below is a real advance.
      return Promise.resolve(
        mockWindowedResponse(
          "[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello",
          "",
        ),
      );
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fontCalls).toBe(1);

      // Spend the full 12-attempt budget (~5m20s), still pending.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(15_000);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(300_000);
      });
      expect(fontCalls).toBe(13);

      // The client gave up before the server dump finished. Advancing the
      // window is a natural re-check point: the commit arms exactly one more
      // poll, which adopts the bundle that has since landed.
      await act(async () => {
        videoRef.current!.currentTime = 65;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fontCalls).toBe(13);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fontCalls).toBe(14);
      expect(instances[instances.length - 1]!.renderer.addFonts).toHaveBeenCalledWith([
        expect.any(Uint8Array),
      ]);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("stops polling fonts after the bounded window re-arm stays pending", async () => {
    vi.useFakeTimers();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/pending-rearm-cap" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        return Promise.resolve(pendingFontBundleResponse());
      }
      return Promise.resolve(
        mockWindowedResponse(
          "[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello",
          "",
        ),
      );
    });
    const videoRef = makeVideoRef(1);
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fontCalls).toBe(1);

      // Spend the base budget, then let the first window advance arm its one
      // allowed re-check. Both stay pending.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(15_000);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(300_000);
      });
      expect(fontCalls).toBe(13);
      await act(async () => {
        videoRef.current!.currentTime = 65;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fontCalls).toBe(14);

      // The next window advance is past the one allowed re-arm: the client
      // gives up on the bundle instead of polling for the rest of the track.
      await act(async () => {
        videoRef.current!.currentTime = 200;
        videoRef.current!.dispatchEvent(new Event("timeupdate"));
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });
      expect(fontCalls).toBe(14);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });

  it("does not refresh a definitive font-less bundle", async () => {
    vi.useFakeTimers();
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/definitive-empty" };
    let fontCalls = 0;
    vi.mocked(fetch).mockImplementation((input) => {
      const url = String(input);
      if (url === track.font_bundle_url) {
        fontCalls += 1;
        return Promise.resolve(definitiveEmptyFontBundleResponse());
      }
      return Promise.resolve(mockFetchResponse("[Script Info]"));
    });
    const videoRef = makeVideoRef();
    const { unmount } = renderHook(() =>
      useASSSubtitles(videoRef, [track], track.index, false, 0, 0),
    );
    try {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(fontCalls).toBe(1);
      // No bounded refresh poll: a cacheable empty bundle is final.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(fontCalls).toBe(1);
    } finally {
      unmount();
      vi.useRealTimers();
    }
  });
});
