// @vitest-environment node

import { describe, expect, it } from "vitest";

import { legacyAutoscanPageTarget, legacyLibrariesAutoscanTarget } from "./autoscanSearchParams";

describe("legacyAutoscanPageTarget", () => {
  it("leaves the Sources page alone when there is no old tab", () => {
    expect(legacyAutoscanPageTarget("")).toBeNull();
    expect(legacyAutoscanPageTarget("?source=42")).toBeNull();
  });

  it("sends the old Activity tab to its page, carrying other parameters", () => {
    expect(legacyAutoscanPageTarget("?tab=activity")).toBe("/admin/autoscan/activity");
    expect(legacyAutoscanPageTarget("?tab=activity&source=42")).toBe(
      "/admin/autoscan/activity?source=42",
    );
  });

  it("sends the old Connections and Settings tabs to the Settings page", () => {
    expect(legacyAutoscanPageTarget("?tab=connections")).toBe("/admin/settings/autoscan");
    expect(legacyAutoscanPageTarget("?tab=settings&source=42")).toBe("/admin/settings/autoscan");
  });

  it("reads an old view parameter when there is no tab", () => {
    expect(legacyAutoscanPageTarget("?view=activity&source=42")).toBe(
      "/admin/autoscan/activity?source=42",
    );
    expect(legacyAutoscanPageTarget("?view=connections")).toBe("/admin/settings/autoscan");
    expect(legacyAutoscanPageTarget("?tab=activity&view=settings")).toBe(
      "/admin/autoscan/activity",
    );
  });

  it("lands an explicit or unknown tab on Sources without the tab", () => {
    expect(legacyAutoscanPageTarget("?tab=sources")).toBe("/admin/autoscan");
    expect(legacyAutoscanPageTarget("?tab=nonsense&source=42")).toBe("/admin/autoscan?source=42");
  });
});

describe("legacyLibrariesAutoscanTarget", () => {
  it("ignores Libraries URLs that do not name the Autoscan tab", () => {
    expect(legacyLibrariesAutoscanTarget("")).toBeNull();
    expect(legacyLibrariesAutoscanTarget("?tab=libraries")).toBeNull();
  });

  it("maps the old tab and its view to the Autoscan pages", () => {
    expect(legacyLibrariesAutoscanTarget("?tab=autoscan")).toBe("/admin/autoscan");
    expect(legacyLibrariesAutoscanTarget("?tab=autoscan&view=activity&source=42")).toBe(
      "/admin/autoscan/activity?source=42",
    );
    expect(legacyLibrariesAutoscanTarget("?tab=autoscan&view=connections")).toBe(
      "/admin/settings/autoscan",
    );
  });
});
