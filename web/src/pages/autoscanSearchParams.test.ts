import { describe, expect, it } from "vitest";

import {
  buildLegacyAutoscanRedirectTarget,
  isLegacyAdvancedTab,
  normalizeTab,
} from "./autoscanSearchParams";

describe("buildLegacyAutoscanRedirectTarget", () => {
  it("lands a bare link on the Autoscan tab", () => {
    expect(buildLegacyAutoscanRedirectTarget("")).toBe("/admin/libraries?tab=autoscan");
  });

  it("translates the legacy tab into the embedded page's view parameter", () => {
    // `tab` names the Libraries tab now, so the panel's own selection has to
    // move to `view` or the link silently lands on Sources.
    expect(buildLegacyAutoscanRedirectTarget("?tab=activity")).toBe(
      "/admin/libraries?tab=autoscan&view=activity",
    );
  });

  it("carries the retired connections and settings tabs through", () => {
    // AdminAutoscan opens its Advanced section for these, but only if the value
    // reaches it.
    expect(buildLegacyAutoscanRedirectTarget("?tab=connections")).toBe(
      "/admin/libraries?tab=autoscan&view=connections",
    );
    expect(buildLegacyAutoscanRedirectTarget("?tab=settings")).toBe(
      "/admin/libraries?tab=autoscan&view=settings",
    );
  });

  it("leaves the default view implicit", () => {
    // An absent `view` already means Sources, and the panel deletes the param
    // when Sources is selected.
    expect(buildLegacyAutoscanRedirectTarget("?tab=sources")).toBe("/admin/libraries?tab=autoscan");
  });

  it("drops a tab value nothing answers to", () => {
    expect(buildLegacyAutoscanRedirectTarget("?tab=nonsense")).toBe(
      "/admin/libraries?tab=autoscan",
    );
  });

  it("keeps unrelated query parameters", () => {
    expect(buildLegacyAutoscanRedirectTarget("?tab=activity&source=42")).toBe(
      "/admin/libraries?tab=autoscan&view=activity&source=42",
    );
  });

  it("emits one view even when the old link already used that name", () => {
    expect(buildLegacyAutoscanRedirectTarget("?view=activity")).toBe(
      "/admin/libraries?tab=autoscan&view=activity",
    );
  });
});

describe("autoscan tab helpers", () => {
  it("falls back to Sources for anything it does not serve", () => {
    expect(normalizeTab("activity")).toBe("activity");
    expect(normalizeTab("sources")).toBe("sources");
    expect(normalizeTab("connections")).toBe("sources");
    expect(normalizeTab(null)).toBe("sources");
  });

  it("recognises only the two retired tabs as Advanced deep links", () => {
    expect(isLegacyAdvancedTab("connections")).toBe(true);
    expect(isLegacyAdvancedTab("settings")).toBe(true);
    expect(isLegacyAdvancedTab("activity")).toBe(false);
    expect(isLegacyAdvancedTab(null)).toBe(false);
  });
});
