import type { RefObject } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useASSSubtitles } from "./useASSSubtitles";
import type { PlayerSubtitleInfo } from "../types";

// Capture the options every JASSUB instance is constructed with, plus the
// instances themselves so tests can observe later timeOffset updates.
const constructorOpts: Array<Record<string, unknown>> = [];
const instances: Array<{ timeOffset: number; resize: ReturnType<typeof vi.fn> }> = [];
let rendererReady: Promise<void> = Promise.resolve();

vi.mock("jassub", () => {
  class MockJASSUB {
    timeOffset = 0;
    ready = rendererReady;
    renderer = { setTrackByUrl: vi.fn().mockResolvedValue(undefined) };
    constructor(opts: Record<string, unknown>) {
      constructorOpts.push(opts);
      this.timeOffset = (opts.timeOffset as number) ?? 0;
      instances.push(this);
    }
    resize = vi.fn().mockResolvedValue(undefined);
    destroy = vi.fn();
  }
  return { default: MockJASSUB };
});

function makeVideoRef(): RefObject<HTMLVideoElement | null> {
  return { current: document.createElement("video") };
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
  return {
    ok: true,
    status: 200,
    text: vi.fn().mockResolvedValue(text),
    arrayBuffer: vi.fn().mockResolvedValue(new ArrayBuffer(8)),
    json: vi.fn().mockResolvedValue([]),
  } as unknown as Response;
}

function mockFontBundleResponse(bytes: string): Response {
  return {
    ok: true,
    status: 200,
    json: vi.fn().mockResolvedValue([{ name: "Attached.ttf", data: btoa(bytes) }]),
  } as unknown as Response;
}

beforeEach(() => {
  constructorOpts.length = 0;
  instances.length = 0;
  rendererReady = Promise.resolve();
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(mockFetchResponse("")));
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

  it("passes fetched ASS content into JASSUB", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      mockFetchResponse("[Events]\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello"),
    );

    renderHook(() => useASSSubtitles(makeVideoRef(), [germanTrack], 6, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    expect(constructorOpts[0]!.subContent).toContain("Dialogue:");
    expect(constructorOpts[0]!.subUrl).toBeUndefined();
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

  it("disables local font probing to avoid permission-related console noise", async () => {
    renderHook(() => useASSSubtitles(makeVideoRef(), [arabicTrack], 5, false, 0, 0));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    expect(constructorOpts[0]!.queryFonts).toBe(false);
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

  it("adds a negative user delay to the constructed timeOffset", async () => {
    renderHook(() => useASSSubtitles(makeVideoRef(), [germanTrack], 6, false, 30, -2000));

    await waitFor(() => expect(constructorOpts).toHaveLength(1));

    expect(constructorOpts[0]!.timeOffset).toBe(32);
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

  it("updates the live instance's timeOffset when the delay changes", async () => {
    const videoRef = makeVideoRef();
    const { rerender } = renderHook(
      ({ delay }) => useASSSubtitles(videoRef, [germanTrack], 6, false, 30, delay),
      { initialProps: { delay: 0 } },
    );

    await waitFor(() => expect(instances).toHaveLength(1));
    expect(instances[0]!.timeOffset).toBe(30);

    rerender({ delay: 2000 });

    await waitFor(() => expect(instances[0]!.timeOffset).toBe(28));
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

  it("aborts a pending font request after failure and fetches fresh fonts on retry", async () => {
    vi.useFakeTimers();
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    const track = { ...attachedFontTrack, font_bundle_url: "/fonts/retry-after-failure" };
    let fontSignal: AbortSignal | undefined;
    let fontRequests = 0;
    let subtitleRequests = 0;
    vi.mocked(fetch).mockImplementation((input, init) => {
      if (String(input) === track.font_bundle_url) {
        if (++fontRequests > 1) return Promise.resolve(mockFontBundleResponse("fresh-font"));
        fontSignal = init?.signal as AbortSignal;
        return new Promise((_, reject) => {
          fontSignal!.addEventListener("abort", () =>
            reject(new DOMException("cancelled", "AbortError")),
          );
        });
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
      expect(fontSignal?.aborted).toBe(true);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(fontRequests).toBe(2);
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
  vi.mocked(fetch).mockResolvedValue({
    ok: true,
    body: {
      getReader: () => ({
        read: () =>
          new Promise((resolve) => {
            setTimeout(
              () =>
                resolve(
                  ++reads <= 3
                    ? { done: false, value: new TextEncoder().encode("[Script Info]\n") }
                    : { done: true },
                ),
              15_000,
            );
          }),
      }),
    },
  } as unknown as Response);
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
