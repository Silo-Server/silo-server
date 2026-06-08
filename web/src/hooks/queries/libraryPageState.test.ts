// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  parseLibraryPageStatePreference,
  serializeLibraryPageStatePreference,
  updateLibraryPageStatePreference,
} from "./libraryPageState";

describe("library page state preference helpers", () => {
  it("parses valid preferences", () => {
    expect(
      parseLibraryPageStatePreference(
        JSON.stringify({
          version: 1,
          libraries: {
            "7": { search: "tab=library&sort=year" },
          },
        }),
      ),
    ).toEqual({
      version: 1,
      libraries: {
        "7": { search: "tab=library&sort=year" },
      },
    });
  });

  it("ignores malformed, wrong-version, and non-string entries", () => {
    expect(parseLibraryPageStatePreference("not json")).toEqual({ version: 1, libraries: {} });
    expect(parseLibraryPageStatePreference(JSON.stringify({ version: 2, libraries: {} }))).toEqual({
      version: 1,
      libraries: {},
    });
    expect(
      parseLibraryPageStatePreference(
        JSON.stringify({
          version: 1,
          libraries: {
            "7": { search: 42 },
            nope: { search: "tab=library" },
            "9": { search: "tab=collections" },
          },
        }),
      ),
    ).toEqual({
      version: 1,
      libraries: {
        "9": { search: "tab=collections" },
      },
    });
  });

  it("updates and serializes per-library entries", () => {
    const next = updateLibraryPageStatePreference(
      {
        version: 1,
        libraries: {
          "1": { search: "tab=collections" },
        },
      },
      7,
      "tab=library&sort=year",
    );

    expect(JSON.parse(serializeLibraryPageStatePreference(next))).toEqual({
      version: 1,
      libraries: {
        "1": { search: "tab=collections" },
        "7": { search: "tab=library&sort=year" },
      },
    });
  });
});
