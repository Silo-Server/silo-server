// @vitest-environment node

import { autoscanWebhookURL } from "./webhookURL";
import { describe, expect, it } from "vitest";

import type { Library } from "@/api/types";

import {
  collapseToRoots,
  expandedRootsFor,
  newMapping,
  hasUsableMapping,
  seedMappings,
  triggersFor,
  usableMappings,
} from "./webhookSetup";

const libraries = [
  { id: 1, name: "Movies", type: "movie", enabled: true, paths: ["/mnt/media/movies"] },
  { id: 2, name: "TV Shows", type: "series", enabled: true, paths: ["/mnt/media/tv"] },
  { id: 3, name: "Everything", type: "mixed", enabled: true, paths: ["/mnt/media/mixed"] },
  { id: 4, name: "Archive", type: "movie", enabled: false, paths: ["/mnt/archive"] },
] as unknown as Library[];

describe("triggersFor", () => {
  // These must stay aligned with importEventTypes/deleteEventTypes in
  // internal/autoscan/arrwebhook/parse.go — telling an operator to tick a box
  // the host ignores is how "I followed the steps" bug reports start.
  it("offers the import triggers the host actually consumes", () => {
    const labels = triggersFor("sonarr").map((t) => t.label);
    expect(labels).toContain("On File Import");
    expect(labels).toContain("On File Upgrade");
    expect(labels).toContain("On Rename");
  });

  it("names the delete trigger specific to each service", () => {
    expect(triggersFor("sonarr").map((t) => t.label)).toContain("On Episode File Delete");
    expect(triggersFor("radarr").map((t) => t.label)).toContain("On Movie File Delete");
  });

  it("shows a combined delete trigger when the provider is unknown", () => {
    const labels = triggersFor("auto").map((t) => t.label);
    expect(
      labels.some((l) => l.includes("Episode File Delete") && l.includes("Movie File Delete")),
    ).toBe(true);
  });
});

describe("collapseToRoots", () => {
  // Rewrites match by longest prefix at a segment boundary, so one rule for a
  // shared ancestor covers every path beneath it. Sibling paths dominate real
  // installs — the dev server's 96 paths contain no parent/child pairs at all,
  // so merely dropping nested paths would collapse nothing.
  it("collapses siblings to their common ancestor", () => {
    expect(
      collapseToRoots(["/mnt/media/movies/00s", "/mnt/media/movies/10s", "/mnt/media/tv/anime"]),
    ).toEqual(["/mnt/media"]);
  });

  it("absorbs children into an ancestor that is also present", () => {
    expect(collapseToRoots(["/mnt/media", "/mnt/media/movies/00s"])).toEqual(["/mnt/media"]);
  });

  it("keeps separate mount points apart", () => {
    // Different storage entirely — merging these to "/" would be wrong. Each
    // mount has a single member here, so neither is widened.
    expect(collapseToRoots(["/mnt/sharedrives/a/x", "/tmp/silo/y"])).toEqual([
      "/mnt/sharedrives/a/x",
      "/tmp/silo/y",
    ]);
  });

  it("leaves a lone path under a mount untouched", () => {
    // With one member there is nothing to merge, so do not widen it.
    expect(collapseToRoots(["/tmp/silo-transcode/test-extras"])).toEqual([
      "/tmp/silo-transcode/test-extras",
    ]);
  });

  it("de-duplicates and ignores trailing slashes and non-absolute entries", () => {
    expect(collapseToRoots(["/mnt/media/", "/mnt/media", "  ", "relative/path"])).toEqual([
      "/mnt/media",
    ]);
  });
});

