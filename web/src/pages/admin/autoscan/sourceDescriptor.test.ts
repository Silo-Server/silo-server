import { describe, expect, it } from "vitest";

import type { AutoscanAvailableSource, AutoscanScanSourceDescriptor, Library } from "@/api/types";

import {
  connectionIsMandatory,
  connectionMatchesKinds,
  DEFAULT_DESCRIPTOR,
  defaultDeliveryMode,
  descriptorFor,
  FILL_FROM_MOVIE_LIBRARY_PATHS,
  FILL_FROM_TV_LIBRARY_PATHS,
  fillValueFromLibraries,
  initialConfigValues,
  needsConnectionStep,
  needsDeliveryChoice,
  parseConfigValues,
  serializeConfigValues,
} from "./sourceDescriptor";

function available(descriptor?: Partial<AutoscanScanSourceDescriptor>): AutoscanAvailableSource {
  return {
    plugin_id: "p",
    capability_id: "c",
    display_name: "Test source",
    descriptor: { ...DEFAULT_DESCRIPTOR, ...descriptor },
  };
}

describe("descriptorFor", () => {
  it("falls back to host defaults when a server sends no descriptor", () => {
    // Guards against an older server: the flow must still work, not blank out.
    const source = { ...available(), descriptor: undefined } as unknown as AutoscanAvailableSource;
    expect(descriptorFor(source)).toEqual(DEFAULT_DESCRIPTOR);
  });

  it("falls back when delivery_modes is empty", () => {
    expect(descriptorFor(available({ delivery_modes: [] }))).toEqual(DEFAULT_DESCRIPTOR);
  });

  it("returns the declared descriptor when present", () => {
    const got = descriptorFor(available({ delivery_modes: ["webhook"], connection: "none" }));
    expect(got.connection).toBe("none");
  });
});

describe("step visibility", () => {
  it("skips the delivery question for a single-mode source", () => {
    expect(needsDeliveryChoice({ delivery_modes: ["poll"], connection: "optional" })).toBe(false);
    expect(needsDeliveryChoice({ delivery_modes: ["webhook"], connection: "optional" })).toBe(
      false,
    );
  });

  it("asks when a source supports both modes", () => {
    expect(
      needsDeliveryChoice({ delivery_modes: ["poll", "webhook"], connection: "optional" }),
    ).toBe(true);
  });

  it("hides the connection step for a credential-free source", () => {
    expect(needsConnectionStep({ delivery_modes: ["poll"], connection: "none" }, "poll")).toBe(
      false,
    );
  });

  it("hides the connection step for webhook delivery even when a connection is allowed", () => {
    // A webhook delivery never uses a bound connection — the provider pushes.
    expect(
      needsConnectionStep(
        { delivery_modes: ["poll", "webhook"], connection: "required" },
        "webhook",
      ),
    ).toBe(false);
  });

  it("shows the connection step for a polling source", () => {
    expect(needsConnectionStep(DEFAULT_DESCRIPTOR, "poll")).toBe(true);
  });

  it("treats connection as mandatory only when required and applicable", () => {
    expect(
      connectionIsMandatory({ delivery_modes: ["poll"], connection: "required" }, "poll"),
    ).toBe(true);
    expect(connectionIsMandatory(DEFAULT_DESCRIPTOR, "poll")).toBe(false);
    expect(
      connectionIsMandatory({ delivery_modes: ["webhook"], connection: "required" }, "webhook"),
    ).toBe(false);
  });
});

describe("defaultDeliveryMode", () => {
  it("prefers poll when both are offered", () => {
    expect(defaultDeliveryMode({ delivery_modes: ["webhook", "poll"], connection: "none" })).toBe(
      "poll",
    );
  });

  it("uses the only mode for a single-mode source", () => {
    expect(defaultDeliveryMode({ delivery_modes: ["webhook"], connection: "none" })).toBe(
      "webhook",
    );
  });
});

describe("connectionMatchesKinds", () => {
  it("offers every connection when no kinds are declared", () => {
    expect(connectionMatchesKinds(DEFAULT_DESCRIPTOR, "anything")).toBe(true);
  });

  it("restricts to declared kinds", () => {
    const d: AutoscanScanSourceDescriptor = {
      delivery_modes: ["poll"],
      connection: "optional",
      connection_kinds: ["sonarr"],
    };
    expect(connectionMatchesKinds(d, "sonarr")).toBe(true);
    expect(connectionMatchesKinds(d, "radarr")).toBe(false);
  });
});

