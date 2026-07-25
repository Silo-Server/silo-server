import { beforeEach, describe, expect, it } from "vitest";
import { appearanceCache, customThemeCache, dateTimeFormatCache, storage } from "./storage";

const KEYS = storage.KEYS;

describe("owned caches", () => {
  beforeEach(() => {
    Object.values(KEYS).forEach((key) => storage.remove(key));
  });

  it("trusts the cache while nobody is known to be signed in", () => {
    storage.set(KEYS.THEME, "cobalt-studio");
    storage.set(KEYS.UI_APPEARANCE_OWNER, "1");

    expect(appearanceCache.isTrusted(null)).toBe(true);
    expect(appearanceCache.get(KEYS.THEME, null)).toBe("cobalt-studio");
  });

  it("trusts the cache for the account that stamped it", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");

    expect(storage.get(KEYS.UI_APPEARANCE_OWNER)).toBe("1");
    expect(appearanceCache.isTrusted("1")).toBe(true);
    expect(appearanceCache.get(KEYS.THEME, "1")).toBe("cobalt-studio");
  });

  it("hides another account's cached values", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");

    expect(appearanceCache.isTrusted("2")).toBe(false);
    expect(appearanceCache.get(KEYS.THEME, "2")).toBeNull();
    expect(appearanceCache.get(KEYS.UI_TEXT_SCALE, "2")).toBeNull();
  });

  it("does not trust an unstamped legacy cache for a known account", () => {
    storage.set(KEYS.THEME, "cobalt-studio");

    expect(appearanceCache.isTrusted("1")).toBe(false);
    expect(appearanceCache.get(KEYS.THEME, "1")).toBeNull();
  });

  it("clear() drops every member value and hands the empty cache to the new owner", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    appearanceCache.set(KEYS.UI_TEXT_SCALE, "large", "1");
    appearanceCache.set(KEYS.UI_TEXT_WEIGHT, "strong", "1");
    appearanceCache.set(KEYS.UI_HIGH_CONTRAST, "true", "1");

    appearanceCache.clear("2");

    expect(storage.get(KEYS.THEME)).toBeNull();
    expect(storage.get(KEYS.UI_TEXT_SCALE)).toBeNull();
    expect(storage.get(KEYS.UI_TEXT_WEIGHT)).toBeNull();
    expect(storage.get(KEYS.UI_HIGH_CONTRAST)).toBeNull();
    expect(appearanceCache.isTrusted("2")).toBe(true);
    expect(appearanceCache.isTrusted("1")).toBe(false);
  });

  it("clear() without an owner leaves the cache unstamped", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");

    appearanceCache.clear(null);

    expect(storage.get(KEYS.THEME)).toBeNull();
    expect(storage.get(KEYS.UI_APPEARANCE_OWNER)).toBeNull();
    expect(appearanceCache.isTrusted("1")).toBe(false);
  });

  it("keeps each cache group's ownership independent", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", "1");
    customThemeCache.set(KEYS.UI_CUSTOM_CSS, "body{}", "2");
    dateTimeFormatCache.set(KEYS.UI_DATE_FORMAT, "iso", "3");

    expect(appearanceCache.isTrusted("1")).toBe(true);
    expect(appearanceCache.isTrusted("2")).toBe(false);
    expect(customThemeCache.isTrusted("2")).toBe(true);
    expect(customThemeCache.isTrusted("1")).toBe(false);
    expect(dateTimeFormatCache.isTrusted("3")).toBe(true);
    expect(dateTimeFormatCache.isTrusted("1")).toBe(false);
  });

  it("stamps nothing when there is no owner to stamp", () => {
    appearanceCache.set(KEYS.THEME, "cobalt-studio", null);

    expect(storage.get(KEYS.THEME)).toBe("cobalt-studio");
    expect(storage.get(KEYS.UI_APPEARANCE_OWNER)).toBeNull();
  });
});
