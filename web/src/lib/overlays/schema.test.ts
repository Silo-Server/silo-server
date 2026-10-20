import { describe, expect, it } from "vitest";

import {
  overlayPrefsForServer,
  parseOverlayPrefs,
  serializeOverlayPrefs,
  withoutUnlistedImages,
} from "./schema";

const ref = (pair: string) => `${pair.repeat(32)}.webp`;

describe("parseOverlayPrefs", () => {
  it("moves a badge stored in the top-right corner to the right edge", () => {
    const prefs = parseOverlayPrefs({
      version: 2,
      preset: "classic",
      order: [],
      items: { rating_imdb: { enabled: true, position: "top-right" } },
    });
    expect(prefs.items.rating_imdb.position).toBe("right-edge");
  });

  it("keeps the style, watch status and images it accepts and drops the rest", () => {
    const prefs = parseOverlayPrefs({
      version: 2,
      preset: "plate",
      watchStatus: "sometimes",
      style: {
        size: 1.5,
        shape: "blob",
        background: "#000000b3",
        textColor: "red",
        borderWidth: 9,
      },
      order: [],
      items: {
        resolution: {
          enabled: true,
          position: "left-edge",
          image: "not-a-ref",
          valueImages: { "2160p": ref("ab"), "": ref("cd"), "1080p": "nope" },
        },
      },
    });
    expect(prefs.preset).toBe("plate");
    expect(prefs.watchStatus).toBeUndefined();
    expect(prefs.style).toEqual({ size: 1.5, background: "#000000b3" });
    expect(prefs.items.resolution).toMatchObject({
      position: "left-edge",
      valueImages: { "2160p": ref("ab") },
    });
    expect(prefs.items.resolution.image).toBeUndefined();
  });

  it("writes a document without new fields exactly as before", () => {
    const stored = {
      version: 2,
      preset: "classic",
      order: [],
      items: { hdr: { enabled: true, position: "top-left" } },
    };
    const reparsed = JSON.parse(serializeOverlayPrefs(parseOverlayPrefs(stored)));
    expect(Object.keys(reparsed)).toEqual(["version", "preset", "order", "items"]);
    expect(reparsed.items.hdr).toEqual({ enabled: true, position: "top-left" });
  });
});

describe("overlayPrefsForServer", () => {
  const prefs = parseOverlayPrefs({
    version: 2,
    preset: "band",
    watchStatus: "off",
    style: { size: 1.2 },
    order: [],
    items: {
      rating_imdb: { enabled: true, position: "left-edge", image: ref("ab") },
      show_status: { enabled: true, position: "right-edge" },
    },
  });

  it("hands a server below settings revision 23 only what it accepts", () => {
    const storable = overlayPrefsForServer(prefs, { manifestRevision: 22, storedIds: new Set() });
    expect(storable.preset).toBe("classic");
    expect(storable).not.toHaveProperty("style");
    expect(storable).not.toHaveProperty("watchStatus");
    expect(storable.items.rating_imdb).toEqual({ enabled: true, position: "top-left" });
    expect(storable.items.show_status.position).toBe("top-right");
  });

  it("keeps everything for a server at revision 23", () => {
    const storable = overlayPrefsForServer(prefs, { manifestRevision: 23, storedIds: new Set() });
    expect(storable).toEqual(prefs);
  });
});

describe("withoutUnlistedImages", () => {
  it("drops refs the library no longer lists so cards do not request them", () => {
    const prefs = parseOverlayPrefs({
      version: 2,
      preset: "classic",
      order: [],
      items: {
        resolution: {
          enabled: true,
          position: "top-left",
          image: ref("aa"),
          valueImages: { "2160p": ref("bb"), "1080p": ref("cc") },
        },
      },
    });
    const rendered = withoutUnlistedImages(prefs, new Set([ref("bb")]));
    expect(rendered.items.resolution).not.toHaveProperty("image");
    expect(rendered.items.resolution.valueImages).toEqual({ "2160p": ref("bb") });
    // Nothing to drop returns the same document, so memoized cards keep it.
    expect(withoutUnlistedImages(rendered, new Set([ref("bb")]))).toBe(rendered);
  });
});
