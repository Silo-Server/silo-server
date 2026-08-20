import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  detectHDRFromMatchMedia,
  detectMaxResolutionFromScreen,
  probeHDR10PlaybackSupport,
  probeWebCapabilities,
  useCodecDetection,
} from "./useCodecDetection";

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("detectMaxResolutionFromScreen", () => {
  it("treats a 2560x1440 display as above the 720p bucket", () => {
    expect(detectMaxResolutionFromScreen(2560, 1440)).toBe("2160p");
  });

  it("keeps a 1280x720 display in the 720p bucket", () => {
    expect(detectMaxResolutionFromScreen(1280, 720)).toBe("720p");
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

  // Safari 26 reports `dynamic-range: standard` on XDR panels, and browsers
  // tone-map HDR onto SDR outputs regardless. Decode evidence stands alone.
  it("probes HDR10 decoding even when the output reports no HDR", async () => {
    const decodingInfo = vi.fn().mockResolvedValue({
      supported: true,
      smooth: true,
      powerEfficient: true,
      keySystemAccess: null,
    });
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
    vi.stubGlobal("matchMedia", () => ({ matches: false }));

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(true);
    expect(decodingInfo).toHaveBeenCalled();
  });

  it("advertises native Dolby Vision Profile 8 from the dvh1 sample entry", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvh1.08.06"' ? "probably" : "",
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

  // `screen` is measured in logical CSS pixels, so a 2x MacBook Pro XDR panel
  // advertises 1080p unless the device pixel ratio is applied.
  it("scales the screen-derived resolution by the device pixel ratio", () => {
    vi.stubGlobal("screen", { width: 1728, height: 1117 });
    vi.stubGlobal("devicePixelRatio", 2);

    expect(probeWebCapabilities().maxResolution).toBe("2160p");
  });

  it("treats a missing device pixel ratio as 1", () => {
    vi.stubGlobal("screen", { width: 1728, height: 1117 });
    vi.stubGlobal("devicePixelRatio", undefined);

    expect(probeWebCapabilities().maxResolution).toBe("1080p");
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
});
