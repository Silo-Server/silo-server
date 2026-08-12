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
          contentType: 'video/mp4; codecs="hev1.2.4.L153.B0"',
          colorGamut: "rec2020",
          transferFunction: "pq",
          hdrMetadataType: "smpteSt2086",
        }),
      }),
    );
  });

  it("does not probe HDR10 decoding against an SDR output", async () => {
    const decodingInfo = vi.fn();
    vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
    vi.stubGlobal("matchMedia", () => ({ matches: false }));

    await expect(probeHDR10PlaybackSupport()).resolves.toBe(false);
    expect(decodingInfo).not.toHaveBeenCalled();
  });

  it("advertises native Dolby Vision Profile 8 only on an HDR output", () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("high") }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvhe.08.06"' ? "probably" : "",
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

  it("does not advertise a Dolby Vision decoder against an SDR output", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const canPlayType = vi
      .spyOn(HTMLMediaElement.prototype, "canPlayType")
      .mockImplementation((mime) => (mime === 'video/mp4; codecs="dvhe.08.06"' ? "probably" : ""));

    const capabilities = probeWebCapabilities();

    expect(capabilities.hdrDetails.dolby_vision_profiles).toEqual([]);
    expect(capabilities.hdrDetails.dolby_vision_profile_levels).toEqual([]);
    expect(canPlayType).not.toHaveBeenCalledWith('video/mp4; codecs="dvhe.08.06"');
  });

  it("does not promote an indeterminate media-element answer to Dolby Vision support", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvhe.08.06"' ? "maybe" : "",
    );

    expect(probeWebCapabilities().hdrDetails.dolby_vision_profiles).toEqual([]);
  });

  it("refreshes Dolby Vision claims when the active HDR output changes", () => {
    let hdr = false;
    const listeners = new Set<() => void>();
    const query = {
      get matches() {
        return hdr;
      },
      addEventListener: (_: string, listener: () => void) => listeners.add(listener),
      removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
    };
    vi.stubGlobal("matchMedia", () => query);
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockImplementation((mime) =>
      mime === 'video/mp4; codecs="dvhe.08.06"' ? "probably" : "",
    );

    const { result, unmount } = renderHook(() => useCodecDetection());
    expect(result.current.hdrDetails.dolby_vision_profiles).toEqual([]);

    act(() => {
      hdr = true;
      for (const listener of listeners) listener();
    });
    expect(result.current.hdrDetails.dolby_vision_profiles).toEqual([8]);
    unmount();
  });

  it("refreshes the active capabilities after the async HDR10 probe", async () => {
    const listeners = new Set<() => void>();
    const query = {
      matches: true,
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