describe("initialConfigValues", () => {
  it("seeds declared defaults and skips fields without one", () => {
    const got = initialConfigValues({
      delivery_modes: ["poll"],
      connection: "none",
      config_form: {
        fields: [
          {
            key: "a",
            label: "A",
            control: "TEXT",
            required: false,
            secret: false,
            multiline: false,
            default_value: "x",
          },
          {
            key: "b",
            label: "B",
            control: "NUMBER",
            required: false,
            secret: false,
            multiline: false,
            default_value: 2,
          },
          {
            key: "c",
            label: "C",
            control: "TEXT",
            required: false,
            secret: false,
            multiline: false,
          },
        ],
      },
    });
    // Values stay typed here; stringifying happens once, at submit.
    expect(got).toEqual({ a: "x", b: 2 });
  });

  it("returns nothing for a source with no config form", () => {
    expect(initialConfigValues(DEFAULT_DESCRIPTOR)).toEqual({});
  });
});

describe("serializeConfigValues / parseConfigValues", () => {
  const descriptor: AutoscanScanSourceDescriptor = {
    delivery_modes: ["poll"],
    connection: "none",
    config_form: {
      fields: [
        {
          key: "on",
          label: "On",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
        },
        {
          key: "tags",
          label: "Tags",
          control: "MULTI_SELECT",
          required: false,
          secret: false,
          multiline: false,
        },
        {
          key: "count",
          label: "Count",
          control: "NUMBER",
          required: false,
          secret: false,
          multiline: false,
        },
      ],
    },
  };

  // Stringifying on every change turned `false` into the truthy "false" (the
  // switch re-rendered enabled) and an array into a join the renderer read back
  // as no selection.
  it("round-trips a false switch without flipping it", () => {
    const stored = serializeConfigValues({ on: false });
    expect(stored).toEqual({ on: "false" });
    expect(parseConfigValues(descriptor, stored).on).toBe(false);
  });

  it("round-trips a multi-select as an array", () => {
    const stored = serializeConfigValues({ tags: ["a", "b"] });
    expect(stored).toEqual({ tags: "a,b" });
    expect(parseConfigValues(descriptor, stored).tags).toEqual(["a", "b"]);
  });

  it("round-trips an empty multi-select", () => {
    expect(parseConfigValues(descriptor, { tags: "" }).tags).toEqual([]);
  });

  it("round-trips a number", () => {
    expect(parseConfigValues(descriptor, serializeConfigValues({ count: 4 })).count).toBe(4);
  });

  it("drops null and undefined rather than writing them as text", () => {
    expect(serializeConfigValues({ a: null, b: undefined, c: "keep" })).toEqual({ c: "keep" });
  });
});

describe("fillValueFromLibraries", () => {
  const libraries = [
    { id: 1, name: "Movies", type: "movie", enabled: true, paths: ["/mnt/movies"] },
    { id: 2, name: "TV", type: "series", enabled: true, paths: ["/mnt/tv"] },
    { id: 3, name: "Everything", type: "mixed", enabled: true, paths: ["/mnt/mixed"] },
    { id: 4, name: "Off", type: "movie", enabled: false, paths: ["/mnt/disabled"] },
  ] as unknown as Library[];

  it("collects movie and mixed paths for a movie fill", () => {
    expect(fillValueFromLibraries(FILL_FROM_MOVIE_LIBRARY_PATHS, libraries)).toBe(
      "/mnt/movies\n/mnt/mixed",
    );
  });

  it("collects series and mixed paths for a TV fill", () => {
    expect(fillValueFromLibraries(FILL_FROM_TV_LIBRARY_PATHS, libraries)).toBe(
      "/mnt/tv\n/mnt/mixed",
    );
  });

  it("skips disabled libraries", () => {
    expect(fillValueFromLibraries(FILL_FROM_MOVIE_LIBRARY_PATHS, libraries)).not.toContain(
      "/mnt/disabled",
    );
  });

  it("returns null for an unknown or absent fill source", () => {
    expect(fillValueFromLibraries("something_else", libraries)).toBeNull();
    expect(fillValueFromLibraries(undefined, libraries)).toBeNull();
  });
});
