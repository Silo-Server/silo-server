import { beforeEach, describe, expect, it } from "vitest";
import { appearanceCache, storage } from "@/utils/storage";
import { DEFAULT_THEME } from "@/lib/themes";
import { appearanceCacheOwner, getInitialTheme } from "./themePreferences";

describe("appearanceCacheOwner", () => {
  // No owner also means no API settings request: the hooks gate the query on
  // having resolved an owner.
  it("has no owner until auth bootstrap finishes", () => {
    expect(appearanceCacheOwner({ loading: true, user: null, profile: null })).toBeNull();
    expect(
      appearanceCacheOwner({ loading: true, user: { id: 1 }, profile: { id: "p1" } }),
    ).toBeNull();
  });

  it("has no owner when nobody is signed in", () => {
    expect(appearanceCacheOwner({ loading: false, user: null, profile: null })).toBeNull();
  });

  // Appearance is profile-scoped: on the profile picker there is no identity
  // to resolve settings for yet, so the cache keeps the device's last look.
  it("has no owner before a profile is selected", () => {
    expect(appearanceCacheOwner({ loading: false, user: { id: 7 }, profile: null })).toBeNull();
  });

  it("identifies the cache owner by user id plus active profile id", () => {
    expect(appearanceCacheOwner({ loading: false, user: { id: 7 }, profile: { id: "p2" } })).toBe(
      "7:p2",
    );
  });

  it("gives sibling profiles on one account distinct owners", () => {
    const user = { id: 7 };
    expect(appearanceCacheOwner({ loading: false, user, profile: { id: "p1" } })).not.toBe(
      appearanceCacheOwner({ loading: false, user, profile: { id: "p2" } }),
    );
  });
});

describe("getInitialTheme", () => {
  beforeEach(() => {
    // Not storage.remove over storage.KEYS: appearanceCache writes namespaced
    // keys ("silo-theme:1") and an owner pointer, none of which appear in
    // storage.KEYS, so that cleanup left both behind and made these cases
    // order-dependent. storage.test.ts already clears the whole store.
    localStorage.clear();
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
