// @vitest-environment node

import { expect, it } from "vitest";
import { findLibraryType, librarySettingSupport } from "./libraryTypes";

it.each(["movies", "series", "mixed", " TV "])("supports seek previews for %s", (type) => {
  expect(librarySettingSupport(type).trickplay).toBe(true);
});
it.each(["music", "", "audiobooks"])("withholds seek previews for %s", (type) => {
  expect(librarySettingSupport(type).trickplay).toBe(false);
});

// Library types are free text on the server, and internal/librarykind accepts
// other spellings of each type; the web resolves them to the same type.
it.each([
  ["mixed", "mixed"],
  [" TV ", "series"],
])("resolves %j to %s", (type, expected) => {
  expect(findLibraryType(type)?.value).toBe(expected);
});
it("leaves an unknown type unresolved", () => {
  expect(findLibraryType("music")).toBeUndefined();
});
