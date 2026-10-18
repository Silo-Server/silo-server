import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  detectHDRFromMatchMedia,
  prewarmCodecDetection,
  probeHDR10PlaybackSupport,
  probeMaxDecodeResolution,
  probeWebCapabilities,
  resetCodecDetectionForTests,
  useCodecDetection,
} from "./useCodecDetection";

afterEach(() => {
  resetCodecDetectionForTests();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

type DecodeAnswer = Pick<MediaCapabilitiesDecodingInfo, "supported" | "smooth" | "powerEfficient">;

const DECODED: DecodeAnswer = { supported: true, smooth: true, powerEfficient: true };
const NOT_DECODED: DecodeAnswer = { supported: false, smooth: false, powerEfficient: false };
const SOFTWARE_STUTTER: DecodeAnswer = { supported: true, smooth: false, powerEfficient: false };
const HARDWARE_NOT_SMOOTH: DecodeAnswer = { supported: true, smooth: false, powerEfficient: true };

/**
 * Answers decoder queries keyed by codec family and picture height, such as
 * "vp09@2160"; anything not listed is not decoded.
 */
function stubDecoder(answers: Record<string, DecodeAnswer>) {
  const decodingInfo = vi.fn((configuration: MediaDecodingConfiguration) => {
    const video = configuration.video;
    const codec = /codecs="(\w{4})/.exec(video?.contentType ?? "")?.[1];
    return Promise.resolve(answers[`${codec}@${video?.height}`] ?? NOT_DECODED);
  });
  vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
  return decodingInfo;
}

/** The queries a decoder stub received, as "codec WIDTHxHEIGHT". */
function queried(decodingInfo: { mock: { calls: MediaDecodingConfiguration[][] } }): string[] {
  return decodingInfo.mock.calls.map(([configuration]) => {
    const video = configuration?.video;
    const codec = /codecs="([^"]+)"/.exec(video?.contentType ?? "")?.[1];
    return `${codec} ${video?.width}x${video?.height}`;
  });
}

const UHD_QUERIES = [
  "avc1.640033 3840x2160",
  "hvc1.1.6.L150.B0 3840x2160",
  "vp09.00.51.08 3840x2160",
  "av01.0.12M.08 3840x2160",
];

