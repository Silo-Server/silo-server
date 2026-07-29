import { describe, expect, it } from "vitest";

import type { Library } from "@/api/types";

import {
  collapseToRoots,
  expandedRootsFor,
  newMapping,
  hasUsableMapping,
  seedMappings,
  settingsPathFor,
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
    expect(labels).toContain("On Import");
    expect(labels).toContain("On Upgrade");
    expect(labels).toContain("On Rename");
  });

  it("names the delete trigger specific to each service", () => {
    expect(triggersFor("sonarr").map((t) => t.label)).toContain("On Episode File Delete");
    expect(triggersFor("radarr").map((t) => t.label)).toContain("On Movie File Delete");
  });

  it("never offers a Sonarr-only trigger to Radarr", () => {
    expect(triggersFor("radarr").map((t) => t.label)).not.toContain("On Episode File Delete");
  });

  it("marks import and upgrade as required, rename as optional", () => {
    const byLabel = new Map(triggersFor("sonarr").map((t) => [t.label, t.required]));
    expect(byLabel.get("On Import")).toBe(true);
    expect(byLabel.get("On Upgrade")).toBe(true);
    expect(byLabel.get("On Rename")).toBe(false);
  });

  it("shows a combined delete trigger when the provider is unknown", () => {
    const labels = triggersFor("auto").map((t) => t.label);
    expect(
      labels.some((l) => l.includes("Episode File Delete") && l.includes("Movie File Delete")),
    ).toBe(true);
  });
});

describe("settingsPathFor", () => {
  it("names the specific service when known", () => {
    expect(settingsPathFor("sonarr")).toContain("Sonarr → Settings → Connect");
    expect(settingsPathFor("radarr")).toContain("Radarr → Settings → Connect");
  });

  it("covers both when the provider is auto", () => {
    expect(settingsPathFor("auto")).toContain("Sonarr/Radarr");
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

  it("collapses the real dev-server shape to one row per mount", () => {
    // Verified against 96 live paths: 95 under /mnt/sharedrives, 1 under /tmp.
    const real = [
      "/mnt/sharedrives/zd-storage-ceph/movies/00s",
      "/mnt/sharedrives/zd-storage-ceph/television-int/zh",
      "/mnt/sharedrives/arkyn-storage-ceph/test_files/1080p",
      "/mnt/sharedrives/zd-storage-books/audiobooks",
      "/tmp/silo-transcode/test-extras",
    ];
    expect(collapseToRoots(real)).toEqual(["/mnt/sharedrives", "/tmp/silo-transcode/test-extras"]);
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

  it("seeds one row for Radarr's movie and mixed libraries", () => {
    expect(seedMappings("radarr", libraries).map((m) => m.to)).toEqual(["/mnt/media"]);
  });

  it("seeds one row across every library when the provider is unknown", () => {
    expect(seedMappings("auto", libraries).map((m) => m.to)).toEqual(["/mnt/media"]);
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

  it("collapses many sibling paths under one mount into a single row", () => {
    // Shape taken from the dev server, where 95 of 96 paths share a mount.
    const sprawling = [
      {
        id: 9,
        name: "Movies",
        type: "movie",
        enabled: true,
        paths: [
          "/mnt/sharedrives/zd-storage-ceph/movies/00s",
          "/mnt/sharedrives/zd-storage-ceph/movies/10s",
          "/mnt/sharedrives/zd-storage-ceph/movies/4k",
          "/mnt/sharedrives/arkyn-storage-ceph/movies",
        ],
      },
    ] as unknown as Library[];

    expect(seedMappings("radarr", sprawling).map((m) => m.to)).toEqual(["/mnt/sharedrives"]);
  });

  it("skips disabled libraries", () => {
    expect(seedMappings("auto", libraries).map((m) => m.to)).not.toContain("/mnt/archive");
  });

  it("leaves the arr-side path blank for the operator to fill", () => {
    expect(seedMappings("sonarr", libraries).every((m) => m.from === "")).toBe(true);
  });

  it("returns nothing when no libraries exist", () => {
    expect(seedMappings("sonarr", [])).toEqual([]);
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
