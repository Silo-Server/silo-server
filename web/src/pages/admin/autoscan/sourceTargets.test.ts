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
  it("prefers rewrite targets over config paths", () => {
    const got = resolvedPathsFor(
      source({
        path_rewrites: [{ from: "/downloads/tv", to: "/mnt/media/tv" }],
        source_config: { movie_flat_paths: "/somewhere/else" },
      }),
      pathDescriptor,
    );
    expect(got).toEqual(["/mnt/media/tv"]);
  });

  it("falls back to path-shaped config for local watchers", () => {
    const got = resolvedPathsFor(
      source({ source_config: { movie_flat_paths: "/mnt/media/movies\n/mnt/media/movies/4k" } }),
      pathDescriptor,
    );
    expect(got).toEqual(["/mnt/media/movies", "/mnt/media/movies/4k"]);
  });

  it("ignores non-path config values", () => {
    // A provider name must never be mistaken for a filesystem path.
    const got = resolvedPathsFor(
      source({ source_config: { movie_flat_paths: "sonarr" } }),
      pathDescriptor,
    );
    expect(got).toEqual([]);
  });

  it("ignores config keys the descriptor does not declare", () => {
    const got = resolvedPathsFor(
      source({ source_config: { unrelated_secret: "/etc/passwd" } }),
      pathDescriptor,
    );
    expect(got).toEqual([]);
  });

  it("strips trailing slashes so roots compare cleanly", () => {
    const got = resolvedPathsFor(
      source({ path_rewrites: [{ from: "/x", to: "/mnt/media/tv/" }] }),
      pathDescriptor,
    );
    expect(got).toEqual(["/mnt/media/tv"]);
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

  it("treats a connection-backed poll source with no rewrites as unknown", () => {
    // Empty rewrites are a valid passthrough: the provider already reports
    // paths that are valid Silo library paths, so nothing needs mapping.
    const got = sourceTargets(
      source({ connection_id: "c1" }),
      { delivery_modes: ["poll"], connection: "optional" },
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.unknown).toBe(true);
  });

  it("treats a webhook source with no rewrites as unknown", () => {
    // The built-in ARR webhook descriptor does not declare emits_native_paths,
    // but its paths arrive in the delivery payload, not from configuration.
    const got = sourceTargets(
      source({ delivery_mode: "webhook" }),
      { delivery_modes: ["webhook"], connection: "none" },
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.unknown).toBe(true);
  });

  it("treats a native-path source with no rewrites as unknown", () => {
    const got = sourceTargets(
      source(),
      { delivery_modes: ["poll"], connection: "none", emits_native_paths: true },
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.unknown).toBe(true);
  });

  it("still warns an optional-connection source with nothing bound", () => {
    // `optional` plus no connection is a local watcher in practice: nothing
    // will hand it paths at runtime, so empty path fields are a real fault.
    const got = sourceTargets(
      source({ connection_id: null }),
      { ...pathDescriptor, connection: "optional" },
      libraries,
    );
    expect(got.unresolvable).toBe(true);
    expect(got.unknown).toBe(false);
  });

  it("does not warn a required-connection source that has none bound", () => {
    // The row already reports the missing connection; a second warning about
    // paths points at the wrong fault.
    const got = sourceTargets(
      source({ connection_id: null }),
      { ...pathDescriptor, connection: "required" },
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.unknown).toBe(true);
  });

  it("does not warn a local watcher that declares no path fields", () => {
    // Nothing for the operator to fill in, so "no paths configured" would point
    // at a control that does not exist.
    const got = sourceTargets(
      source(),
      { delivery_modes: ["poll"], connection: "none" },
      libraries,
    );
    expect(got.unresolvable).toBe(false);
    expect(got.unknown).toBe(true);
  });

  it("still matches libraries for a webhook source that has rewrites", () => {
    const got = sourceTargets(
      source({
        delivery_mode: "webhook",
        path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      }),
      { delivery_modes: ["webhook"], connection: "none" },
      libraries,
    );
    expect(got.unknown).toBe(false);
    expect(got.libraries.map((l) => l.name)).toEqual(["TV Shows"]);
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
  it("names the libraries a source feeds", () => {
    expect(describeTargets({ libraries, unresolvable: false, unknown: false })).toBe(
      "Movies, TV Shows",
    );
  });

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
  it("still resolves rewrite targets when no config form is declared", () => {
    const got = sourceTargets(
      source({ path_rewrites: [{ from: "/dl", to: "/mnt/media/movies" }] }),
      DEFAULT_DESCRIPTOR,
      libraries,
    );
    expect(got.libraries.map((l) => l.name)).toEqual(["Movies"]);
  });

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
