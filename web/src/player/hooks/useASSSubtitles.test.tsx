import type { RefObject } from "react";
import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useASSSubtitles } from "./useASSSubtitles";
import type { PlayerSubtitleInfo } from "../types";

// Capture the options every JASSUB instance is constructed with, plus the
// instances themselves so tests can observe later timeOffset updates.
const constructorOpts: Array<Record<string, unknown>> = [];
const instances: Array<{ timeOffset: number }> = [];

vi.mock("jassub", () => {
  class MockJASSUB {
    timeOffset = 0;
    ready = Promise.resolve();
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
