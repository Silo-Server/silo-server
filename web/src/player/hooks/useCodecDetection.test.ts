import { afterEach, describe, expect, it, vi } from "vitest";
import {
  detectHDRFromMatchMedia,
  detectMaxResolutionFromScreen,
  probeWebCapabilities,
} from "./useCodecDetection";

afterEach(() => {
  vi.restoreAllMocks();
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
