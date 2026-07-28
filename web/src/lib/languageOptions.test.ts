import { describe, expect, it } from "vitest";

import { LANGUAGES } from "@/player/utils/languageNames";
import { LANGUAGE_OPTIONS, NAMED_LANGUAGE_OPTIONS } from "./languageOptions";

describe("languageOptions", () => {
  it("covers every language the shared list knows", () => {
    // The hand-written settings registry this replaced enumerated its own
    // subset, so a language the player could name was not always selectable in
    // settings. Deriving the list means the two cannot drift.
    expect(NAMED_LANGUAGE_OPTIONS.map((option) => option.value)).toEqual(
      LANGUAGES.map((language) => language.code),
    );
  });

  it("prefixes the unset entry only on the nullable list", () => {
    expect(LANGUAGE_OPTIONS[0]).toEqual({ value: "", label: "No preference" });
    expect(NAMED_LANGUAGE_OPTIONS.some((option) => option.value === "")).toBe(false);
  });
});
