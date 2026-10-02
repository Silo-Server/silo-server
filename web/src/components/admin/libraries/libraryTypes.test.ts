import { expect, it } from "vitest";
import { librarySettingSupport } from "./libraryTypes";

it.each(["movie", "movies", "series", "tv", "show", "tvshows", "mixed", " TV "])(
  "supports seek previews for %s",
  (type) => {
    expect(librarySettingSupport(type).trickplay).toBe(true);
  },
);
it.each(["music", "custom", "", "audiobooks", "ebooks", "podcasts"])(
  "withholds seek previews for %s",
  (type) => {
    expect(librarySettingSupport(type).trickplay).toBe(false);
  },
);
