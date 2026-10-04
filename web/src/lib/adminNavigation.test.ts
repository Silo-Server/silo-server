// @vitest-environment node

import { describe, expect, it } from "vitest";

import { filterSettingsSearchGroups } from "@/components/settings/settingsSearch";

import { buildAdminCommandNavSections } from "./adminNavigation";

function hrefsFor(query: string): string[] {
  return filterSettingsSearchGroups(buildAdminCommandNavSections(undefined), query).flatMap(
    (group) => group.items.map((item) => item.href),
  );
}

describe("admin command palette", () => {
  it.each(["sonarr", "radarr", "request servers", "routing", "anime", "quota", "request limit"])(
    "finds the Requests settings page for %s",
    (query) => {
      expect(hrefsFor(query)).toContain("/admin/settings/requests");
    },
  );

  it("still finds the request queue", () => {
    expect(hrefsFor("approvals")).toContain("/admin/requests");
  });
});
