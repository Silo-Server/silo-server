import { describe, expect, it } from "vitest";

import { filterSettingsSearchEntries } from "@/components/settings/settingsSearch";

import { ADMIN_SETTINGS_NAV } from "./adminSettingsSearch";

describe("adminSettingsSearch", () => {
  it("finds the Redis database number on the General page", () => {
    const page = ADMIN_SETTINGS_NAV.find((item) => item.id === "general");

    expect(filterSettingsSearchEntries(page?.settings, "database number")).toEqual([
      { label: "Database number" },
    ]);
  });

  it("finds the shared account allowance under its current label", () => {
    const page = ADMIN_SETTINGS_NAV.find((item) =>
      item.settings?.some((s) => s.label === "Rate limiting"),
    );

    expect(filterSettingsSearchEntries(page?.settings, "per address")).toEqual([
      { label: "Per address and account" },
    ]);
  });
});
