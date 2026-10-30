import { describe, expect, it } from "vitest";

import { heroTrailerEnabled, heroTrailerStage } from "./heroTrailer";

describe("heroTrailerEnabled", () => {
  const slide = { hasClip: true, reducedMotion: false, saveData: false };

  it("plays a clip only when one exists and neither setting rules it out", () => {
    expect(heroTrailerEnabled(slide)).toBe(true);
    expect(heroTrailerEnabled({ ...slide, hasClip: false })).toBe(false);
    expect(heroTrailerEnabled({ ...slide, reducedMotion: true })).toBe(false);
    expect(heroTrailerEnabled({ ...slide, saveData: true })).toBe(false);
  });
});

describe("heroTrailerStage", () => {
  const base = { enabled: true, held: false, ready: false, faded: false };

  it("stays on the still until the hold is over and the clip is ready", () => {
    expect(heroTrailerStage(base)).toBe("still");
    expect(heroTrailerStage({ ...base, held: true })).toBe("still");
    expect(heroTrailerStage({ ...base, ready: true })).toBe("still");
    expect(heroTrailerStage({ ...base, held: true, ready: true })).toBe("black");
  });

  it("shows the clip once the fade has run its course", () => {
    expect(heroTrailerStage({ ...base, held: true, ready: true, faded: true })).toBe("video");
  });

  it("never leaves the still for a clip it cannot play", () => {
    expect(
      heroTrailerStage({ enabled: false, held: true, ready: true, faded: true }),
    ).toBe("still");
  });
});
