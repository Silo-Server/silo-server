import { describe, expect, it } from "vitest";

import { filterSettingsSearchEntries } from "@/components/settings/settingsSearch";

import { ADMIN_SETTINGS_NAV } from "./adminSettingsSearch";

describe("adminSettingsSearch", () => {
  it("finds the Redis database number on the Storage & Database page", () => {
    const page = ADMIN_SETTINGS_NAV.find((item) => item.id === "infrastructure");

    expect(filterSettingsSearchEntries(page?.settings, "database number")).toEqual([
      { label: "Database number" },
    ]);
  });
});
