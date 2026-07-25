import { beforeEach, describe, expect, it } from "vitest";
import { appearanceCache, storage } from "./storage";

const KEYS = storage.KEYS;

describe("appearance cache namespacing", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("reads back what the same account wrote", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");

    expect(appearanceCache.get(KEYS.THEME, "1")).toBe("cobalt-studio");
  });

  it("hides another account's cached values", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");

    expect(appearanceCache.get(KEYS.THEME, "2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBeNull();
  });

  it("keeps both accounts' values, so returning to the first still warm starts", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.THEME, "oxblood-noir", "2");

    expect(appearanceCache.get(KEYS.THEME, "1")).toBe("cobalt-studio");
    expect(appearanceCache.get(KEYS.THEME, "2")).toBe("oxblood-noir");
  });

  it("ignores values written before namespacing existed", () => {
    storage.set(KEYS.THEME, "cobalt-studio");

    expect(appearanceCache.get(KEYS.THEME, "1")).toBeNull();
  });

  it("falls back to the last account that wrote while nobody is signed in", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");

    expect(appearanceCache.get(KEYS.THEME, null)).toBe("cobalt-studio");
  });

  it("follows the pointer to the most recent account, not the first", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.THEME, "oxblood-noir", "2");

    expect(appearanceCache.get(KEYS.THEME, null)).toBe("oxblood-noir");
  });

  it("keeps a never-signed-in device's values in their own namespace", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", null);

    expect(appearanceCache.get(KEYS.THEME, null)).toBe("cobalt-studio");
    // Not the bare key, and not visible to a real account.
    expect(storage.get(KEYS.THEME)).toBeNull();
    expect(appearanceCache.get(KEYS.THEME, "1")).toBeNull();
  });

  it("routes a signed-out write into the last account's namespace without moving the pointer", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.THEME, "evergreen-studio", null);

    // Reads and writes resolve their namespace the same way, so a theme change
    // made on the login screen is the one the next read sees. It lands in the
    // last account's cache, which is only a warm start: their server value
    // still wins once the settings request resolves.
    expect(storage.get(KEYS.UI_CACHE_OWNER)).toBe("1");
    expect(appearanceCache.get(KEYS.THEME, null)).toBe("evergreen-studio");
    expect(appearanceCache.get(KEYS.THEME, "1")).toBe("evergreen-studio");
    expect(appearanceCache.get(KEYS.THEME, "2")).toBeNull();
  });

  it("keeps every key in the group independently namespaced", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.UI_CUSTOM_CSS, "body{}", "1");
    appearanceCache.set(KEYS.UI_DATE_FORMAT, "iso", "2");

    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "1")).toBe("body{}");
    expect(appearanceCache.get(KEYS.UI_CUSTOM_CSS, "2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_DATE_FORMAT, "2")).toBe("iso");
    expect(appearanceCache.get(KEYS.UI_DATE_FORMAT, "1")).toBeNull();
  });
});
