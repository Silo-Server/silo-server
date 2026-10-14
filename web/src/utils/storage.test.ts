import { beforeEach, describe, expect, it } from "vitest";
import { appearanceCache, storage } from "./storage";

const KEYS = storage.KEYS;

describe("appearance cache namespacing", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("keeps both accounts' values, so returning to the first still warm starts", () => {
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBeNull();
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "x-large", "2");

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1")).toBe("large");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBe("x-large");
  });

  it("ignores values written before namespacing existed", () => {
    storage.set(KEYS.UI_TEXT_SCALE, "large");

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1")).toBeNull();
  });

  it("follows the pointer to the most recent account, not the first", () => {
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, null)).toBe("large");
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "x-large", "2");

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, null)).toBe("x-large");
  });

  it("keeps a never-signed-in device's values in their own namespace", () => {
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", null);

    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, null)).toBe("large");
    // Not the bare key, and not visible to a real account.
    expect(storage.get(KEYS.UI_TEXT_SCALE)).toBeNull();
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1")).toBeNull();
  });

  it("routes a signed-out write into the last account's namespace without moving the pointer", () => {
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "default", null);

    // Reads and writes resolve their namespace the same way, so a change
    // made on the login screen is the one the next read sees. It lands in the
    // last account's cache, which is only a warm start: their server value
    // still wins once the settings request resolves.
    expect(storage.get(KEYS.UI_CACHE_OWNER)).toBe("1");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, null)).toBe("default");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "1")).toBe("default");
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBeNull();
  });

  it("keeps every key in the group independently namespaced", () => {
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
    appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", "1");
    appearanceCache.set(KEYS.UI_DATE_FORMAT, "iso", "2");

    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "1")).toBe("true");
    expect(appearanceCache.get(KEYS.UI_HIGH_CONTRAST, "2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_DATE_FORMAT, "2")).toBe("iso");
    expect(appearanceCache.get(KEYS.UI_DATE_FORMAT, "1")).toBeNull();
  });
});
