import { describe, expect, it } from "vitest";

import type { ItemVideo } from "@/api/types";
import { pickHeroTrailer, shouldPlayHeroTrailer, youtubeHeroEmbedUrl } from "./heroTrailer";

function video(overrides: Partial<ItemVideo> = {}): ItemVideo {
  return {
    kind: "clip",
    site: "youtube",
    site_key: "abc123",
    is_official: false,
    ...overrides,
  };
}

describe("pickHeroTrailer", () => {
  it("returns null when there are no YouTube keys", () => {
    expect(pickHeroTrailer(undefined)).toBeNull();
    expect(pickHeroTrailer([])).toBeNull();
    expect(pickHeroTrailer([video({ site: "vimeo", kind: "trailer" })])).toBeNull();
    expect(pickHeroTrailer([video({ site_key: "  " })])).toBeNull();
  });

  it("prefers an official trailer over a teaser and unofficial trailer", () => {
    const officialTrailer = video({ kind: "trailer", site_key: "official", is_official: true });
    expect(
      pickHeroTrailer([
        video({ kind: "teaser", site_key: "teaser", is_official: true }),
        video({ kind: "trailer", site_key: "unofficial" }),
        officialTrailer,
      ]),
    ).toBe(officialTrailer);
  });

  it("falls back to any trailer, then teaser, then first YouTube clip", () => {
    expect(
      pickHeroTrailer([
        video({ kind: "featurette", site_key: "feat" }),
        video({ kind: "trailer", site_key: "t" }),
      ])?.site_key,
    ).toBe("t");
    expect(pickHeroTrailer([video({ kind: "teaser", site_key: "s" })])?.site_key).toBe("s");
    expect(pickHeroTrailer([video({ kind: "clip", site_key: "c" })])?.site_key).toBe("c");
  });
});

describe("youtubeHeroEmbedUrl", () => {
  it("mutes, loops, and autoplays on the privacy host", () => {
    const url = youtubeHeroEmbedUrl("dQw4w9wgGcQ");
    expect(url.startsWith("https://www.youtube-nocookie.com/embed/dQw4w9wgGcQ?")).toBe(true);
    expect(url).toContain("autoplay=1");
    expect(url).toContain("mute=1");
    expect(url).toContain("loop=1");
    expect(url).toContain("playlist=dQw4w9wgGcQ");
    expect(url).toContain("controls=0");
  });
});

describe("shouldPlayHeroTrailer", () => {
  it("plays only on a visible wide viewport without reduced motion", () => {
    expect(
      shouldPlayHeroTrailer({
        reducedMotion: false,
        viewportWideEnough: true,
        documentVisible: true,
      }),
    ).toBe(true);
    expect(
      shouldPlayHeroTrailer({
        reducedMotion: true,
        viewportWideEnough: true,
        documentVisible: true,
      }),
    ).toBe(false);
    expect(
      shouldPlayHeroTrailer({
        reducedMotion: false,
        viewportWideEnough: false,
        documentVisible: true,
      }),
    ).toBe(false);
    expect(
      shouldPlayHeroTrailer({
        reducedMotion: false,
        viewportWideEnough: true,
        documentVisible: false,
      }),
    ).toBe(false);
  });
});
