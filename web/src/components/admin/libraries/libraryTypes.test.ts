// @vitest-environment node

import { expect, it } from "vitest";
import { librarySettingSupport } from "./libraryTypes";

it.each(["movies", "series", "mixed", " TV "])("supports seek previews for %s", (type) => {
  expect(librarySettingSupport(type).trickplay).toBe(true);
});
it.each(["music", "", "audiobooks"])("withholds seek previews for %s", (type) => {
  expect(librarySettingSupport(type).trickplay).toBe(false);
});