describe("expandedRootsFor", () => {
  // A collapsed row assumes the arr mirrors Silo's tree below the root. When it
  // does not (/downloads/films vs /downloads/series), the operator needs a row
  // per branch — these are the branches the editor offers.
  it("offers the distinct child directories under a collapsed root", () => {
    expect(
      expandedRootsFor("/mnt/media", [
        "/mnt/media/movies/00s",
        "/mnt/media/movies/10s",
        "/mnt/media/tv/anime",
      ]).sort(),
    ).toEqual(["/mnt/media/movies", "/mnt/media/tv"]);
  });

  it("offers nothing when every path shares one child", () => {
    // Splitting here would produce the same single rule, so it is not an option.
    expect(
      expandedRootsFor("/mnt/media", ["/mnt/media/movies/00s", "/mnt/media/movies/10s"]),
    ).toEqual([]);
  });

  it("ignores paths outside the root", () => {
    expect(expandedRootsFor("/mnt/media", ["/srv/other/movies", "/srv/other/tv"])).toEqual([]);
  });
});

describe("seedMappings", () => {
  // These libraries all sit under /mnt/media, so each provider's selection
  // collapses to that single shared root — one row, not one per library.
  it("seeds one row for Sonarr's TV and mixed libraries", () => {
    expect(seedMappings("sonarr", libraries).map((m) => m.to)).toEqual(["/mnt/media"]);
  });

  it("still separates libraries on genuinely different mounts", () => {
    const split = [
      { id: 1, name: "A", type: "movie", enabled: true, paths: ["/mnt/media/movies"] },
      { id: 2, name: "B", type: "movie", enabled: true, paths: ["/srv/other/movies"] },
    ] as unknown as Library[];
    expect(seedMappings("radarr", split).map((m) => m.to)).toEqual([
      "/mnt/media/movies",
      "/srv/other/movies",
    ]);
  });

  it("skips disabled libraries", () => {
    expect(seedMappings("auto", libraries).map((m) => m.to)).not.toContain("/mnt/archive");
  });
});

describe("usableMappings", () => {
  it("keeps only rows with both sides filled", () => {
    const got = usableMappings([
      newMapping("/mnt/media/tv", "/downloads/tv"),
      newMapping("/mnt/media/movies", ""),
      newMapping("", "/downloads/movies"),
    ]);
    expect(got).toEqual([{ from: "/downloads/tv", to: "/mnt/media/tv" }]);
  });

  it("trims surrounding whitespace", () => {
    expect(usableMappings([newMapping("  /b  ", "  /a  ")])).toEqual([{ from: "/a", to: "/b" }]);
  });
});

describe("hasUsableMapping", () => {
  // A webhook source with no mapping accepts deliveries and resolves nothing —
  // the silent failure the guided flow exists to prevent.
  it("is false when every row is incomplete", () => {
    expect(hasUsableMapping([newMapping("/mnt/media/tv", "")])).toBe(false);
    expect(hasUsableMapping([])).toBe(false);
  });

  it("is true once one row is complete", () => {
    expect(
      hasUsableMapping([
        newMapping("/mnt/media/movies", ""),
        newMapping("/mnt/media/tv", "/downloads/tv"),
      ]),
    ).toBe(true);
  });
});

describe("v2 autoscan callback projection", () => {
  it.each([
    [
      "/api/v1/autoscan/webhooks/existing-token",
      "https://ui.example.test/api/v2/autoscan/webhooks/existing-token",
    ],
    [
      "https://server.example.test/silo/api/v1/autoscan/webhooks/existing-token",
      "https://server.example.test/silo/api/v2/autoscan/webhooks/existing-token",
    ],
    [
      "https://server.example.test/api/v2/autoscan/webhooks/existing-token",
      "https://server.example.test/api/v2/autoscan/webhooks/existing-token",
    ],
    [
      "/api/v1/autoscan/webhooks/token%2Bvalue?deployment=one",
      "https://ui.example.test/api/v2/autoscan/webhooks/token%2Bvalue?deployment=one",
    ],
    ["/api/v1/unrelated/existing-token", "https://ui.example.test/api/v1/unrelated/existing-token"],
  ])("preserves host, prefix and secret for %s", (input, expected) => {
    expect(autoscanWebhookURL(input, "https://ui.example.test")).toBe(expected);
  });
});
