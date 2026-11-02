// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { AutoscanScanSourceDescriptor, AutoscanSource, Library } from "@/api/types";

import { DEFAULT_DESCRIPTOR } from "./sourceDescriptor";
import { describeTargets, resolvedPathsFor, sourceTargets } from "./sourceTargets";

const libraries = [
  { id: 1, name: "Movies", type: "movie", enabled: true, paths: ["/mnt/media/movies"] },
  { id: 2, name: "TV Shows", type: "series", enabled: true, paths: ["/mnt/media/tv"] },
] as unknown as Library[];

function source(overrides: Partial<AutoscanSource> = {}): AutoscanSource {
  return {
    id: "s1",
    plugin_id: "p",
    capability_id: "c",
    enabled: true,
    delivery_mode: "poll",
    path_rewrites: [],
    source_config: {},
    ...overrides,
  } as AutoscanSource;
}

const pathDescriptor: AutoscanScanSourceDescriptor = {
  delivery_modes: ["poll"],
  connection: "none",
  config_form: {
    fields: [
      {
        key: "movie_flat_paths",
        label: "Movie paths",
        control: "TEXTAREA",
        required: false,
        secret: false,
        multiline: true,
      },
    ],
  },
};

describe("resolvedPathsFor", () => {
  it.each<[string, Partial<AutoscanSource>, string[]]>([
    [
      "prefers rewrite targets over config paths",
      {
        path_rewrites: [{ from: "/downloads/tv", to: "/mnt/media/tv" }],
        source_config: { movie_flat_paths: "/somewhere/else" },
      },
      ["/mnt/media/tv"],
    ],
    [
      "falls back to path-shaped config for local watchers",
      { source_config: { movie_flat_paths: "/mnt/media/movies\n/mnt/media/movies/4k" } },
      ["/mnt/media/movies", "/mnt/media/movies/4k"],
    ],
    // A provider name must never be mistaken for a filesystem path.
    ["ignores non-path config values", { source_config: { movie_flat_paths: "sonarr" } }, []],
    [
      "ignores config keys the descriptor does not declare",
      { source_config: { unrelated_secret: "/etc/passwd" } },
      [],
    ],
    [
      "strips trailing slashes so roots compare cleanly",
      { path_rewrites: [{ from: "/x", to: "/mnt/media/tv/" }] },
      ["/mnt/media/tv"],
    ],
  ])("%s", (_name, overrides, want) => {
    expect(resolvedPathsFor(source(overrides), pathDescriptor)).toEqual(want);
  });
});

describe("sourceTargets", () => {
  it("matches a library when the source path sits under its root", () => {
    const got = sourceTargets(
      source({ path_rewrites: [{ from: "/dl", to: "/mnt/media/tv/Show" }] }),
      pathDescriptor,
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.libraries.map((l) => l.name)).toEqual(["TV Shows"]);
  });

  it("matches when the source watches a parent of the library root", () => {
    // A watcher on /mnt/media legitimately feeds both libraries beneath it.
    const got = sourceTargets(
      source({ source_config: { movie_flat_paths: "/mnt/media" } }),
      pathDescriptor,
      libraries,
    );
    expect(got.libraries.map((l) => l.name)).toEqual(["Movies", "TV Shows"]);
  });

  it("flags a local watcher with no path information as unresolvable", () => {
    // The silent-failure case: looks configured, can never match anything.
    const got = sourceTargets(source(), pathDescriptor, libraries);
    expect(got.unresolvable).toBe(true);
    expect(got.unknown).toBe(false);
    expect(got.libraries).toEqual([]);
  });

  it.each<[string, Partial<AutoscanSource>, AutoscanScanSourceDescriptor, boolean, boolean]>([
    // Empty rewrites are a valid passthrough: the provider already reports
    // paths that are valid Silo library paths, so nothing needs mapping.
    [
      "treats a connection-backed poll source with no rewrites as unknown",
      { connection_id: "c1" },
      { delivery_modes: ["poll"], connection: "optional" },
      false,
      true,
    ],
    // The built-in ARR webhook descriptor does not declare emits_native_paths,
    // but its paths arrive in the delivery payload, not from configuration.
    [
      "treats a webhook source with no rewrites as unknown",
      { delivery_mode: "webhook" },
      { delivery_modes: ["webhook"], connection: "none" },
      false,
      true,
    ],
    [
      "treats a native-path source with no rewrites as unknown",
      {},
      { delivery_modes: ["poll"], connection: "none", emits_native_paths: true },
      false,
      true,
    ],
    // `optional` plus no connection is a local watcher in practice: nothing
    // will hand it paths at runtime, so empty path fields are a real fault.
    [
      "still warns an optional-connection source with nothing bound",
      { connection_id: null },
      { ...pathDescriptor, connection: "optional" },
      true,
      false,
    ],
    // The row already reports the missing connection; a second warning about
    // paths points at the wrong fault.
    [
      "does not warn a required-connection source that has none bound",
      { connection_id: null },
      { ...pathDescriptor, connection: "required" },
      false,
      true,
    ],
    // Nothing for the operator to fill in, so "no paths configured" would point
    // at a control that does not exist.
    [
      "does not warn a local watcher that declares no path fields",
      {},
      { delivery_modes: ["poll"], connection: "none" },
      false,
      true,
    ],
  ])("%s", (_name, overrides, descriptor, unresolvable, unknown) => {
    const got = sourceTargets(source(overrides), descriptor, libraries);
    expect(got.unresolvable).toBe(unresolvable);
    expect(got.unknown).toBe(unknown);
  });

  it("distinguishes configured-but-unmatched from unconfigured", () => {
    const got = sourceTargets(
      source({ path_rewrites: [{ from: "/dl", to: "/mnt/other/stuff" }] }),
      pathDescriptor,
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.libraries).toEqual([]);
  });

  it("does not match on a shared path prefix that is not a real parent", () => {
    const got = sourceTargets(
      source({ path_rewrites: [{ from: "/dl", to: "/mnt/media/movies-archive" }] }),
      pathDescriptor,
      libraries,
    );
    expect(got.libraries).toEqual([]);
  });
});

describe("describeTargets", () => {
  it("reports the two failure states distinctly", () => {
    expect(describeTargets({ libraries: [], unresolvable: true, unknown: false })).toBe(
      "No paths configured",
    );
    expect(describeTargets({ libraries: [], unresolvable: false, unknown: false })).toBe(
      "No matching library",
    );
  });
});

describe("default descriptor sources", () => {
  it("reads any path-shaped config when the descriptor declares no fields", () => {
    // With no declared field list there is nothing to filter on, so absolute
    // values are the only signal available.
    const got = sourceTargets(
      source({ source_config: { anything: "/mnt/media/tv" } }),
      DEFAULT_DESCRIPTOR,
      libraries,
    );
    expect(got.libraries.map((l) => l.name)).toEqual(["TV Shows"]);
  });
});