describe("probeMaxDecodeResolution", () => {
  it("queries every codec at 4K at once, as a file", () => {
    const decodingInfo = vi.fn<
      (configuration: MediaDecodingConfiguration) => Promise<DecodeAnswer>
    >(() => new Promise(() => {}));
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    void probeMaxDecodeResolution();

    expect(queried(decodingInfo)).toEqual(UHD_QUERIES);
    expect(decodingInfo.mock.calls.every(([configuration]) => configuration.type === "file")).toBe(
      true,
    );
  });

  it("reports 2160p when the browser decodes 4K H.264 smoothly", async () => {
    const decodingInfo = stubDecoder({ "avc1@2160": DECODED });

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
    expect(queried(decodingInfo)).toEqual(UHD_QUERIES);
  });

  // The server downscales anything above the reported limit, whatever its
  // codec, so a 4K VP9 decoder must not be capped by H.264.
  it("reports 2160p for a browser that decodes only VP9 at 4K", async () => {
    stubDecoder({ "vp09@2160": DECODED, "avc1@1080": DECODED });

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
  });

  // iOS Safari reports even small HEVC streams as not smooth while its
  // hardware decoder plays them in real time.
  it("trusts a hardware decoder whose small reference stream is also not smooth", async () => {
    const decodingInfo = stubDecoder({
      "hvc1@2160": HARDWARE_NOT_SMOOTH,
      "hvc1@720": HARDWARE_NOT_SMOOTH,
      "avc1@1080": DECODED,
    });

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
    expect(queried(decodingInfo)).toContain("hvc1.1.6.L93.B0 1280x720");
  });

  // Chromium derives smooth from frames dropped in earlier playback, so a
  // hardware decoder that struggled at 4K is reported as not smooth there.
  it("keeps a not-smooth 4K answer when the reference stream plays smoothly", async () => {
    const decodingInfo = stubDecoder({
      "avc1@2160": HARDWARE_NOT_SMOOTH,
      "avc1@720": DECODED,
      "avc1@1080": DECODED,
    });

    await expect(probeMaxDecodeResolution()).resolves.toBe("1080p");
    expect(queried(decodingInfo)).toContain("avc1.64001F 1280x720");
  });

  it("caps at 1080p when 4K would stutter in software", async () => {
    stubDecoder({ "avc1@2160": SOFTWARE_STUTTER, "avc1@1080": DECODED });

    await expect(probeMaxDecodeResolution()).resolves.toBe("1080p");
  });

  it("counts a 4K query the browser throws on as no, keeping the other answers", async () => {
    const decodingInfo = vi.fn((configuration: MediaDecodingConfiguration) => {
      const contentType = configuration.video?.contentType ?? "";
      if (contentType.includes("av01")) return Promise.reject(new TypeError("unparsable codec"));
      const height = configuration.video?.height;
      if (contentType.includes("avc1") && height === 2160) return Promise.resolve(SOFTWARE_STUTTER);
      if (contentType.includes("avc1") && height === 1080) return Promise.resolve(DECODED);
      return Promise.resolve(NOT_DECODED);
    });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    await expect(probeMaxDecodeResolution()).resolves.toBe("1080p");
  });

  it("caps at 1080p when every codec rejects 4K", async () => {
    stubDecoder({ "avc1@1080": DECODED });

    await expect(probeMaxDecodeResolution()).resolves.toBe("1080p");
  });

  // A browser without H.264 rejects 1080p too; that is about the codec, not
  // the size, and the flat codec lists already carry it.
  it("reports 2160p when H.264 is rejected at every size", async () => {
    stubDecoder({});

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
  });

  it("reports 2160p without the Media Capabilities API", async () => {
    vi.stubGlobal("navigator", {});

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
  });

  it("reports 2160p when the query fails", async () => {
    vi.stubGlobal("navigator", {
      mediaCapabilities: { decodingInfo: vi.fn().mockRejectedValue(new TypeError("bad config")) },
    });

    await expect(probeMaxDecodeResolution()).resolves.toBe("2160p");
  });
});

describe("detectHDRFromMatchMedia", () => {
  const fakeMatchMedia = (matching: string[]) =>
    ((query: string) => ({ matches: matching.includes(query) })) as unknown as typeof matchMedia;

  it("returns false when matchMedia is unavailable", () => {
    expect(detectHDRFromMatchMedia(undefined)).toBe(false);
  });

  it("returns true when dynamic-range reports high", () => {
    expect(detectHDRFromMatchMedia(fakeMatchMedia(["(dynamic-range: high)"]))).toBe(true);
  });

  it("returns true when only video-dynamic-range reports high (Firefox)", () => {
    expect(detectHDRFromMatchMedia(fakeMatchMedia(["(video-dynamic-range: high)"]))).toBe(true);
  });

  it("returns false when neither query matches", () => {
    expect(detectHDRFromMatchMedia(fakeMatchMedia([]))).toBe(false);
  });
});

