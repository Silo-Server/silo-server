import { describe, expect, it } from "vitest";
import { detectHDRFromMatchMedia, detectMaxResolutionFromScreen } from "./useCodecDetection";

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
