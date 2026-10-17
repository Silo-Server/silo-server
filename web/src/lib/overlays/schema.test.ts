import { buildDefaultPrefs, parseOverlayPrefs } from "@/lib/overlays";
import { describe, expect, it } from "vitest";

// The native clients draw their watched check in a poster's top-right corner,
// so no badge starts there; a user can still move one in.
describe("default overlay positions", () => {
  it("leaves the top-right corner empty", () => {
    const prefs = parseOverlayPrefs({
      version: 2,
      preset: "classic",
      order: [],
      items: { imdb_top_250: { enabled: true }, rt_certified_fresh: { enabled: true } },
    });
    const positions = Object.values(prefs.items).map((item) => item.position);
    expect(positions).not.toContain("top-right");
    expect(Object.values(buildDefaultPrefs().items).map((item) => item.position)).not.toContain(
      "top-right",
    );
  });

  it("keeps a stored top-right position", () => {
    const prefs = parseOverlayPrefs({
      version: 2,
      preset: "classic",
      order: [],
      items: { rating_imdb: { enabled: true, position: "top-right" } },
    });
    expect(prefs.items.rating_imdb).toMatchObject({ enabled: true, position: "top-right" });
  });
});