describe("probeWebCapabilities", () => {
  it("advertises HDR10 only after the exact progressive Media Capabilities probe", async () => {
    const decodingInfo = vi.fn().mockResolvedValue({
      supported: true,
      smooth: true,
      powerEfficient: true,
      keySystemAccess: null,
    });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(true);
    expect(decodingInfo).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "file",
        video: expect.objectContaining({
          contentType: 'video/mp4; codecs="hvc1.2.4.L153.B0"',
          colorGamut: "rec2020",
          transferFunction: "pq",
          hdrMetadataType: "smpteSt2086",
        }),
      }),
    );
  });

  // iOS Safari reports every HEVC configuration as not smooth, even a small
  // SDR stream, while its hardware decoder (power efficient) plays them in real time.
  it("accepts a hardware decoder where the browser reports no HEVC stream as smooth", async () => {
    const decodingInfo = vi
      .fn()
      .mockResolvedValue({ supported: true, smooth: false, powerEfficient: true });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(true);
    expect(decodingInfo).toHaveBeenLastCalledWith(
      expect.objectContaining({ video: expect.objectContaining({ width: 640, height: 360 }) }),
    );
  });

  // Chromium derives smooth from frames dropped in earlier playback, so a
  // hardware decoder that struggled at 4K is reported as not smooth there.
  it("keeps a not-smooth answer from a browser that plays the small stream smoothly", async () => {
    const decodingInfo = vi.fn().mockImplementation((configuration: MediaDecodingConfiguration) =>
      Promise.resolve({
        supported: true,
        smooth: configuration.video?.width === 640,
        powerEfficient: true,
      }),
    );
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(false);
  });

  it("does not claim HDR10 for a decoder that is neither smooth nor power efficient", async () => {
    const decodingInfo = vi
      .fn()
      .mockResolvedValue({ supported: true, smooth: false, powerEfficient: false });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(false);
    expect(decodingInfo).toHaveBeenCalledTimes(1);
  });

  // The strip remux delivers an hvc1-labeled file; support reported only for
  // hev1 is evidence for bytes Silo never sends and must not earn the claim.
  it("does not promote an hev1-only answer to an HDR10 claim", async () => {
    const decodingInfo = vi.fn().mockImplementation((configuration: MediaDecodingConfiguration) => {
      const supported = configuration.video?.contentType.includes("hev1") ?? false;
      return Promise.resolve({ supported, smooth: supported, powerEfficient: supported });
    });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(false);
  });

  it("advertises native Dolby Vision Profile 8 from the dvh1 sample entry", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === "application/vnd.apple.mpegurl" || mime === 'video/mp4; codecs="dvh1.08.06"'
        ? "probably"
        : "",
    );

    const capabilities = probeWebCapabilities();

    expect(capabilities.hdrDetails).toEqual({
      hdr10: false,
      hdr10_plus: false,
      hlg: false,
      dolby_vision_profiles: [8],
      dolby_vision_profile_levels: [{ profile: 8, max_level: 6, bl_compatibility_ids: [1] }],
    });
    expect(capabilities.codecsVideo).not.toContain("hevc");
    expect(capabilities.progressiveCodecsVideo).toContain("hevc");
    expect(capabilities.nativeHLS).toBe(true);
  });

  // The preserve remux tags its output dvh1; a browser that answers only for
  // dvhe has given no evidence for that file and must not earn the claim.
  it("does not promote a dvhe-only answer to a Dolby Vision claim", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvhe.08.06"' ? "probably" : "",
    );

    expect(probeWebCapabilities().hdrDetails.dolby_vision_profiles).toEqual([]);
  });

  // The generic media query describes the output, not the decoder. Safari 26
  // reports no HDR on an XDR display while answering "probably" for dvh1.
  it("keeps a definitive Dolby Vision answer from an output reporting no HDR", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvh1.08.06"' ? "probably" : "",
    );

    const capabilities = probeWebCapabilities();

    expect(capabilities.hdr).toBe(false);
    expect(capabilities.hdrDetails.dolby_vision_profiles).toEqual([8]);
    expect(capabilities.hdrDetails.dolby_vision_profile_levels).toEqual([
      { profile: 8, max_level: 6, bl_compatibility_ids: [1] },
    ]);
  });

  it("does not promote an indeterminate media-element answer to Dolby Vision support", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime.startsWith('video/mp4; codecs="dv') ? "maybe" : "",
    );

    expect(probeWebCapabilities().hdrDetails.dolby_vision_profiles).toEqual([]);
  });

  // The browser scales a 1080p picture into a 720p window itself, so a small
  // screen must not make Auto transcode a file the decoder plays.
  it("does not derive the resolution limit from the screen", () => {
    vi.stubGlobal("screen", { width: 1280, height: 720 });
    vi.stubGlobal("devicePixelRatio", 1);

    expect(probeWebCapabilities().maxResolution).toBe("2160p");
  });

  it("adopts the decoder's resolution limit once the async probes settle", async () => {
    vi.stubGlobal("screen", { width: 3840, height: 2160 });
    stubDecoder({ "avc1@2160": SOFTWARE_STUTTER, "avc1@1080": DECODED });

    const { result, unmount } = renderHook(() => useCodecDetection());
    expect(result.current.settled).toBe(false);
    expect(result.current.maxResolution).toBe("2160p");

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.maxResolution).toBe("1080p");
    unmount();
  });

  // A size query that never answers must not hold playback start past the
  // HDR10 probe by more than the deadline; its late answer serves later starts.
  it("assumes 2160p when the size probe misses its deadline", async () => {
    vi.useFakeTimers();
    const pending: Array<(answer: DecodeAnswer) => void> = [];
    vi.stubGlobal("navigator", {
      mediaCapabilities: {
        decodingInfo: vi.fn((configuration: MediaDecodingConfiguration) =>
          configuration.video?.transferFunction === "pq"
            ? Promise.resolve(NOT_DECODED)
            : new Promise<DecodeAnswer>((resolve) => pending.push(resolve)),
        ),
      },
    });

    const first = renderHook(() => useCodecDetection());
    await act(() => vi.advanceTimersByTimeAsync(1_999));
    expect(first.result.current.settled).toBe(false);
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(first.result.current.settled).toBe(true);
    expect(first.result.current.maxResolution).toBe("2160p");
    first.unmount();

    vi.useRealTimers();
    for (const resolve of pending.splice(0)) resolve(SOFTWARE_STUTTER);
    // Every 4K answer failed, so the 1080p H.264 query follows.
    await waitFor(() => expect(pending).toHaveLength(1));
    pending[0]!(DECODED);

    const second = renderHook(() => useCodecDetection());
    await waitFor(() => expect(second.result.current.settled).toBe(true));
    expect(second.result.current.maxResolution).toBe("1080p");
    second.unmount();
  });

  // Moving a window between displays can change what the decoder will admit to,
  // so the media-query listeners still drive a re-probe.
  it("re-probes Dolby Vision claims when the active output changes", () => {
    let decodes = false;
    const listeners = new Set<() => void>();
    const query = {
      matches: false,
      addEventListener: (_: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
    };
    vi.stubGlobal("matchMedia", () => query);
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      decodes && mime === 'video/mp4; codecs="dvh1.08.06"' ? "probably" : "",
    );

    const { result, unmount } = renderHook(() => useCodecDetection());
    expect(result.current.hdrDetails.dolby_vision_profiles).toEqual([]);

    act(() => {
      decodes = true;
      for (const listener of listeners) listener();
    });
    expect(result.current.hdrDetails.dolby_vision_profiles).toEqual([8]);
    unmount();
  });

  it("refreshes the active capabilities after the async HDR10 probe", async () => {
    const listeners = new Set<() => void>();
    // The output reports no HDR: the decode probe must still run and be trusted.
    const query = {
      matches: false,
      addEventListener: (_: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
    };
    vi.stubGlobal("matchMedia", () => query);
    vi.stubGlobal("navigator", {
      mediaCapabilities: {
        decodingInfo: vi.fn().mockResolvedValue({
          supported: true,
          smooth: true,
          powerEfficient: true,
          keySystemAccess: null,
        }),
      },
    });

    const { result, unmount } = renderHook(() => useCodecDetection());
    expect(result.current.settled).toBe(false);
    expect(result.current.hdrDetails.hdr10).toBe(false);
    expect(result.current.codecsVideo).not.toContain("hevc");
    expect(result.current.progressiveCodecsVideo).not.toContain("hevc");
    await act(async () => Promise.resolve());
    expect(result.current.hdrDetails).toMatchObject({
      hdr10: true,
      hdr10_max_width: 3840,
      hdr10_max_height: 2160,
      hdr10_max_frame_rate: 24,
      hdr10_max_bitrate_kbps: 80_000,
    });
    expect(result.current.codecsVideo).not.toContain("hevc");
    expect(result.current.progressiveCodecsVideo).toContain("hevc");
    expect(result.current.settled).toBe(true);
    unmount();
  });

  it("reuses a capability probe warmed before playback mounts", async () => {
    const decodingInfo = vi.fn().mockResolvedValue({
      supported: true,
      smooth: true,
      powerEfficient: true,
      keySystemAccess: null,
    });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });

    await prewarmCodecDetection();
    const { result, unmount } = renderHook(() => useCodecDetection());
    await act(async () => Promise.resolve());

    // One HDR10 query and one 4K query per codec, all issued by the warm-up.
    expect(decodingInfo).toHaveBeenCalledTimes(5);
    expect(result.current.settled).toBe(true);
    expect(result.current.hdrDetails.hdr10).toBe(true);
    expect(result.current.maxResolution).toBe("2160p");
    unmount();
  });

  it("advertises browser-playable MP3, FLAC, and OGG audio", () => {
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      ["audio/mp4", "audio/mpeg", "audio/flac", "audio/ogg"].some((supported) =>
        mime.startsWith(supported),
      )
        ? "probably"
        : "",
    );

    const capabilities = probeWebCapabilities();

    expect(capabilities.codecsAudio).toEqual(
      expect.arrayContaining(["mp3", "flac", "opus", "vorbis"]),
    );
    expect(capabilities.progressiveCodecsAudio).toEqual(expect.arrayContaining(["flac", "opus"]));
    expect(capabilities.progressiveCodecsAudio).not.toContain("vorbis");
    expect(capabilities.containers).toEqual(expect.arrayContaining(["mp4", "mp3", "flac", "ogg"]));
  });

  it("probes WebM with a codec that belongs to the container", () => {
    const canPlayType = vi
      .spyOn(HTMLMediaElement.prototype, "canPlayType")
      .mockImplementation((mime) =>
        mime === 'video/webm; codecs="vp09.00.10.08"' ? "probably" : "",
      );

    const capabilities = probeWebCapabilities();

    expect(capabilities.containers).toContain("webm");
    expect(canPlayType).toHaveBeenCalledWith('video/webm; codecs="vp09.00.10.08"');
    expect(canPlayType).not.toHaveBeenCalledWith('video/webm; codecs="avc1.640028"');
  });

  it("does not advertise native MKV playback on Firefox", () => {
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0",
    });
    const supportedTypes = new Set([
      'video/x-matroska; codecs="avc1.640028"',
      'video/mp4; codecs="avc1.640028"',
      'audio/mp4; codecs="mp4a.40.2"',
      'audio/webm; codecs="vorbis"',
    ]);
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      supportedTypes.has(mime) ? "probably" : "",
    );

    const capabilities = probeWebCapabilities();

    expect(capabilities.containers).not.toContain("mkv");
    expect(capabilities.codecsVideo).toContain("h264");
    expect(capabilities.codecsAudio).toContain("aac");
    expect(capabilities.codecsAudio).toContain("vorbis");
    expect(capabilities.progressiveCodecsAudio).toContain("aac");
    expect(capabilities.progressiveCodecsAudio).not.toContain("vorbis");
  });

  it("keeps advertising native MKV playback on other capable browsers", () => {
    vi.stubGlobal("navigator", {
      userAgent:
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0 Safari/537.36",
    });
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/x-matroska; codecs="avc1.640028"' ? "probably" : "",
    );

    expect(probeWebCapabilities().containers).toContain("mkv");
  });
});
