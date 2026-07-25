import { beforeEach, describe, expect, it } from "vitest";
import { appearanceCache, storage } from "@/utils/storage";
import { DEFAULT_THEME } from "@/lib/themes";
import { appearanceCacheOwner, getInitialTheme } from "./themePreferences";

describe("appearanceCacheOwner", () => {
  // No owner also means no API settings request: the hooks gate the query on
  // having resolved an owner.
  it("has no owner until auth bootstrap finishes", () => {
    expect(appearanceCacheOwner({ loading: true, user: null })).toBeNull();
    expect(appearanceCacheOwner({ loading: true, user: { id: 1 } })).toBeNull();
  });

  it("has no owner when nobody is signed in", () => {
    expect(appearanceCacheOwner({ loading: false, user: null })).toBeNull();
  });

  it("identifies the cache owner by the authenticated user id", () => {
    expect(appearanceCacheOwner({ loading: false, user: { id: 7 } })).toBe("7");
  });
});

describe("getInitialTheme", () => {
  beforeEach(() => {
    Object.values(storage.KEYS).forEach((key) => storage.remove(key));
  });

  it("warms up from the cache while the owner is unknown", () => {
    appearanceCache.set(storage.KEYS.THEME, "cobalt-studio", "1");

    expect(getInitialTheme(null)).toBe("cobalt-studio");
  });

  it("warms up from the cache for the account that stored it", () => {
    appearanceCache.set(storage.KEYS.THEME, "cobalt-studio", "1");

    expect(getInitialTheme("1")).toBe("cobalt-studio");
  });

  it("ignores another account's cached theme", () => {
    appearanceCache.set(storage.KEYS.THEME, "cobalt-studio", "1");

    expect(getInitialTheme("2")).toBe(DEFAULT_THEME);
  });
});
